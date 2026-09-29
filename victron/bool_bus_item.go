package victron

import (
	"fmt"
	"log"
	"reflect"

	"github.com/godbus/dbus/v5"
)

type BoolBusItem struct {
	busItemImpl
	value bool
}

func NewBoolBusItem(value bool) BoolBusItem {
	return BoolBusItem{value: value}
}

func (f *BoolBusItem) SetValue(val dbus.Variant) (int, *dbus.Error) {
	log.Printf("%s Received %s - %v", f.getObjectPath(), reflect.TypeOf(val.Value()), val.Value())

	switch v := val.Value().(type) {
	case bool:
		f.value = v
		return 0, nil
	case int:
		f.value = v != 0
		return 0, nil
	case int32:
		f.value = v != 0
		return 0, nil
	case int64:
		f.value = v != 0
		return 0, nil
	case uint32:
		f.value = v != 0
		return 0, nil
	}

	return -1, dbus.NewError(
		"com.victronenergy.BusItem.Error",
		[]any{fmt.Sprintf("expected bool/int for switch value, got %T", val.Value())},
	)
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
