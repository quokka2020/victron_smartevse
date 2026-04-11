package victron

import (
	"fmt"
	"log"
	"reflect"

	"github.com/godbus/dbus/v5"
)

type EV_AutoStart int32

const (
	EV_AutoStart_Disabled = EV_AutoStart(0)
	EV_AutoStart_Enabled  = EV_AutoStart(1)
)

var ev_autostart = map[EV_AutoStart]string{
	EV_AutoStart_Enabled:  "Enabled",
	EV_AutoStart_Disabled: "Disabled",
}

func (a EV_AutoStart) ToString() string {
	if v, found := ev_autostart[a]; found {
		return v
	}
	return "Unknown"
}

type EvAutoStartBusItem struct {
	bus_item_impl
	autostart EV_AutoStart
	callback  func(mode EV_AutoStart)
}

func NewEvAutoStartBusItem(autostart EV_AutoStart) EvAutoStartBusItem {
	return EvAutoStartBusItem{
		autostart: autostart,
	}
}

func (f *EvAutoStartBusItem) SetValue(val dbus.Variant) (int, *dbus.Error) {
	log.Printf("%s Received %s - %v", f.getObjectPath(), reflect.TypeOf(val.Value()), val.Value())
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
	if f.callback != nil {
		f.callback(new_autostart)
	}
	return 0, nil
}

func (f *EvAutoStartBusItem) GetValue() (dbus.Variant, *dbus.Error) {
	return dbus.MakeVariant(int32(f.autostart)), nil
}

func (f *EvAutoStartBusItem) GetText() (string, *dbus.Error) {
	return ev_autostart[f.autostart], nil
}

func (f *EvAutoStartBusItem) change(autostart EV_AutoStart) {
	f.autostart = autostart
}
