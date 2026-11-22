package victron

import (
	"encoding/json"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type emitRecord struct {
	path   dbus.ObjectPath
	signal string
	values []any
}

type s2FakeConn struct {
	mu      sync.Mutex
	emitted []emitRecord
}

func (f *s2FakeConn) Close() error              { return nil }
func (f *s2FakeConn) BusObject() dbus.BusObject { return nil }
func (f *s2FakeConn) Object(dest string, path dbus.ObjectPath) dbus.BusObject {
	return nil
}
func (f *s2FakeConn) AddMatchSignal(options ...dbus.MatchOption) error { return nil }
func (f *s2FakeConn) Signal(ch chan<- *dbus.Signal)                    {}
func (f *s2FakeConn) RemoveSignal(ch chan<- *dbus.Signal)              {}
func (f *s2FakeConn) Export(v any, path dbus.ObjectPath, iface string) error {
	return nil
}
func (f *s2FakeConn) ExportAll(v any, path dbus.ObjectPath, iface string) error {
	return nil
}
func (f *s2FakeConn) RequestName(name string, flags dbus.RequestNameFlags) (dbus.RequestNameReply, error) {
	return dbus.RequestNameReplyPrimaryOwner, nil
}
func (f *s2FakeConn) ReleaseName(name string) (dbus.ReleaseNameReply, error) {
	return dbus.ReleaseNameReplyReleased, nil
}
func (f *s2FakeConn) Emit(path dbus.ObjectPath, name string, values ...any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.emitted = append(f.emitted, emitRecord{path: path, signal: name, values: values})
	return nil
}

func (f *s2FakeConn) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.emitted)
}

func (f *s2FakeConn) snapshot() []emitRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	copyOf := make([]emitRecord, len(f.emitted))
	copy(copyOf, f.emitted)
	return copyOf
}

var _ DBusConn = (*s2FakeConn)(nil)

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

const handshakeResponseMessageID = "a47e7b0d-12fc-4f6d-8907-99666f6a37f0"

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// s2Harness bundles a resource manager with a fake connection, clock and a
// recording of the control/release callbacks.
type s2Harness struct {
	t        *testing.T
	conn     *s2FakeConn
	rm       *s2ResourceManager
	clock    *fakeClock
	mu       sync.Mutex
	commands []S2ChargeCommand
	releases int
	active   []bool
	autoMode bool
}

func newS2Harness(t *testing.T, deviceName string) *s2Harness {
	t.Helper()
	h := &s2Harness{
		t:        t,
		conn:     &s2FakeConn{},
		clock:    &fakeClock{now: time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)},
		autoMode: true,
	}
	rm := newS2ResourceManager(h.conn, deviceName)
	rm.publishInterval = time.Hour
	rm.defaultControlType = ""
	rm.now = h.clock.Now
	rm.minCurrentReader = func() float64 { return 6 }
	rm.maxCurrentReader = func() float64 { return 16 }
	rm.autoModeReader = func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.autoMode
	}
	rm.controlCallback = func(cmd S2ChargeCommand) {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.commands = append(h.commands, cmd)
	}
	rm.releaseCallback = func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.releases++
	}
	rm.sessionActiveChanged = func(active bool) {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.active = append(h.active, active)
	}
	h.rm = rm
	t.Cleanup(rm.Close)
	return h
}

func (h *s2Harness) setAutoMode(auto bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.autoMode = auto
}

func (h *s2Harness) lastCommand() (S2ChargeCommand, int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.commands) == 0 {
		return S2ChargeCommand{}, 0
	}
	return h.commands[len(h.commands)-1], len(h.commands)
}

func (h *s2Harness) releaseCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.releases
}

func (h *s2Harness) connect(clientID string) {
	h.t.Helper()
	ok, dbusErr := h.rm.Connect(clientID, 15)
	require.Nil(h.t, dbusErr)
	require.True(h.t, ok)
	require.Nil(h.t, h.rm.Message(clientID, `{"message_type":"HandshakeResponse","message_id":"`+handshakeResponseMessageID+`","selected_protocol_version":"0.0.2-beta"}`))
}

func (h *s2Harness) send(clientID string, message map[string]any) {
	h.t.Helper()
	payload, err := json.Marshal(message)
	require.NoError(h.t, err)
	require.Nil(h.t, h.rm.Message(clientID, string(payload)))
}

func (h *s2Harness) selectControl(clientID, controlType string) {
	h.t.Helper()
	h.send(clientID, map[string]any{"message_type": "SelectControlType", "message_id": newMessageID(), "control_type": controlType})
}

// instruct sends an OMBC.Instruction and returns the ReceptionStatus for it.
func (h *s2Harness) instruct(clientID, modeID string, factor float64, abnormal bool) map[string]any {
	h.t.Helper()
	msgID := newMessageID()
	h.send(clientID, map[string]any{
		"message_type":          "OMBC.Instruction",
		"message_id":            msgID,
		"id":                    newMessageID(),
		"execution_time":        nowRFC3339(),
		"operation_mode_id":     modeID,
		"operation_mode_factor": factor,
		"abnormal_condition":    abnormal,
	})
	for _, msg := range h.messages("ReceptionStatus") {
		if msg["subject_message_id"] == msgID {
			return msg
		}
	}
	h.t.Fatalf("no ReceptionStatus for instruction %s", msgID)
	return nil
}

func (h *s2Harness) messages(messageType string) []map[string]any {
	h.t.Helper()
	result := []map[string]any{}
	for _, record := range h.conn.snapshot() {
		if record.signal != s2Iface+".Message" {
			continue
		}
		payload := decodePayload(h.t, record.values[1])
		if payload["message_type"] == messageType {
			result = append(result, payload)
		}
	}
	return result
}

func (h *s2Harness) last(messageType string) map[string]any {
	h.t.Helper()
	msgs := h.messages(messageType)
	require.NotEmpty(h.t, msgs, "no %s emitted", messageType)
	return msgs[len(msgs)-1]
}

func (h *s2Harness) disconnectSignals() []emitRecord {
	result := []emitRecord{}
	for _, record := range h.conn.snapshot() {
		if record.signal == s2Iface+".Disconnect" {
			result = append(result, record)
		}
	}
	return result
}

func (h *s2Harness) modeID(label string) string {
	h.t.Helper()
	sd := h.last("OMBC.SystemDescription")
	for _, raw := range sd["operation_modes"].([]any) {
		mode := raw.(map[string]any)
		if mode["diagnostic_label"] == label {
			return mode["id"].(string)
		}
	}
	h.t.Fatalf("operation mode %q not found", label)
	return ""
}

func (h *s2Harness) timerID(label string) string {
	h.t.Helper()
	sd := h.last("OMBC.SystemDescription")
	for _, raw := range sd["timers"].([]any) {
		timer := raw.(map[string]any)
		if timer["diagnostic_label"] == label {
			return timer["id"].(string)
		}
	}
	h.t.Fatalf("timer %q not found", label)
	return ""
}

func modeLabels(sd map[string]any) []string {
	labels := []string{}
	for _, raw := range sd["operation_modes"].([]any) {
		labels = append(labels, raw.(map[string]any)["diagnostic_label"].(string))
	}
	return labels
}

func TestS2RMConnectAdvertisesHandshakeAndDetails(t *testing.T) {
	h := newS2Harness(t, "SmartEVSE-1001")
	require.NoError(t, h.rm.Export())

	ok, dbusErr := h.rm.Connect("cem-client", 15)
	require.Nil(t, dbusErr)
	require.True(t, ok)
	require.Len(t, h.conn.emitted, 1)

	first := h.conn.emitted[0]
	assert.Equal(t, s2Path, first.path)
	assert.Equal(t, s2Iface+".Message", first.signal)
	assert.Equal(t, "cem-client", first.values[0])
	handshake := decodePayload(t, first.values[1])
	assert.Equal(t, "Handshake", handshake["message_type"])
	assertUUID(t, handshake["message_id"])
	assert.Equal(t, "RM", handshake["role"])
	assert.Equal(t, []any{"0.0.2-beta"}, handshake["supported_protocol_versions"])

	require.Nil(t, h.rm.Message("cem-client", `{"message_type":"HandshakeResponse","message_id":"`+handshakeResponseMessageID+`","selected_protocol_version":"0.0.2-beta"}`))
	records := h.conn.snapshot()
	require.Len(t, records, 3)
	ack := decodePayload(t, records[1].values[1])
	assert.Equal(t, "ReceptionStatus", ack["message_type"])
	assert.Equal(t, handshakeResponseMessageID, ack["subject_message_id"])
	assert.Equal(t, "OK", ack["status"])

	details := decodePayload(t, records[2].values[1])
	assert.Equal(t, "ResourceManagerDetails", details["message_type"])
	assertUUID(t, details["resource_id"])
	assert.Equal(t, "SmartEVSE-1001", details["name"])
	assert.Equal(t, "SmartEVSE", details["manufacturer"])
	assert.Equal(t, float64(2000), details["instruction_processing_delay"])
	assert.Equal(t, []any{"NOT_CONTROLABLE", "OPERATION_MODE_BASED_CONTROL"}, details["available_control_types"])
	assert.Equal(t, []any{"ELECTRIC.POWER.L1", "ELECTRIC.POWER.L2", "ELECTRIC.POWER.L3"}, details["provides_power_measurement_types"])
	roles := details["roles"].([]any)
	require.Len(t, roles, 1)
	assert.Equal(t, "ENERGY_CONSUMER", roles[0].(map[string]any)["role"])

	assert.Equal(t, []bool{false, true}, h.active)
}

func TestS2RMRejectsUnsupportedHandshakeVersion(t *testing.T) {
	h := newS2Harness(t, "SmartEVSE-1002")
	ok, _ := h.rm.Connect("cem-client", 15)
	require.True(t, ok)
	require.Nil(t, h.rm.Message("cem-client", `{"message_type":"HandshakeResponse","message_id":"`+handshakeResponseMessageID+`","selected_protocol_version":"9.9"}`))
	assert.Equal(t, "INVALID_CONTENT", h.last("ReceptionStatus")["status"])
	assert.Empty(t, h.messages("ResourceManagerDetails"))
}

func TestS2RMNotControllableSendsPowerMeasurement(t *testing.T) {
	h := newS2Harness(t, "SmartEVSE-1003")
	h.rm.powerL1Reader = func() float64 { return 700.1 }
	h.rm.powerL2Reader = func() float64 { return 800.2 }
	h.rm.powerL3Reader = func() float64 { return 722.2 }
	h.connect("cem-client")
	h.selectControl("cem-client", s2ControlNone)

	assert.Empty(t, h.messages("OMBC.SystemDescription"))
	assertPowerMeasurementValues(t, h.last("PowerMeasurement"), map[string]float64{
		"ELECTRIC.POWER.L1": 700.1,
		"ELECTRIC.POWER.L2": 800.2,
		"ELECTRIC.POWER.L3": 722.2,
	})
	_, commands := h.lastCommand()
	assert.Zero(t, commands)

	before := len(h.messages("PowerMeasurement"))
	require.NoError(t, h.rm.emitPeriodicState())
	assert.Len(t, h.messages("PowerMeasurement"), before+1)
}

func TestS2RMDefaultSinglePhaseAdvertises1380WAt6A(t *testing.T) {
	h := newS2Harness(t, "SmartEVSE-single-phase-default")
	h.connect("cem-client")
	h.selectControl("cem-client", s2ControlOMBC)
	modes := h.last("OMBC.SystemDescription")["operation_modes"].([]any)
	sixA := modes[1].(map[string]any)
	assert.Equal(t, "6A", sixA["diagnostic_label"])
	ranges := sixA["power_ranges"].([]any)
	require.Len(t, ranges, 1)
	assert.Equal(t, float64(1380), ranges[0].(map[string]any)["end_of_range"])
}

func TestS2RMUpdatesDescriptionWhenThreePhasesDetected(t *testing.T) {
	h := newS2Harness(t, "SmartEVSE-three-phase-detected")
	phases := 1
	h.rm.phasesReader = func() int { return phases }
	h.connect("cem-client")
	h.selectControl("cem-client", s2ControlOMBC)
	sixA := h.modeID("6A")
	require.Equal(t, "OK", h.instruct("cem-client", sixA, 1, false)["status"])

	phases = 3
	require.NoError(t, h.rm.emitPeriodicState())
	descriptions := h.messages("OMBC.SystemDescription")
	require.Len(t, descriptions, 2)
	modes := descriptions[1]["operation_modes"].([]any)
	ranges := modes[1].(map[string]any)["power_ranges"].([]any)
	require.Len(t, ranges, 3)
	var total float64
	for _, value := range ranges {
		total += value.(map[string]any)["end_of_range"].(float64)
	}
	assert.Equal(t, 4140.0, total)
	assert.Equal(t, sixA, h.last("OMBC.Status")["active_operation_mode_id"])
}

func TestS2RMSystemDescriptionHasStandbyAndCurrentSteps(t *testing.T) {
	h := newS2Harness(t, "SmartEVSE-1004")
	h.rm.phasesReader = func() int { return 3 }
	h.connect("cem-client")
	h.selectControl("cem-client", s2ControlOMBC)

	sd := h.last("OMBC.SystemDescription")
	assert.Equal(t, []string{"Standby", "6A", "7A", "8A", "9A", "10A", "11A", "12A", "13A", "14A", "15A", "16A"}, modeLabels(sd))

	modes := sd["operation_modes"].([]any)
	standby := modes[0].(map[string]any)
	assertUUID(t, standby["id"])
	ranges := standby["power_ranges"].([]any)
	require.Len(t, ranges, 3)
	assert.Equal(t, float64(0), ranges[0].(map[string]any)["end_of_range"])

	tenA := modes[5].(map[string]any)
	ranges = tenA["power_ranges"].([]any)
	require.Len(t, ranges, 3)
	for i, raw := range ranges {
		r := raw.(map[string]any)
		assert.Equal(t, "ELECTRIC.POWER.L"+string(rune('1'+i)), r["commodity_quantity"])
		assert.Equal(t, float64(2300), r["start_of_range"])
		assert.Equal(t, float64(2300), r["end_of_range"])
	}

	transitions := sd["transitions"].([]any)
	assert.Len(t, transitions, 12*11)
	timers := sd["timers"].([]any)
	require.Len(t, timers, 2)
	assert.Equal(t, float64(1*60*1000), timers[0].(map[string]any)["duration"])

	minOn, minOff := h.timerID("min-on"), h.timerID("min-off")
	standbyID, sixA, sevenA := h.modeID("Standby"), h.modeID("6A"), h.modeID("7A")
	found := 0
	for _, raw := range transitions {
		tr := raw.(map[string]any)
		switch {
		case tr["from"] == standbyID && tr["to"] == sixA:
			assert.Equal(t, []any{minOn}, tr["start_timers"])
			assert.Equal(t, []any{minOff}, tr["blocking_timers"])
			found++
		case tr["from"] == sixA && tr["to"] == standbyID:
			assert.Equal(t, []any{minOff}, tr["start_timers"])
			assert.Equal(t, []any{minOn}, tr["blocking_timers"])
			found++
		case tr["from"] == sixA && tr["to"] == sevenA:
			assert.Equal(t, []any{}, tr["start_timers"])
			assert.Equal(t, []any{}, tr["blocking_timers"])
			found++
		}
	}
	assert.Equal(t, 3, found)

	status := h.last("OMBC.Status")
	assert.Equal(t, standbyID, status["active_operation_mode_id"])
	assert.Equal(t, float64(1), status["operation_mode_factor"])
}

func TestS2RMModelHonoursPhasesAndMaxChargePower(t *testing.T) {
	h := newS2Harness(t, "SmartEVSE-1005")
	h.rm.phasesReader = func() int { return 1 }
	h.rm.maxChargePowerReader = func() float64 { return 2300 }
	h.connect("cem-client")
	h.selectControl("cem-client", s2ControlOMBC)

	sd := h.last("OMBC.SystemDescription")
	assert.Equal(t, []string{"Standby", "6A", "7A", "8A", "9A", "10A"}, modeLabels(sd))
	tenA := sd["operation_modes"].([]any)[5].(map[string]any)
	ranges := tenA["power_ranges"].([]any)
	require.Len(t, ranges, 1)
	assert.Equal(t, "ELECTRIC.POWER.L1", ranges[0].(map[string]any)["commodity_quantity"])
}

func TestS2ChargeModelWidensStepsForLargeRanges(t *testing.T) {
	model := buildS2ChargeModel("rid", 3, 6, 80, 0)
	require.LessOrEqual(t, len(model.modes)-1, s2MaxChargeSteps)
	assert.Equal(t, 6, model.modes[1].currentA)
	assert.Equal(t, 80, model.modes[len(model.modes)-1].currentA)
	sd := model.systemDescription("rid", 1, 1)
	assert.LessOrEqual(t, len(sd["transitions"].([]map[string]any)), 1000)
	assert.LessOrEqual(t, len(sd["operation_modes"].([]map[string]any)), 100)

	full := buildS2ChargeModel("rid", 3, 6, 32, 0)
	assert.Equal(t, 27, len(full.modes)-1)

	degenerate := buildS2ChargeModel("rid", 0, 0, 0, 0)
	assert.Equal(t, 1, degenerate.phases)
	assert.Equal(t, 1, len(degenerate.modes)-1)
	assert.Equal(t, 6, degenerate.modes[1].currentA)
}

func TestS2RMInitialModeFollowsChargerState(t *testing.T) {
	h := newS2Harness(t, "SmartEVSE-1006")
	h.rm.chargerStateReader = func() (bool, float64) { return true, 10.4 }
	h.connect("cem-client")
	h.selectControl("cem-client", s2ControlOMBC)

	assert.Equal(t, h.modeID("10A"), h.last("OMBC.Status")["active_operation_mode_id"])
	cmd, n := h.lastCommand()
	require.Equal(t, 1, n)
	assert.Equal(t, S2ChargeCommand{Charge: true, CurrentA: 10}, cmd)

	text, dbusErr := h.rm.GetText()
	require.Nil(t, dbusErr)
	assert.True(t, strings.Contains(text, "control=OPERATION_MODE_BASED_CONTROL"), text)
	assert.True(t, strings.Contains(text, "active_mode=10A"), text)
	assert.True(t, strings.Contains(text, "controlling=true"), text)
}

func TestS2RMInitialStandbyPausesCharger(t *testing.T) {
	h := newS2Harness(t, "SmartEVSE-1007")
	h.connect("cem-client")
	h.selectControl("cem-client", s2ControlOMBC)

	cmd, n := h.lastCommand()
	require.Equal(t, 1, n)
	assert.Equal(t, S2ChargeCommand{Charge: false}, cmd)
}

func TestS2RMBootstrapWaitsForReadinessAndDelay(t *testing.T) {
	h := newS2Harness(t, "SmartEVSE-1008")
	ready := false
	h.rm.ombcReadyReader = func() bool { return ready }
	h.rm.ombcBootstrapDelay = 2 * time.Second
	h.connect("cem-client")
	h.selectControl("cem-client", s2ControlOMBC)
	assert.Empty(t, h.messages("OMBC.SystemDescription"))

	// Instructions before the description are a temporary error.
	reception := h.instruct("cem-client", "c4bd8ab2-8c9b-4e57-9b3e-7f1e9d8a0001", 1, false)
	assert.Equal(t, "TEMPORARY_ERROR", reception["status"])

	ready = true
	require.NoError(t, h.rm.emitPeriodicState())
	assert.Empty(t, h.messages("OMBC.SystemDescription"))

	h.clock.Advance(3 * time.Second)
	require.NoError(t, h.rm.emitPeriodicState())
	assert.Len(t, h.messages("OMBC.SystemDescription"), 1)
	assert.Len(t, h.messages("OMBC.Status"), 1)
}

func TestS2RMInstructionTransitionsAndTimers(t *testing.T) {
	h := newS2Harness(t, "opportunity_loads")
	h.connect("opportunity_loads")
	h.selectControl("opportunity_loads", s2ControlOMBC)
	standbyID, tenA, twelveA := h.modeID("Standby"), h.modeID("10A"), h.modeID("12A")
	minOn, minOff := h.timerID("min-on"), h.timerID("min-off")

	reception := h.instruct("opportunity_loads", tenA, 1, false)
	assert.Equal(t, "OK", reception["status"])
	cmd, _ := h.lastCommand()
	assert.Equal(t, S2ChargeCommand{Charge: true, CurrentA: 10}, cmd)
	status := h.last("OMBC.Status")
	assert.Equal(t, tenA, status["active_operation_mode_id"])
	assert.Equal(t, standbyID, status["previous_operation_mode_id"])
	assert.NotEmpty(t, status["transition_timestamp"])
	timer := h.last("OMBC.TimerStatus")
	assert.Equal(t, minOn, timer["timer_id"])
	assert.Equal(t, h.clock.Now().Add(1*time.Minute).Format(time.RFC3339Nano), timer["finished_at"])

	// Changing between charge steps is not blocked by min-on.
	h.clock.Advance(30 * time.Second)
	reception = h.instruct("opportunity_loads", twelveA, 1, false)
	assert.Equal(t, "OK", reception["status"])
	cmd, _ = h.lastCommand()
	assert.Equal(t, S2ChargeCommand{Charge: true, CurrentA: 12}, cmd)
	assert.Len(t, h.messages("OMBC.TimerStatus"), 1)

	// Stopping is blocked while min-on runs.
	_, before := h.lastCommand()
	reception = h.instruct("opportunity_loads", standbyID, 1, false)
	assert.Equal(t, "TEMPORARY_ERROR", reception["status"])
	assert.Contains(t, reception["diagnostic_label"], "min-on")
	_, after := h.lastCommand()
	assert.Equal(t, before, after)
	assert.Equal(t, twelveA, h.last("OMBC.Status")["active_operation_mode_id"])

	h.clock.Advance(30 * time.Second)
	reception = h.instruct("opportunity_loads", standbyID, 1, false)
	assert.Equal(t, "OK", reception["status"])
	cmd, _ = h.lastCommand()
	assert.Equal(t, S2ChargeCommand{Charge: false}, cmd)
	assert.Equal(t, minOff, h.last("OMBC.TimerStatus")["timer_id"])

	// Starting again is blocked by min-off unless it is an abnormal condition.
	reception = h.instruct("opportunity_loads", tenA, 1, false)
	assert.Equal(t, "TEMPORARY_ERROR", reception["status"])
	assert.Contains(t, reception["diagnostic_label"], "min-off")
	reception = h.instruct("opportunity_loads", tenA, 1, true)
	assert.Equal(t, "OK", reception["status"])
	cmd, _ = h.lastCommand()
	assert.Equal(t, S2ChargeCommand{Charge: true, CurrentA: 10}, cmd)
}

func TestS2RMInstructionIsIdempotent(t *testing.T) {
	h := newS2Harness(t, "SmartEVSE-1010")
	h.connect("cem-client")
	h.selectControl("cem-client", s2ControlOMBC)
	tenA := h.modeID("10A")

	require.Equal(t, "OK", h.instruct("cem-client", tenA, 1, false)["status"])
	_, n := h.lastCommand()
	statuses := len(h.messages("OMBC.Status"))

	require.Equal(t, "OK", h.instruct("cem-client", tenA, 1, false)["status"])
	_, n2 := h.lastCommand()
	assert.Equal(t, n, n2)
	assert.Len(t, h.messages("OMBC.Status"), statuses+1)
}

func TestS2RMPlugInReportsStandbyAndRequestsFreshInstruction(t *testing.T) {
	h := newS2Harness(t, "SmartEVSE-plugin")
	h.connect("cem-client")
	h.selectControl("cem-client", s2ControlOMBC)
	standby, tenA := h.modeID("Standby"), h.modeID("10A")
	require.Equal(t, "OK", h.instruct("cem-client", tenA, 1, false)["status"])

	h.rm.onVehiclePluggedIn()
	assert.Empty(t, h.disconnectSignals())
	cmd, _ := h.lastCommand()
	assert.Equal(t, S2ChargeCommand{}, cmd)
	status := h.last("OMBC.Status")
	assert.Equal(t, standby, status["active_operation_mode_id"])
	assert.Equal(t, tenA, status["previous_operation_mode_id"])
	assert.Equal(t, h.timerID("min-off"), h.last("OMBC.TimerStatus")["timer_id"])
	assert.Equal(t, "TEMPORARY_ERROR", h.instruct("cem-client", tenA, 1, false)["status"])

	h.clock.Advance(h.rm.minOffDuration)
	assert.Equal(t, "OK", h.instruct("cem-client", tenA, 1, false)["status"])
	cmd, _ = h.lastCommand()
	assert.Equal(t, S2ChargeCommand{Charge: true, CurrentA: 10}, cmd)
}

func TestS2RMPlugInWhileAlreadyInStandbyDoesNotRestartTimer(t *testing.T) {
	h := newS2Harness(t, "SmartEVSE-plugin-standby")
	h.connect("cem-client")
	h.selectControl("cem-client", s2ControlOMBC)
	h.rm.onVehiclePluggedIn()
	assert.Equal(t, h.modeID("Standby"), h.last("OMBC.Status")["active_operation_mode_id"])
	assert.Empty(t, h.messages("OMBC.TimerStatus"))
	assert.Equal(t, "OK", h.instruct("cem-client", h.modeID("10A"), 1, false)["status"])
}

func TestS2RMDynamicESSDoesNotReceiveTimerStatus(t *testing.T) {
	h := newS2Harness(t, "SmartEVSE-1011")
	h.connect(s2ClientDynamicESS)
	h.selectControl(s2ClientDynamicESS, s2ControlOMBC)
	require.Equal(t, "OK", h.instruct(s2ClientDynamicESS, h.modeID("16A"), 1, false)["status"])
	assert.Empty(t, h.messages("OMBC.TimerStatus"))
	// The timer is still enforced.
	assert.Equal(t, "TEMPORARY_ERROR", h.instruct(s2ClientDynamicESS, h.modeID("Standby"), 1, false)["status"])
}

func TestS2RMRejectsInvalidInstructions(t *testing.T) {
	h := newS2Harness(t, "SmartEVSE-1012")
	h.connect("cem-client")

	assert.Equal(t, "INVALID_CONTENT", h.instruct("cem-client", "c4bd8ab2-8c9b-4e57-9b3e-7f1e9d8a0001", 1, false)["status"])

	h.selectControl("cem-client", s2ControlOMBC)
	tenA := h.modeID("10A")
	_, commands := h.lastCommand()

	assert.Equal(t, "INVALID_CONTENT", h.instruct("cem-client", "c4bd8ab2-8c9b-4e57-9b3e-7f1e9d8a0001", 1, false)["status"])
	assert.Equal(t, "INVALID_CONTENT", h.instruct("cem-client", tenA, 1.5, false)["status"])
	assert.Equal(t, "INVALID_CONTENT", h.instruct("cem-client", tenA, -0.1, false)["status"])

	h.setAutoMode(false)
	reception := h.instruct("cem-client", tenA, 1, false)
	assert.Equal(t, "TEMPORARY_ERROR", reception["status"])
	assert.Contains(t, reception["diagnostic_label"], "Auto")

	_, after := h.lastCommand()
	assert.Equal(t, commands, after)

	h.setAutoMode(true)
	// A factor below 1 is accepted but the fixed-power step is used as is.
	assert.Equal(t, "OK", h.instruct("cem-client", tenA, 0.5, false)["status"])
	cmd, _ := h.lastCommand()
	assert.Equal(t, S2ChargeCommand{Charge: true, CurrentA: 10}, cmd)
	assert.Equal(t, float64(1), h.last("OMBC.Status")["operation_mode_factor"])
}

func TestS2RMRefreshesModelWhenLimitsChange(t *testing.T) {
	h := newS2Harness(t, "SmartEVSE-1013")
	maxCurrent := 16.0
	phases := 3
	h.rm.maxCurrentReader = func() float64 { return maxCurrent }
	h.rm.phasesReader = func() int { return phases }
	h.connect("cem-client")
	h.selectControl("cem-client", s2ControlOMBC)
	require.Equal(t, "OK", h.instruct("cem-client", h.modeID("16A"), 1, false)["status"])

	require.NoError(t, h.rm.emitPeriodicState())
	assert.Len(t, h.messages("OMBC.SystemDescription"), 1)

	maxCurrent = 10
	require.NoError(t, h.rm.emitPeriodicState())
	sd := h.last("OMBC.SystemDescription")
	assert.Len(t, h.messages("OMBC.SystemDescription"), 2)
	assert.Equal(t, "10A", modeLabels(sd)[len(modeLabels(sd))-1])
	assert.Equal(t, h.modeID("10A"), h.last("OMBC.Status")["active_operation_mode_id"])
	cmd, _ := h.lastCommand()
	assert.Equal(t, S2ChargeCommand{Charge: true, CurrentA: 10}, cmd)

	phases = 1
	require.NoError(t, h.rm.emitPeriodicState())
	assert.Len(t, h.messages("OMBC.SystemDescription"), 3)
	ranges := h.last("OMBC.SystemDescription")["operation_modes"].([]any)[1].(map[string]any)["power_ranges"].([]any)
	assert.Len(t, ranges, 1)
	assert.Equal(t, h.modeID("10A"), h.last("OMBC.Status")["active_operation_mode_id"])
}

func TestS2RMWatchdogDropsSilentSession(t *testing.T) {
	h := newS2Harness(t, "SmartEVSE-1014")
	h.connect("cem-client")
	h.selectControl("cem-client", s2ControlOMBC)
	require.Equal(t, "OK", h.instruct("cem-client", h.modeID("10A"), 1, false)["status"])

	h.clock.Advance(30 * time.Second)
	ok, _ := h.rm.KeepAlive("cem-client")
	require.True(t, ok)
	h.clock.Advance(30 * time.Second)
	assert.False(t, h.rm.checkWatchdog())

	h.clock.Advance(10 * time.Second)
	assert.True(t, h.rm.checkWatchdog())
	signals := h.disconnectSignals()
	require.Len(t, signals, 1)
	assert.Equal(t, []any{"cem-client", "KeepAlive missed"}, signals[0].values)
	assert.Equal(t, 1, h.releaseCount())
	assert.Equal(t, false, h.active[len(h.active)-1])

	ok, _ = h.rm.KeepAlive("cem-client")
	assert.False(t, ok)
}

func TestS2RMRejectsSecondClientAndUnknownClients(t *testing.T) {
	h := newS2Harness(t, "SmartEVSE-1015")
	h.connect("cem-client")

	ok, dbusErr := h.rm.Connect("other-client", 15)
	require.Nil(t, dbusErr)
	assert.False(t, ok)

	ok, _ = h.rm.KeepAlive("other-client")
	assert.False(t, ok)
	require.Nil(t, h.rm.Message("other-client", `{"message_type":"SelectControlType","message_id":"fd941d1e-fd9b-4db0-b636-6c3a90df8bd6","control_type":"NOT_CONTROLABLE"}`))
	signals := h.disconnectSignals()
	require.Len(t, signals, 2)
	assert.Equal(t, "other-client", signals[0].values[0])
	assert.Empty(t, h.messages("PowerMeasurement"))

	// Reconnecting the same client is allowed.
	ok, _ = h.rm.Connect("cem-client", 15)
	assert.True(t, ok)
}

func TestS2RMReleasesOnDisconnectAndControlTypeChange(t *testing.T) {
	h := newS2Harness(t, "SmartEVSE-1016")
	h.connect("cem-client")
	h.selectControl("cem-client", s2ControlOMBC)
	assert.Zero(t, h.releaseCount())

	h.selectControl("cem-client", s2ControlNone)
	assert.Equal(t, 1, h.releaseCount())

	h.selectControl("cem-client", s2ControlOMBC)
	require.Nil(t, h.rm.Disconnect("someone-else"))
	assert.Equal(t, 1, h.releaseCount())
	require.Nil(t, h.rm.Disconnect("cem-client"))
	assert.Equal(t, 2, h.releaseCount())
	assert.Equal(t, false, h.active[len(h.active)-1])

	// Release is only done once per period of control.
	require.Nil(t, h.rm.Disconnect("cem-client"))
	assert.Equal(t, 2, h.releaseCount())
}

func TestS2RMModeChangesReleaseAndReapply(t *testing.T) {
	h := newS2Harness(t, "SmartEVSE-1017")
	h.connect("cem-client")
	h.selectControl("cem-client", s2ControlOMBC)
	require.Equal(t, "OK", h.instruct("cem-client", h.modeID("8A"), 1, false)["status"])
	_, n := h.lastCommand()

	h.setAutoMode(false)
	h.rm.onModeChanged(EV_Mode_Auto, EV_Mode_Manual)
	assert.Equal(t, 1, h.releaseCount())

	h.setAutoMode(true)
	h.rm.onModeChanged(EV_Mode_Manual, EV_Mode_Auto)
	cmd, n2 := h.lastCommand()
	assert.Equal(t, n+1, n2)
	assert.Equal(t, S2ChargeCommand{Charge: true, CurrentA: 8}, cmd)
}

func TestS2RMBootstrapSkipsApplyOutsideAutoMode(t *testing.T) {
	h := newS2Harness(t, "SmartEVSE-1018")
	h.setAutoMode(false)
	h.connect("cem-client")
	h.selectControl("cem-client", s2ControlOMBC)
	assert.Len(t, h.messages("OMBC.SystemDescription"), 1)
	_, n := h.lastCommand()
	assert.Zero(t, n)
	require.Nil(t, h.rm.Disconnect("cem-client"))
	assert.Zero(t, h.releaseCount())
}

func TestS2RMRunningTimersAreResentOnNewSession(t *testing.T) {
	h := newS2Harness(t, "SmartEVSE-1019")
	h.connect("cem-client")
	h.selectControl("cem-client", s2ControlOMBC)
	require.Equal(t, "OK", h.instruct("cem-client", h.modeID("10A"), 1, false)["status"])
	require.Nil(t, h.rm.Disconnect("cem-client"))

	h.connect("cem-client")
	h.selectControl("cem-client", s2ControlOMBC)
	timers := h.messages("OMBC.TimerStatus")
	require.Len(t, timers, 2)
	assert.Equal(t, h.timerID("min-on"), timers[1]["timer_id"])
}

func TestS2RMCanUseEvChargerReaders(t *testing.T) {
	charger := newEvChargerFields(nil, 6, 10, 16, 0, 0)
	charger.SetAcL1Power(700.1)
	charger.SetAcL2Power(800.2)
	charger.SetAcL3Power(722.2)

	h := newS2Harness(t, "SmartEVSE-1020")
	h.rm.powerL1Reader = charger.GetAcL1Power
	h.rm.powerL2Reader = charger.GetAcL2Power
	h.rm.powerL3Reader = charger.GetAcL3Power
	h.connect("cem-client")
	h.selectControl("cem-client", s2ControlNone)
	assertPowerMeasurementValues(t, h.last("PowerMeasurement"), map[string]float64{
		"ELECTRIC.POWER.L1": 700.1,
		"ELECTRIC.POWER.L2": 800.2,
		"ELECTRIC.POWER.L3": 722.2,
	})
}

func TestS2RMSummarizeSkipsRoutineMessages(t *testing.T) {
	h := newS2Harness(t, "SmartEVSE-1021")
	h.connect("cem-client")
	h.selectControl("cem-client", s2ControlOMBC)
	tenA := h.modeID("10A")

	assert.Empty(t, h.rm.summarizeMessage("PowerMeasurement", map[string]any{}))
	assert.Empty(t, h.rm.summarizeMessage("ReceptionStatus", map[string]any{"status": "OK"}))
	assert.Equal(t, `type=ReceptionStatus status=TEMPORARY_ERROR label="blocked"`,
		h.rm.summarizeMessage("ReceptionStatus", map[string]any{"status": "TEMPORARY_ERROR", "diagnostic_label": "blocked"}))
	assert.Equal(t, "type=OMBC.Instruction mode=10A factor=1 abnormal=false",
		h.rm.summarizeMessage("OMBC.Instruction", map[string]any{"operation_mode_id": tenA, "operation_mode_factor": 1.0}))
	assert.Equal(t, "type=OMBC.Status mode=10A",
		h.rm.summarizeMessage("OMBC.Status", map[string]any{"active_operation_mode_id": tenA}))
}

func assertPowerMeasurementValues(t *testing.T, measurement map[string]any, expected map[string]float64) {
	t.Helper()
	values, ok := measurement["values"].([]any)
	require.True(t, ok)
	require.Len(t, values, len(expected))
	actual := map[string]float64{}
	for _, raw := range values {
		entry := raw.(map[string]any)
		actual[entry["commodity_quantity"].(string)] = entry["value"].(float64)
	}
	assert.Equal(t, expected, actual)
}

func assertUUID(t *testing.T, raw any) {
	t.Helper()
	s, ok := raw.(string)
	require.True(t, ok)
	assert.Regexp(t, uuidPattern, s)
}

func decodePayload(t *testing.T, raw any) map[string]any {
	t.Helper()
	s, ok := raw.(string)
	require.True(t, ok)
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(s), &payload))
	return payload
}
