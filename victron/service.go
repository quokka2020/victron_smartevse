package victron

import (
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"
)

type Service struct {
	// lock for values
	mu             sync.Mutex
	parent         *VictronHandler
	name           string
	bus_items      map[string]BusItem
	deviceInstance int
	deviceName     string
	deviceClass    string
}

var nonAlphanumberic = regexp.MustCompile("[^a-zA-Z0-9]+")

// TODO: validate name
func (handler *VictronHandler) NewService(name string) (*Service, error) {
	parts := strings.Split(name, ".")
	if len(parts) < 3 {
		return nil, fmt.Errorf("name %q must have at least 3 parts", name)
	}

	deviceName := parts[len(parts)-1]
	deviceName = nonAlphanumberic.ReplaceAllString(deviceName, "_")
	deviceName = strings.ToLower(deviceName)

	deviceClass := parts[len(parts)-2]

	name = strings.Join(parts[:len(parts)-1], ".") + "." + deviceName

	s := &Service{
		parent:         handler,
		name:           name,
		bus_items:      map[string]BusItem{},
		deviceName:     deviceName,
		deviceClass:    deviceClass,
		deviceInstance: -1,
	}

	return s, nil
}

func (s *Service) Close() error {
	reply, err := s.parent.dbusconn.ReleaseName(s.name)
	if err != nil {
		return fmt.Errorf("failed to release name %s: %w", s.name, err)
	}

	if reply != dbus.ReleaseNameReplyReleased {
		return fmt.Errorf("failed to release name %v: %d", s.name, reply)
	}

	return nil
}

func (s *Service) GetOrCreateDeviceInstance() (int, error) {
	// TODO create in settings
	s.deviceInstance = 1
	return s.deviceInstance, nil
}

func (s *Service) Register() error {
	root_path := dbus.ObjectPath("/")

	w := &service_wrapper{service: s}

	if err := s.parent.dbusconn.ExportAll(
		w,
		root_path,
		"com.victronenergy.BusItem",
	); err != nil {
		return fmt.Errorf("failed to export object: %w", err)
	}

	// Build a tree of all registered paths so introspection can
	// discover child nodes at every level (/, /Ac, /Ac/L1, …).
	childrenOf := s.buildPathTree()

	// Root node: include the BusItem interface + direct children
	node := &introspect.Node{}
	node.Name = "com.victronenergy.BusItem"
	iface := &introspect.Interface{}
	iface.Name = "com.victronenergy.BusItem"
	iface.Methods = introspect.Methods(w)
	for _, method := range iface.Methods {
		if method.Name == "GetText" || method.Name == "GetValue" {
			method.Args[0].Name = "value"
		}
		if method.Name == "GetItems" {
			method.Args[0].Name = "value"
		}
	}
	node.Interfaces = append(node.Interfaces, *iface)
	for _, child := range childrenOf["/"] {
		node.Children = append(node.Children, introspect.Node{Name: child})
	}
	dbusXMLinsp := introspect.NewIntrospectable(node)

	if err := s.parent.dbusconn.Export(
		dbusXMLinsp,
		root_path,
		"org.freedesktop.DBus.Introspectable"); err != nil {
		return err
	}

	// Export introspectables for intermediate paths (e.g. /Ac, /Ac/L1)
	// so that tools walking the tree can discover deeper nodes.
	for parentPath, children := range childrenOf {
		if parentPath == "/" {
			continue // already handled above
		}
		intermediateNode := &introspect.Node{}
		for _, child := range children {
			child_node := introspect.Node{Name: child}
			intermediateNode.Children = append(intermediateNode.Children, child_node)
			if _, found := s.bus_items[parentPath]; !found {
				pw := &part_service_wrapper{
					service: s,
					path:    fmt.Sprintf("%s/", parentPath),
				}

				iface := &introspect.Interface{}
				iface.Name = "com.victronenergy.BusItem"
				iface.Methods = introspect.Methods(pw)
				for _, method := range iface.Methods {
					if method.Name == "GetText" || method.Name == "GetValue" {
						method.Args[0].Name = "value"
					}
					if method.Name == "GetItems" {
						method.Args[0].Name = "value"
					}
				}
				intermediateNode.Interfaces = append(intermediateNode.Interfaces, *iface)

				if err := s.parent.dbusconn.ExportAll(
					pw,
					dbus.ObjectPath(parentPath),
					"com.victronenergy.BusItem",
				); err != nil {
					return fmt.Errorf("failed to export object: %w", err)
				}
			}
		}

		if err := s.parent.dbusconn.Export(
			introspect.NewIntrospectable(intermediateNode),
			dbus.ObjectPath(parentPath),
			"org.freedesktop.DBus.Introspectable"); err != nil {
			return fmt.Errorf("failed to export introspectable for %s: %w", parentPath, err)
		}
	}

	reply, err := s.parent.dbusconn.RequestName(s.name, dbus.NameFlagDoNotQueue)
	if err != nil {
		return fmt.Errorf("failed to request name: %w", err)
	}

	if reply != dbus.RequestNameReplyPrimaryOwner {
		return fmt.Errorf("name %q already taken", s.name)
	}

	return nil
}

// buildPathTree collects every registered path and returns a map from
// each parent path to its direct child names.
// For example, given paths /Ac/Power, /Ac/L1/Power, /Status:
//
//	"/"    -> ["Ac", "Status"]
//	"/Ac"  -> ["Power", "L1"]
//	"/Ac/L1" -> ["Power"]
func (s *Service) buildPathTree() map[string][]string {
	childrenOf := map[string][]string{}
	seen := map[string]map[string]bool{}

	for path := range s.bus_items {
		parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
		for i := range parts {
			var parent string
			if i == 0 {
				parent = "/"
			} else {
				parent = "/" + strings.Join(parts[:i], "/")
			}
			child := parts[i]

			if seen[parent] == nil {
				seen[parent] = map[string]bool{}
			}
			if !seen[parent][child] {
				seen[parent][child] = true
				childrenOf[parent] = append(childrenOf[parent], child)
			}
		}
	}

	return childrenOf
}

func (s *Service) AddPath(path string, value BusItem) error {
	var err error

	// log.Printf("AddPath %s %s", s.name, path)

	// value.setObjectPath(s.parent.dbusconn.Object(s.name, dbus.ObjectPath(path)).Path(),path)
	value.setObjectPath(dbus.ObjectPath(path))

	// log.Printf("Named :%v",s.parent.dbusconn.Names())

	err = s.parent.dbusconn.ExportAll(
		value,
		value.getObjectPath(),
		"com.victronenergy.BusItem",
	)
	if err != nil {
		return fmt.Errorf("failed to export service value: %w", err)
	}

	node := &introspect.Node{}
	node.Name = "com.victronenergy.BusItem"
	iface := &introspect.Interface{}
	iface.Name = "com.victronenergy.BusItem"
	iface.Methods = introspect.Methods(value)
	node.Interfaces = append(node.Interfaces, *iface)
	dbusXMLinsp := introspect.NewIntrospectable(node)

	err = s.parent.dbusconn.Export(
		dbusXMLinsp,
		value.getObjectPath(),
		"org.freedesktop.DBus.Introspectable")
	if err != nil {
		return err
	}

	s.mu.Lock()
	s.bus_items[path] = value
	s.mu.Unlock()

	return nil
}

func (s *Service) PropertiesChanged(item BusItem) error {
	value, _ := item.GetValue()
	text, _ := item.GetText()
	payload := map[string]dbus.Variant{
		"Value": dbus.MakeVariant(value),
		"Text":  dbus.MakeVariant(text),
	}
	return s.parent.dbusconn.Emit(
		item.getObjectPath(),
		"com.victronenergy.BusItem.PropertiesChanged",
		payload,
	)
}

func (s *Service) emitItemsChanged(modifyable_items map[string]BusItem) {
	items := map[string]map[string]dbus.Variant{}
	for _, item := range modifyable_items {
		value, _ := item.GetValue()
		text, _ := item.GetText()
		items[string(item.getObjectPath())] = map[string]dbus.Variant{
			"Value": dbus.MakeVariant(value),
			"Text":  dbus.MakeVariant(text),
		}
	}

	if len(items) > 0 {
		s.parent.dbusconn.Emit("/", "com.victronenergy.BusItem.ItemsChanged", items)
	}
}
