package testhelper

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"victron_smartevse/victron"

	"github.com/godbus/dbus/v5"
)

// fakeObject implements dbus.BusObject. Only Call is used by the production code;
// all other methods are stubs.
type fakeObject struct {
	conn *FakeDBusConn
	dest string
	path dbus.ObjectPath
}

func (o *fakeObject) Call(method string, flags dbus.Flags, args ...any) *dbus.Call {
	call := &dbus.Call{}
	switch method {
	case "org.freedesktop.DBus.ListNames":
		call.Body = []any{[]string{"com.victronenergy.settings"}}
	case "GetValue":
		value, err := o.conn.handleGetValue(o.dest, o.path)
		if err != nil {
			call.Err = dbus.NewError("com.victronenergy.Error", []any{err.Error()})
		} else {
			call.Body = []any{value}
		}
	case "AddSetting":
		result, err := o.conn.handleAddSetting(o.dest, o.path, args)
		if err != nil {
			call.Err = dbus.NewError("com.victronenergy.Error", []any{err.Error()})
		} else {
			call.Body = []any{result}
		}
	}
	return call
}

func (o *fakeObject) CallWithContext(_ context.Context, method string, flags dbus.Flags, args ...any) *dbus.Call {
	return o.Call(method, flags, args...)
}
func (o *fakeObject) Go(method string, flags dbus.Flags, ch chan *dbus.Call, args ...any) *dbus.Call {
	return nil
}
func (o *fakeObject) GoWithContext(_ context.Context, method string, flags dbus.Flags, ch chan *dbus.Call, args ...any) *dbus.Call {
	return nil
}
func (o *fakeObject) GetProperty(_ string) (dbus.Variant, error)  { return dbus.Variant{}, nil }
func (o *fakeObject) SetProperty(_ string, _ interface{}) error   { return nil }
func (o *fakeObject) StoreProperty(_ string, _ interface{}) error { return nil }
func (o *fakeObject) Destination() string                         { return o.dest }
func (o *fakeObject) Path() dbus.ObjectPath                       { return o.path }
func (o *fakeObject) AddMatchSignal(iface, member string, options ...dbus.MatchOption) *dbus.Call {
	return &dbus.Call{}
}
func (o *fakeObject) RemoveMatchSignal(iface, member string, options ...dbus.MatchOption) *dbus.Call {
	return &dbus.Call{}
}

type Emit struct {
	Path   dbus.ObjectPath
	Signal string
	Values []any
}

type FakeDBusConn struct {
	mu sync.Mutex

	Settings      map[string]string
	RequestNames  []string
	ReleaseNames  []string
	Exported      []dbus.ObjectPath
	ExportedAll   []dbus.ObjectPath
	Emitted       []Emit
	MatchRequests int
}

func NewFakeDBusConn() *FakeDBusConn {
	return &FakeDBusConn{Settings: map[string]string{}}
}

func (c *FakeDBusConn) Close() error { return nil }

func (c *FakeDBusConn) BusObject() dbus.BusObject {
	return &fakeObject{conn: c, dest: "org.freedesktop.DBus", path: "/org/freedesktop/DBus"}
}

func (c *FakeDBusConn) Object(dest string, path dbus.ObjectPath) dbus.BusObject {
	return &fakeObject{conn: c, dest: dest, path: path}
}

func (c *FakeDBusConn) AddMatchSignal(options ...dbus.MatchOption) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.MatchRequests += len(options)
	return nil
}

func (c *FakeDBusConn) Signal(ch chan<- *dbus.Signal)       { _ = ch }
func (c *FakeDBusConn) RemoveSignal(ch chan<- *dbus.Signal) { _ = ch }

func (c *FakeDBusConn) Export(v any, path dbus.ObjectPath, iface string) error {
	_ = v
	_ = iface
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Exported = append(c.Exported, path)
	return nil
}

func (c *FakeDBusConn) ExportAll(v any, path dbus.ObjectPath, iface string) error {
	_ = v
	_ = iface
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ExportedAll = append(c.ExportedAll, path)
	return nil
}

func (c *FakeDBusConn) RequestName(name string, flags dbus.RequestNameFlags) (dbus.RequestNameReply, error) {
	_ = flags
	c.mu.Lock()
	defer c.mu.Unlock()
	c.RequestNames = append(c.RequestNames, name)
	return dbus.RequestNameReplyPrimaryOwner, nil
}

func (c *FakeDBusConn) ReleaseName(name string) (dbus.ReleaseNameReply, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ReleaseNames = append(c.ReleaseNames, name)
	return dbus.ReleaseNameReplyReleased, nil
}

func (c *FakeDBusConn) Emit(path dbus.ObjectPath, name string, values ...any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Emitted = append(c.Emitted, Emit{Path: path, Signal: name, Values: values})
	return nil
}

func (c *FakeDBusConn) handleGetValue(dest string, path dbus.ObjectPath) (string, error) {
	if dest != "com.victronenergy.settings" {
		return "", fmt.Errorf("unexpected destination %s", dest)
	}
	group := extractSettingsGroup(path)
	c.mu.Lock()
	value, found := c.Settings[group]
	c.mu.Unlock()
	if !found {
		return "", fmt.Errorf("setting not found")
	}
	return value, nil
}

func (c *FakeDBusConn) handleAddSetting(dest string, path dbus.ObjectPath, args []any) (int, error) {
	if dest != "com.victronenergy.settings" || path != "/Settings/Devices" {
		return 0, fmt.Errorf("unexpected AddSetting target %s %s", dest, path)
	}
	if len(args) < 3 {
		return 0, fmt.Errorf("insufficient AddSetting args")
	}
	group, ok := args[0].(string)
	if !ok {
		return 0, fmt.Errorf("expected group string")
	}
	defaultValue, ok := args[2].(dbus.Variant)
	if !ok {
		return 0, fmt.Errorf("expected default value variant")
	}
	defaultString, ok := defaultValue.Value().(string)
	if !ok {
		return 0, fmt.Errorf("expected default value string")
	}
	c.mu.Lock()
	c.Settings[group] = defaultString
	c.mu.Unlock()
	return 0, nil
}

func extractSettingsGroup(path dbus.ObjectPath) string {
	parts := strings.Split(string(path), "/")
	if len(parts) < 5 {
		return ""
	}
	return parts[3]
}

// ensure FakeDBusConn satisfies the victron.DBusConn interface at compile time
var _ victron.DBusConn = (*FakeDBusConn)(nil)
