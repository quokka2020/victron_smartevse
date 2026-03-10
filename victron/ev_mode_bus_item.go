package victron

import (
	"fmt"
	"log"
	"reflect"

	"github.com/godbus/dbus/v5"
)

type EV_Mode int

const (
	EV_Mode_Manual    = EV_Mode(0)
	EV_Mode_Automatic = EV_Mode(1)
	EV_Mode_Scheduled = EV_Mode(2)
)

var ev_mode = map[EV_Mode]string{
	EV_Mode_Manual:    "Manual",
	EV_Mode_Automatic: "Automatic",
	EV_Mode_Scheduled: "Scheduled",
}

type EvModeBusItem struct {
	bus_item_impl
	mode     EV_Mode
	callback func(mode EV_Mode)
}

func NewEvModeBusItem(mode EV_Mode) EvModeBusItem {
	return EvModeBusItem{
		mode: mode,
	}
}

func (f *EvModeBusItem) SetValue(val dbus.Variant) (int, *dbus.Error) {
	log.Printf("%s Received %s - %v - %s", f.getObjectPath(), reflect.TypeOf(val.Value()), val.Value(), val.String())
	value, err := variant_int_value(val)
	if err != nil {
		return -1, err
	}

	new_mode := EV_Mode(value)
	if _, found := ev_mode[new_mode]; !found {
		return -1, dbus.NewError(
			"com.victronenergy.BusItem.Error",
			[]any{fmt.Sprintf("Not a number %v", err)},
		)
	}

	f.mode = new_mode
	if f.callback != nil {
		f.callback(new_mode)
	}
	return 0, nil
}

func (f *EvModeBusItem) GetValue() (any, *dbus.Error) {
	return f.mode, nil
}

func (f *EvModeBusItem) GetText() (string, *dbus.Error) {
	return ev_mode[f.mode], nil
}

func (f *EvModeBusItem) change(mode EV_Mode) {
	f.mode = mode
}
