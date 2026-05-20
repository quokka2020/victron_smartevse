package victron

import (
	"fmt"
	"log"
	"reflect"

	"github.com/godbus/dbus/v5"
)

type EV_Status int32

const (
	EV_Status_Disconnected                = EV_Status(0)
	EV_Status_Connected                   = EV_Status(1)
	EV_Status_Charging                    = EV_Status(2)
	EV_Status_Charged                     = EV_Status(3)
	EV_Status_Waiting_for_sun             = EV_Status(4)
	EV_Status_Waiting_for_RFID            = EV_Status(5)
	EV_Status_Waiting_for_start           = EV_Status(6)
	EV_Status_Low_SOC                     = EV_Status(7)
	EV_Status_Ground_test_error           = EV_Status(8)
	EV_Status_Welded_contacts_test_error  = EV_Status(9)
	EV_Status_CP_input_test_error_shorted = EV_Status(10)
	EV_Status_Residual_current_detected   = EV_Status(11)
	EV_Status_Undervoltage_detected       = EV_Status(12)
	EV_Status_Overvoltage_detected        = EV_Status(13)
	EV_Status_Overheating_detected        = EV_Status(14)
	// EV_Status_Reserved = EV_Status(15)
	// EV_Status_Reserved = EV_Status(16)
	// EV_Status_Reserved = EV_Status(17)
	// EV_Status_Reserved = EV_Status(18)
	// EV_Status_Reserved = EV_Status(19)
	EV_Status_Charging_limit       = EV_Status(20)
	EV_Status_Start_charging       = EV_Status(21)
	EV_Status_Switching_to_3_phase = EV_Status(22)
	EV_Status_Switching_to_1_phase = EV_Status(23)
	EV_Status_Stop_charging        = EV_Status(24)
)

var ev_status = map[EV_Status]string{
	EV_Status_Disconnected:                "Disconnected",
	EV_Status_Connected:                   "Connected",
	EV_Status_Charging:                    "Charging",
	EV_Status_Charged:                     "Charged",
	EV_Status_Waiting_for_sun:             "Waiting for sun",
	EV_Status_Waiting_for_RFID:            "Waiting for RFID",
	EV_Status_Waiting_for_start:           "Waiting for start",
	EV_Status_Low_SOC:                     "Low SOC",
	EV_Status_Ground_test_error:           "Ground test error",
	EV_Status_Welded_contacts_test_error:  "Welded contacts test error",
	EV_Status_CP_input_test_error_shorted: "CP input test error (shorted)",
	EV_Status_Residual_current_detected:   "Residual current detected",
	EV_Status_Undervoltage_detected:       "Undervoltage detected",
	EV_Status_Overvoltage_detected:        "Overvoltage detected",
	EV_Status_Overheating_detected:        "Overheating detected",
	// EV_Status_Reserved = EV_Status{ Value: 15, Text: "Reserved"}
	// EV_Status_Reserved = EV_Status{ Value: 16, Text: "Reserved"}
	// EV_Status_Reserved = EV_Status{ Value: 17, Text: "Reserved"}
	// EV_Status_Reserved = EV_Status{ Value: 18, Text: "Reserved"}
	// EV_Status_Reserved = EV_Status{ Value: 19, Text: "Reserved"}
	EV_Status_Charging_limit:       "Charging limit",
	EV_Status_Start_charging:       "Start charging",
	EV_Status_Switching_to_3_phase: "Switching to 3-phase",
	EV_Status_Switching_to_1_phase: "Switching to 1-phase",
	EV_Status_Stop_charging:        "Stop charging",
}

type EvStatusBusItem struct {
	bus_item_impl
	status EV_Status
}

func NewEvStatusBusItem(status EV_Status) EvStatusBusItem {
	return EvStatusBusItem{
		status: status,
	}
}

func (f *EvStatusBusItem) SetValue(val dbus.Variant) (int, *dbus.Error) {
	log.Printf("%s Received %s - %v", f.getObjectPath(), reflect.TypeOf(val.Value()), val.Value())
	value, err := variant_int_value(val)
	if err != nil {
		return -1, err
	}

	new_status := EV_Status(value)
	if _, found := ev_status[new_status]; !found {
		return -1, dbus.NewError(
			"com.victronenergy.BusItem.Error",
			[]any{fmt.Sprintf("Not a number %v", err)},
		)
	}

	f.status = new_status
	return 0, nil
}

func (f *EvStatusBusItem) GetValue() (dbus.Variant, *dbus.Error) {
	return dbus.MakeVariant(int32(f.status)), nil
}

func (f *EvStatusBusItem) GetText() (string, *dbus.Error) {
	return ev_status[f.status], nil
}

func (f *EvStatusBusItem) change(status EV_Status) {
	f.status = status
}
