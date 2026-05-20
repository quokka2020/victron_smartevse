package victron

import (
	"fmt"
	"log"
	"reflect"

	"github.com/godbus/dbus/v5"
)

type EV_Position int32

const (
	EV_Position_AC_Output EV_Position = 0
	EV_Position_AC_Input  EV_Position = 1
)

var ev_position_text = map[EV_Position]string{
	EV_Position_AC_Output: "AC Output",
	EV_Position_AC_Input:  "AC Input",
}

type EvPositionBusItem struct {
	bus_item_impl
	position EV_Position
	callback func(EV_Position)
}

func NewEvPositionBusItem(position EV_Position) EvPositionBusItem {
	return EvPositionBusItem{position: position}
}

func (f *EvPositionBusItem) SetValue(val dbus.Variant) (int, *dbus.Error) {
	log.Printf("%s Received %s - %v", f.getObjectPath(), reflect.TypeOf(val.Value()), val.Value())
	v, err := variant_int_value(val)
	if err != nil {
		return -1, err
	}
	p := EV_Position(v)
	if _, ok := ev_position_text[p]; !ok {
		return -1, dbus.NewError(
			"com.victronenergy.BusItem.Error",
			[]any{fmt.Sprintf("invalid /Position value: %d (valid: 0=AC Output 1=AC Input)", v)},
		)
	}
	f.position = p
	if f.callback != nil {
		f.callback(p)
	}
	return 0, nil
}

func (f *EvPositionBusItem) GetValue() (dbus.Variant, *dbus.Error) {
	return dbus.MakeVariant(int32(f.position)), nil
}
func (f *EvPositionBusItem) GetText() (string, *dbus.Error) {
	return ev_position_text[f.position], nil
}
func (f *EvPositionBusItem) change(position EV_Position) { f.position = position }
