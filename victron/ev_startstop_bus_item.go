package victron

import (
	"fmt"

	"github.com/godbus/dbus/v5"
)

type EvStartStop int32

const (
	EvStartStopStop  = EvStartStop(0)
	EvStartStopStart = EvStartStop(1)
)

var evStartStop = map[EvStartStop]string{
	EvStartStopStart: "Enable charging",
	EvStartStopStop:  "Disable charging",
}

type EvStartStopBusItem struct {
	busItemImpl
	start    EvStartStop
	callback func(mode EvStartStop)
}

func NewEvStartStopBusItem(start EvStartStop) EvStartStopBusItem {
	return EvStartStopBusItem{
		start: start,
	}
}

func (f *EvStartStopBusItem) SetValue(val dbus.Variant) (int, *dbus.Error) {
	value, err := variantIntValue(val)
	if err != nil {
		return -1, err
	}

	newStart := EvStartStop(value)
	if _, found := evStartStop[newStart]; !found {
		return -1, dbus.NewError(
			"com.victronenergy.BusItem.Error",
			[]any{fmt.Sprintf("invalid /StartStop value: %d (valid: 0=Stop 1=Start)", value)},
		)
	}

	f.start = newStart
	if f.callback != nil {
		f.callback(newStart)
	}
	return 0, nil
}

func (f *EvStartStopBusItem) GetValue() (dbus.Variant, *dbus.Error) {
	return dbus.MakeVariant(int32(f.start)), nil
}

func (f *EvStartStopBusItem) GetText() (string, *dbus.Error) {
	return evStartStop[f.start], nil
}

func (f *EvStartStopBusItem) change(start EvStartStop) {
	f.start = start
}
