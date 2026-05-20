package victron

import (
	"fmt"
	"log"
	"reflect"

	"github.com/godbus/dbus/v5"
)

type UnitBusItem struct {
	bus_item_impl
	unit      string
	value     float64
	presision int
	callback  func(value float64)
}

func NewUnitFormatterObject(value float64, unit string, presision int) UnitBusItem {
	return UnitBusItem{
		unit:      unit,
		value:     value,
		presision: presision,
	}
}

func (f *UnitBusItem) SetValue(val dbus.Variant) (int, *dbus.Error) {
	log.Printf("%s Received %s - %v", f.getObjectPath(), reflect.TypeOf(val.Value()), val.Value())
	value, err := variant_float_value(val)
	if err != nil {
		return -1, err
	}

	f.value = value
	if f.callback != nil {
		f.callback(value)
	}
	return 0, nil
}

func (f *UnitBusItem) GetValue() (dbus.Variant, *dbus.Error) {
	return dbus.MakeVariant(f.value), nil
}

func (f *UnitBusItem) GetText() (string, *dbus.Error) {
	return fmt.Sprintf("%.*f %s", f.presision, f.value, f.unit), nil
}

func (f *UnitBusItem) change(value float64) {
	f.value = value
}
