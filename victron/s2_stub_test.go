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
	_ = dest
	_ = path
	return nil
}
func (f *s2FakeConn) AddMatchSignal(options ...dbus.MatchOption) error {
	_ = options
	return nil
}
func (f *s2FakeConn) Signal(ch chan<- *dbus.Signal)       { _ = ch }
func (f *s2FakeConn) RemoveSignal(ch chan<- *dbus.Signal) { _ = ch }
func (f *s2FakeConn) Export(v any, path dbus.ObjectPath, iface string) error {
	_ = v
	_ = path
	_ = iface
	return nil
}
func (f *s2FakeConn) ExportAll(v any, path dbus.ObjectPath, iface string) error {
	_ = v
	_ = path
	_ = iface
	return nil
}
func (f *s2FakeConn) RequestName(name string, flags dbus.RequestNameFlags) (dbus.RequestNameReply, error) {
	_ = name
	_ = flags
	return dbus.RequestNameReplyPrimaryOwner, nil
}
func (f *s2FakeConn) ReleaseName(name string) (dbus.ReleaseNameReply, error) {
	_ = name
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

func TestS2StubConnectAdvertisesHandshakeAndDetails(t *testing.T) {
	fakeConn, stub := newTestS2Stub(t, "SmartEVSE-1001")

	require.NoError(t, stub.Export())

	ok, dbusErr := stub.Connect("cem-client", 15)
	require.Nil(t, dbusErr)
	require.True(t, ok)
	require.Len(t, fakeConn.emitted, 1)

	first := fakeConn.emitted[0]
	assert.Equal(t, s2Path, first.path)
	assert.Equal(t, s2Iface+".Message", first.signal)
	assert.Equal(t, "cem-client", first.values[0])

	payload1 := decodePayload(t, first.values[1])
	assert.Equal(t, "Handshake", payload1["message_type"])
	assertUUID(t, payload1["message_id"])
	assert.Equal(t, "RM", payload1["role"])
	assert.Equal(t, []any{"0.0.2-beta"}, payload1["supported_protocol_versions"])

	establishSession(t, stub, fakeConn, "cem-client")
	require.Len(t, fakeConn.emitted, 3)

	ack := decodePayload(t, fakeConn.emitted[1].values[1])
	assert.Equal(t, "ReceptionStatus", ack["message_type"])
	assert.Equal(t, handshakeResponseMessageID, ack["subject_message_id"])

	second := fakeConn.emitted[2]
	payload2 := decodePayload(t, second.values[1])
	assert.Equal(t, "ResourceManagerDetails", payload2["message_type"])
	assertUUID(t, payload2["message_id"])
	assertUUID(t, payload2["resource_id"])
	assert.Equal(t, "SmartEVSE-1001", payload2["name"])
	assert.Equal(t, "SmartEVSE", payload2["manufacturer"])
	assert.Equal(t, "SmartEVSE v3", payload2["model"])
	assert.Equal(t, "SmartEVSE-1001", payload2["serial_number"])
	assert.Equal(t, float64(0), payload2["instruction_processing_delay"])
	assert.Equal(t, false, payload2["provides_forecast"])
	assert.Equal(t, []any{"NOT_CONTROLABLE", "OPERATION_MODE_BASED_CONTROL"}, payload2["available_control_types"])
	assert.Equal(t, []any{"ELECTRIC.POWER.L1", "ELECTRIC.POWER.L2", "ELECTRIC.POWER.L3"}, payload2["provides_power_measurement_types"])
	roles, ok := payload2["roles"].([]any)
	require.True(t, ok)
	require.Len(t, roles, 1)
	role, ok := roles[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "ENERGY_CONSUMER", role["role"])
	assert.Equal(t, "ELECTRICITY", role["commodity"])
}

func TestS2StubMessageLogsAndAcksReceptionStatus(t *testing.T) {
	fakeConn, stub := newTestS2Stub(t, "SmartEVSE-1002")

	ok, dbusErr := stub.Connect("cem-client", 15)
	require.Nil(t, dbusErr)
	require.True(t, ok)

	establishSession(t, stub, fakeConn, "cem-client")
	require.Len(t, fakeConn.emitted, 3)

	ack := fakeConn.emitted[1]
	payload := decodePayload(t, ack.values[1])
	assert.Equal(t, "ReceptionStatus", payload["message_type"])
	assert.Equal(t, handshakeResponseMessageID, payload["subject_message_id"])
	assert.Equal(t, "OK", payload["status"])
}

func TestS2StubTracksHandshakeAndControlTypeInText(t *testing.T) {
	fakeConn, stub := newTestS2Stub(t, "SmartEVSE-1003")

	ok, dbusErr := stub.Connect("cem-client", 15)
	require.Nil(t, dbusErr)
	require.True(t, ok)

	establishSession(t, stub, fakeConn, "cem-client")

	err := stub.Message("cem-client", `{"message_type":"SelectControlType","message_id":"fd941d1e-fd9b-4db0-b636-6c3a90df8bd6","control_type":"NOT_CONTROLABLE"}`)
	require.Nil(t, err)

	text, dbusErr := stub.GetText()
	require.Nil(t, dbusErr)
	assert.True(t, strings.Contains(text, "connected=true"))
	assert.True(t, strings.Contains(text, "protocol=0.0.2-beta"))
	assert.True(t, strings.Contains(text, "control=NOT_CONTROLABLE"))
	assert.True(t, strings.Contains(text, "last_rx=SelectControlType"))
}

func TestS2StubSelectNotControlableEmitsPowerMeasurement(t *testing.T) {
	fakeConn, stub := newTestS2Stub(t, "SmartEVSE-1005")
	stub.powerReader = func() float64 { return 3456.7 }
	stub.powerL1Reader = func() float64 { return 1111.1 }
	stub.powerL2Reader = func() float64 { return 2222.2 }
	stub.powerL3Reader = func() float64 { return 123.4 }

	ok, dbusErr := stub.Connect("cem-client", 15)
	require.Nil(t, dbusErr)
	require.True(t, ok)
	establishSession(t, stub, fakeConn, "cem-client")

	err := stub.Message("cem-client", `{"message_type":"SelectControlType","message_id":"fd941d1e-fd9b-4db0-b636-6c3a90df8bd6","control_type":"NOT_CONTROLABLE"}`)
	require.Nil(t, err)
	require.Len(t, fakeConn.emitted, 5)

	ack := decodePayload(t, fakeConn.emitted[3].values[1])
	assert.Equal(t, "ReceptionStatus", ack["message_type"])
	assert.Equal(t, "fd941d1e-fd9b-4db0-b636-6c3a90df8bd6", ack["subject_message_id"])

	measurement := decodePayload(t, fakeConn.emitted[4].values[1])
	assert.Equal(t, "PowerMeasurement", measurement["message_type"])
	assertUUID(t, measurement["message_id"])
	assert.NotEmpty(t, measurement["measurement_timestamp"])
	assertPowerMeasurementValues(t, measurement, map[string]float64{
		"ELECTRIC.POWER.L1": 1111.1,
		"ELECTRIC.POWER.L2": 2222.2,
		"ELECTRIC.POWER.L3": 123.4,
	})
}

func TestS2StubSelectOmbcEmitsSystemDescriptionAndStatus(t *testing.T) {
	fakeConn, stub := newTestS2Stub(t, "SmartEVSE-1006")
	stub.maxChargePowerReader = func() float64 { return 6400 }

	ok, dbusErr := stub.Connect("cem-client", 15)
	require.Nil(t, dbusErr)
	require.True(t, ok)
	establishSession(t, stub, fakeConn, "cem-client")

	err := stub.Message("cem-client", `{"message_type":"SelectControlType","message_id":"fd941d1e-fd9b-4db0-b636-6c3a90df8bd6","control_type":"OPERATION_MODE_BASED_CONTROL"}`)
	require.Nil(t, err)
	require.Len(t, fakeConn.emitted, 6)

	ack := decodePayload(t, fakeConn.emitted[3].values[1])
	assert.Equal(t, "ReceptionStatus", ack["message_type"])

	sd := decodePayload(t, fakeConn.emitted[4].values[1])
	assert.Equal(t, "OMBC.SystemDescription", sd["message_type"])
	assertUUID(t, sd["message_id"])
	assert.NotEmpty(t, sd["valid_from"])
	timers, ok := sd["timers"].([]any)
	require.True(t, ok)
	assert.Len(t, timers, 0)
	transitions, ok := sd["transitions"].([]any)
	require.True(t, ok)
	assert.Len(t, transitions, 2)
	operationModes, ok := sd["operation_modes"].([]any)
	require.True(t, ok)
	require.Len(t, operationModes, 2)
	idleMode, ok := operationModes[0].(map[string]any)
	require.True(t, ok)
	chargeMode, ok := operationModes[1].(map[string]any)
	require.True(t, ok)
	assertUUID(t, idleMode["id"])
	assert.Equal(t, "Idle", idleMode["diagnostic_label"])
	assertUUID(t, chargeMode["id"])
	assert.Equal(t, "Charge", chargeMode["diagnostic_label"])
	chargeRanges, ok := chargeMode["power_ranges"].([]any)
	require.True(t, ok)
	require.Len(t, chargeRanges, 1)
	chargeRange, ok := chargeRanges[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "ELECTRIC.POWER.L1", chargeRange["commodity_quantity"])
	assert.Equal(t, float64(6400), chargeRange["end_of_range"])
	transition, ok := transitions[0].(map[string]any)
	require.True(t, ok)
	assertUUID(t, transition["id"])
	assert.Equal(t, idleMode["id"], transition["from"])
	assert.Equal(t, chargeMode["id"], transition["to"])
	transitionBack, ok := transitions[1].(map[string]any)
	require.True(t, ok)
	assertUUID(t, transitionBack["id"])
	assert.Equal(t, chargeMode["id"], transitionBack["from"])
	assert.Equal(t, idleMode["id"], transitionBack["to"])

	status := decodePayload(t, fakeConn.emitted[5].values[1])
	assert.Equal(t, "OMBC.Status", status["message_type"])
	assertUUID(t, status["message_id"])
	assert.Equal(t, idleMode["id"], status["active_operation_mode_id"])
	assert.Equal(t, float64(1), status["operation_mode_factor"])
}

func TestS2StubDefersOmbcBootstrapUntilReady(t *testing.T) {
	fakeConn, stub := newTestS2Stub(t, "SmartEVSE-2001")
	stub.publishInterval = 10 * time.Millisecond
	ready := false
	stub.ombcReadyReader = func() bool { return ready }

	ok, dbusErr := stub.Connect("cem-client", 15)
	require.Nil(t, dbusErr)
	require.True(t, ok)
	establishSession(t, stub, fakeConn, "cem-client")

	err := stub.Message("cem-client", `{"message_type":"SelectControlType","message_id":"fd941d1e-fd9b-4db0-b636-6c3a90df8bd6","control_type":"OPERATION_MODE_BASED_CONTROL"}`)
	require.Nil(t, err)
	require.Len(t, fakeConn.emitted, 4)

	ack := decodePayload(t, fakeConn.emitted[3].values[1])
	assert.Equal(t, "ReceptionStatus", ack["message_type"])
	assert.Equal(t, "OK", ack["status"])

	time.Sleep(30 * time.Millisecond)
	records := fakeConn.snapshot()
	assert.Len(t, records, 4)

	ready = true
	waitForEmittedCount(t, fakeConn, 6, 400*time.Millisecond)
	records = fakeConn.snapshot()
	assert.Equal(t, "OMBC.SystemDescription", decodePayload(t, records[4].values[1])["message_type"])
	assert.Equal(t, "OMBC.Status", decodePayload(t, records[5].values[1])["message_type"])
}

func TestS2StubDefersOmbcBootstrapUntilDelayElapsed(t *testing.T) {
	fakeConn, stub := newTestS2Stub(t, "SmartEVSE-2002")
	stub.publishInterval = 10 * time.Millisecond
	stub.ombcReadyReader = func() bool { return true }
	stub.ombcBootstrapDelay = 100 * time.Millisecond

	ok, dbusErr := stub.Connect("cem-client", 15)
	require.Nil(t, dbusErr)
	require.True(t, ok)
	establishSession(t, stub, fakeConn, "cem-client")

	err := stub.Message("cem-client", `{"message_type":"SelectControlType","message_id":"fd941d1e-fd9b-4db0-b636-6c3a90df8bd7","control_type":"OPERATION_MODE_BASED_CONTROL"}`)
	require.Nil(t, err)
	require.Len(t, fakeConn.emitted, 4)

	time.Sleep(40 * time.Millisecond)
	records := fakeConn.snapshot()
	assert.Len(t, records, 4)

	waitForEmittedCount(t, fakeConn, 6, 500*time.Millisecond)
	records = fakeConn.snapshot()
	assert.Equal(t, "OMBC.SystemDescription", decodePayload(t, records[4].values[1])["message_type"])
	assert.Equal(t, "OMBC.Status", decodePayload(t, records[5].values[1])["message_type"])
}

func TestS2StubOmbcInstructionEmitsUpdatedStatus(t *testing.T) {
	fakeConn, stub := newTestS2Stub(t, "SmartEVSE-1007")

	ok, dbusErr := stub.Connect("cem-client", 15)
	require.Nil(t, dbusErr)
	require.True(t, ok)
	establishSession(t, stub, fakeConn, "cem-client")

	err := stub.Message("cem-client", `{"message_type":"SelectControlType","message_id":"fd941d1e-fd9b-4db0-b636-6c3a90df8bd6","control_type":"OPERATION_MODE_BASED_CONTROL"}`)
	require.Nil(t, err)
	require.Len(t, fakeConn.emitted, 6)

	chargeModeID := stub.ombcChargeModeID()
	err = stub.Message("cem-client", `{"message_type":"OMBC.Instruction","message_id":"2e25d604-2919-4504-86f1-49b20f2049ce","id":"2b262775-ad3e-4a34-a8b0-a5bcc3c5132a","execution_time":"2026-09-08T12:00:00Z","operation_mode_id":"`+chargeModeID+`","operation_mode_factor":0.5,"abnormal_condition":false}`)
	require.Nil(t, err)
	require.Len(t, fakeConn.emitted, 8)

	ack := decodePayload(t, fakeConn.emitted[6].values[1])
	assert.Equal(t, "ReceptionStatus", ack["message_type"])
	assert.Equal(t, "OK", ack["status"])
	assert.Equal(t, "2e25d604-2919-4504-86f1-49b20f2049ce", ack["subject_message_id"])

	status := decodePayload(t, fakeConn.emitted[7].values[1])
	assert.Equal(t, "OMBC.Status", status["message_type"])
	assertUUID(t, status["message_id"])
	assert.Equal(t, chargeModeID, status["active_operation_mode_id"])
	assert.Equal(t, float64(0.5), status["operation_mode_factor"])
	assert.Equal(t, stub.ombcIdleModeID(), status["previous_operation_mode_id"])
	assert.NotEmpty(t, status["transition_timestamp"])

	text, dbusErr := stub.GetText()
	require.Nil(t, dbusErr)
	assert.True(t, strings.Contains(text, "active_mode="+chargeModeID))
	assert.True(t, strings.Contains(text, "control=OPERATION_MODE_BASED_CONTROL"))
	assert.True(t, strings.Contains(text, "last_rx=OMBC.Instruction"))
}

func TestS2StubRejectsInvalidOmbcInstruction(t *testing.T) {
	fakeConn, stub := newTestS2Stub(t, "SmartEVSE-1008")

	ok, dbusErr := stub.Connect("cem-client", 15)
	require.Nil(t, dbusErr)
	require.True(t, ok)
	establishSession(t, stub, fakeConn, "cem-client")

	err := stub.Message("cem-client", `{"message_type":"SelectControlType","message_id":"fd941d1e-fd9b-4db0-b636-6c3a90df8bd6","control_type":"OPERATION_MODE_BASED_CONTROL"}`)
	require.Nil(t, err)
	require.Len(t, fakeConn.emitted, 6)

	err = stub.Message("cem-client", `{"message_type":"OMBC.Instruction","message_id":"2e25d604-2919-4504-86f1-49b20f2049ce","id":"2b262775-ad3e-4a34-a8b0-a5bcc3c5132a","execution_time":"2026-09-08T12:00:00Z","operation_mode_id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","operation_mode_factor":1.2,"abnormal_condition":false}`)
	require.Nil(t, err)
	require.Len(t, fakeConn.emitted, 7)

	ack := decodePayload(t, fakeConn.emitted[6].values[1])
	assert.Equal(t, "ReceptionStatus", ack["message_type"])
	assert.Equal(t, "INVALID_CONTENT", ack["status"])
	assert.Equal(t, "2e25d604-2919-4504-86f1-49b20f2049ce", ack["subject_message_id"])
	assert.Contains(t, ack["diagnostic_label"], "unknown operation_mode_id")

	text, dbusErr := stub.GetText()
	require.Nil(t, dbusErr)
	assert.True(t, strings.Contains(text, "active_mode="+stub.ombcIdleModeID()))
}

func TestS2StubDoesNotAckReceptionStatus(t *testing.T) {
	fakeConn, stub := newTestS2Stub(t, "SmartEVSE-1004")

	ok, dbusErr := stub.Connect("cem-client", 15)
	require.Nil(t, dbusErr)
	require.True(t, ok)

	err := stub.Message("cem-client", `{"message_type":"ReceptionStatus","message_id":"fd941d1e-fd9b-4db0-b636-6c3a90df8bd6","subject_message_id":"a47e7b0d-12fc-4f6d-8907-99666f6a37f0","status":"OK"}`)
	require.Nil(t, err)
	assert.Len(t, fakeConn.emitted, 1)
}

func TestS2StubPeriodicNotControlablePublisher(t *testing.T) {
	fakeConn, stub := newTestS2Stub(t, "SmartEVSE-1009")
	stub.publishInterval = 10 * time.Millisecond
	stub.powerReader = func() float64 { return 789.0 }
	stub.powerL1Reader = func() float64 { return 111.0 }
	stub.powerL2Reader = func() float64 { return 222.0 }
	stub.powerL3Reader = func() float64 { return 333.0 }

	ok, dbusErr := stub.Connect("cem-client", 15)
	require.Nil(t, dbusErr)
	require.True(t, ok)
	establishSession(t, stub, fakeConn, "cem-client")

	err := stub.Message("cem-client", `{"message_type":"SelectControlType","message_id":"fd941d1e-fd9b-4db0-b636-6c3a90df8bd6","control_type":"NOT_CONTROLABLE"}`)
	require.Nil(t, err)
	require.Len(t, fakeConn.emitted, 5)

	waitForEmittedCount(t, fakeConn, 6, 300*time.Millisecond)
	periodicRecords := fakeConn.snapshot()
	periodic := decodePayload(t, periodicRecords[len(periodicRecords)-1].values[1])
	assert.Equal(t, "PowerMeasurement", periodic["message_type"])
	assertPowerMeasurementValues(t, periodic, map[string]float64{
		"ELECTRIC.POWER.L1": 111.0,
		"ELECTRIC.POWER.L2": 222.0,
		"ELECTRIC.POWER.L3": 333.0,
	})

	stub.Close()
	countAfterClose := fakeConn.count()
	time.Sleep(30 * time.Millisecond)
	assert.Equal(t, countAfterClose, fakeConn.count())
}

func TestS2StubPeriodicOmbcStatusPublisher(t *testing.T) {
	fakeConn, stub := newTestS2Stub(t, "SmartEVSE-1010")
	stub.publishInterval = 10 * time.Millisecond

	ok, dbusErr := stub.Connect("cem-client", 15)
	require.Nil(t, dbusErr)
	require.True(t, ok)
	establishSession(t, stub, fakeConn, "cem-client")

	err := stub.Message("cem-client", `{"message_type":"SelectControlType","message_id":"fd941d1e-fd9b-4db0-b636-6c3a90df8bd6","control_type":"OPERATION_MODE_BASED_CONTROL"}`)
	require.Nil(t, err)
	require.Len(t, fakeConn.emitted, 6)

	waitForEmittedCount(t, fakeConn, 7, 300*time.Millisecond)
	periodicRecords := fakeConn.snapshot()
	periodic := decodePayload(t, periodicRecords[len(periodicRecords)-1].values[1])
	assert.Equal(t, "OMBC.Status", periodic["message_type"])
}

func TestS2StubReconnectRestoresOmbcSelectionAndActiveMode(t *testing.T) {
	fakeConn, stub := newTestS2Stub(t, "SmartEVSE-1011")

	ok, dbusErr := stub.Connect("cem-client-a", 15)
	require.Nil(t, dbusErr)
	require.True(t, ok)
	establishSession(t, stub, fakeConn, "cem-client-a")

	err := stub.Message("cem-client-a", `{"message_type":"SelectControlType","message_id":"fd941d1e-fd9b-4db0-b636-6c3a90df8bd6","control_type":"OPERATION_MODE_BASED_CONTROL"}`)
	require.Nil(t, err)

	chargeModeID := stub.ombcChargeModeID()
	err = stub.Message("cem-client-a", `{"message_type":"OMBC.Instruction","message_id":"2e25d604-2919-4504-86f1-49b20f2049ce","id":"2b262775-ad3e-4a34-a8b0-a5bcc3c5132a","execution_time":"2026-09-08T12:00:00Z","operation_mode_id":"`+chargeModeID+`","operation_mode_factor":0.5,"abnormal_condition":false}`)
	require.Nil(t, err)

	err = stub.Disconnect("cem-client-a")
	require.Nil(t, err)

	text, dbusErr := stub.GetText()
	require.Nil(t, dbusErr)
	assert.True(t, strings.Contains(text, "connected=false"))
	assert.True(t, strings.Contains(text, "control=OPERATION_MODE_BASED_CONTROL"))
	assert.True(t, strings.Contains(text, "active_mode="+chargeModeID))

	ok, dbusErr = stub.Connect("cem-client-b", 15)
	require.Nil(t, dbusErr)
	require.True(t, ok)
	establishSession(t, stub, fakeConn, "cem-client-b")

	records := fakeConn.snapshot()
	require.Len(t, records, 13)
	restoredSD := decodePayload(t, records[11].values[1])
	assert.Equal(t, "OMBC.SystemDescription", restoredSD["message_type"])
	restoredStatus := decodePayload(t, records[12].values[1])
	assert.Equal(t, "OMBC.Status", restoredStatus["message_type"])
	assert.Equal(t, chargeModeID, restoredStatus["active_operation_mode_id"])
	assert.Equal(t, float64(0.5), restoredStatus["operation_mode_factor"])
}

func TestS2StubCanUseEvChargerAcPowerGetter(t *testing.T) {
	charger := newEvChargerFields(nil, 6, 10, 16, 0, 0)
	charger.SetAcPower(2222.5)
	charger.SetAcL1Power(700.1)
	charger.SetAcL2Power(800.2)
	charger.SetAcL3Power(722.2)

	fakeConn, stub := newTestS2Stub(t, "SmartEVSE-1012")
	stub.powerReader = charger.GetAcPower
	stub.powerL1Reader = charger.GetAcL1Power
	stub.powerL2Reader = charger.GetAcL2Power
	stub.powerL3Reader = charger.GetAcL3Power

	ok, dbusErr := stub.Connect("cem-client", 15)
	require.Nil(t, dbusErr)
	require.True(t, ok)
	establishSession(t, stub, fakeConn, "cem-client")

	err := stub.Message("cem-client", `{"message_type":"SelectControlType","message_id":"fd941d1e-fd9b-4db0-b636-6c3a90df8bd6","control_type":"NOT_CONTROLABLE"}`)
	require.Nil(t, err)

	records := fakeConn.snapshot()
	measurement := decodePayload(t, records[len(records)-1].values[1])
	assertPowerMeasurementValues(t, measurement, map[string]float64{
		"ELECTRIC.POWER.L1": 700.1,
		"ELECTRIC.POWER.L2": 800.2,
		"ELECTRIC.POWER.L3": 722.2,
	})
}

func TestS2StubFreshSessionDefaultsToOmbcState(t *testing.T) {
	fakeConn := &s2FakeConn{}
	stub := newS2RMStub(fakeConn, "SmartEVSE-1013")
	stub.publishInterval = time.Hour
	t.Cleanup(func() {
		stub.Close()
	})

	ok, dbusErr := stub.Connect("cem-client", 15)
	require.Nil(t, dbusErr)
	require.True(t, ok)

	establishSession(t, stub, fakeConn, "cem-client")
	records := fakeConn.snapshot()
	require.Len(t, records, 5)
	assert.Equal(t, "OMBC.SystemDescription", decodePayload(t, records[3].values[1])["message_type"])
	status := decodePayload(t, records[4].values[1])
	assert.Equal(t, "OMBC.Status", status["message_type"])
	assert.Equal(t, stub.ombcIdleModeID(), status["active_operation_mode_id"])

	text, dbusErr := stub.GetText()
	require.Nil(t, dbusErr)
	assert.True(t, strings.Contains(text, "control=OPERATION_MODE_BASED_CONTROL"))
	assert.True(t, strings.Contains(text, "active_mode="+stub.ombcIdleModeID()))
}

const handshakeResponseMessageID = "a47e7b0d-12fc-4f6d-8907-99666f6a37f0"

func establishSession(t *testing.T, stub *s2RMStub, fakeConn *s2FakeConn, clientID string) {
	t.Helper()
	err := stub.Message(clientID, `{"message_type":"HandshakeResponse","message_id":"`+handshakeResponseMessageID+`","selected_protocol_version":"0.0.2-beta"}`)
	require.Nil(t, err)
	records := fakeConn.snapshot()
	require.GreaterOrEqual(t, len(records), 3)
	assert.Equal(t, "ReceptionStatus", decodePayload(t, records[1].values[1])["message_type"])
	assert.Equal(t, "ResourceManagerDetails", decodePayload(t, records[2].values[1])["message_type"])
}

func newTestS2Stub(t *testing.T, deviceName string) (*s2FakeConn, *s2RMStub) {
	t.Helper()
	fakeConn := &s2FakeConn{}
	stub := newS2RMStub(fakeConn, deviceName)
	stub.publishInterval = time.Hour
	stub.defaultControlType = ""
	t.Cleanup(func() {
		stub.Close()
	})
	return fakeConn, stub
}

func waitForEmittedCount(t *testing.T, fakeConn *s2FakeConn, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fakeConn.count() >= want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	require.GreaterOrEqual(t, fakeConn.count(), want)
}

func assertPowerMeasurementValues(t *testing.T, measurement map[string]any, expected map[string]float64) {
	t.Helper()
	values, ok := measurement["values"].([]any)
	require.True(t, ok)
	require.Len(t, values, len(expected))
	actual := map[string]float64{}
	for _, raw := range values {
		entry, ok := raw.(map[string]any)
		require.True(t, ok)
		quantity, ok := entry["commodity_quantity"].(string)
		require.True(t, ok)
		value, ok := entry["value"].(float64)
		require.True(t, ok)
		actual[quantity] = value
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
