package smartevse

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
	"victron_smartevse/victron"

	"github.com/hashicorp/mdns"
	"github.com/quokka2020/gohelpers/mqtthelper"
	"github.com/quokka2020/gohelpers/util"
)

var web = web_interface{}

type EvHandler struct {
	mqtt    *mqtthelper.Mqtt_Helper
	evs     []*SmartEVSE
	victron *victron.VictronHandler
}

type SmartEVSE struct {
	mqtt    *mqtthelper.Mqtt_Helper
	publish func(topic, payload string)

	Name       string
	IP         string
	SerialNr   int
	Version    string
	Prefix     string
	victron_ev *victron.Victron_EV_Charger

	current_min float64
	current     float64
	current_max float64
	charged     float64
	total       float64

	activeStrategyMode       string
	managedMode              victron.EV_Mode
	autoStart                victron.EV_AutoStart
	overrideCurrent          float64
	managedModeKnown         bool
	managedAutoControlActive bool

	mode        string
	evplugstate string
	state       string
	access      string

	session_time      float64
	last_session_time float64
}

var cfg_smartevse_ips = util.GetEnv("SMARTEVSE_IPS", "")

func NewEvHandler(mqtt *mqtthelper.Mqtt_Helper) (*EvHandler, error) {
	var err error
	handler := EvHandler{
		mqtt: mqtt,
	}

	err = handler.findSmartEVSEs()
	if err != nil {
		return nil, err
	}

	for _, ev := range handler.evs {
		err = ev.loadInfo()
		if err != nil {
			return nil, err
		}
		log.Printf("loaded: %s %d - %s", ev.Name, ev.SerialNr, ev.Prefix)
	}

	handler.evs = dedupeBySerial(handler.evs)

	for _, ev := range handler.evs {
		ev.subscribe(mqtt)
	}

	return &handler, nil
}

func dedupeBySerial(evs []*SmartEVSE) []*SmartEVSE {
	seen := map[int]string{}
	result := make([]*SmartEVSE, 0, len(evs))

	for _, ev := range evs {
		if prevIP, exists := seen[ev.SerialNr]; exists {
			log.Printf("Duplicate SmartEVSE serial %d detected (%s and %s); keeping the first entry", ev.SerialNr, prevIP, ev.IP)
			continue
		}
		seen[ev.SerialNr] = ev.IP
		result = append(result, ev)
	}

	return result
}

func (handler *EvHandler) Close() error {
	return nil
}

func (handler *EvHandler) findBySerial(serial int) (*SmartEVSE, error) {
	for _, evse := range handler.evs {
		if evse.SerialNr == serial {
			return evse, nil
		}
	}
	return nil, fmt.Errorf("smartevse with serial %d not found", serial)
}

func (handler *EvHandler) SetManagedAutoControl(serial int, active bool, overrideCurrent float64) error {
	evse, err := handler.findBySerial(serial)
	if err != nil {
		return err
	}
	evse.setManagedAutoControl(active, overrideCurrent)
	return nil
}

func (handler *EvHandler) findSmartEVSEs() error {
	if cfg_smartevse_ips != "" {
		ips := strings.SplitSeq(cfg_smartevse_ips, ",")
		for ip := range ips {
			handler.evs = append(handler.evs, &SmartEVSE{
				mqtt: handler.mqtt,
				// Name: strings.Trim(entry.Host, ".local."),
				IP: ip,
			})
		}
		return nil
	}
	entriesCh := make(chan *mdns.ServiceEntry, 4)
	defer close(entriesCh)
	go func() {
		for entry := range entriesCh {
			if strings.HasPrefix(entry.Host, "SmartEVSE-") {
				handler.evs = append(handler.evs, &SmartEVSE{
					mqtt: handler.mqtt,
					Name: strings.Trim(entry.Host, ".local."),
					IP:   entry.AddrV4.String(),
				})
			}
		}
	}()

	params := mdns.QueryParam{
		Service:     "_http._tcp",
		DisableIPv6: true,
		Entries:     entriesCh,
		Logger:      log.New(io.Discard, "", log.LstdFlags),
	}

	ctx := context.Background()
	// Start the lookup
	err := mdns.QueryContext(ctx, &params)
	if err != nil {
		log.Printf("failed to lookup err:%v", err)
		return err
	}

	return nil
}

func (ev *SmartEVSE) loadInfo() error {
	raw, err := web.settings(ev.IP)
	if err != nil {
		return err
	}
	ev.SerialNr = raw.SerialNr
	ev.Version = raw.Version
	if raw.MQTT == nil {
		return fmt.Errorf("mqtt is not configured")
	}
	ev.Prefix = raw.MQTT.Prefix
	if ev.Name == "" {
		ev.Name = fmt.Sprintf("smartevse-%d", ev.SerialNr)
	}

	ev.current_min = raw.Settings.Current_Min
	ev.current = raw.Settings.Charge_Current / 10
	ev.current_max = raw.Settings.Current_Max
	ev.overrideCurrent = raw.Settings.Override_Current / 10
	ev.autoStart = victron.EV_AutoStart_Enabled
	ev.mode = canonicalizeSmartEVSEMode(raw.Mode)
	if isStrategyMode(ev.mode) {
		ev.activeStrategyMode = ev.mode
	}
	if raw.Evse != nil {
		ev.state = raw.Evse.State
		ev.access = rawAccessToString(raw.Evse.Access)
	}
	if ev.activeStrategyMode == "" && ev.state != "" {
		ev.activeStrategyMode = inferStrategyModeFromState(ev.state)
	}
	if ev.activeStrategyMode != "" && !isPauseMode(ev.activeStrategyMode) {
		ev.managedMode = ev.managedModeFromRuntime()
		ev.managedModeKnown = true
	}

	if raw.EvMeter != nil {
		ev.total = raw.EvMeter.Total_Wh / 1000
		ev.charged = raw.EvMeter.Charged_Wh / 1000
	}

	ev.session_time = 0
	ev.last_session_time = 0
	return nil
}

func (ev *SmartEVSE) subscribe(mqtt *mqtthelper.Mqtt_Helper) {
	topic := fmt.Sprintf("%s/#", ev.Prefix)
	mqtt.AddStringSubscriptionFull(topic, ev.mqttReceived)
}

func (ev *SmartEVSE) findSub(topic string) string {
	if len(topic) < len(ev.Prefix)+2 {
		return topic
	}
	return topic[len(ev.Prefix)+1:]
}

var floatSubtopics = []string{
	"EVCurrentL1",
	"EVCurrentL2",
	"EVCurrentL3",
	"MaxCurrent",
	"ChargeCurrent",
	"CurrentOverride",
	"EVChargePower",
	"EVEnergyCharged",
	"EVTotalEnergyCharged",
	"ESPTemp",
}

func canonicalizeSmartEVSEMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "smart":
		return "Smart"
	case "solar":
		return "Solar"
	case "pause":
		return "Pause"
	case "off":
		return "Off"
	default:
		return strings.TrimSpace(mode)
	}
}

func rawAccessToString(access int) string {
	switch access {
	case 1:
		return "Allow"
	case 0:
		return "Deny"
	default:
		return ""
	}
}

func isPauseMode(mode string) bool {
	mode = canonicalizeSmartEVSEMode(mode)
	return mode == "Pause"
}

func isStrategyMode(mode string) bool {
	mode = canonicalizeSmartEVSEMode(mode)
	return mode == "Smart" || mode == "Solar"
}

func inferStrategyModeFromState(state string) string {
	state = strings.ToLower(strings.TrimSpace(state))
	if strings.Contains(state, "solar") || strings.Contains(state, "no power available") {
		return "Solar"
	}
	if state != "" {
		return "Smart"
	}
	return ""
}

func managedModeFromStrategy(strategy string) victron.EV_Mode {
	if canonicalizeSmartEVSEMode(strategy) == "Solar" {
		return victron.EV_Mode_Auto
	}
	return victron.EV_Mode_Manual
}

func (ev *SmartEVSE) managedModeFromRuntime() victron.EV_Mode {
	if ev.activeStrategyMode == "Smart" && ev.managedAutoControlActive {
		return victron.EV_Mode_Auto
	}
	return managedModeFromStrategy(ev.activeStrategyMode)
}

func (ev *SmartEVSE) effectiveManagedMode() victron.EV_Mode {
	if ev.managedModeKnown {
		return ev.managedMode
	}
	if ev.activeStrategyMode != "" {
		return managedModeFromStrategy(ev.activeStrategyMode)
	}
	return victron.EV_Mode_Manual
}

func (ev *SmartEVSE) targetStrategyForManagedMode() string {
	switch ev.effectiveManagedMode() {
	case victron.EV_Mode_Manual:
		return "Smart"
	case victron.EV_Mode_Auto:
		if ev.managedAutoControlActive {
			return "Smart"
		}
		return "Solar"
	default:
		return ""
	}
}

func (ev *SmartEVSE) isPaused() bool {
	return isPauseMode(ev.mode)
}

func (ev *SmartEVSE) deriveStatus() victron.EV_Status {
	if ev.evplugstate == "Disconnected" {
		return victron.EV_Status_Disconnected
	}
	if ev.isPaused() {
		return victron.EV_Status_Waiting_for_start
	}

	state := strings.ToLower(strings.TrimSpace(ev.state))
	solarStrategy := ev.targetStrategyForManagedMode() == "Solar"

	switch {
	case strings.Contains(state, "waiting for rfid"):
		return victron.EV_Status_Waiting_for_RFID
	case strings.Contains(state, "charged"):
		return victron.EV_Status_Charged
	case strings.Contains(state, "charging") && !strings.Contains(state, "stopped") && !strings.Contains(state, "stop charging"):
		return victron.EV_Status_Charging
	case strings.Contains(state, "no power available") && solarStrategy:
		return victron.EV_Status_Waiting_for_sun
	case strings.Contains(state, "solar"):
		return victron.EV_Status_Waiting_for_sun
	case strings.Contains(state, "ready to charge"):
		return victron.EV_Status_Waiting_for_start
	case strings.Contains(state, "connected to ev"):
		return victron.EV_Status_Connected
	case strings.Contains(state, "charging stopped") && solarStrategy:
		return victron.EV_Status_Waiting_for_sun
	case strings.Contains(state, "charging stopped"):
		return victron.EV_Status_Connected
	case strings.Contains(state, "stop charging"):
		return victron.EV_Status_Connected
	default:
		return victron.EV_Status_Connected
	}
}

func (ev *SmartEVSE) syncVictronState() {
	if ev.victron_ev == nil {
		return
	}
	ev.victron_ev.SetMode(ev.effectiveManagedMode())
	if ev.isPaused() {
		ev.victron_ev.SetStartStop(victron.EV_StartStop_Stop)
	} else {
		ev.victron_ev.SetStartStop(victron.EV_StartStop_Start)
	}
	ev.victron_ev.SetAutoStart(ev.autoStart)
	ev.victron_ev.SetStatus(ev.deriveStatus())
}

func (ev *SmartEVSE) publishFullTopic(topic, payload string) {
	if ev.publish != nil {
		ev.publish(topic, payload)
		return
	}
	if ev.mqtt != nil {
		ev.mqtt.PublishFullTopic(topic, payload)
	}
}

func (ev *SmartEVSE) applyRunningModeLocally(mode string) {
	mode = canonicalizeSmartEVSEMode(mode)
	ev.mode = mode
	if isStrategyMode(mode) {
		ev.activeStrategyMode = mode
	}
}

func (ev *SmartEVSE) requestRunningMode(mode string) {
	mode = canonicalizeSmartEVSEMode(mode)
	if mode == "" {
		return
	}
	topic := fmt.Sprintf("%s/Set/Mode", ev.Prefix)
	ev.publishFullTopic(topic, mode)
	ev.applyRunningModeLocally(mode)
	ev.syncVictronState()
}

func (ev *SmartEVSE) setManagedAutoControl(active bool, overrideCurrent float64) {
	ev.managedAutoControlActive = active
	if active {
		ev.overrideCurrent = overrideCurrent
	} else {
		ev.overrideCurrent = 0
	}

	topic := fmt.Sprintf("%s/Set/CurrentOverride", ev.Prefix)
	payload := fmt.Sprintf("%d", int32(math.RoundToEven(ev.overrideCurrent*10)))
	ev.publishFullTopic(topic, payload)

	if ev.effectiveManagedMode() == victron.EV_Mode_Auto && !ev.isPaused() {
		ev.requestRunningMode(ev.targetStrategyForManagedMode())
		return
	}
	ev.syncVictronState()
}

func (ev *SmartEVSE) clearManagedAutoControl() {
	ev.setManagedAutoControl(false, 0)
}

func (ev *SmartEVSE) clearPauseOnReconnectIfNeeded(previousPlugState string) {
	if previousPlugState == "Disconnected" && ev.evplugstate == "Connected" && ev.autoStart == victron.EV_AutoStart_Enabled && ev.isPaused() {
		ev.requestRunningMode(ev.targetStrategyForManagedMode())
	}
}

func (ev *SmartEVSE) enforcePauseOnConnectIfAutoStartDisabled(previousPlugState string) {
	//log.Printf("enforcePauseOnConnectIfAutoStartDisabled: previousPlugState=%s evplugstate=%s autoStart=%d isPaused=%t", previousPlugState, ev.evplugstate, ev.autoStart, ev.isPaused())
	if previousPlugState != "Connected" && ev.evplugstate == "Connected" && ev.autoStart == victron.EV_AutoStart_Disabled && !ev.isPaused() {
		ev.requestRunningMode("Pause")
	}
}

func (ev *SmartEVSE) mqttReceived(topic string, value string) {
	if ev.victron_ev == nil {
		log.Printf("No victron_ev yet, dropping topic:%s payload:%s", topic, value)
		return
	}
	sub := ev.findSub(topic)
	if sub == topic {
		log.Printf("invalid topic %s", topic)
		return
	}
	updateState := false

	// string values
	switch sub {
	case "connected":
		ev.victron_ev.SetConnected(value == "online")
		return
	case "Access":
		ev.access = value
	case "State":
		ev.state = value
		updateState = true
	case "EVPlugState":
		previousPlugState := ev.evplugstate
		ev.evplugstate = value
		updateState = true
		ev.enforcePauseOnConnectIfAutoStartDisabled(previousPlugState)
		ev.clearPauseOnReconnectIfNeeded(previousPlugState)
	case "Error":
	case "Mode":
		ev.applyRunningModeLocally(value)
		if isStrategyMode(value) {
			if !(ev.managedModeKnown && ev.targetStrategyForManagedMode() == canonicalizeSmartEVSEMode(value)) {
				ev.managedMode = ev.managedModeFromRuntime()
				ev.managedModeKnown = true
			}
		}
		updateState = true
	}
	if updateState {
		if ev.evplugstate == "Disconnected" {
			ev.session_time = 0
			ev.last_session_time = 0
			ev.victron_ev.SetSessionTime(ev.session_time)
		}
		ev.syncVictronState()
		return
	}

	if !slices.Contains(floatSubtopics, sub) {
		return
	}
	// float values
	i, err := strconv.ParseFloat(value, 64)
	if err != nil {
		log.Printf("Got a non-number from %s with payload [%s] err:%v", topic, value, err)
		return
	}
	switch sub {
	case "EVCurrentL1":
		ev.victron_ev.ChangeCurrentL1(i / 10)
		if i/10 > 0 {
			ev.setSessionTime()
		}
	case "EVCurrentL2":
		ev.victron_ev.ChangeCurrentL2(i / 10)
		if i/10 > 0 {
			ev.setSessionTime()
		}
	case "EVCurrentL3":
		ev.victron_ev.ChangeCurrentL3(i / 10)
		if i/10 > 0 {
			ev.setSessionTime()
		}
	case "MaxCurrent":
		ev.victron_ev.SetMaxCurrent(i / 10)
	case "ChargeCurrent":
		ev.victron_ev.SetChargeCurrent(i / 10)
	case "CurrentOverride":
		ev.overrideCurrent = i / 10
	case "EVChargePower":
		ev.victron_ev.SetAcPower(i)
	case "EVEnergyCharged":
		ev.victron_ev.SetSessionEnergy(i / 1000)
	case "EVTotalEnergyCharged":
		ev.victron_ev.SetTotalEnergy(i / 1000)
	case "ESPTemp":
		ev.victron_ev.SetTemperature(i)
	}
}

func (ev *SmartEVSE) setSessionTime() {
	now := time.Now().Unix()
	if ev.last_session_time > 0 {
		ev.session_time += float64(now) - ev.last_session_time
		ev.victron_ev.SetSessionTime(ev.session_time)
	}
	ev.last_session_time = float64(now)
}

func (evse *SmartEVSE) modeChangedCallback(mode victron.EV_Mode) error {
	log.Printf("Request to change mode to: %d", mode)
	switch mode {
	case victron.EV_Mode_Manual:
		evse.managedMode = mode
		evse.managedModeKnown = true
		if !evse.isPaused() {
			evse.requestRunningMode(evse.targetStrategyForManagedMode())
		} else {
			evse.syncVictronState()
		}
	case victron.EV_Mode_Auto:
		evse.managedMode = mode
		evse.managedModeKnown = true
		if !evse.isPaused() {
			evse.requestRunningMode(evse.targetStrategyForManagedMode())
		} else {
			evse.syncVictronState()
		}
	case victron.EV_Mode_Scheduled:
		return errors.New("/Mode=Scheduled is not supported by SmartEVSE; use Manual or Auto")
	default:
		log.Printf("Don't know what to do for mode:%d", mode)
	}
	return nil
}

func (evse *SmartEVSE) setOverrideCurrentChangedCallback(value, _, max float64) {
	if value == max {
		value = 0 // 0 means no override, so if the value is the same as max, we can set it to 0 to disable the override
	}
	evse.overrideCurrent = value
	log.Printf("Request to change override current to: %f", value)
	topic := fmt.Sprintf("%s/Set/CurrentOverride", evse.Prefix)
	payload := fmt.Sprintf("%d", int32(math.RoundToEven(value*10)))
	evse.publishFullTopic(topic, payload)
}

func (evse *SmartEVSE) startStopChangedCallback(mode victron.EV_StartStop) {
	log.Printf("Request to change start/stop to: %d", mode)
	if mode == victron.EV_StartStop_Stop {
		evse.requestRunningMode("Pause")
		return
	}
	evse.requestRunningMode(evse.targetStrategyForManagedMode())
}

func (evse *SmartEVSE) autoStartChangedCallback(mode victron.EV_AutoStart) {
	log.Printf("Request to change autostart to: %d", mode)
	evse.autoStart = mode
	evse.syncVictronState()
}

func (ev *EvHandler) WriteMainsmeter() {
	l1, l2, l3 := ev.victron.Grid()
	//log.Printf("Grid L1:%f L2:%f L3:%f", l1, l2, l3)

	payload := fmt.Sprintf("%d:%d:%d", int32(math.RoundToEven(l1*10)), int32(math.RoundToEven(l2*10)), int32(math.RoundToEven(l3*10)))
	for _, smartevse := range ev.evs {
		topic := fmt.Sprintf("%s/Set/MainsMeter", smartevse.Prefix)
		ev.mqtt.PublishFullTopic(topic, payload)
	}
}

func (ev *EvHandler) WriteHomebattery() {
	battery := ev.victron.BatteryCurrent()
	//log.Printf("Battery current:%f", battery)

	payload := fmt.Sprintf("%d", int32(math.RoundToEven(battery*10)))
	for _, smartevse := range ev.evs {
		topic := fmt.Sprintf("%s/Set/HomeBatteryCurrent", smartevse.Prefix)
		ev.mqtt.PublishFullTopic(topic, payload)
	}
}
