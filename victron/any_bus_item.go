package victron

import (
	"fmt"
	"log"
	"reflect"

	"github.com/godbus/dbus/v5"
)

type AnyBusItem struct {
	bus_item_impl
	value any
}

func NewAnyBusItem(value any) *AnyBusItem {
	return &AnyBusItem{
		value: value,
	}
}

func (f *AnyBusItem) SetValue(val dbus.Variant) (int, *dbus.Error) {
	log.Printf("%s Received %s - %v - %s", f.getObjectPath(), reflect.TypeOf(val.Value()), val.Value(), val.String())

	return -1, dbus.NewError(
		"com.victronenergy.BusItem.Error",
		[]any{"Not expected to be changed"},
	)
}

func (f *AnyBusItem) GetValue() (dbus.Variant, *dbus.Error) {
	return dbus.MakeVariant(f.value), nil
}

func (f *AnyBusItem) GetText() (string, *dbus.Error) {
	return fmt.Sprintf("%v", f.value), nil
}
