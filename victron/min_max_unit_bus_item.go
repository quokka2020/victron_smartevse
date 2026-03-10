package victron

import (
	"fmt"
	"log"
	"reflect"
	"strconv"

	"github.com/godbus/dbus/v5"
)

type MinMaxUnitBusItem struct {
	bus_item_impl

	unit      string
	value     float64
	min       float64
	max       float64
	presision int
}

func NewMinMaxUnitBusItem(value, min, max float64, unit string, presision int) MinMaxUnitBusItem {
	return MinMaxUnitBusItem{
		unit:      unit,
		min:       min,
		max:       max,
		value:     value,
		presision: presision,
	}
}

func (f *MinMaxUnitBusItem) SetValue(val dbus.Variant) (int, *dbus.Error) {
	log.Printf("%s Received %s - %v - %s", f.getObjectPath(), reflect.TypeOf(val.Value()), val.Value(), val.String())
	value, err := strconv.ParseFloat(val.String(), 64)
	if err != nil {
		return -1, dbus.NewError(
			"com.victronenergy.BusItem.Error",
			[]any{fmt.Sprintf("Not a number %v", err)},
		)
	}

	if value < f.min {
		return -1, dbus.NewError(
			"com.victronenergy.BusItem.Error",
			[]any{fmt.Sprintf("value %.*f to low range %.*f..%.*f", f.presision, value, f.presision, f.min, f.presision, f.max)},
		)
	}
	if value > f.max {
		return -1, dbus.NewError(
			"com.victronenergy.BusItem.Error",
			[]any{fmt.Sprintf("value %.*f to high range %.*f..%.*f", f.presision, value, f.presision, f.min, f.presision, f.max)},
		)
	}
	f.value = value
	return 0, nil
}

func (f *MinMaxUnitBusItem) GetValue() (any, *dbus.Error) {
	return f.value, nil
}

func (f *MinMaxUnitBusItem) GetMin() (any, *dbus.Error) {
	return f.min, nil
}

func (f *MinMaxUnitBusItem) GetMax() (any, *dbus.Error) {
	return f.max, nil
}

func (f *MinMaxUnitBusItem) GetText() (string, *dbus.Error) {
	return fmt.Sprintf("%.*f %s", f.presision, f.value, f.unit), nil
}

func (f *MinMaxUnitBusItem) change(value float64) {
	f.value = value
}
