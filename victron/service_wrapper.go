package victron

import (
	"log"
	"maps"
	"reflect"
	"strings"

	"github.com/godbus/dbus/v5"
)

type service_wrapper struct {
	service *Service
}

type part_service_wrapper struct {
	service *Service
	path    string
}

// dbus signature a{sa{sv}}
func (s *service_wrapper) GetItems() (map[string]map[string]any, *dbus.Error) {
	out := make(map[string]map[string]any)

	s.service.mu.Lock()
	items := maps.Clone(s.service.bus_items)
	s.service.mu.Unlock()

	for path, item := range items {
		val, err := item.GetValue()
		if err != nil {
			log.Printf("GetItems Value Path:%s err:%v %s", path, err, reflect.TypeOf(err))
			return nil, err
		}

		text, err := item.GetText()
		if err != nil {
			log.Printf("GetItems Text Path:%s err:%v", path, err)
			return nil, err
		}

		// Note with leading /
		out[path] = map[string]any{
			"Value": val,
			"Text":  text,
		}
	}

	return out, nil
}

func (s *service_wrapper) GetText() (map[string]string, *dbus.Error) {
	part := part_service_wrapper{
		service: s.service,
		path:    "/",
	}
	return part.GetText()
}

func (s *part_service_wrapper) GetText() (map[string]string, *dbus.Error) {
	out := map[string]string{}

	s.service.mu.Lock()
	items := maps.Clone(s.service.bus_items)
	s.service.mu.Unlock()

	for path, item := range items {
		if !strings.HasPrefix(path, s.path) {
			continue
		}
		// Note without leading /
		export := path[len(s.path):]
		text, err := item.GetText()
		if err != nil {
			log.Printf("GetItems Text Path:%s err:%v", path, err)
			return nil, err
		}
		out[export] = text
	}

	return out, nil
}

func (s *service_wrapper) GetValue() (map[string]any, *dbus.Error) {
	part := part_service_wrapper{
		service: s.service,
		path:    "/",
	}
	return part.GetValue()
}

func (s *part_service_wrapper) GetValue() (map[string]any, *dbus.Error) {
	out := map[string]any{}

	s.service.mu.Lock()
	items := maps.Clone(s.service.bus_items)
	s.service.mu.Unlock()

	for path, item := range items {
		if !strings.HasPrefix(path, s.path) {
			continue
		}
		// Note without leading /
		export := path[len(s.path):]

		value, err := item.GetValue()
		if err != nil {
			log.Printf("GetValue value Path:%s err:%v", path, err)
			return nil, err
		}
		out[export] = value
	}

	return out, nil
}
