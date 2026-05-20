package victron

import (
	"fmt"
	"log"
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
/MinCurrent                --> Read: Min charging current (A)
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
    1 = Auto
    2 = Scheduled
/Model                     --> Model, e.g. AC22E or AC22NS (for No Screen)
/Position                  --> Read/Write: Charger position (number)
    0 = AC Output
    1 = AC Input
/Role                      --> Device role: "evcharger"
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
*/

type Victron_EV_Charger struct {
	parent           *VictronHandler
	service          *Service
	running          bool
	constant_paths   map[string]BusItem
	modifyable_items map[string]BusItem

	connected ManualBusItem // /Connected  int32: 0=offline 1=online

	power    UnitBusItem // /Ac/Power
	power_l1 UnitBusItem // /Ac/L1/Power
	power_l2 UnitBusItem // /Ac/L2/Power
	power_l3 UnitBusItem // /Ac/L3/Power

	current     UnitBusItem       // /Current
	set_current MinMaxUnitBusItem // /SetCurrent (writable, bounded)
	max_current UnitBusItem       // /MaxCurrent
	min_current UnitBusItem       // /MinCurrent

	energy_forward UnitBusItem // /Ac/Energy/Forward
	session_energy UnitBusItem // /Session/Energy
	session_time   UnitBusItem // /Session/Time
	charging_time  UnitBusItem // /ChargingTime (deprecated alias, kept in sync with session_time)
	session_cost   UnitBusItem // /Session/Cost

	temperature UnitBusItem // /MCU/Temperature

	status    EvStatusBusItem
	mode      EvModeBusItem
	autostart EvAutoStartBusItem
	startStop EvStartStopBusItem
	position  EvPositionBusItem // /Position
}

func newEvChargerFields(parent *VictronHandler, min, current, max, session_energy, total float64) Victron_EV_Charger {
	return Victron_EV_Charger{
		parent: parent,

		connected: *NewManualBusItem(int32(0), "Disconnected"),

		power:    NewUnitFormatterObject(0, "W", 1),
		power_l1: NewUnitFormatterObject(0, "W", 1),
		power_l2: NewUnitFormatterObject(0, "W", 1),
		power_l3: NewUnitFormatterObject(0, "W", 1),

		current:        NewUnitFormatterObject(current, "A", 1),
		set_current:    NewMinMaxUnitBusItem(current, min, max, "A", 0),
		max_current:    NewUnitFormatterObject(max, "A", 0),
		min_current:    NewUnitFormatterObject(min, "A", 0),
		energy_forward: NewUnitFormatterObject(total, "kWh", 3),
		session_energy: NewUnitFormatterObject(session_energy, "kWh", 3),
		session_time:   NewUnitFormatterObject(0, "s", 0),
		charging_time:  NewUnitFormatterObject(0, "s", 0),
		session_cost:   NewUnitFormatterObject(0, "", 2),

		temperature: NewUnitFormatterObject(20, "C", 0),

		status:    NewEvStatusBusItem(EV_Status_Disconnected),
		mode:      NewEvModeBusItem(EV_Mode_Manual),
		autostart: NewEvAutoStartBusItem(EV_AutoStart_Enabled),
		startStop: NewEvStartStopBusItem(EV_StartStop_Stop),
		position:  NewEvPositionBusItem(EV_Position_AC_Output),
	}
}

func (ev *Victron_EV_Charger) initModifyableItems() {
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
		"/MinCurrent":        &ev.min_current,
		"/Ac/Energy/Forward": &ev.energy_forward,
		"/Session/Energy":    &ev.session_energy,
		"/Session/Time":      &ev.session_time,
		"/ChargingTime":      &ev.charging_time,
		"/Session/Cost":      &ev.session_cost,
		"/MCU/Temperature":   &ev.temperature,
		"/AutoStart":         &ev.autostart,
		"/StartStop":         &ev.startStop,
		"/Mode":              &ev.mode,
		"/Position":          &ev.position,
	}
}

func (handler *VictronHandler) CreateEvCharger(serial int, version, connection string, min, current, max float64, charged float64, total float64) (*Victron_EV_Charger, error) {
	var err error

	ev := newEvChargerFields(handler, min, current, max, charged, total)

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

	ev.constant_paths = map[string]BusItem{
		"/ProductName":          NewAnyBusItem("SmartEVSE"),
		"/DeviceName":           NewAnyBusItem(deviceName),
		"/CustomName":           NewAnyBusItem(deviceName),
		"/AllowedRoles":         NewAnyBusItem([]string{"evcharger"}),
		"/Mgmt/Connection":      NewAnyBusItem(connection),
		"/Mgmt/ProcessName":     NewAnyBusItem(os.Args[0]),
		"/Mgmt/ProcessVersion":  NewAnyBusItem(global.Version),
		"/DeviceInstance":       NewAnyBusItem(int32(deviceInstance)),
		"/Model":                NewAnyBusItem("SmartEVSE v3"),
		"/ProductId":            NewAnyBusItem(int32(0xFFFF)),
		"/Serial":               NewAnyBusItem(fmt.Sprintf("%d", serial)),
		"/HardwareVersion":      NewAnyBusItem(int32(3)),
		"/FirmwareVersion":      NewAnyBusItem(version),
		"/Role":                 NewAnyBusItem("evcharger"),
		"/PositionIsAdjustable": NewAnyBusItem(int32(1)),
		"/IsGenericEnergyMeter": NewAnyBusItem(int32(0)),
		"/EnableDisplay":        NewAnyBusItem(int32(1)),
	}

	for path, value := range ev.constant_paths {
		if err := ev.service.AddPath(path, value); err != nil {
			return ev.return_and_close(fmt.Errorf("failed to add path %s: %w", path, err))
		}
	}

	// Must be called after the struct is in its final heap location so that
	// all pointers in the map reference fields of this ev, not a copy.
	ev.initModifyableItems()

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
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		for ev.running {
			select {
			case <-ticker.C:
				ev.PublishUpdates()
			}
		}
	}()

	return &ev, nil
}

func (ev *Victron_EV_Charger) return_and_close(err error) (*Victron_EV_Charger, error) {
	ev.running = false
	if ev.service != nil {
		ev.service.Close()
	}
	return nil, err
}

// notify emits an immediate PropertiesChanged signal for the given item so
// that the GX display reflects changes without waiting for the heartbeat ticker.
func (ev *Victron_EV_Charger) notify(item BusItem) {
	if ev.service != nil {
		if err := ev.service.PropertiesChanged(item); err != nil {
			log.Printf("notify %s: PropertiesChanged error: %v", item.getObjectPath(), err)
		}
	}
}

func (ev *Victron_EV_Charger) SetModeChangedCallback(callback func(mode EV_Mode)) {
	ev.mode.callback = callback
}

func (ev *Victron_EV_Charger) SetOverrideCurrentChangedCallback(callback func(overrideCurrent, min, max float64)) {
	ev.set_current.callback = callback
}

func (ev *Victron_EV_Charger) SetStartStopChangedCallback(callback func(mode EV_StartStop)) {
	ev.startStop.callback = callback
}

func (ev *Victron_EV_Charger) SetAutoStartChangedCallback(callback func(mode EV_AutoStart)) {
	ev.autostart.callback = callback
}

func (ev *Victron_EV_Charger) SetPositionChangedCallback(callback func(EV_Position)) {
	ev.position.callback = callback
}

func (ev *Victron_EV_Charger) PublishUpdates() {
	ev.service.emitItemsChanged(ev.modifyable_items)
}

func (ev *Victron_EV_Charger) PublishConstants() {
	ev.service.emitItemsChanged(ev.constant_paths)
}

func (ev *Victron_EV_Charger) SetConnected(connected bool) {
	if connected {
		ev.connected.change(int32(1), "Connected")
	} else {
		ev.connected.change(int32(0), "Disconnected")
	}
	ev.notify(&ev.connected)
}

// ChangeConnected is a deprecated alias for SetConnected.
func (ev *Victron_EV_Charger) ChangeConnected(connected bool) {
	ev.SetConnected(connected)
}

func (ev *Victron_EV_Charger) SetAcPower(power float64) {
	ev.power.change(power)
	ev.notify(&ev.power)
}

// ChangeChargePower is a deprecated alias for SetAcPower.
func (ev *Victron_EV_Charger) ChangeChargePower(power float64) {
	ev.SetAcPower(power)
}

func (ev *Victron_EV_Charger) SetAcL1Power(power float64) {
	ev.power_l1.change(power)
	ev.notify(&ev.power_l1)
}

func (ev *Victron_EV_Charger) SetAcL2Power(power float64) {
	ev.power_l2.change(power)
	ev.notify(&ev.power_l2)
}

func (ev *Victron_EV_Charger) SetAcL3Power(power float64) {
	ev.power_l3.change(power)
	ev.notify(&ev.power_l3)
}

// ChangeCurrentL1 is a deprecated alias; callers should use SetAcL1Power with voltage * current.
func (ev *Victron_EV_Charger) ChangeCurrentL1(current float64) {
	ev.SetAcL1Power(ev.parent.consumption_l1_v.lastValue * current)
}

// ChangeCurrentL2 is a deprecated alias; callers should use SetAcL2Power with voltage * current.
func (ev *Victron_EV_Charger) ChangeCurrentL2(current float64) {
	ev.SetAcL2Power(ev.parent.consumption_l2_v.lastValue * current)
}

// ChangeCurrentL3 is a deprecated alias; callers should use SetAcL3Power with voltage * current.
func (ev *Victron_EV_Charger) ChangeCurrentL3(current float64) {
	ev.SetAcL3Power(ev.parent.consumption_l3_v.lastValue * current)
}

func (ev *Victron_EV_Charger) SetMaxCurrent(current float64) {
	ev.max_current.change(current)
	ev.notify(&ev.max_current)
}

// ChangeMaxCurrent is a deprecated alias for SetMaxCurrent.
func (ev *Victron_EV_Charger) ChangeMaxCurrent(current float64) {
	ev.SetMaxCurrent(current)
}

func (ev *Victron_EV_Charger) SetChargeCurrent(current float64) {
	ev.current.change(current)
	ev.notify(&ev.current)
}

// ChangeChargeCurrent is a deprecated alias for SetChargeCurrent.
func (ev *Victron_EV_Charger) ChangeChargeCurrent(current float64) {
	ev.SetChargeCurrent(current)
}

// SetCurrentLimits updates /MinCurrent, /SetCurrent, and /MaxCurrent atomically.
func (ev *Victron_EV_Charger) SetCurrentLimits(min, current, max float64) {
	ev.min_current.change(min)
	ev.set_current.setBounds(min, current, max)
	ev.max_current.change(max)
	ev.notify(&ev.min_current)
	ev.notify(&ev.set_current)
	ev.notify(&ev.max_current)
}

// SetCurrent is a deprecated alias for SetCurrentLimits.
func (ev *Victron_EV_Charger) SetCurrent(min, value, max float64) {
	ev.SetCurrentLimits(min, value, max)
}

// SetSessionEnergy sets /Session/Energy in kWh.
func (ev *Victron_EV_Charger) SetSessionEnergy(energy float64) {
	ev.session_energy.change(energy)
	ev.notify(&ev.session_energy)
}

// EnergyCharged is a deprecated alias for SetSessionEnergy.
func (ev *Victron_EV_Charger) EnergyCharged(energy float64) {
	ev.SetSessionEnergy(energy)
}

// SetTotalEnergy sets /Ac/Energy/Forward in kWh.
func (ev *Victron_EV_Charger) SetTotalEnergy(energy float64) {
	ev.energy_forward.change(energy)
	ev.notify(&ev.energy_forward)
}

// TotalCharged is a deprecated alias for SetTotalEnergy.
func (ev *Victron_EV_Charger) TotalCharged(energy float64) {
	ev.SetTotalEnergy(energy)
}

// SetSessionTime sets /Session/Time and /ChargingTime (deprecated alias) in seconds.
func (ev *Victron_EV_Charger) SetSessionTime(seconds float64) {
	ev.session_time.change(seconds)
	ev.charging_time.change(seconds)
	ev.notify(&ev.session_time)
	ev.notify(&ev.charging_time)
}

// EnergyTime is a deprecated alias for SetSessionTime.
func (ev *Victron_EV_Charger) EnergyTime(seconds float64) {
	ev.SetSessionTime(seconds)
}

// SetTemperature sets /MCU/Temperature in °C.
func (ev *Victron_EV_Charger) SetTemperature(temp float64) {
	ev.temperature.change(temp)
	ev.notify(&ev.temperature)
}

// Temperature is a deprecated alias for SetTemperature.
func (ev *Victron_EV_Charger) Temperature(temp float64) {
	ev.SetTemperature(temp)
}

func (ev *Victron_EV_Charger) SetStatus(status EV_Status) {
	ev.status.change(status)
	ev.notify(&ev.status)
}

// ChangeStatus is a deprecated alias for SetStatus.
func (ev *Victron_EV_Charger) ChangeStatus(status EV_Status) {
	ev.SetStatus(status)
}

func (ev *Victron_EV_Charger) SetMode(mode EV_Mode) {
	ev.mode.change(mode)
	ev.notify(&ev.mode)
}

// ChangeMode is a deprecated alias for SetMode.
func (ev *Victron_EV_Charger) ChangeMode(mode EV_Mode) {
	ev.SetMode(mode)
}

// SetStartStop syncs hardware→DBus /StartStop without triggering a callback.
func (ev *Victron_EV_Charger) SetStartStop(s EV_StartStop) {
	ev.startStop.change(s)
	ev.notify(&ev.startStop)
}
