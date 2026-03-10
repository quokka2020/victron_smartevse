package victron

import (
	"fmt"
	"log"
	"reflect"
	"strconv"

	"github.com/godbus/dbus/v5"
)

type UnitBusItem struct {
	bus_item_impl
	unit      string
	value     float64
	presision int
}

func NewUnitFormatterObject(value float64, unit string, presision int) UnitBusItem {
	return UnitBusItem{
		unit:      unit,
		value:     value,
		presision: presision,
	}
}

func (f *UnitBusItem) SetValue(val dbus.Variant) (int, *dbus.Error) {
	log.Printf("%s Received %s - %v - %s", f.getObjectPath(), reflect.TypeOf(val.Value()), val.Value(), val.String())
	value, err := strconv.ParseFloat(val.String(), 64)
	if err != nil {
		return -1, dbus.NewError(
			"com.victronenergy.BusItem.Error",
			[]any{fmt.Sprintf("Not a number %v", err)},
		)
	}

	f.value = value
	return 0, nil
}

func (f *UnitBusItem) GetValue() (any, *dbus.Error) {
	return f.value, nil
}

func (f *UnitBusItem) GetText() (string, *dbus.Error) {
	return fmt.Sprintf("%.*f %s", f.presision, f.value, f.unit), nil
}

func (f *UnitBusItem) change(value float64) {
	f.value = value
}
