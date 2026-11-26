package smartevse

import (
	"context"
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

var web = webInterface{}

type EvHandler struct {
	mqtt    *mqtthelper.Mqtt_Helper
	evs     []*SmartEVSE
	victron *victron.Handler
}

type SmartEVSE struct {
	mqtt    *mqtthelper.Mqtt_Helper
	publish func(topic, payload string)

	Name        string
	IP          string
	SerialNr    int
	Version     string
	Prefix      string
	victronEvcs *victron.EvCharger

	current    float64
	currentMin float64
	currentMax float64
	charged    float64
	total      float64

	activeStrategyMode       string
	managedMode              victron.EV_Mode
	overrideCurrent          float64
	managedModeKnown         bool
	managedAutoControlActive bool

	mode        string
	evPlugState string
	state       string
	access      string

	sessionTime     float64
	lastSessionTime float64

	s2 s2State
}

var smartevseIps = util.GetEnv("SMARTEVSE_IPS", "")

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
	for _, ev := range handler.evs {
		if ev != nil && ev.victronEvcs != nil {
			ev.victronEvcs.Close()
		}
	}
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
	if smartevseIps != "" {
		ips := strings.SplitSeq(smartevseIps, ",")
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

func (smartEvse *SmartEVSE) loadInfo() error {
	raw, err := web.settings(smartEvse.IP)
	if err != nil {
		return err
	}
	smartEvse.SerialNr = raw.SerialNr
	smartEvse.Version = raw.Version
	if raw.MQTT == nil {
		return fmt.Errorf("mqtt is not configured")
	}
	smartEvse.Prefix = raw.MQTT.Prefix
	if smartEvse.Name == "" {
		smartEvse.Name = fmt.Sprintf("smartevse-%d", smartEvse.SerialNr)
	}

	smartEvse.current = raw.Settings.ChargeCurrent / 10
	smartEvse.currentMin = raw.Settings.CurrentMin
	smartEvse.currentMax = raw.Settings.CurrentMax
	smartEvse.overrideCurrent = raw.Settings.OverrideCurrent / 10
	//smartEvse.autoStart = victron.EV_AutoStart_Enabled
	smartEvse.mode = canonicalizeSmartEVSEMode(raw.Mode)
	if isStrategyMode(smartEvse.mode) {
		smartEvse.activeStrategyMode = smartEvse.mode
	}
	if raw.Evse != nil {
		smartEvse.state = raw.Evse.State
		smartEvse.access = rawAccessToString(raw.Evse.Access)
	}
	if smartEvse.activeStrategyMode == "" && smartEvse.state != "" {
		smartEvse.activeStrategyMode = inferStrategyModeFromState(smartEvse.state)
	}
	if smartEvse.activeStrategyMode != "" && !isPauseMode(smartEvse.activeStrategyMode) {
		smartEvse.managedMode = smartEvse.managedModeFromRuntime()
		smartEvse.managedModeKnown = true
	}

	if raw.EvMeter != nil {
		smartEvse.total = raw.EvMeter.TotalWh / 1000
		smartEvse.charged = raw.EvMeter.ChargedWh / 1000
	}

	smartEvse.sessionTime = 0
	smartEvse.lastSessionTime = 0
	return nil
}

func (smartEvse *SmartEVSE) subscribe(mqtt *mqtthelper.Mqtt_Helper) {
	topic := fmt.Sprintf("%s/#", smartEvse.Prefix)
	mqtt.AddStringSubscriptionFull(topic, smartEvse.mqttReceived)
}

func (smartEvse *SmartEVSE) findSub(topic string) string {
	if len(topic) < len(smartEvse.Prefix)+2 {
		return topic
	}
	return topic[len(smartEvse.Prefix)+1:]
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

func (smartEvse *SmartEVSE) managedModeFromRuntime() victron.EV_Mode {
	if smartEvse.activeStrategyMode == "Smart" && smartEvse.managedAutoControlActive {
		return victron.EV_Mode_Auto
	}
	return managedModeFromStrategy(smartEvse.activeStrategyMode)
}

func (smartEvse *SmartEVSE) effectiveManagedMode() victron.EV_Mode {
	if smartEvse.managedModeKnown {
		return smartEvse.managedMode
	}
	if smartEvse.activeStrategyMode != "" {
		return managedModeFromStrategy(smartEvse.activeStrategyMode)
	}
	return victron.EV_Mode_Manual
}

func (smartEvse *SmartEVSE) targetStrategyForManagedMode() string {
	switch smartEvse.effectiveManagedMode() {
	case victron.EV_Mode_Manual:
		return "Smart"
	case victron.EV_Mode_Auto:
		if smartEvse.managedAutoControlActive {
			return "Smart"
		}
		return "Solar"
	case victron.EV_Mode_Scheduled:
		// SmartEVSE has no time schedules; Scheduled behaves as Solar
		return "Solar"
	default:
		return ""
	}
}

func (smartEvse *SmartEVSE) isPaused() bool {
	return isPauseMode(smartEvse.mode)
}

func (smartEvse *SmartEVSE) deriveStatus() victron.EV_Status {
	if smartEvse.evPlugState == "Disconnected" {
		return victron.EV_Status_Disconnected
	}
	if smartEvse.isPaused() {
		return victron.EV_Status_Waiting_for_start
	}

	state := strings.ToLower(strings.TrimSpace(smartEvse.state))
	solarStrategy := smartEvse.targetStrategyForManagedMode() == "Solar"

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

func (smartEvse *SmartEVSE) syncVictronState() {
	if smartEvse.victronEvcs == nil {
		return
	}
	smartEvse.victronEvcs.SetMode(smartEvse.effectiveManagedMode())
	if smartEvse.isPaused() {
		smartEvse.victronEvcs.SetStartStop(victron.EvStartStopStop)
	} else {
		smartEvse.victronEvcs.SetStartStop(victron.EvStartStopStart)
	}
	// AutoStart is managed internally by Victron - set via callbacks in ev.go
	smartEvse.victronEvcs.SetStatus(smartEvse.deriveStatus())
}

func (smartEvse *SmartEVSE) publishFullTopic(topic, payload string) {
	if smartEvse.publish != nil {
		smartEvse.publish(topic, payload)
		return
	}
	if smartEvse.mqtt != nil {
		smartEvse.mqtt.PublishFull(topic, payload)
	}
}

func (smartEvse *SmartEVSE) applyRunningModeLocally(mode string) {
	mode = canonicalizeSmartEVSEMode(mode)
	smartEvse.mode = mode
	if isStrategyMode(mode) {
		smartEvse.activeStrategyMode = mode
	}
}

func (smartEvse *SmartEVSE) requestRunningMode(mode string) {
	mode = canonicalizeSmartEVSEMode(mode)
	if mode == "" {
		return
	}
	topic := fmt.Sprintf("%s/Set/Mode", smartEvse.Prefix)
	smartEvse.publishFullTopic(topic, mode)
	smartEvse.applyRunningModeLocally(mode)
	smartEvse.syncVictronState()
}

func (smartEvse *SmartEVSE) setManagedAutoControl(active bool, overrideCurrent float64) {
	smartEvse.managedAutoControlActive = active
	if active {
		smartEvse.overrideCurrent = overrideCurrent
	} else {
		smartEvse.overrideCurrent = 0
	}

	topic := fmt.Sprintf("%s/Set/CurrentOverride", smartEvse.Prefix)
	payload := fmt.Sprintf("%d", int32(math.RoundToEven(smartEvse.overrideCurrent*10)))
	smartEvse.publishFullTopic(topic, payload)

	if smartEvse.effectiveManagedMode() == victron.EV_Mode_Auto && !smartEvse.isPaused() {
		smartEvse.requestRunningMode(smartEvse.targetStrategyForManagedMode())
		return
	}
	smartEvse.syncVictronState()
}

func (smartEvse *SmartEVSE) clearManagedAutoControl() {
	smartEvse.setManagedAutoControl(false, 0)
}

func (smartEvse *SmartEVSE) clearPauseOnReconnectIfNeeded(previousPlugState string) {
	if previousPlugState == "Disconnected" && smartEvse.evPlugState == "Connected" && smartEvse.victronEvcs.AutoStart() == victron.EV_AutoStart_Enabled && smartEvse.isPaused() {
		smartEvse.requestRunningMode(smartEvse.targetStrategyForManagedMode())
	}
}

func (smartEvse *SmartEVSE) enforcePauseOnConnectIfAutoStartDisabled(previousPlugState string) {
	if previousPlugState != "Connected" && smartEvse.evPlugState == "Connected" && smartEvse.victronEvcs.AutoStart() == victron.EV_AutoStart_Disabled && !smartEvse.isPaused() {
		smartEvse.requestRunningMode("Pause")
	}
}

func (smartEvse *SmartEVSE) mqttReceived(topic string, value string) {
	if smartEvse.victronEvcs == nil {
		log.Printf("No victronEv yet, dropping topic:%s payload:%s", topic, value)
		return
	}
	sub := smartEvse.findSub(topic)
	if sub == topic {
		log.Printf("invalid topic %s", topic)
		return
	}
	updateState := false

	// string values
	switch sub {
	case "connected":
		smartEvse.victronEvcs.SetConnected(value == "online")
		return
	case "Access":
		smartEvse.access = value
	case "State":
		smartEvse.state = value
		updateState = true
	case "EVPlugState":
		previousPlugState := smartEvse.evPlugState
		smartEvse.evPlugState = value
		updateState = true
		if !smartEvse.handleS2PlugChange(previousPlugState) {
			smartEvse.enforcePauseOnConnectIfAutoStartDisabled(previousPlugState)
			smartEvse.clearPauseOnReconnectIfNeeded(previousPlugState)
		}
	case "Error":
	case "Mode":
		previousMode := smartEvse.mode
		smartEvse.applyRunningModeLocally(value)
		if isStrategyMode(value) {
			unchanged := smartEvse.managedModeKnown && canonicalizeSmartEVSEMode(previousMode) == canonicalizeSmartEVSEMode(value)
			if !unchanged && !(smartEvse.managedModeKnown && smartEvse.targetStrategyForManagedMode() == canonicalizeSmartEVSEMode(value)) {
				smartEvse.managedMode = smartEvse.managedModeFromRuntime()
				smartEvse.managedModeKnown = true
			}
		}
		updateState = true
	}
	if updateState {
		if smartEvse.evPlugState == "Disconnected" {
			smartEvse.sessionTime = 0
			smartEvse.lastSessionTime = 0
			smartEvse.victronEvcs.SetSessionTime(smartEvse.sessionTime)
		}
		smartEvse.syncVictronState()
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
		smartEvse.victronEvcs.ChangeCurrentL1(i / 10)
		smartEvse.updatePhaseCurrent(0, i/10)
		if i > 0 {
			smartEvse.setSessionTime()
		}
	case "EVCurrentL2":
		smartEvse.victronEvcs.ChangeCurrentL2(i / 10)
		smartEvse.updatePhaseCurrent(1, i/10)
		if i > 0 {
			smartEvse.setSessionTime()
		}
	case "EVCurrentL3":
		smartEvse.victronEvcs.ChangeCurrentL3(i / 10)
		smartEvse.updatePhaseCurrent(2, i/10)
		if i > 0 {
			smartEvse.setSessionTime()
		}
	case "MaxCurrent":
		// keep the /SetCurrent bounds in sync, otherwise writes are validated against the startup max
		smartEvse.currentMax = i / 10
		_, current, _ := smartEvse.victronEvcs.CurrentLimits()
		current = math.Max(smartEvse.currentMin, math.Min(current, smartEvse.currentMax))
		smartEvse.victronEvcs.SetCurrentLimits(smartEvse.currentMin, current, smartEvse.currentMax)
	case "ChargeCurrent":
		smartEvse.victronEvcs.SetChargeCurrent(i / 10)
	case "CurrentOverride":
		smartEvse.overrideCurrent = i / 10
	case "EVChargePower":
		smartEvse.victronEvcs.SetAcPower(i)
	case "EVEnergyCharged":
		smartEvse.victronEvcs.SetSessionEnergy(i / 1000)
	case "EVTotalEnergyCharged":
		smartEvse.victronEvcs.SetTotalEnergy(i / 1000)
	case "ESPTemp":
		smartEvse.victronEvcs.SetTemperature(i)
	}
}

func (smartEvse *SmartEVSE) setSessionTime() {
	now := time.Now().Unix()
	if smartEvse.lastSessionTime > 0 {
		smartEvse.sessionTime += float64(now) - smartEvse.lastSessionTime
		smartEvse.victronEvcs.SetSessionTime(smartEvse.sessionTime)
	}
	smartEvse.lastSessionTime = float64(now)
}

func (smartEvse *SmartEVSE) modeChangedCallback(mode victron.EV_Mode) error {
	log.Printf("Request to change mode to: %d", mode)
	switch mode {
	case victron.EV_Mode_Manual, victron.EV_Mode_Auto, victron.EV_Mode_Scheduled:
		smartEvse.managedMode = mode
		smartEvse.managedModeKnown = true
		if !smartEvse.isPaused() {
			smartEvse.requestRunningMode(smartEvse.targetStrategyForManagedMode())
		} else {
			smartEvse.syncVictronState()
		}
	default:
		log.Printf("Don't know what to do for mode:%d", mode)
	}
	return nil
}

func (smartEvse *SmartEVSE) setOverrideCurrentChangedCallback(value, _, max float64) {
	if value == max {
		value = 0 // 0 means no override, so if the value is the same as max, we can set it to 0 to disable the override
	}
	smartEvse.overrideCurrent = value
	log.Printf("Request to change override current to: %f", value)
	topic := fmt.Sprintf("%s/Set/CurrentOverride", smartEvse.Prefix)
	payload := fmt.Sprintf("%d", int32(math.RoundToEven(value*10)))
	smartEvse.publishFullTopic(topic, payload)
}

func (smartEvse *SmartEVSE) startStopChangedCallback(mode victron.EvStartStop) {
	log.Printf("Request to change start/stop to: %d", mode)
	if mode == victron.EvStartStopStop {
		smartEvse.s2.userPaused = true
		smartEvse.requestRunningMode("Pause")
		return
	}
	smartEvse.s2.userPaused = false
	if smartEvse.s2Controlled() {
		smartEvse.applyS2Command()
		return
	}
	smartEvse.requestRunningMode(smartEvse.targetStrategyForManagedMode())
}

func (handler *EvHandler) WriteMainsMeter() {
	l1, l2, l3 := handler.victron.Grid()

	payload := fmt.Sprintf("%d:%d:%d", int32(math.RoundToEven(l1*10)), int32(math.RoundToEven(l2*10)), int32(math.RoundToEven(l3*10)))
	for _, smartevse := range handler.evs {
		topic := fmt.Sprintf("%s/Set/MainsMeter", smartevse.Prefix)
		handler.mqtt.PublishFull(topic, payload)
	}
}

func (handler *EvHandler) WriteHomeBattery() {
	battery := handler.victron.BatteryCurrent()

	payload := fmt.Sprintf("%d", int32(math.RoundToEven(battery*10)))
	for _, smartevse := range handler.evs {
		topic := fmt.Sprintf("%s/Set/HomeBatteryCurrent", smartevse.Prefix)
		handler.mqtt.PublishFull(topic, payload)
	}
}
