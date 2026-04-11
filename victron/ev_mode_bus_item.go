package victron

import (
	"fmt"
	"log"
	"reflect"

	"github.com/godbus/dbus/v5"
)

type EV_Mode int32

const (
	EV_Mode_Manual    = EV_Mode(0)
	EV_Mode_Auto      = EV_Mode(1)
	EV_Mode_Scheduled = EV_Mode(2)
)

// ev_mode maps the three valid modes. Note: dbus_modbustcp/attributes.csv only
// lists 0=Manual and 1=Auto because its modbus register surface is limited, but
// the live Venus OS driver (dbus-modbus-client/ev_charger.py) and the GX display
// (gui-v2/src/enums.h, gui-v2/data/EvChargers.qml) all define SCHEDULED=2 as a
// valid mode.
var ev_mode = map[EV_Mode]string{
	EV_Mode_Manual:    "Manual",
	EV_Mode_Auto:      "Auto",
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
	log.Printf("%s Received %s - %v", f.getObjectPath(), reflect.TypeOf(val.Value()), val.Value())
	value, err := variant_int_value(val)
	if err != nil {
		return -1, err
	}

	new_mode := EV_Mode(value)
	if _, found := ev_mode[new_mode]; !found {
		return -1, dbus.NewError(
			"com.victronenergy.BusItem.Error",
			[]any{fmt.Sprintf("invalid /Mode value: %d (valid: 0=Manual 1=Auto 2=Scheduled)", value)},
		)
	}

	f.mode = new_mode
	if f.callback != nil {
		f.callback(new_mode)
	}
	return 0, nil
}

func (f *EvModeBusItem) GetValue() (dbus.Variant, *dbus.Error) {
	return dbus.MakeVariant(int32(f.mode)), nil
}

func (f *EvModeBusItem) GetText() (string, *dbus.Error) {
	return ev_mode[f.mode], nil
}

func (f *EvModeBusItem) change(mode EV_Mode) {
	f.mode = mode
}
