package victron

import (
	"fmt"
	"os"
	"time"
	"victron_smartevse/global"
)

/*
com.victronenergy.evcharger

/Ac/Power                  --> Write: AC Power (W)
/Ac/L1/Power               --> Write: L1 Power used (W)
/Ac/L2/Power               --> Write: L2 Power used (W)
/Ac/L3/Power               --> Write: L3 Power used (W)
/Ac/Energy/Forward         --> Write: Charged Energy (kWh)

/Current                   --> Write: Actual charging current (A)
/MaxCurrent                --> Read/Write: Max charging current (A)
/SetCurrent                --> Read/Write: Charging current (A)

/AutoStart                 --> Read/Write: Start automatically (number)
    0 = Charger autostart disabled
    1 = Charger autostart enabled
/ChargingTime              <-- Session charging time (seconds) - DEPRECATED
/Session/Time              <-- Session charging time (seconds)
/Session/Energy            <-- Session charging energy (kWh)
/Session/Cost              <-- Session cost (no currency)
/Session/SavedCost         <-- Optional: Session saved cost (no currency)

/EnableDisplay             --> Read/Write: Lock charger display (number)
    0 = Control disabled
    1 = Control enabled
/Mode                      --> Read/Write: Charge mode (number)
    0 = Manual
    1 = Automatic
    2 = Scheduled
/Model                     --> Model, e.g. AC22E or AC22NS (for No Screen)
/Position                  --> Write: Charger position (number)
    0 = AC Output
    1 = AC Input
/Role                      --> Unknown usage
/StartStop                 --> Read/Write: Enable charging (number)
    0 = Enable charging: False
    1 = Enable charging: True
/Status                    --> Write: Status (number)
    0 = Disconnected
    1 = Connected
    2 = Charging
    3 = Charged
    4 = Waiting for sun
    5 = Waiting for RFID
    6 = Waiting for start
    7 = Low SOC
    8 = Ground test error
    9 = Welded contacts test error
    10 = CP input test error (shorted)
    11 = Residual current detected
    12 = Undervoltage detected
    13 = Overvoltage detected
    14 = Overheating detected
    15 = Reserved
    16 = Reserved
    17 = Reserved
    18 = Reserved
    19 = Reserved
    20 = Charging limit
    21 = Start charging
    22 = Switching to 3-phase
    23 = Switching to 1-phase
    24 = Stop charging
/IsGenericEnergyMeter      <-- The device measuring the EVSE is a generic energy meter (lacks
                               EVSE specific functions such as StartStop)
*/

type Victron_EV_Charger struct {
	parent           *VictronHandler
	service          *Service
	running          bool
	modifyable_items map[string]BusItem

	connected ManualBusItem

	power    UnitBusItem
	power_l1 UnitBusItem
	power_l2 UnitBusItem
	power_l3 UnitBusItem

	current     UnitBusItem
	set_current MinMaxUnitBusItem
	max_current MinMaxUnitBusItem

	session_time   UnitBusItem
	session_energy UnitBusItem
	total_charged  UnitBusItem

	temperature UnitBusItem

	status    EvStatusBusItem
	mode      EvModeBusItem
	autostart EvAutoStartBusItem
}

func (handler *VictronHandler) CreateEvChanger(serial int, version, connection string, min, current, max float64, session_energy float64, total float64) (*Victron_EV_Charger, error) {
	var err error

	ev := Victron_EV_Charger{
		parent: handler,

		connected: NewManualBusItem(1, "Connected"),

		power:    NewUnitFormatterObject(0, "W", 1),
		power_l1: NewUnitFormatterObject(0, "W", 1),
		power_l2: NewUnitFormatterObject(0, "W", 1),
		power_l3: NewUnitFormatterObject(0, "W", 1),

		current:        NewUnitFormatterObject(current, "A", 1),
		set_current:    NewMinMaxUnitBusItem(current, min, max, "A", 0),
		max_current:    NewMinMaxUnitBusItem(max, min, max, "A", 0),
		session_time:   NewUnitFormatterObject(0, "s", 3),
		session_energy: NewUnitFormatterObject(session_energy, "kWh", 3),
		total_charged:  NewUnitFormatterObject(total, "kWh", 3),

		temperature: NewUnitFormatterObject(20, "C", 0),

		status:    NewEvStatusBusItem(EV_Status_Disconnected),
		mode:      NewEvModeBusItem(EV_Mode_Scheduled),
		autostart: NewEvAutoStartBusItem(EV_AutoStart_Enabled),
	}

	deviceName := fmt.Sprintf("SmartEVSE-%d", serial)
	serviceName := "com.victronenergy.evcharger." + deviceName

	ev.service, err = handler.NewService(serviceName)
	if err != nil {
		return nil, fmt.Errorf("failed to create service: %w", err)
	}

	// From here on we need to call close
	var deviceInstance int
	deviceInstance, err = ev.service.GetOrCreateDeviceInstance()
	if err != nil {
		return ev.return_and_close(fmt.Errorf("failed to get device instance: %w", err))
	}

	constant_paths := map[string]BusItem{
		"/ProductName":          NewAnyBusItem("SmartEVSE"),
		"/CustomName":           NewAnyBusItem(deviceName),
		"/DeviceName":           NewAnyBusItem(deviceName),
		"/Mgmt/Connection":      NewAnyBusItem(deviceName),
		"/Mgmt/ProcessName":     NewAnyBusItem(os.Args[0]),
		"/Mgmt/ProcessVersion":  NewAnyBusItem(global.Version),
		"/DeviceInstance":       NewAnyBusItem(deviceInstance),
		"/Model":                NewAnyBusItem("SmartEVSE v3"),
		"/ProductId":            NewAnyBusItem(65535),
		"/Serial":               NewAnyBusItem(serial),
		"/HardwareVersion":      NewAnyBusItem(3),
		"/FirmwareVersion":      NewAnyBusItem(version),
		"/Position":             NewAnyBusItem(0),
		"/Connected":            NewAnyBusItem(1),
		"/IsGenericEnergyMeter": NewAnyBusItem(0),
		"/EnableDisplay":        NewAnyBusItem(1),
	}

	for path, value := range constant_paths {
		if err := ev.service.AddPath(path, value); err != nil {
			return ev.return_and_close(fmt.Errorf("failed to add path %s: %w", path, err))
		}
	}

	ev.modifyable_items = map[string]BusItem{
		"/Connected":         &ev.connected,
		"/Status":            &ev.status,
		"/Ac/Power":          &ev.power,
		"/Ac/L1/Power":       &ev.power_l1,
		"/Ac/L2/Power":       &ev.power_l2,
		"/Ac/L3/Power":       &ev.power_l3,
		"/Current":           &ev.current,
		"/SetCurrent":        &ev.set_current,
		"/MaxCurrent":        &ev.max_current,
		"/Session/Energy":    &ev.session_energy,
		"/Session/Time":      &ev.session_time,
		"/Ac/Energy/Forward": &ev.total_charged,
		"/MCU/Temperature":   &ev.temperature,
		"/AutoStart":         &ev.autostart,
		"/Mode":              &ev.mode,
	}

	for path, value := range ev.modifyable_items {
		if err := ev.service.AddPath(path, value); err != nil {
			return ev.return_and_close(fmt.Errorf("failed to add path %s: %w", path, err))
		}
	}

	err = ev.service.Register()
	if err != nil {
		return ev.return_and_close(fmt.Errorf("failed to register service: %w", err))
	}

	go func() {
		ev.running = true
		for ev.running {
			<-time.After(5 * time.Second)
			ev.PublishUpdates()
		}
	}()

	// no error
	return &ev, nil
}

func (ev *Victron_EV_Charger) return_and_close(err error) (*Victron_EV_Charger, error) {
	ev.running = false
	if ev.service != nil {
		ev.service.Close()
	}
	return nil, err
}

func (ev *Victron_EV_Charger) SetModeChangedCallback(callback func(mode EV_Mode)) {
	ev.mode.callback = callback
}

func (ev *Victron_EV_Charger) PublishUpdates() {
	ev.service.emitItemsChanged(ev.modifyable_items)
}

func (ev *Victron_EV_Charger) ChangeConnected(connected bool) {
	if connected {
		ev.connected.change(1, "Connected")
	} else {
		ev.connected.change(0, "Disconnected")
	}
}

func (ev *Victron_EV_Charger) ChangeChargePower(power float64) {
	ev.power.change(power)
}

func (ev *Victron_EV_Charger) ChangeCurrentL1(current float64) {
	power := ev.parent.consumption_l1_v.lastValue * current
	ev.power_l1.change(power)
}

func (ev *Victron_EV_Charger) ChangeCurrentL2(current float64) {
	power := ev.parent.consumption_l2_v.lastValue * current
	ev.power_l2.change(power)
}

func (ev *Victron_EV_Charger) ChangeCurrentL3(current float64) {
	power := ev.parent.consumption_l3_v.lastValue * current
	ev.power_l3.change(power)
}

func (ev *Victron_EV_Charger) ChangeMaxCurrent(current float64) {
	ev.max_current.change(current)
}

func (ev *Victron_EV_Charger) ChangeChargeCurrent(current float64) {
	ev.current.change(current)
}

// in seconds
func (ev *Victron_EV_Charger) EnergyTime(seconds int64) {
	ev.session_time.change(float64(seconds))
}

// in kWh
func (ev *Victron_EV_Charger) EnergyCharged(energy float64) {
	ev.session_energy.change(energy)
}

// in kWh
func (ev *Victron_EV_Charger) TotalCharged(energy float64) {
	ev.total_charged.change(energy)
}

// in C
func (ev *Victron_EV_Charger) Temperature(temp float64) {
	ev.temperature.change(temp)
}

func (ev *Victron_EV_Charger) ChangeStatus(status EV_Status) {
	ev.status.change(status)
}

func (ev *Victron_EV_Charger) ChangeMode(mode EV_Mode) {
	ev.mode.change(mode)
	if mode == EV_Mode_Automatic {
		ev.autostart.change(EV_AutoStart_Enabled)
	} else {
		ev.autostart.change(EV_AutoStart_Disabled)
	}
}

func (ev *Victron_EV_Charger) Set_Current(min, value, max float64) {
	ev.set_current.min = min
	ev.set_current.value = value
	ev.set_current.max = max

	ev.max_current.min = min
	ev.max_current.value = max
	ev.max_current.max = max
}
