package victron

import (
	"fmt"
	"strconv"

	"github.com/godbus/dbus/v5"
)

type BusItem interface {
	getObjectPath() dbus.ObjectPath
	setObjectPath(object_path dbus.ObjectPath)
	GetValue() (any, *dbus.Error)
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

func variant_int_value(val dbus.Variant) (int64, *dbus.Error) {
	value, err := strconv.ParseInt(val.String(), 10, 64)
	if err != nil {
		return -1, dbus.NewError(
			"com.victronenergy.BusItem.Error",
			[]any{fmt.Sprintf("Not a number %v", err)},
		)
	}
	return value, nil
}
