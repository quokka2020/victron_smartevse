package victron

import (
	"log"
	"reflect"

	"github.com/godbus/dbus/v5"
)

type ManualBusItem struct {
	bus_item_impl
	value any
	text  string
}

func NewManualBusItem(value any, text string) *ManualBusItem {
	return &ManualBusItem{
		value: value,
		text:  text,
	}
}

func (f *ManualBusItem) SetValue(val dbus.Variant) (int, *dbus.Error) {
	log.Printf("%s Received %s - %v - %s", f.getObjectPath(), reflect.TypeOf(val.Value()), val.Value(), val.String())
	return -1, dbus.NewError(
		"com.victronenergy.BusItem.Error",
		[]any{"Not expected to be changed"},
	)
}

func (f *ManualBusItem) GetValue() (dbus.Variant, *dbus.Error) {
	return dbus.MakeVariant(f.value), nil
}

func (f *ManualBusItem) GetText() (string, *dbus.Error) {
	return f.text, nil
}

func (f *ManualBusItem) change(value any, text string) {
	f.value = value
	f.text = text
}
