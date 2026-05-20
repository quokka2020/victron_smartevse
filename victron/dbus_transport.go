package victron

import "github.com/godbus/dbus/v5"

// DBusConn abstracts the DBus connection used by Victron services.
// *dbus.Conn satisfies this interface directly, so no wrapper is needed in production.
// A fake implementation is used in tests.
type DBusConn interface {
	Close() error
	BusObject() dbus.BusObject
	Object(dest string, path dbus.ObjectPath) dbus.BusObject
	AddMatchSignal(options ...dbus.MatchOption) error
	Signal(ch chan<- *dbus.Signal)
	RemoveSignal(ch chan<- *dbus.Signal)
	Export(v any, path dbus.ObjectPath, iface string) error
	ExportAll(v any, path dbus.ObjectPath, iface string) error
	RequestName(name string, flags dbus.RequestNameFlags) (dbus.RequestNameReply, error)
	ReleaseName(name string) (dbus.ReleaseNameReply, error)
	Emit(path dbus.ObjectPath, name string, values ...any) error
}
