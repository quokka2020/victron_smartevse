package victron

import (
	"crypto/rand"
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/quokka2020/gohelpers/util"
)

// s2Debug enables logging of full S2 payloads (env S2_DEBUG=1). Without it only
// compact summaries of relevant messages are logged.
var s2Debug = util.GetEnvBool("S2_DEBUG", false)

const (
	s2Iface                  = "com.victronenergy.S2"
	s2Path                   = dbus.ObjectPath("/S2/0/Rm")
	s2DefaultPublishInterval = 5 * time.Second
	s2DefaultMinOnDuration   = 1 * time.Minute
	s2DefaultMinOffDuration  = 1 * time.Minute
	// s2DefaultWatchdogFactor is applied to the CEM's keep-alive interval
	// before the session is considered dead.
	s2DefaultWatchdogFactor            = 2.5
	s2DefaultInstructionProcessingTime = 2 * time.Second
	// s2ClientDynamicESS is the client id used by Dynamic ESS. It does not
	// implement OMBC.TimerStatus and answers it with PERMANENT_ERROR.
	s2ClientDynamicESS = "dynamic_ess"

	s2ControlNone = "NOT_CONTROLABLE"
	s2ControlOMBC = "OPERATION_MODE_BASED_CONTROL"
)

var supportedProtocolVersions = []string{"0.0.2-beta"}

// s2ResourceManager implements the S2 Resource Manager side of the Victron S2
// D-Bus transport (com.victronenergy.S2 on /S2/0/Rm) for a SmartEVSE charger.
// It offers Operation Mode Based Control with a Standby mode and fixed-power
// charge current steps, and translates instructions into charger commands via
// controlCallback. When the CEM goes away or the charger leaves Auto mode the
// charger is handed back via releaseCallback.
type s2ResourceManager struct {
	conn       DBusConn
	deviceName string
	resourceID string

	publishInterval            time.Duration
	ombcBootstrapDelay         time.Duration
	minOnDuration              time.Duration
	minOffDuration             time.Duration
	instructionProcessingDelay time.Duration
	watchdogFactor             float64
	defaultControlType         string
	now                        func() time.Time

	ombcReadyReader      func() bool
	powerL1Reader        func() float64
	powerL2Reader        func() float64
	powerL3Reader        func() float64
	maxChargePowerReader func() float64
	minCurrentReader     func() float64
	maxCurrentReader     func() float64
	phasesReader         func() int
	autoModeReader       func() bool
	chargerStateReader   func() (charging bool, currentA float64)
	controlCallback      func(S2ChargeCommand)
	releaseCallback      func()
	sessionActiveChanged func(active bool)

	mu                      sync.Mutex
	clientID                string
	connected               bool
	sessionEstablished      bool
	keepAliveS              int32
	connectedAt             time.Time
	sessionEstablishedAt    time.Time
	lastSeen                time.Time
	selectedProtocolVersion string
	selectedControlType     string
	model                   s2ChargeModel
	ombcDescriptionSent     bool
	activeOperationModeID   string
	previousOperationModeID string
	lastTransitionAt        time.Time
	timerFinishedAt         map[string]time.Time
	controlling             bool
	lastRxType              string
	lastTxType              string
	lastRxMessageID         string
	rxCount                 int
	txCount                 int
	publisherStop           chan struct{}
	publisherDone           chan struct{}
}

// s2Actions collects the follow-up work decided while holding the mutex, so
// that messages and callbacks are emitted without holding it.
type s2Actions struct {
	bootstrap       bool
	selectedControl bool
	release         bool
	ombcStatus      bool
	timerStatus     []string
	apply           *S2ChargeCommand
}

func newS2ResourceManager(conn DBusConn, deviceName string) *s2ResourceManager {
	return &s2ResourceManager{
		conn:                       conn,
		deviceName:                 deviceName,
		resourceID:                 deterministicUUID(deviceName),
		publishInterval:            s2DefaultPublishInterval,
		minOnDuration:              s2DefaultMinOnDuration,
		minOffDuration:             s2DefaultMinOffDuration,
		instructionProcessingDelay: s2DefaultInstructionProcessingTime,
		watchdogFactor:             s2DefaultWatchdogFactor,
		ombcReadyReader:            func() bool { return true },
		powerL1Reader:              func() float64 { return 0 },
		powerL2Reader:              func() float64 { return 0 },
		powerL3Reader:              func() float64 { return 0 },
		maxChargePowerReader:       func() float64 { return 0 },
		minCurrentReader:           func() float64 { return s2DefaultMinA },
		maxCurrentReader:           func() float64 { return 16 },
		phasesReader:               func() int { return s2DefaultEVPhases },
		autoModeReader:             func() bool { return true },
		chargerStateReader:         func() (bool, float64) { return false, 0 },
		sessionActiveChanged:       func(bool) {},
		timerFinishedAt:            map[string]time.Time{},
	}
}

func (s *s2ResourceManager) Export() error {
	return s.conn.ExportAll(s, s2Path, s2Iface)
}

func (s *s2ResourceManager) Close() {
	s.stopPublisher()
	s.mu.Lock()
	clientID := ""
	if s.connected {
		clientID = s.clientID
	}
	s.resetSessionLocked()
	s.mu.Unlock()
	if clientID != "" {
		s.emitDisconnectSignal(clientID, "Resource manager shutting down")
	}
	s.publishSessionActive(false)
	s.release("resource manager closed")
}

func (s *s2ResourceManager) Discover() (bool, *dbus.Error) { return true, nil }

func (s *s2ResourceManager) GetText() (string, *dbus.Error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	activeMode := "-"
	if mode, ok := s.model.find(s.activeOperationModeID); ok && s.activeOperationModeID != "" {
		activeMode = mode.label
	}
	return fmt.Sprintf("S2 RM (%s) connected=%t client=%s protocol=%s control=%s active_mode=%s controlling=%t phases=%d rx=%d tx=%d last_rx=%s",
		s.deviceName, s.connected, dashIfEmpty(s.clientID), dashIfEmpty(s.selectedProtocolVersion), dashIfEmpty(s.selectedControlType),
		activeMode, s.controlling, s.model.phases, s.rxCount, s.txCount, dashIfEmpty(s.lastRxType)), nil
}

func (s *s2ResourceManager) Connect(clientID string, keepAliveS int32) (bool, *dbus.Error) {
	s.mu.Lock()
	if s.connected && s.clientID != clientID {
		current := s.clientID
		s.mu.Unlock()
		log.Printf("S2 CONNECT rejected [%s] client=%s, already connected to client=%s", s.deviceName, clientID, current)
		return false, nil
	}
	s.mu.Unlock()

	s.stopPublisher()
	s.release("new S2 session")

	s.mu.Lock()
	s.resetSessionLocked()
	s.clientID = clientID
	s.connected = true
	s.keepAliveS = keepAliveS
	s.connectedAt = s.nowTime()
	s.lastSeen = s.nowTime()
	s.mu.Unlock()

	log.Printf("S2 CONNECT [%s] client=%s keepalive=%ds", s.deviceName, clientID, keepAliveS)
	s.publishSessionActive(false)
	handshake := map[string]any{
		"message_type":                "Handshake",
		"message_id":                  newMessageID(),
		"role":                        "RM",
		"supported_protocol_versions": supportedProtocolVersions,
	}
	if err := s.emitMessage(clientID, handshake); err != nil {
		log.Printf("S2 handshake emit failed [%s]: %v", s.deviceName, err)
		return false, nil
	}
	s.startPublisher()
	return true, nil
}

func (s *s2ResourceManager) KeepAlive(clientID string) (bool, *dbus.Error) {
	s.mu.Lock()
	if !s.connected || s.clientID != clientID {
		s.mu.Unlock()
		log.Printf("S2 KEEPALIVE rejected [%s] client=%s", s.deviceName, clientID)
		s.emitDisconnectSignal(clientID, "Not connected")
		return false, nil
	}
	s.lastSeen = s.nowTime()
	s.mu.Unlock()
	return true, nil
}

func (s *s2ResourceManager) Disconnect(clientID string) *dbus.Error {
	s.mu.Lock()
	wasConnected := s.connected && s.clientID == clientID
	if wasConnected {
		s.resetSessionLocked()
	}
	s.mu.Unlock()
	if !wasConnected {
		log.Printf("S2 DISCONNECT ignored [%s] client=%s", s.deviceName, clientID)
		return nil
	}
	log.Printf("S2 DISCONNECT [%s] client=%s", s.deviceName, clientID)
	s.stopPublisher()
	s.publishSessionActive(false)
	s.release("CEM disconnected")
	return nil
}

func (s *s2ResourceManager) Message(clientID, payload string) *dbus.Error {
	msgType, msgID, envelope := parseS2Envelope(payload)
	s.logTraffic("RX", clientID, msgType, payload, envelope)
	s.mu.Lock()
	connected := s.connected && s.clientID == clientID
	receptionStatus := "OK"
	diagnosticLabel := ""
	var act s2Actions
	if connected {
		s.lastSeen = s.nowTime()
		s.rxCount++
		s.lastRxType = msgType
		s.lastRxMessageID = msgID
		act = s.planIncomingLocked(msgType, envelope, &receptionStatus, &diagnosticLabel)
	}
	s.mu.Unlock()
	if !connected {
		log.Printf("S2 RX dropped [%s] client=%s is not connected", s.deviceName, clientID)
		s.emitDisconnectSignal(clientID, "Not connected")
		return nil
	}
	if shouldAck(msgType, msgID) {
		reception := map[string]any{
			"message_type":       "ReceptionStatus",
			"subject_message_id": msgID,
			"status":             receptionStatus,
		}
		if diagnosticLabel != "" {
			reception["diagnostic_label"] = diagnosticLabel
		}
		if err := s.emitMessage(clientID, reception); err != nil {
			log.Printf("S2 reception emit failed [%s]: %v", s.deviceName, err)
		}
	}
	if msgType == "HandshakeResponse" && receptionStatus == "OK" {
		s.publishSessionActive(true)
	}
	if act.release {
		s.release("control type changed")
	}
	if act.bootstrap {
		if err := s.emitBootstrapState(clientID); err != nil {
			log.Printf("S2 bootstrap emit failed [%s]: %v", s.deviceName, err)
		}
	}
	if act.selectedControl {
		if err := s.emitSelectedControlTypeState(clientID); err != nil {
			log.Printf("S2 control state emit failed [%s]: %v", s.deviceName, err)
		}
		s.restartPublisher()
	}
	if act.ombcStatus {
		if err := s.emitOMBCStatus(clientID); err != nil {
			log.Printf("S2 OMBC status emit failed [%s]: %v", s.deviceName, err)
		}
	}
	for _, timerID := range act.timerStatus {
		if err := s.emitTimerStatus(clientID, timerID); err != nil {
			log.Printf("S2 OMBC timer status emit failed [%s]: %v", s.deviceName, err)
		}
	}
	if act.apply != nil {
		s.applyCommand(*act.apply)
	}
	return nil
}

// onModeChanged is called after the EV charger /Mode changed. S2 only controls
// the charger while it is in Auto mode.
func (s *s2ResourceManager) onModeChanged(oldMode, newMode EV_Mode) {
	if oldMode == newMode {
		return
	}
	if newMode != EV_Mode_Auto {
		s.release("mode left Auto")
		return
	}
	s.mu.Lock()
	ready := s.connected && s.sessionEstablished && s.selectedControlType == s2ControlOMBC && s.ombcDescriptionSent
	mode, found := s.model.find(s.activeOperationModeID)
	s.mu.Unlock()
	if ready && found {
		s.applyCommand(mode.command())
	}
}

// onVehiclePluggedIn resets the active OMBC mode so the CEM can decide
// whether to start the new charging session.
func (s *s2ResourceManager) onVehiclePluggedIn() {
	s.mu.Lock()
	if !s.connected || !s.sessionEstablished || s.selectedControlType != s2ControlOMBC || !s.ombcDescriptionSent || len(s.model.modes) == 0 {
		s.mu.Unlock()
		return
	}
	clientID := s.clientID
	standby := s.model.standby()
	active, found := s.model.find(s.activeOperationModeID)
	timerID := ""
	if found && !active.isStandby() {
		now := s.nowTime().UTC()
		s.previousOperationModeID = active.id
		s.activeOperationModeID = standby.id
		s.lastTransitionAt = now
		timerID = s.model.minOffID
		s.timerFinishedAt[timerID] = now.Add(s.minOffDuration)
	}
	s.mu.Unlock()

	s.applyCommand(standby.command())
	if err := s.emitOMBCStatus(clientID); err != nil {
		log.Printf("S2 plug-in status emit failed [%s]: %v", s.deviceName, err)
	}
	if timerID != "" {
		if err := s.emitTimerStatus(clientID, timerID); err != nil {
			log.Printf("S2 plug-in timer status emit failed [%s]: %v", s.deviceName, err)
		}
	}
}

func (s *s2ResourceManager) applyCommand(cmd S2ChargeCommand) {
	if s.autoModeReader != nil && !s.autoModeReader() {
		log.Printf("S2 apply skipped [%s] charger not in Auto mode (charge=%t current=%.0fA)", s.deviceName, cmd.Charge, cmd.CurrentA)
		return
	}
	s.mu.Lock()
	s.controlling = true
	s.mu.Unlock()
	log.Printf("S2 apply [%s] charge=%t current=%.0fA", s.deviceName, cmd.Charge, cmd.CurrentA)
	if s.controlCallback != nil {
		s.controlCallback(cmd)
	}
}

func (s *s2ResourceManager) release(reason string) {
	s.mu.Lock()
	wasControlling := s.controlling
	s.controlling = false
	s.mu.Unlock()
	if !wasControlling {
		return
	}
	log.Printf("S2 release [%s] %s", s.deviceName, reason)
	if s.releaseCallback != nil {
		s.releaseCallback()
	}
}

func (s *s2ResourceManager) resetSessionLocked() {
	s.connected = false
	s.sessionEstablished = false
	s.sessionEstablishedAt = time.Time{}
	s.clientID = ""
	s.keepAliveS = 0
	s.selectedProtocolVersion = ""
	s.selectedControlType = ""
	s.ombcDescriptionSent = false
	s.activeOperationModeID = ""
	s.previousOperationModeID = ""
	s.lastTransitionAt = time.Time{}
	s.lastRxType = ""
	s.lastTxType = ""
	s.lastRxMessageID = ""
	s.rxCount = 0
	s.txCount = 0
}

// checkWatchdog drops the session when the CEM stopped sending keep-alives or
// messages. It returns true when the session was dropped.
func (s *s2ResourceManager) checkWatchdog() bool {
	s.mu.Lock()
	if !s.connected || s.keepAliveS <= 0 || s.watchdogFactor <= 0 {
		s.mu.Unlock()
		return false
	}
	timeout := time.Duration(float64(s.keepAliveS) * s.watchdogFactor * float64(time.Second))
	silence := s.nowTime().Sub(s.lastSeen)
	if silence <= timeout {
		s.mu.Unlock()
		return false
	}
	clientID := s.clientID
	s.resetSessionLocked()
	s.mu.Unlock()
	log.Printf("S2 WATCHDOG [%s] client=%s silent for %s, dropping session", s.deviceName, clientID, silence.Round(time.Second))
	s.emitDisconnectSignal(clientID, "KeepAlive missed")
	s.publishSessionActive(false)
	s.release("keep-alive missed")
	return true
}

func (s *s2ResourceManager) restartPublisher() {
	s.stopPublisher()
	s.startPublisher()
}

func (s *s2ResourceManager) startPublisher() {
	s.mu.Lock()
	if s.publishInterval <= 0 || s.publisherStop != nil || !s.connected {
		s.mu.Unlock()
		return
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	interval := s.publishInterval
	s.publisherStop = stop
	s.publisherDone = done
	s.mu.Unlock()
	go s.publisherLoop(stop, done, interval)
}

func (s *s2ResourceManager) stopPublisher() {
	s.mu.Lock()
	stop := s.publisherStop
	done := s.publisherDone
	s.publisherStop = nil
	s.publisherDone = nil
	s.mu.Unlock()
	if stop == nil {
		return
	}
	close(stop)
	if done != nil {
		<-done
	}
}

func (s *s2ResourceManager) publisherLoop(stop <-chan struct{}, done chan<- struct{}, interval time.Duration) {
	defer close(done)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			if s.checkWatchdog() {
				return
			}
			if err := s.emitPeriodicState(); err != nil {
				log.Printf("S2 periodic emit failed [%s]: %v", s.deviceName, err)
			}
		}
	}
}

func (s *s2ResourceManager) emitPeriodicState() error {
	s.mu.Lock()
	ready := s.connected && s.sessionEstablished && s.clientID != ""
	clientID := s.clientID
	controlType := s.selectedControlType
	descriptionSent := s.ombcDescriptionSent
	s.mu.Unlock()
	if !ready {
		return nil
	}
	switch controlType {
	case s2ControlNone:
		return s.emitPowerMeasurement(clientID)
	case s2ControlOMBC:
		if !descriptionSent {
			if !s.canEmitOMBCBootstrap() {
				return nil
			}
			return s.bootstrapOMBC(clientID)
		}
		if err := s.refreshOMBCModel(clientID); err != nil {
			return err
		}
		return s.emitPowerMeasurement(clientID)
	default:
		return nil
	}
}

func (s *s2ResourceManager) planIncomingLocked(messageType string, envelope map[string]any, receptionStatus, diagnosticLabel *string) s2Actions {
	if envelope == nil {
		*receptionStatus = "INVALID_MESSAGE"
		*diagnosticLabel = "invalid JSON payload"
		return s2Actions{}
	}
	switch messageType {
	case "HandshakeResponse":
		return s.planHandshakeResponseLocked(toString(envelope["selected_protocol_version"]), receptionStatus, diagnosticLabel)
	case "SelectControlType":
		return s.planSelectControlTypeLocked(toString(envelope["control_type"]), receptionStatus, diagnosticLabel)
	case "OMBC.Instruction":
		return s.planOMBCInstructionLocked(envelope, receptionStatus, diagnosticLabel)
	default:
		return s2Actions{}
	}
}

func (s *s2ResourceManager) planHandshakeResponseLocked(selectedVersion string, receptionStatus, diagnosticLabel *string) s2Actions {
	if selectedVersion == "" {
		*receptionStatus = "INVALID_CONTENT"
		*diagnosticLabel = "missing selected_protocol_version"
		return s2Actions{}
	}
	if !containsString(supportedProtocolVersions, selectedVersion) {
		*receptionStatus = "INVALID_CONTENT"
		*diagnosticLabel = fmt.Sprintf("unsupported selected_protocol_version %q", selectedVersion)
		return s2Actions{}
	}
	s.selectedProtocolVersion = selectedVersion
	s.sessionEstablished = true
	s.sessionEstablishedAt = s.nowTime()
	return s2Actions{bootstrap: true}
}

func (s *s2ResourceManager) planSelectControlTypeLocked(controlType string, receptionStatus, diagnosticLabel *string) s2Actions {
	if !s.sessionEstablished {
		*receptionStatus = "TEMPORARY_ERROR"
		*diagnosticLabel = "Connection not yet established."
		return s2Actions{}
	}
	switch controlType {
	case s2ControlNone:
		s.selectedControlType = controlType
		s.activeOperationModeID = ""
		s.previousOperationModeID = ""
		s.lastTransitionAt = time.Time{}
		s.ombcDescriptionSent = false
		return s2Actions{selectedControl: true, release: true}
	case s2ControlOMBC:
		s.selectedControlType = controlType
		s.ombcDescriptionSent = false
		return s2Actions{selectedControl: true}
	case "":
		*receptionStatus = "INVALID_CONTENT"
		*diagnosticLabel = "missing control_type"
		return s2Actions{}
	default:
		*receptionStatus = "INVALID_CONTENT"
		*diagnosticLabel = fmt.Sprintf("unsupported control_type %q", controlType)
		return s2Actions{}
	}
}

func (s *s2ResourceManager) planOMBCInstructionLocked(envelope map[string]any, receptionStatus, diagnosticLabel *string) s2Actions {
	reject := func(status, label string) s2Actions {
		*receptionStatus = status
		*diagnosticLabel = label
		return s2Actions{}
	}
	if !s.sessionEstablished {
		return reject("TEMPORARY_ERROR", "Connection not yet established.")
	}
	if s.selectedControlType != s2ControlOMBC {
		return reject("INVALID_CONTENT", "OMBC.Instruction received while OMBC is not selected")
	}
	if !s.ombcDescriptionSent {
		return reject("TEMPORARY_ERROR", "OMBC.SystemDescription not yet sent")
	}
	operationModeID := toString(envelope["operation_mode_id"])
	if operationModeID == "" {
		return reject("INVALID_CONTENT", "missing operation_mode_id")
	}
	target, ok := s.model.find(operationModeID)
	if !ok {
		return reject("INVALID_CONTENT", fmt.Sprintf("unknown operation_mode_id %q", operationModeID))
	}
	factor, ok := toFloat64(envelope["operation_mode_factor"])
	if !ok {
		return reject("INVALID_CONTENT", "missing or invalid operation_mode_factor")
	}
	if factor < 0 || factor > 1 {
		return reject("INVALID_CONTENT", fmt.Sprintf("operation_mode_factor %.3f out of range", factor))
	}
	if s.autoModeReader != nil && !s.autoModeReader() {
		return reject("TEMPORARY_ERROR", "EV charger is not in Auto mode")
	}
	abnormal, _ := envelope["abnormal_condition"].(bool)

	active, ok := s.model.find(s.activeOperationModeID)
	if !ok {
		active = s.model.standby()
	}
	if target.id == active.id {
		act := s2Actions{ombcStatus: true}
		if !s.controlling {
			cmd := target.command()
			act.apply = &cmd
		}
		return act
	}

	now := s.nowTime()
	startTimers, blockingTimers := s.model.transitionTimers(active, target)
	if !abnormal {
		for _, timerID := range blockingTimers {
			if finishedAt, running := s.timerFinishedAt[timerID]; running && now.Before(finishedAt) {
				return reject("TEMPORARY_ERROR", fmt.Sprintf("transition %s -> %s blocked by timer %s until %s",
					active.label, target.label, s.timerLabel(timerID), finishedAt.UTC().Format(time.RFC3339)))
			}
		}
	}

	s.previousOperationModeID = active.id
	s.activeOperationModeID = target.id
	s.lastTransitionAt = now.UTC()
	act := s2Actions{ombcStatus: true}
	for _, timerID := range startTimers {
		s.timerFinishedAt[timerID] = now.Add(s.timerDuration(timerID))
		act.timerStatus = append(act.timerStatus, timerID)
	}
	cmd := target.command()
	act.apply = &cmd
	return act
}

func (s *s2ResourceManager) timerDuration(timerID string) time.Duration {
	if timerID == s.model.minOnID {
		return s.minOnDuration
	}
	return s.minOffDuration
}

func (s *s2ResourceManager) timerLabel(timerID string) string {
	if timerID == s.model.minOnID {
		return "min-on"
	}
	return "min-off"
}

func (s *s2ResourceManager) emitBootstrapState(clientID string) error {
	if err := s.emitResourceManagerDetails(clientID); err != nil {
		return err
	}
	return s.emitDefaultControlState(clientID)
}

func (s *s2ResourceManager) emitResourceManagerDetails(clientID string) error {
	rmDetails := map[string]any{
		"message_type":  "ResourceManagerDetails",
		"message_id":    newMessageID(),
		"resource_id":   s.resourceID,
		"name":          s.deviceName,
		"manufacturer":  "SmartEVSE",
		"model":         "SmartEVSE v3",
		"serial_number": s.deviceName,
		"roles": []map[string]any{{
			"role":      "ENERGY_CONSUMER",
			"commodity": "ELECTRICITY",
		}},
		"instruction_processing_delay": s.instructionProcessingDelay.Milliseconds(),
		"available_control_types":      []string{s2ControlNone, s2ControlOMBC},
		"provides_forecast":            false,
		"provides_power_measurement_types": []string{
			"ELECTRIC.POWER.L1",
			"ELECTRIC.POWER.L2",
			"ELECTRIC.POWER.L3",
		},
	}
	return s.emitMessage(clientID, rmDetails)
}

func (s *s2ResourceManager) emitDefaultControlState(clientID string) error {
	s.mu.Lock()
	if s.selectedControlType == "" && s.defaultControlType != "" {
		s.selectedControlType = s.defaultControlType
	}
	controlType := s.selectedControlType
	s.mu.Unlock()
	if controlType == "" {
		return nil
	}
	if err := s.emitSelectedControlTypeState(clientID); err != nil {
		return err
	}
	s.startPublisher()
	return nil
}

func (s *s2ResourceManager) emitSelectedControlTypeState(clientID string) error {
	s.mu.Lock()
	controlType := s.selectedControlType
	s.mu.Unlock()
	switch controlType {
	case s2ControlNone:
		return s.emitPowerMeasurement(clientID)
	case s2ControlOMBC:
		if !s.canEmitOMBCBootstrap() {
			log.Printf("S2 OMBC bootstrap deferred [%s] client=%s waiting for readiness/delay", s.deviceName, clientID)
			return nil
		}
		return s.bootstrapOMBC(clientID)
	default:
		return nil
	}
}

func (s *s2ResourceManager) currentModel() s2ChargeModel {
	return buildS2ChargeModel(s.resourceID, s.readPhases(), readFloat(s.minCurrentReader), readFloat(s.maxCurrentReader), readFloat(s.maxChargePowerReader))
}

func (s *s2ResourceManager) readPhases() int {
	if s.phasesReader == nil {
		return s2DefaultEVPhases
	}
	return s.phasesReader()
}

// bootstrapOMBC sends the system description and the initial status. The
// initial operation mode follows the real charger state (and is applied), so
// that the CEM and the charger agree on what is active: Dynamic ESS only sends
// an instruction when its target differs from the reported mode.
func (s *s2ResourceManager) bootstrapOMBC(clientID string) error {
	model := s.currentModel()
	charging, currentA := false, 0.0
	if s.chargerStateReader != nil {
		charging, currentA = s.chargerStateReader()
	}
	now := s.nowTime()
	s.mu.Lock()
	s.model = model
	s.ombcDescriptionSent = true
	if _, ok := model.find(s.activeOperationModeID); !ok {
		initial := model.standby()
		if charging {
			initial = model.nearestStep(currentA)
		}
		s.activeOperationModeID = initial.id
		s.previousOperationModeID = ""
		s.lastTransitionAt = time.Time{}
	}
	active, _ := model.find(s.activeOperationModeID)
	runningTimers := []string{}
	for _, timerID := range []string{model.minOnID, model.minOffID} {
		if finishedAt, ok := s.timerFinishedAt[timerID]; ok && now.Before(finishedAt) {
			runningTimers = append(runningTimers, timerID)
		}
	}
	s.mu.Unlock()

	if err := s.emitOMBCSystemDescription(clientID, model); err != nil {
		return err
	}
	if err := s.emitOMBCStatus(clientID); err != nil {
		return err
	}
	for _, timerID := range runningTimers {
		if err := s.emitTimerStatus(clientID, timerID); err != nil {
			return err
		}
	}
	s.applyCommand(active.command())
	return nil
}

// refreshOMBCModel re-sends the system description when the charge model
// changed (phases, current limits or max charge power). An active charge step
// that no longer exists is moved to the nearest remaining step.
func (s *s2ResourceManager) refreshOMBCModel(clientID string) error {
	model := s.currentModel()
	s.mu.Lock()
	if model.signature == s.model.signature {
		s.mu.Unlock()
		return nil
	}
	previousModel := s.model
	s.model = model
	moved := false
	if _, ok := model.find(s.activeOperationModeID); !ok {
		next := model.standby()
		if old, found := previousModel.find(s.activeOperationModeID); found && !old.isStandby() {
			next = model.nearestStep(float64(old.currentA))
		}
		s.previousOperationModeID = s.activeOperationModeID
		s.activeOperationModeID = next.id
		s.lastTransitionAt = s.nowTime().UTC()
		moved = true
	}
	active, _ := model.find(s.activeOperationModeID)
	controlling := s.controlling
	s.mu.Unlock()

	log.Printf("S2 OMBC model changed [%s] %s -> %s", s.deviceName, previousModel.signature, model.signature)
	if err := s.emitOMBCSystemDescription(clientID, model); err != nil {
		return err
	}
	if err := s.emitOMBCStatus(clientID); err != nil {
		return err
	}
	if moved && controlling {
		s.applyCommand(active.command())
	}
	return nil
}

func (s *s2ResourceManager) emitPowerMeasurement(clientID string) error {
	measurement := map[string]any{
		"message_type":          "PowerMeasurement",
		"message_id":            newMessageID(),
		"measurement_timestamp": nowRFC3339(),
		"values": []map[string]any{{
			"commodity_quantity": "ELECTRIC.POWER.L1",
			"value":              readFloat(s.powerL1Reader),
		}, {
			"commodity_quantity": "ELECTRIC.POWER.L2",
			"value":              readFloat(s.powerL2Reader),
		}, {
			"commodity_quantity": "ELECTRIC.POWER.L3",
			"value":              readFloat(s.powerL3Reader),
		}},
	}
	return s.emitMessage(clientID, measurement)
}

func readFloat(reader func() float64) float64 {
	if reader == nil {
		return 0
	}
	return reader()
}

func (s *s2ResourceManager) isOMBCReady() bool {
	if s.ombcReadyReader == nil {
		return true
	}
	return s.ombcReadyReader()
}

func (s *s2ResourceManager) canEmitOMBCBootstrap() bool {
	if !s.isOMBCReady() {
		return false
	}
	s.mu.Lock()
	establishedAt := s.sessionEstablishedAt
	delay := s.ombcBootstrapDelay
	s.mu.Unlock()
	if delay <= 0 || establishedAt.IsZero() {
		return true
	}
	return s.nowTime().Sub(establishedAt) >= delay
}

func (s *s2ResourceManager) emitOMBCSystemDescription(clientID string, model s2ChargeModel) error {
	return s.emitMessage(clientID, model.systemDescription(s.resourceID, s.minOnDuration.Milliseconds(), s.minOffDuration.Milliseconds()))
}

func (s *s2ResourceManager) emitOMBCStatus(clientID string) error {
	s.mu.Lock()
	activeModeID := s.activeOperationModeID
	if activeModeID == "" && len(s.model.modes) > 0 {
		activeModeID = s.model.standby().id
	}
	status := map[string]any{
		"message_type":             "OMBC.Status",
		"message_id":               newMessageID(),
		"active_operation_mode_id": activeModeID,
		"operation_mode_factor":    1.0,
	}
	if s.previousOperationModeID != "" {
		status["previous_operation_mode_id"] = s.previousOperationModeID
	}
	if !s.lastTransitionAt.IsZero() {
		status["transition_timestamp"] = s.lastTransitionAt.Format(time.RFC3339Nano)
	}
	s.mu.Unlock()
	return s.emitMessage(clientID, status)
}

func (s *s2ResourceManager) emitTimerStatus(clientID, timerID string) error {
	if clientID == s2ClientDynamicESS {
		return nil
	}
	s.mu.Lock()
	finishedAt, ok := s.timerFinishedAt[timerID]
	s.mu.Unlock()
	if !ok {
		return nil
	}
	return s.emitMessage(clientID, map[string]any{
		"message_type": "OMBC.TimerStatus",
		"message_id":   newMessageID(),
		"timer_id":     timerID,
		"finished_at":  finishedAt.UTC().Format(time.RFC3339Nano),
	})
}

func (s *s2ResourceManager) emitMessage(clientID string, message map[string]any) error {
	payload, err := json.Marshal(message)
	if err != nil {
		return err
	}
	msgType := toString(message["message_type"])
	msg := string(payload)
	s.mu.Lock()
	s.txCount++
	s.lastTxType = msgType
	s.mu.Unlock()
	s.logTraffic("TX", clientID, msgType, msg, message)
	return s.conn.Emit(s2Path, s2Iface+".Message", clientID, msg)
}

// logTraffic logs an S2 message. With S2_DEBUG the full payload is logged
// (except the large OMBC.SystemDescription); otherwise periodic
// PowerMeasurements and OK ReceptionStatus messages are skipped and other
// messages are summarised on one line.
func (s *s2ResourceManager) logTraffic(direction, clientID, msgType, payload string, message map[string]any) {
	if msgType == "OMBC.SystemDescription" {
		modes, transitions := 0, 0
		if v, ok := message["operation_modes"].([]map[string]any); ok {
			modes = len(v)
		} else if v, ok := message["operation_modes"].([]any); ok {
			modes = len(v)
		}
		if v, ok := message["transitions"].([]map[string]any); ok {
			transitions = len(v)
		} else if v, ok := message["transitions"].([]any); ok {
			transitions = len(v)
		}
		log.Printf("S2 %s [%s] client=%s type=%s modes=%d transitions=%d bytes=%d", direction, s.deviceName, clientID, msgType, modes, transitions, len(payload))
		return
	}
	if s2Debug {
		log.Printf("S2 %s [%s] client=%s payload=%s", direction, s.deviceName, clientID, payload)
		return
	}
	summary := s.summarizeMessage(msgType, message)
	if summary == "" {
		return
	}
	log.Printf("S2 %s [%s] client=%s %s", direction, s.deviceName, clientID, summary)
}

// summarizeMessage returns a compact description of an S2 message, or "" when
// the message is routine and should not be logged.
func (s *s2ResourceManager) summarizeMessage(msgType string, message map[string]any) string {
	switch msgType {
	case "PowerMeasurement":
		return ""
	case "ReceptionStatus":
		status := toString(message["status"])
		if status == "OK" {
			return ""
		}
		return fmt.Sprintf("type=ReceptionStatus status=%s label=%q", status, toString(message["diagnostic_label"]))
	case "OMBC.Instruction":
		factor, _ := toFloat64(message["operation_mode_factor"])
		abnormal, _ := message["abnormal_condition"].(bool)
		return fmt.Sprintf("type=%s mode=%s factor=%g abnormal=%t", msgType, s.modeLabel(toString(message["operation_mode_id"])), factor, abnormal)
	case "OMBC.Status":
		return fmt.Sprintf("type=%s mode=%s", msgType, s.modeLabel(toString(message["active_operation_mode_id"])))
	case "OMBC.TimerStatus":
		s.mu.Lock()
		timer := s.timerLabel(toString(message["timer_id"]))
		s.mu.Unlock()
		return fmt.Sprintf("type=%s timer=%s finished_at=%s", msgType, timer, toString(message["finished_at"]))
	case "SelectControlType":
		return fmt.Sprintf("type=%s control=%s", msgType, toString(message["control_type"]))
	case "HandshakeResponse":
		return fmt.Sprintf("type=%s version=%s", msgType, toString(message["selected_protocol_version"]))
	default:
		return "type=" + dashIfEmpty(msgType)
	}
}

func (s *s2ResourceManager) modeLabel(modeID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if mode, ok := s.model.find(modeID); ok {
		return mode.label
	}
	return dashIfEmpty(modeID)
}

func (s *s2ResourceManager) emitDisconnectSignal(clientID, reason string) {
	log.Printf("S2 TX DISCONNECT [%s] client=%s reason=%s", s.deviceName, clientID, reason)
	if err := s.conn.Emit(s2Path, s2Iface+".Disconnect", clientID, reason); err != nil {
		log.Printf("S2 disconnect emit failed [%s]: %v", s.deviceName, err)
	}
}

func (s *s2ResourceManager) publishSessionActive(active bool) {
	if s.sessionActiveChanged != nil {
		s.sessionActiveChanged(active)
	}
}

func (s *s2ResourceManager) nowTime() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func dashIfEmpty(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

func shouldAck(messageType, msgID string) bool {
	return messageType != "" && messageType != "ReceptionStatus" && msgID != ""
}

func parseS2Envelope(payload string) (msgType, msgID string, envelope map[string]any) {
	if err := json.Unmarshal([]byte(payload), &envelope); err != nil {
		return "", "", nil
	}
	return toString(envelope["message_type"]), toString(envelope["message_id"]), envelope
}

func toString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case fmt.Stringer:
		return x.String()
	default:
		return ""
	}
}

func toFloat64(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}

func containsString(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

func nowRFC3339() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func newMessageID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func deterministicUUID(seed string) string {
	h := sha1.Sum([]byte(seed))
	var b [16]byte
	copy(b[:], h[:16])
	b[6] = (b[6] & 0x0f) | 0x50
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
