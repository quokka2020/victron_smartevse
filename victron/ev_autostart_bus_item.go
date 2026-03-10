package victron

import (
	"fmt"
	"log"
	"reflect"

	"github.com/godbus/dbus/v5"
)

type EV_AutoStart int

const (
	EV_AutoStart_Disabled = EV_AutoStart(0)
	EV_AutoStart_Enabled  = EV_AutoStart(1)
)

var ev_autostart = map[EV_AutoStart]string{
	EV_AutoStart_Enabled:  "Enabled",
	EV_AutoStart_Disabled: "Disabled",
}

type EvAutoStartBusItem struct {
	bus_item_impl
	autostart EV_AutoStart
}

func NewEvAutoStartBusItem(autostart EV_AutoStart) EvAutoStartBusItem {
	return EvAutoStartBusItem{
		autostart: autostart,
	}
}

func (f *EvAutoStartBusItem) SetValue(val dbus.Variant) (int, *dbus.Error) {
	log.Printf("%s Received %s - %v - %s", f.getObjectPath(), reflect.TypeOf(val.Value()), val.Value(), val.String())
	value, err := variant_int_value(val)
	if err != nil {
		return -1, err
	}

	new_autostart := EV_AutoStart(value)
	if _, found := ev_autostart[new_autostart]; !found {
		return -1, dbus.NewError(
			"com.victronenergy.BusItem.Error",
			[]any{fmt.Sprintf("Not a number %v", err)},
		)
	}

	f.autostart = new_autostart
	return 0, nil
}

func (f *EvAutoStartBusItem) GetValue() (any, *dbus.Error) {
	return f.autostart, nil
}

func (f *EvAutoStartBusItem) GetText() (string, *dbus.Error) {
	return ev_autostart[f.autostart], nil
}

func (f *EvAutoStartBusItem) change(autostart EV_AutoStart) {
	f.autostart = autostart
}
