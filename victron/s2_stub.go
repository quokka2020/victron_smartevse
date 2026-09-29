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
)

const (
	s2Iface                  = "com.victronenergy.S2"
	s2Path                   = dbus.ObjectPath("/S2/0/Rm")
	s2DefaultPublishInterval = 5 * time.Second
)

var supportedProtocolVersions = []string{"0.0.2-beta"}

type s2RMStub struct {
	conn                    DBusConn
	deviceName              string
	resourceID              string
	publishInterval         time.Duration
	ombcBootstrapDelay      time.Duration
	defaultControlType      string
	ombcReadyReader         func() bool
	powerReader             func() float64
	powerL1Reader           func() float64
	powerL2Reader           func() float64
	powerL3Reader           func() float64
	maxChargePowerReader    func() float64
	sessionActiveChanged    func(active bool)
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
	activeOperationModeID   string
	previousOperationModeID string
	operationModeFactor     float64
	lastTransitionAt        time.Time
	lastRxType              string
	lastTxType              string
	lastRxMessageID         string
	rxCount                 int
	txCount                 int
	publisherStop           chan struct{}
	publisherDone           chan struct{}
	ombcDescriptionSent     bool
}

func newS2RMStub(conn DBusConn, deviceName string) *s2RMStub {
	return &s2RMStub{
		conn:                 conn,
		deviceName:           deviceName,
		resourceID:           deterministicUUID(deviceName),
		publishInterval:      s2DefaultPublishInterval,
		ombcBootstrapDelay:   0,
		defaultControlType:   "OPERATION_MODE_BASED_CONTROL",
		ombcReadyReader:      func() bool { return true },
		powerReader:          func() float64 { return 0 },
		powerL1Reader:        func() float64 { return 0 },
		powerL2Reader:        func() float64 { return 0 },
		powerL3Reader:        func() float64 { return 0 },
		maxChargePowerReader: func() float64 { return 11000 },
		sessionActiveChanged: func(bool) {},
	}
}
func (s *s2RMStub) Export() error {
	return s.conn.ExportAll(s, s2Path, s2Iface)
}
func (s *s2RMStub) Close() {
	s.stopPublisher()
	s.publishSessionActive(false)
}
func (s *s2RMStub) Discover() (bool, *dbus.Error) { return true, nil }
func (s *s2RMStub) GetText() (string, *dbus.Error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	client := s.clientID
	if client == "" {
		client = "-"
	}
	protocol := s.selectedProtocolVersion
	if protocol == "" {
		protocol = "-"
	}
	control := s.selectedControlType
	if control == "" {
		control = "-"
	}
	activeMode := s.activeOperationModeID
	if activeMode == "" {
		activeMode = "-"
	}
	lastRx := s.lastRxType
	if lastRx == "" {
		lastRx = "-"
	}
	return fmt.Sprintf("S2 stub (%s) connected=%t client=%s protocol=%s control=%s active_mode=%s rx=%d tx=%d last_rx=%s", s.deviceName, s.connected, client, protocol, control, activeMode, s.rxCount, s.txCount, lastRx), nil
}
func (s *s2RMStub) Connect(clientID string, keepAliveS int32) (bool, *dbus.Error) {
	s.stopPublisher()
	s.mu.Lock()
	s.clientID = clientID
	s.connected = true
	s.sessionEstablished = false
	s.keepAliveS = keepAliveS
	s.connectedAt = time.Now()
	s.sessionEstablishedAt = time.Time{}
	s.lastSeen = time.Now()
	s.selectedProtocolVersion = ""
	s.ombcDescriptionSent = false
	s.lastRxType = ""
	s.lastTxType = ""
	s.lastRxMessageID = ""
	s.rxCount = 0
	s.txCount = 0
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
	return true, nil
}
func (s *s2RMStub) KeepAlive(clientID string) (bool, *dbus.Error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.connected || s.clientID != clientID {
		log.Printf("S2 KEEPALIVE rejected [%s] client=%s", s.deviceName, clientID)
		return false, nil
	}
	s.lastSeen = time.Now()
	log.Printf("S2 KEEPALIVE [%s] client=%s", s.deviceName, clientID)
	return true, nil
}
func (s *s2RMStub) Disconnect(clientID string) *dbus.Error {
	s.mu.Lock()
	wasConnected := s.connected && s.clientID == clientID
	s.connected = false
	s.sessionEstablished = false
	s.sessionEstablishedAt = time.Time{}
	s.ombcDescriptionSent = false
	s.clientID = ""
	s.keepAliveS = 0
	s.mu.Unlock()
	s.stopPublisher()
	s.publishSessionActive(false)
	if wasConnected {
		log.Printf("S2 DISCONNECT [%s] client=%s", s.deviceName, clientID)
	} else {
		log.Printf("S2 DISCONNECT ignored [%s] client=%s", s.deviceName, clientID)
	}
	return nil
}
func (s *s2RMStub) Message(clientID, payload string) *dbus.Error {
	msgType, msgID, envelope := parseS2Envelope(payload)
	log.Printf("S2 RX [%s] client=%s type=%s payload=%s", s.deviceName, clientID, msgType, payload)
	s.mu.Lock()
	connected := s.connected && s.clientID == clientID
	receptionStatus := "OK"
	diagnosticLabel := ""
	triggerBootstrap := false
	triggerSelectedControl := false
	triggerOMBCStatus := false
	if connected {
		s.lastSeen = time.Now()
		s.rxCount++
		s.lastRxType = msgType
		s.lastRxMessageID = msgID
		triggerBootstrap, triggerSelectedControl, triggerOMBCStatus = s.planIncomingResponseLocked(msgType, envelope, &receptionStatus, &diagnosticLabel)
	}
	s.mu.Unlock()
	if connected && shouldAck(msgType, msgID) {
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
	if connected && msgType == "HandshakeResponse" && receptionStatus == "OK" {
		s.publishSessionActive(true)
	}
	if connected && triggerBootstrap {
		if err := s.emitBootstrapState(clientID); err != nil {
			log.Printf("S2 bootstrap emit failed [%s]: %v", s.deviceName, err)
		}
	}
	if connected && triggerSelectedControl {
		if err := s.emitSelectedControlTypeState(clientID); err != nil {
			log.Printf("S2 control state emit failed [%s]: %v", s.deviceName, err)
		}
		s.restartPublisher()
	}
	if connected && triggerOMBCStatus {
		if err := s.emitOMBCStatus(clientID); err != nil {
			log.Printf("S2 OMBC status emit failed [%s]: %v", s.deviceName, err)
		}
	}
	return nil
}
func (s *s2RMStub) restartPublisher() {
	s.stopPublisher()
	s.startPublisher()
}
func (s *s2RMStub) startPublisher() {
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
func (s *s2RMStub) stopPublisher() {
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
func (s *s2RMStub) publisherLoop(stop <-chan struct{}, done chan<- struct{}, interval time.Duration) {
	defer close(done)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			if err := s.emitPeriodicState(); err != nil {
				log.Printf("S2 periodic emit failed [%s]: %v", s.deviceName, err)
			}
		}
	}
}
func (s *s2RMStub) emitPeriodicState() error {
	s.mu.Lock()
	connected := s.connected
	sessionEstablished := s.sessionEstablished
	clientID := s.clientID
	controlType := s.selectedControlType
	ombcDescriptionSent := s.ombcDescriptionSent
	s.mu.Unlock()
	if !connected || !sessionEstablished || clientID == "" {
		return nil
	}
	switch controlType {
	case "NOT_CONTROLABLE":
		return s.emitPowerMeasurement(clientID)
	case "OPERATION_MODE_BASED_CONTROL":
		if !ombcDescriptionSent {
			if !s.canEmitOMBCBootstrap() {
				return nil
			}
			if err := s.emitOMBCSystemDescription(clientID); err != nil {
				return err
			}
			s.mu.Lock()
			s.ombcDescriptionSent = true
			s.mu.Unlock()
		}
		return s.emitOMBCStatus(clientID)
	default:
		return nil
	}
}
func (s *s2RMStub) planIncomingResponseLocked(messageType string, envelope map[string]any, receptionStatus, diagnosticLabel *string) (emitBootstrapState, emitSelectedControlState, emitOMBCStatus bool) {
	if envelope == nil {
		*receptionStatus = "INVALID_MESSAGE"
		*diagnosticLabel = "invalid JSON payload"
		return false, false, false
	}
	switch messageType {
	case "HandshakeResponse":
		return s.planHandshakeResponseLocked(toString(envelope["selected_protocol_version"]), receptionStatus, diagnosticLabel)
	case "SelectControlType":
		return s.planSelectControlTypeLocked(toString(envelope["control_type"]), receptionStatus, diagnosticLabel)
	case "OMBC.Instruction":
		return s.applyOMBCInstructionLocked(envelope, receptionStatus, diagnosticLabel)
	default:
		return false, false, false
	}
}
func (s *s2RMStub) planHandshakeResponseLocked(selectedVersion string, receptionStatus, diagnosticLabel *string) (emitBootstrapState, emitSelectedControlState, emitOMBCStatus bool) {
	if selectedVersion == "" {
		*receptionStatus = "INVALID_CONTENT"
		*diagnosticLabel = "missing selected_protocol_version"
		return false, false, false
	}
	if !containsString(supportedProtocolVersions, selectedVersion) {
		*receptionStatus = "INVALID_CONTENT"
		*diagnosticLabel = fmt.Sprintf("unsupported selected_protocol_version %q", selectedVersion)
		return false, false, false
	}
	s.selectedProtocolVersion = selectedVersion
	s.sessionEstablished = true
	s.sessionEstablishedAt = time.Now()
	*receptionStatus = "OK"
	return true, false, false
}
func (s *s2RMStub) planSelectControlTypeLocked(controlType string, receptionStatus, diagnosticLabel *string) (emitBootstrapState, emitSelectedControlState, emitOMBCStatus bool) {
	if !s.sessionEstablished {
		*receptionStatus = "TEMPORARY_ERROR"
		*diagnosticLabel = "Connection not yet established."
		return false, false, false
	}
	switch controlType {
	case "NOT_CONTROLABLE":
		s.selectedControlType = controlType
		s.activeOperationModeID = ""
		s.previousOperationModeID = ""
		s.operationModeFactor = 0
		s.lastTransitionAt = time.Time{}
		s.ombcDescriptionSent = false
		*receptionStatus = "OK"
		return false, true, false
	case "OPERATION_MODE_BASED_CONTROL":
		s.selectedControlType = controlType
		if !s.isKnownOMBCMode(s.activeOperationModeID) {
			s.activeOperationModeID = s.ombcIdleModeID()
			s.previousOperationModeID = ""
			s.lastTransitionAt = time.Time{}
		}
		if s.operationModeFactor == 0 {
			s.operationModeFactor = 1.0
		}
		s.ombcDescriptionSent = false
		*receptionStatus = "OK"
		return false, true, false
	case "":
		*receptionStatus = "INVALID_CONTENT"
		*diagnosticLabel = "missing control_type"
		return false, false, false
	default:
		*receptionStatus = "INVALID_CONTENT"
		*diagnosticLabel = fmt.Sprintf("unsupported control_type %q", controlType)
		return false, false, false
	}
}
func (s *s2RMStub) applyOMBCInstructionLocked(envelope map[string]any, receptionStatus, diagnosticLabel *string) (emitBootstrapState, emitSelectedControlState, emitOMBCStatus bool) {
	if !s.sessionEstablished {
		*receptionStatus = "TEMPORARY_ERROR"
		*diagnosticLabel = "Connection not yet established."
		return false, false, false
	}
	if s.selectedControlType != "OPERATION_MODE_BASED_CONTROL" {
		*receptionStatus = "INVALID_CONTENT"
		*diagnosticLabel = "OMBC.Instruction received while OMBC is not selected"
		return false, false, false
	}
	operationModeID := toString(envelope["operation_mode_id"])
	if operationModeID == "" {
		*receptionStatus = "INVALID_CONTENT"
		*diagnosticLabel = "missing operation_mode_id"
		return false, false, false
	}
	if !s.isKnownOMBCMode(operationModeID) {
		*receptionStatus = "INVALID_CONTENT"
		*diagnosticLabel = fmt.Sprintf("unknown operation_mode_id %q", operationModeID)
		return false, false, false
	}
	factor, ok := toFloat64(envelope["operation_mode_factor"])
	if !ok {
		*receptionStatus = "INVALID_CONTENT"
		*diagnosticLabel = "missing or invalid operation_mode_factor"
		return false, false, false
	}
	if factor < 0 || factor > 1 {
		*receptionStatus = "INVALID_CONTENT"
		*diagnosticLabel = fmt.Sprintf("operation_mode_factor %.3f out of range", factor)
		return false, false, false
	}
	if operationModeID != s.activeOperationModeID {
		s.previousOperationModeID = s.activeOperationModeID
		s.lastTransitionAt = time.Now().UTC()
	}
	s.activeOperationModeID = operationModeID
	s.operationModeFactor = factor
	*receptionStatus = "OK"
	return false, false, true
}
func (s *s2RMStub) emitBootstrapState(clientID string) error {
	if err := s.emitResourceManagerDetails(clientID); err != nil {
		return err
	}
	return s.emitRestoredState(clientID)
}
func (s *s2RMStub) emitResourceManagerDetails(clientID string) error {
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
		"instruction_processing_delay": 0,
		"available_control_types":      []string{"NOT_CONTROLABLE", "OPERATION_MODE_BASED_CONTROL"},
		"provides_forecast":            false,
		"provides_power_measurement_types": []string{
			"ELECTRIC.POWER.L1",
			"ELECTRIC.POWER.L2",
			"ELECTRIC.POWER.L3",
		},
	}
	return s.emitMessage(clientID, rmDetails)
}
func (s *s2RMStub) emitSelectedControlTypeState(clientID string) error {
	s.mu.Lock()
	controlType := s.selectedControlType
	s.mu.Unlock()
	switch controlType {
	case "NOT_CONTROLABLE":
		return s.emitPowerMeasurement(clientID)
	case "OPERATION_MODE_BASED_CONTROL":
		if !s.canEmitOMBCBootstrap() {
			log.Printf("S2 OMBC bootstrap deferred [%s] client=%s waiting for readiness/delay", s.deviceName, clientID)
			return nil
		}
		if err := s.emitOMBCSystemDescription(clientID); err != nil {
			return err
		}
		s.mu.Lock()
		s.ombcDescriptionSent = true
		s.mu.Unlock()
		return s.emitOMBCStatus(clientID)
	default:
		log.Printf("S2 SELECT_CONTROL_TYPE [%s] no stub follow-up for control=%s", s.deviceName, controlType)
		return nil
	}
}
func (s *s2RMStub) emitRestoredState(clientID string) error {
	s.mu.Lock()
	s.applyDefaultControlStateLocked()
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
func (s *s2RMStub) applyDefaultControlStateLocked() {
	if s.selectedControlType != "" || s.defaultControlType == "" {
		return
	}
	s.selectedControlType = s.defaultControlType
	s.previousOperationModeID = ""
	s.lastTransitionAt = time.Time{}
	switch s.defaultControlType {
	case "NOT_CONTROLABLE":
		s.activeOperationModeID = ""
		s.operationModeFactor = 0
	case "OPERATION_MODE_BASED_CONTROL":
		s.activeOperationModeID = s.ombcIdleModeID()
		s.operationModeFactor = 1.0
	}
}
func (s *s2RMStub) emitPowerMeasurement(clientID string) error {
	powerL1 := s.readPowerValue(s.powerL1Reader)
	powerL2 := s.readPowerValue(s.powerL2Reader)
	powerL3 := s.readPowerValue(s.powerL3Reader)
	measurement := map[string]any{
		"message_type":          "PowerMeasurement",
		"message_id":            newMessageID(),
		"measurement_timestamp": nowRFC3339(),
		"values": []map[string]any{{
			"commodity_quantity": "ELECTRIC.POWER.L1",
			"value":              powerL1,
		}, {
			"commodity_quantity": "ELECTRIC.POWER.L2",
			"value":              powerL2,
		}, {
			"commodity_quantity": "ELECTRIC.POWER.L3",
			"value":              powerL3,
		}},
	}
	return s.emitMessage(clientID, measurement)
}
func (s *s2RMStub) readPowerValue(reader func() float64) float64 {
	if reader == nil {
		return 0
	}
	return reader()
}

func (s *s2RMStub) isOMBCReady() bool {
	if s.ombcReadyReader == nil {
		return true
	}
	return s.ombcReadyReader()
}

func (s *s2RMStub) canEmitOMBCBootstrap() bool {
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
	return time.Since(establishedAt) >= delay
}

func (s *s2RMStub) emitOMBCSystemDescription(clientID string) error {
	idleID := s.ombcIdleModeID()
	chargeID := s.ombcChargeModeID()
	transitionID := deterministicUUID(s.resourceID + ":ombc-transition-idle-to-charge")
	transitionBackID := deterministicUUID(s.resourceID + ":ombc-transition-charge-to-idle")
	maxChargePower := s.readPowerValue(s.maxChargePowerReader)
	if maxChargePower <= 0 {
		maxChargePower = 11000
	}
	description := map[string]any{
		"message_type": "OMBC.SystemDescription",
		"message_id":   newMessageID(),
		"valid_from":   nowRFC3339(),
		"operation_modes": []map[string]any{
			{
				"id":               idleID,
				"diagnostic_label": "Idle",
				"power_ranges": []map[string]any{{
					"commodity_quantity": "ELECTRIC.POWER.L1",
					"start_of_range":     0.0,
					"end_of_range":       0.0,
				}},
				"abnormal_condition_only": false,
			},
			{
				"id":               chargeID,
				"diagnostic_label": "Charge",
				"power_ranges": []map[string]any{{
					"commodity_quantity": "ELECTRIC.POWER.L1",
					"start_of_range":     0.0,
					"end_of_range":       maxChargePower,
				}},
				"abnormal_condition_only": false,
			},
		},
		"transitions": []map[string]any{
			{
				"id":                      transitionID,
				"from":                    idleID,
				"to":                      chargeID,
				"start_timers":            []string{},
				"blocking_timers":         []string{},
				"abnormal_condition_only": false,
			},
			{
				"id":                      transitionBackID,
				"from":                    chargeID,
				"to":                      idleID,
				"start_timers":            []string{},
				"blocking_timers":         []string{},
				"abnormal_condition_only": false,
			},
		},
		"timers": []map[string]any{},
	}
	return s.emitMessage(clientID, description)
}
func (s *s2RMStub) emitOMBCStatus(clientID string) error {
	s.mu.Lock()
	activeModeID := s.activeOperationModeID
	if activeModeID == "" {
		activeModeID = s.ombcIdleModeID()
	}
	factor := s.operationModeFactor
	if factor == 0 {
		factor = 1.0
	}
	status := map[string]any{
		"message_type":             "OMBC.Status",
		"message_id":               newMessageID(),
		"active_operation_mode_id": activeModeID,
		"operation_mode_factor":    factor,
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
func (s *s2RMStub) ombcIdleModeID() string   { return deterministicUUID(s.resourceID + ":ombc-idle") }
func (s *s2RMStub) ombcChargeModeID() string { return deterministicUUID(s.resourceID + ":ombc-charge") }
func (s *s2RMStub) isKnownOMBCMode(id string) bool {
	return id == s.ombcIdleModeID() || id == s.ombcChargeModeID()
}
func (s *s2RMStub) emitMessage(clientID string, message map[string]any) error {
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
	log.Printf("S2 TX [%s] client=%s payload=%s", s.deviceName, clientID, msg)
	return s.conn.Emit(s2Path, s2Iface+".Message", clientID, msg)
}
func (s *s2RMStub) publishSessionActive(active bool) {
	if s.sessionActiveChanged != nil {
		s.sessionActiveChanged(active)
	}
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
