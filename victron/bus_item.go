package victron

import (
	"fmt"

	"github.com/godbus/dbus/v5"
)

type BusItem interface {
	getObjectPath() dbus.ObjectPath
	setObjectPath(object_path dbus.ObjectPath)
	GetValue() (dbus.Variant, *dbus.Error)
	GetText() (string, *dbus.Error)
	SetValue(value dbus.Variant) (int, *dbus.Error)
}

type bus_item_impl struct {
	object_path dbus.ObjectPath
}

func (i *bus_item_impl) getObjectPath() dbus.ObjectPath {
	return i.object_path
}

func (i *bus_item_impl) setObjectPath(object_path dbus.ObjectPath) {
	i.object_path = object_path
}

// variant_int_value extracts an integer from a dbus.Variant using a type
// switch over the concrete Go types that godbus produces. Using val.String()
// would return GVariant text format (e.g. "uint32:1") which cannot be parsed
// as a plain integer.
func variant_int_value(val dbus.Variant) (int64, *dbus.Error) {
	switch v := val.Value().(type) {
	case int:
		return int64(v), nil
	case int16:
		return int64(v), nil
	case int32:
		return int64(v), nil
	case int64:
		return v, nil
	case uint16:
		return int64(v), nil
	case uint32:
		return int64(v), nil
	case float64:
		return int64(v), nil
	}
	return 0, dbus.NewError(
		"com.victronenergy.BusItem.Error",
		[]any{fmt.Sprintf("expected integer, got %T", val.Value())},
	)
}

// variant_float_value extracts a float64 from a dbus.Variant using a type
// switch. The dbus-modbus-client sends uint16 for current/power registers,
// so we must handle integer types in addition to float64.
func variant_float_value(val dbus.Variant) (float64, *dbus.Error) {
	switch v := val.Value().(type) {
	case float64:
		return v, nil
	case int:
		return float64(v), nil
	case int16:
		return float64(v), nil
	case int32:
		return float64(v), nil
	case int64:
		return float64(v), nil
	case uint16:
		return float64(v), nil
	case uint32:
		return float64(v), nil
	}
	return 0, dbus.NewError(
		"com.victronenergy.BusItem.Error",
		[]any{fmt.Sprintf("expected number, got %T", val.Value())},
	)
}
