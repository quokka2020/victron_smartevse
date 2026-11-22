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

type EvCharger struct {
	parent          *Handler
	service         *Service
	s2              *s2ResourceManager
	running         bool
	constantPaths   map[string]BusItem
	modifiablePaths map[string]BusItem

	s2ControlCallback func(cmd S2ChargeCommand)
	s2ReleaseCallback func()
	evPhases          int
	modePersisted     bool

	connected ManualBusItem // /Connected  int32: 0=offline 1=online

	power   UnitBusItem // /Ac/Power
	powerL1 UnitBusItem // /Ac/L1/Power
	powerL2 UnitBusItem // /Ac/L2/Power
	powerL3 UnitBusItem // /Ac/L3/Power

	current    UnitBusItem       // /Current
	setCurrent MinMaxUnitBusItem // /SetCurrent (writable, bounded)
	maxCurrent UnitBusItem       // /MaxCurrent
	minCurrent UnitBusItem       // /MinCurrent

	energyForward UnitBusItem // /Ac/Energy/Forward
	sessionEnergy UnitBusItem // /Session/Energy
	sessionTime   UnitBusItem // /Session/Time
	chargingTime  UnitBusItem // /ChargingTime (deprecated alias, kept in sync with sessionTime)
	sessionCost   UnitBusItem // /Session/Cost

	temperature UnitBusItem // /MCU/Temperature

	status    EvStatusBusItem
	mode      EvModeBusItem
	autostart EvAutoStartBusItem
	startStop EvStartStopBusItem
	position  EvPositionBusItem // /Position

	s2Active           BoolBusItem // /S2/0/Active
	s2MaxChargePower   UnitBusItem // /S2/0/RmSettings/MaxChargePower
	s2RememberEvPhases BoolBusItem // /S2/0/RmSettings/RememberEvPhases
	gxChargingCurrent  UnitBusItem // /GxAutoMode/ChargingCurrent (current requested by S2 control)
}

func newEvChargerFields(parent *Handler, min, current, max, sessionEnergy, total float64) EvCharger {
	return EvCharger{
		parent:    parent,
		evPhases:  s2DefaultEVPhases,
		connected: *NewManualBusItem(int32(0), "Disconnected"),

		power:   NewUnitFormatterObject(0, "W", 1),
		powerL1: NewUnitFormatterObject(0, "W", 1),
		powerL2: NewUnitFormatterObject(0, "W", 1),
		powerL3: NewUnitFormatterObject(0, "W", 1),

		current:       NewUnitFormatterObject(current, "A", 1),
		setCurrent:    NewMinMaxUnitBusItem(current, min, max, "A", 0),
		maxCurrent:    NewUnitFormatterObject(max, "A", 0),
		minCurrent:    NewUnitFormatterObject(min, "A", 0),
		energyForward: NewUnitFormatterObject(total, "kWh", 3),
		sessionEnergy: NewUnitFormatterObject(sessionEnergy, "kWh", 3),
		sessionTime:   NewUnitFormatterObject(0, "s", 0),
		chargingTime:  NewUnitFormatterObject(0, "s", 0),
		sessionCost:   NewUnitFormatterObject(0, "", 2),

		temperature: NewUnitFormatterObject(20, "C", 0),

		status:    NewEvStatusBusItem(EV_Status_Disconnected),
		mode:      NewEvModeBusItem(EV_Mode_Manual),
		autostart: NewEvAutoStartBusItem(EV_AutoStart_Enabled),
		startStop: NewEvStartStopBusItem(EvStartStopStop),
		position:  NewEvPositionBusItem(EV_Position_AC_Output),

		s2Active:           NewBoolBusItem(false),
		s2MaxChargePower:   NewUnitFormatterObject(max*230*3, "W", 0),
		s2RememberEvPhases: NewBoolBusItem(false),
		gxChargingCurrent:  NewUnitFormatterObject(0, "A", 0),
	}
}

func (ev *EvCharger) initModifyableItems() {
	ev.modifiablePaths = map[string]BusItem{
		"/Connected":                        &ev.connected,
		"/Status":                           &ev.status,
		"/Ac/Power":                         &ev.power,
		"/Ac/L1/Power":                      &ev.powerL1,
		"/Ac/L2/Power":                      &ev.powerL2,
		"/Ac/L3/Power":                      &ev.powerL3,
		"/Current":                          &ev.current,
		"/SetCurrent":                       &ev.setCurrent,
		"/MaxCurrent":                       &ev.maxCurrent,
		"/MinCurrent":                       &ev.minCurrent,
		"/Ac/Energy/Forward":                &ev.energyForward,
		"/Session/Energy":                   &ev.sessionEnergy,
		"/Session/Time":                     &ev.sessionTime,
		"/ChargingTime":                     &ev.chargingTime,
		"/Session/Cost":                     &ev.sessionCost,
		"/MCU/Temperature":                  &ev.temperature,
		"/AutoStart":                        &ev.autostart,
		"/StartStop":                        &ev.startStop,
		"/Mode":                             &ev.mode,
		"/Position":                         &ev.position,
		"/S2/0/Active":                      &ev.s2Active,
		"/S2/0/RmSettings/MaxChargePower":   &ev.s2MaxChargePower,
		"/S2/0/RmSettings/RememberEvPhases": &ev.s2RememberEvPhases,
		"/GxAutoMode/ChargingCurrent":       &ev.gxChargingCurrent,
	}
}

func (h *Handler) CreateEvCharger(serial int, version, connection string, min, current, max float64, charged float64, total float64) (*EvCharger, error) {
	var err error

	ev := newEvChargerFields(h, min, current, max, charged, total)

	deviceName := fmt.Sprintf("SmartEVSE-%d", serial)
	serviceName := "com.victronenergy.evcharger." + deviceName

	ev.service, err = h.NewService(serviceName)
	if err != nil {
		return nil, fmt.Errorf("failed to create service: %w", err)
	}

	// From here on we need to call close
	var deviceInstance int
	deviceInstance, err = ev.service.GetOrCreateDeviceInstance()
	if err != nil {
		return ev.returnAndClose(fmt.Errorf("failed to get device instance: %w", err))
	}

	if err := ev.restorePersistentSettings(); err != nil {
		return ev.returnAndClose(fmt.Errorf("failed to restore persistent settings: %w", err))
	}
	// Set internal callbacks for persistence management - these are not exposed publicly
	ev.setAutoStartChangedCallback(ev.handleAutoStartChanged)
	ev.setPositionChangedCallback(ev.handlePositionChanged)
	ev.s2MaxChargePower.callback = ev.handleS2MaxChargePowerChanged
	ev.s2RememberEvPhases.callback = ev.handleS2RememberEvPhasesChanged

	ev.constantPaths = map[string]BusItem{
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

	for path, value := range ev.constantPaths {
		if err := ev.service.AddPath(path, value); err != nil {
			return ev.returnAndClose(fmt.Errorf("failed to add path %s: %w", path, err))
		}
	}

	// Must be called after the struct is in its final heap location so that
	// all pointers in the map reference fields of this ev, not a copy.
	ev.initModifyableItems()

	for path, value := range ev.modifiablePaths {
		if err := ev.service.AddPath(path, value); err != nil {
			return ev.returnAndClose(fmt.Errorf("failed to add path %s: %w", path, err))
		}
	}

	// Emit initial values for persistent settings (AutoStart and Position)
	ev.notify(&ev.autostart)
	ev.notify(&ev.position)

	ev.s2 = newS2ResourceManager(h.dbusConn, deviceName)
	ev.s2.defaultControlType = ""
	ev.s2.ombcReadyReader = ev.IsOmbcReady
	ev.s2.powerL1Reader = ev.GetAcL1Power
	ev.s2.powerL2Reader = ev.GetAcL2Power
	ev.s2.powerL3Reader = ev.GetAcL3Power
	ev.s2.maxChargePowerReader = ev.GetS2MaxChargePower
	ev.s2.minCurrentReader = func() float64 { return ev.minCurrent.value }
	ev.s2.maxCurrentReader = func() float64 { return ev.maxCurrent.value }
	ev.s2.phasesReader = ev.EvPhases
	ev.s2.autoModeReader = func() bool { return ev.mode.mode == EV_Mode_Auto }
	ev.s2.chargerStateReader = func() (bool, float64) {
		return ev.status.status == EV_Status_Charging, ev.current.value
	}
	ev.s2.controlCallback = ev.applyS2Command
	ev.s2.releaseCallback = ev.releaseS2Control
	ev.s2.sessionActiveChanged = ev.setS2Active
	ev.mode.changed = func(oldMode, newMode EV_Mode) {
		ev.persistMode(newMode)
		ev.s2.onModeChanged(oldMode, newMode)
	}
	ev.mode.written = ev.ensureModePersisted
	if err := ev.s2.Export(); err != nil {
		return ev.returnAndClose(fmt.Errorf("failed to export s2 resource manager: %w", err))
	}

	err = ev.service.Register()
	if err != nil {
		return ev.returnAndClose(fmt.Errorf("failed to register service: %w", err))
	}

	go func() {
		ev.running = true
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		for range ticker.C {
			if !ev.running {
				return
			}
			ev.PublishUpdates()
		}
	}()

	return &ev, nil
}

func (ev *EvCharger) restorePersistentSettings() error {
	persistedMode, err := ev.service.GetOrCreateDeviceIntSetting("Mode", -1, -1, int32(EV_Mode_Scheduled))
	if err != nil {
		return err
	}
	if persistedMode != -1 {
		if _, ok := ev_mode[EV_Mode(persistedMode)]; !ok {
			return fmt.Errorf("invalid persisted /Mode: %d", persistedMode)
		}
		ev.mode.change(EV_Mode(persistedMode))
		ev.modePersisted = true
	}

	persistedAutoStart, err := ev.service.GetOrCreateDeviceIntSetting("AutoStart", int32(ev.autostart.autostart), int32(EV_AutoStart_Disabled), int32(EV_AutoStart_Enabled))
	if err != nil {
		return err
	}
	if _, ok := ev_autostart[EV_AutoStart(persistedAutoStart)]; ok {
		ev.autostart.change(EV_AutoStart(persistedAutoStart))
	}

	persistedPosition, err := ev.service.GetOrCreateDeviceIntSetting("Position", int32(ev.position.position), int32(EV_Position_AC_Output), int32(EV_Position_AC_Input))
	if err != nil {
		return err
	}
	if _, ok := ev_position_text[EV_Position(persistedPosition)]; ok {
		ev.position.change(EV_Position(persistedPosition))
	}

	persistedMaxChargePower, err := ev.service.GetOrCreateDeviceFloatSetting(s2MaxChargePowerSetting, ev.s2MaxChargePower.value, 0, 100000)
	if err != nil {
		return err
	}
	if persistedMaxChargePower >= 0 {
		ev.s2MaxChargePower.change(persistedMaxChargePower)
	}

	persistedRememberEvPhases, err := ev.service.GetOrCreateDeviceIntSetting(s2RememberEvPhasesSetting, boolToInt32(ev.s2RememberEvPhases.value), 0, 1)
	if err != nil {
		return err
	}
	ev.s2RememberEvPhases.change(persistedRememberEvPhases != 0)

	return nil
}

const (
	s2MaxChargePowerSetting   = "S2MaxChargePower"
	s2RememberEvPhasesSetting = "S2RememberEvPhases"
)

func boolToInt32(value bool) int32 {
	if value {
		return 1
	}
	return 0
}

func (ev *EvCharger) handleS2MaxChargePowerChanged(value float64) {
	if err := ev.service.SetDeviceFloatSetting(s2MaxChargePowerSetting, value); err != nil {
		log.Printf("persist /S2/0/RmSettings/MaxChargePower: %v", err)
	}
}

func (ev *EvCharger) handleS2RememberEvPhasesChanged(value bool) {
	if err := ev.service.SetDeviceIntSetting(s2RememberEvPhasesSetting, boolToInt32(value)); err != nil {
		log.Printf("persist /S2/0/RmSettings/RememberEvPhases: %v", err)
	}
}

func (ev *EvCharger) handleAutoStartChanged(mode EV_AutoStart) {
	if err := ev.service.SetDeviceIntSetting("AutoStart", int32(mode)); err != nil {
		log.Printf("persist /AutoStart: %v", err)
	}
}

func (ev *EvCharger) handlePositionChanged(position EV_Position) {
	if err := ev.service.SetDeviceIntSetting("Position", int32(position)); err != nil {
		log.Printf("persist /Position: %v", err)
	}
}

func (ev *EvCharger) persistMode(mode EV_Mode) {
	if err := ev.service.SetDeviceIntSetting("Mode", int32(mode)); err != nil {
		ev.modePersisted = false
		log.Printf("persist /Mode: %v", err)
		return
	}
	ev.modePersisted = true
}

func (ev *EvCharger) ensureModePersisted(mode EV_Mode) {
	if !ev.modePersisted {
		ev.persistMode(mode)
	}
}

// HasPersistedMode distinguishes a saved mode from a newly created setting.
func (ev *EvCharger) HasPersistedMode() bool {
	return ev.modePersisted
}

func (ev *EvCharger) returnAndClose(err error) (*EvCharger, error) {
	ev.Close()
	if ev.service != nil {
		_ = ev.service.Close()
	}
	return nil, err
}

func (ev *EvCharger) Close() {
	ev.running = false
	if ev.s2 != nil {
		ev.s2.Close()
	}
}

// notify emits an immediate PropertiesChanged signal for the given item so
// that the GX display reflects changes without waiting for the heartbeat ticker.
func (ev *EvCharger) notify(item BusItem) {
	if ev.service != nil {
		if err := ev.service.PropertiesChanged(item); err != nil {
			log.Printf("notify %s: PropertiesChanged error: %v", item.getObjectPath(), err)
		}
	}
}

func (ev *EvCharger) SetModeChangedCallback(callback func(mode EV_Mode) error) {
	ev.mode.callback = callback
}

func (ev *EvCharger) SetOverrideCurrentChangedCallback(callback func(overrideCurrent, min, max float64)) {
	ev.setCurrent.callback = callback
}

func (ev *EvCharger) SetStartStopChangedCallback(callback func(mode EvStartStop)) {
	ev.startStop.callback = callback
}

func (ev *EvCharger) setAutoStartChangedCallback(callback func(mode EV_AutoStart)) {
	ev.autostart.callback = callback
}

func (ev *EvCharger) setPositionChangedCallback(callback func(position EV_Position)) {
	ev.position.callback = callback
}

func (ev *EvCharger) PublishUpdates() {
	ev.service.emitItemsChanged(ev.modifiablePaths)
}

func (ev *EvCharger) PublishConstants() {
	ev.service.emitItemsChanged(ev.constantPaths)
}

func (ev *EvCharger) SetConnected(connected bool) {
	if connected {
		ev.connected.change(int32(1), "Connected")
	} else {
		ev.connected.change(int32(0), "Disconnected")
	}
	ev.notify(&ev.connected)
}

// ChangeConnected is a deprecated alias for SetConnected.
func (ev *EvCharger) ChangeConnected(connected bool) {
	ev.SetConnected(connected)
}

func (ev *EvCharger) SetAcPower(power float64) {
	ev.power.change(power)
	ev.notify(&ev.power)
}

func (ev *EvCharger) GetAcPower() float64 {
	return ev.power.value
}

func (ev *EvCharger) GetAcL1Power() float64 {
	return ev.powerL1.value
}

func (ev *EvCharger) GetAcL2Power() float64 {
	return ev.powerL2.value
}

func (ev *EvCharger) GetAcL3Power() float64 {
	return ev.powerL3.value
}

func (ev *EvCharger) IsOmbcReady() bool {
	if ev.parent == nil {
		return false
	}
	return ev.parent.acOutL1V.lastValue > 0
}

func (ev *EvCharger) GetS2MaxChargePower() float64 {
	return ev.s2MaxChargePower.value
}

// SetS2ControlCallback registers the handler that drives the charger for the
// operation mode selected by an S2 CEM (Dynamic ESS, Opportunity Loads).
func (ev *EvCharger) SetS2ControlCallback(callback func(cmd S2ChargeCommand)) {
	ev.s2ControlCallback = callback
}

// SetS2ReleaseCallback registers the handler that returns the charger to its
// normal behaviour when S2 control ends.
func (ev *EvCharger) SetS2ReleaseCallback(callback func()) {
	ev.s2ReleaseCallback = callback
}

// ResetS2OnPlugIn reports Standby to the active S2 controller for a new EV.
func (ev *EvCharger) ResetS2OnPlugIn() {
	ev.setGxChargingCurrent(0)
	ev.s2.onVehiclePluggedIn()
}

func (ev *EvCharger) applyS2Command(cmd S2ChargeCommand) {
	if cmd.Charge {
		ev.setGxChargingCurrent(cmd.CurrentA)
	} else {
		ev.setGxChargingCurrent(0)
	}
	if ev.s2ControlCallback != nil {
		ev.s2ControlCallback(cmd)
	}
}

func (ev *EvCharger) releaseS2Control() {
	ev.setGxChargingCurrent(0)
	if ev.s2ReleaseCallback != nil {
		ev.s2ReleaseCallback()
	}
}

func (ev *EvCharger) setGxChargingCurrent(current float64) {
	ev.gxChargingCurrent.change(current)
	ev.notify(&ev.gxChargingCurrent)
}

// SetEvPhases sets the number of phases the connected EV charges on. It is
// used to build the S2 charge model.
func (ev *EvCharger) SetEvPhases(phases int) {
	if phases < 1 || phases > s2DefaultMaxPhase {
		return
	}
	ev.evPhases = phases
}

// ResetEvPhases restores the default assumption for a new EV.
func (ev *EvCharger) ResetEvPhases() {
	ev.SetEvPhases(s2DefaultEVPhases)
}

func (ev *EvCharger) EvPhases() int {
	return ev.evPhases
}

// RememberEvPhases reports /S2/0/RmSettings/RememberEvPhases: keep the detected
// phase count after the EV is unplugged.
func (ev *EvCharger) RememberEvPhases() bool {
	return ev.s2RememberEvPhases.value
}

func (ev *EvCharger) Mode() EV_Mode {
	return ev.mode.mode
}

func (ev *EvCharger) setS2Active(active bool) {
	ev.s2Active.change(active)
	ev.notify(&ev.s2Active)
}

// ChangeChargePower is a deprecated alias for SetAcPower.
func (ev *EvCharger) ChangeChargePower(power float64) {
	ev.SetAcPower(power)
}

func (ev *EvCharger) SetAcL1Power(power float64) {
	ev.powerL1.change(power)
	ev.notify(&ev.powerL1)
}

func (ev *EvCharger) SetAcL2Power(power float64) {
	ev.powerL2.change(power)
	ev.notify(&ev.powerL2)
}

func (ev *EvCharger) SetAcL3Power(power float64) {
	ev.powerL3.change(power)
	ev.notify(&ev.powerL3)
}

// ChangeCurrentL1 is a deprecated alias; callers should use SetAcL1Power with voltage * current.
func (ev *EvCharger) ChangeCurrentL1(current float64) {
	var voltage float64
	if ev.parent != nil {
		if ev.Position() == EV_Position_AC_Output {
			voltage = ev.parent.acOutL1V.lastValue
		} else {
			voltage = ev.parent.acInL1V.lastValue
		}
	} else {
		voltage = 0
	}
	ev.SetAcL1Power(voltage * current)
}

// ChangeCurrentL2 is a deprecated alias; callers should use SetAcL2Power with voltage * current.
func (ev *EvCharger) ChangeCurrentL2(current float64) {
	var voltage float64
	if ev.parent != nil {
		if ev.Position() == EV_Position_AC_Output {
			voltage = ev.parent.acOutL2V.lastValue
		} else {
			voltage = ev.parent.acInL2V.lastValue
		}
	} else {
		voltage = 0
	}
	ev.SetAcL2Power(voltage * current)
}

// ChangeCurrentL3 is a deprecated alias; callers should use SetAcL3Power with voltage * current.
func (ev *EvCharger) ChangeCurrentL3(current float64) {
	var voltage float64
	if ev.parent != nil {
		if ev.Position() == EV_Position_AC_Output {
			voltage = ev.parent.acOutL3V.lastValue
		} else {
			voltage = ev.parent.acInL3V.lastValue
		}
	} else {
		voltage = 0
	}
	ev.SetAcL3Power(voltage * current)
}

func (ev *EvCharger) SetMaxCurrent(current float64) {
	ev.maxCurrent.change(current)
	ev.notify(&ev.maxCurrent)
}

func (ev *EvCharger) SetChargeCurrent(current float64) {
	ev.current.change(current)
	ev.notify(&ev.current)
}

// SetCurrentLimits updates /MinCurrent, /SetCurrent, and /MaxCurrent atomically.
func (ev *EvCharger) SetCurrentLimits(min, current, max float64) {
	ev.minCurrent.change(min)
	ev.setCurrent.setBounds(min, current, max)
	ev.maxCurrent.change(max)
	ev.notify(&ev.minCurrent)
	ev.notify(&ev.setCurrent)
	ev.notify(&ev.maxCurrent)
}

// CurrentLimits returns the /SetCurrent bounds and value as min, current, max.
func (ev *EvCharger) CurrentLimits() (float64, float64, float64) {
	return ev.setCurrent.min, ev.setCurrent.value, ev.setCurrent.max
}

// SetSessionEnergy sets /Session/Energy in kWh.
func (ev *EvCharger) SetSessionEnergy(energy float64) {
	ev.sessionEnergy.change(energy)
	ev.notify(&ev.sessionEnergy)
}

// SetTotalEnergy sets /Ac/Energy/Forward in kWh.
func (ev *EvCharger) SetTotalEnergy(energy float64) {
	ev.energyForward.change(energy)
	ev.notify(&ev.energyForward)
}

// SetSessionTime sets /Session/Time and /ChargingTime (deprecated alias) in seconds.
func (ev *EvCharger) SetSessionTime(seconds float64) {
	ev.sessionTime.change(seconds)
	ev.chargingTime.change(seconds)
	ev.notify(&ev.sessionTime)
	ev.notify(&ev.chargingTime)
}

// SetTemperature sets /MCU/Temperature in °C.
func (ev *EvCharger) SetTemperature(temp float64) {
	ev.temperature.change(temp)
	ev.notify(&ev.temperature)
}

func (ev *EvCharger) SetStatus(status EV_Status) {
	ev.status.change(status)
	ev.notify(&ev.status)
}

func (ev *EvCharger) SetMode(mode EV_Mode) {
	ev.mode.change(mode)
	ev.ensureModePersisted(mode)
	ev.notify(&ev.mode)
}

// SetStartStop syncs hardware→DBus /StartStop without triggering a callback.
func (ev *EvCharger) SetStartStop(s EvStartStop) {
	ev.startStop.change(s)
	ev.notify(&ev.startStop)
}

// SetAutoStart syncs hardware/internal bridge state→DBus /AutoStart without triggering a callback.
func (ev *EvCharger) SetAutoStart(a EV_AutoStart) {
	ev.autostart.change(a)
	ev.notify(&ev.autostart)
}

func (ev *EvCharger) AutoStart() EV_AutoStart {
	return ev.autostart.autostart
}

func (ev *EvCharger) Position() EV_Position {
	return ev.position.position
}
