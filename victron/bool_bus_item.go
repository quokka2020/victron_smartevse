package victron

import (
	"fmt"
	"log"
	"reflect"

	"github.com/godbus/dbus/v5"
)

type BoolBusItem struct {
	busItemImpl
	value    bool
	callback func(value bool)
}

func NewBoolBusItem(value bool) BoolBusItem {
	return BoolBusItem{value: value}
}

func (f *BoolBusItem) SetValue(val dbus.Variant) (int, *dbus.Error) {
	log.Printf("%s Received %s - %v", f.getObjectPath(), reflect.TypeOf(val.Value()), val.Value())

	var value bool
	switch v := val.Value().(type) {
	case bool:
		value = v
	case int:
		value = v != 0
	case int32:
		value = v != 0
	case int64:
		value = v != 0
	case uint32:
		value = v != 0
	default:
		return -1, dbus.NewError(
			"com.victronenergy.BusItem.Error",
			[]any{fmt.Sprintf("expected bool/int for switch value, got %T", val.Value())},
		)
	}

	f.value = value
	if f.callback != nil {
		f.callback(value)
	}
	return 0, nil
}

func (f *BoolBusItem) GetValue() (dbus.Variant, *dbus.Error) {
	return dbus.MakeVariant(f.value), nil
}

func (f *BoolBusItem) GetText() (string, *dbus.Error) {
	if f.value {
		return "On", nil
	}
	return "Off", nil
}

func (f *BoolBusItem) change(value bool) {
	f.value = value
}
