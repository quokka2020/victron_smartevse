package victron

import (
	"fmt"
	"log"
	"reflect"

	"github.com/godbus/dbus/v5"
)

type EV_StartStop int32

const (
	EV_StartStop_Stop  = EV_StartStop(0)
	EV_StartStop_Start = EV_StartStop(1)
)

var ev_startstop = map[EV_StartStop]string{
	EV_StartStop_Start: "Enable charging",
	EV_StartStop_Stop:  "Disable charging",
}

type EvStartStopBusItem struct {
	bus_item_impl
	start    EV_StartStop
	callback func(mode EV_StartStop)
}

func NewEvStartStopBusItem(start EV_StartStop) EvStartStopBusItem {
	return EvStartStopBusItem{
		start: start,
	}
}

func (f *EvStartStopBusItem) SetValue(val dbus.Variant) (int, *dbus.Error) {
	log.Printf("%s Received %s - %v", f.getObjectPath(), reflect.TypeOf(val.Value()), val.Value())
	value, err := variant_int_value(val)
	if err != nil {
		return -1, err
	}

	new_start := EV_StartStop(value)
	if _, found := ev_startstop[new_start]; !found {
		return -1, dbus.NewError(
			"com.victronenergy.BusItem.Error",
			[]any{fmt.Sprintf("Not a number %v", err)},
		)
	}

	f.start = new_start
	if f.callback != nil {
		f.callback(new_start)
	}
	return 0, nil
}

func (f *EvStartStopBusItem) GetValue() (dbus.Variant, *dbus.Error) {
	return dbus.MakeVariant(int32(f.start)), nil
}

func (f *EvStartStopBusItem) GetText() (string, *dbus.Error) {
	return ev_startstop[f.start], nil
}

func (f *EvStartStopBusItem) change(start EV_StartStop) {
	f.start = start
}
