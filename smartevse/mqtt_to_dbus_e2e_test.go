package smartevse

import (
	"testing"

	"victron_smartevse/victron"

	"github.com/godbus/dbus/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newMQTTToDBusTestEV(t *testing.T) (*SmartEVSE, *victron.Victron_EV_Charger, *fakeDBusConn) {
	t.Helper()

	fakeConn := newFakeDBusConn()
	vh := victron.NewVictronHandlerWithConn(fakeConn)
	vh.SetConsumptionVoltages(230, 230, 230)
	t.Cleanup(func() {
		_ = vh.Close()
	})

	ev := &SmartEVSE{
		Prefix:      "SmartEVSE",
		IP:          "192.168.1.10",
		SerialNr:    1001,
		Version:     "v3.10.0",
		current_min: 6,
		current:     16,
		current_max: 32,
	}

	handler := &EvHandler{evs: []*SmartEVSE{ev}}
	require.NoError(t, handler.RegisterInVictron(vh))
	require.NotNil(t, ev.victron_ev)

	return ev, ev.victron_ev, fakeConn
}

func requirePathValue[T any](t *testing.T, path string, fakeConn *fakeDBusConn) T {
	t.Helper()
	payload := lastEmittedPayload(t, dbus.ObjectPath(path), fakeConn)
	v, ok := payload["Value"]
	require.True(t, ok, "no Value in emitted payload for path %s", path)
	typed, ok := v.Value().(T)
	require.True(t, ok, "path %s expected type %T got %T", path, *new(T), v.Value())
	return typed
}

func requirePathText(t *testing.T, path string, fakeConn *fakeDBusConn) string {
	t.Helper()
	payload := lastEmittedPayload(t, dbus.ObjectPath(path), fakeConn)
	v, ok := payload["Text"]
	require.True(t, ok, "no Text in emitted payload for path %s", path)
	text, ok := v.Value().(string)
	require.True(t, ok, "path %s Text expected string got %T", path, v.Value())
	return text
}

func lastEmittedPayload(t *testing.T, path dbus.ObjectPath, fakeConn *fakeDBusConn) map[string]dbus.Variant {
	t.Helper()
	emitted := fakeConn.Emitted
	for i := len(emitted) - 1; i >= 0; i-- {
		e := emitted[i]
		if e.Path == path && e.Signal == "com.victronenergy.BusItem.PropertiesChanged" {
			require.Len(t, e.Values, 1, "expected 1 value in PropertiesChanged signal for %s", path)
			payload, ok := e.Values[0].(map[string]dbus.Variant)
			require.True(t, ok, "unexpected PropertiesChanged payload type %T for %s", e.Values[0], path)
			return payload
		}
	}
	t.Fatalf("no PropertiesChanged signal emitted for path %s", path)
	return nil
}

func TestMQTTToDBusE2E_ConnectionAndStateMapping(t *testing.T) {
	ev, _, fakeConn := newMQTTToDBusTestEV(t)
	assert.Contains(t, fakeConn.RequestNames, "com.victronenergy.evcharger.smartevse_1001")

	ev.mqttReceived("SmartEVSE/connected", "online")
	assert.Equal(t, int32(1), requirePathValue[int32](t, "/Connected", fakeConn))
	assert.Equal(t, "Connected", requirePathText(t, "/Connected", fakeConn))

	ev.mqttReceived("SmartEVSE/EVPlugState", "Connected")
	ev.mqttReceived("SmartEVSE/State", "Charging")
	ev.mqttReceived("SmartEVSE/Mode", "Smart")

	assert.Equal(t, int32(victron.EV_Status_Charging), requirePathValue[int32](t, "/Status", fakeConn))
	assert.Equal(t, "Charging", requirePathText(t, "/Status", fakeConn))
	assert.Equal(t, int32(victron.EV_Mode_Auto), requirePathValue[int32](t, "/Mode", fakeConn))
	assert.Equal(t, "Auto", requirePathText(t, "/Mode", fakeConn))

	// EVPlugState=Disconnected always forces disconnected status regardless of state/mode.
	ev.mqttReceived("SmartEVSE/EVPlugState", "Disconnected")
	assert.Equal(t, int32(victron.EV_Status_Disconnected), requirePathValue[int32](t, "/Status", fakeConn))
	assert.Equal(t, "Disconnected", requirePathText(t, "/Status", fakeConn))

	ev.mqttReceived("SmartEVSE/connected", "offline")
	assert.Equal(t, int32(0), requirePathValue[int32](t, "/Connected", fakeConn))
}

func TestMQTTToDBusE2E_ModeMapping(t *testing.T) {
	tests := []struct {
		name         string
		mqttMode     string
		expectedMode victron.EV_Mode
		expectedText string
	}{
		{name: "Off to Manual", mqttMode: "Off", expectedMode: victron.EV_Mode_Manual, expectedText: "Manual"},
		{name: "Smart to Auto", mqttMode: "Smart", expectedMode: victron.EV_Mode_Auto, expectedText: "Auto"},
		{name: "Solar to Scheduled", mqttMode: "Solar", expectedMode: victron.EV_Mode_Scheduled, expectedText: "Scheduled"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev, _, fakeConn := newMQTTToDBusTestEV(t)

			ev.mqttReceived("SmartEVSE/EVPlugState", "Connected")
			ev.mqttReceived("SmartEVSE/State", "Connected to EV")
			ev.mqttReceived("SmartEVSE/Mode", tt.mqttMode)

			assert.Equal(t, int32(tt.expectedMode), requirePathValue[int32](t, "/Mode", fakeConn))
			assert.Equal(t, tt.expectedText, requirePathText(t, "/Mode", fakeConn))
		})
	}
}

func TestMQTTToDBusE2E_FloatTopicScaling(t *testing.T) {
	ev, _, fakeConn := newMQTTToDBusTestEV(t)

	ev.mqttReceived("SmartEVSE/MaxCurrent", "320")
	assert.InDelta(t, 32.0, requirePathValue[float64](t, "/MaxCurrent", fakeConn), 0.0001)

	ev.mqttReceived("SmartEVSE/ChargeCurrent", "165")
	assert.InDelta(t, 16.5, requirePathValue[float64](t, "/Current", fakeConn), 0.0001)

	ev.mqttReceived("SmartEVSE/EVChargePower", "3450")
	assert.InDelta(t, 3450.0, requirePathValue[float64](t, "/Ac/Power", fakeConn), 0.0001)

	ev.mqttReceived("SmartEVSE/EVEnergyCharged", "6721")
	assert.InDelta(t, 6.721, requirePathValue[float64](t, "/Session/Energy", fakeConn), 0.0001)

	ev.mqttReceived("SmartEVSE/EVTotalEnergyCharged", "3255525")
	assert.InDelta(t, 3255.525, requirePathValue[float64](t, "/Ac/Energy/Forward", fakeConn), 0.0001)

	ev.mqttReceived("SmartEVSE/ESPTemp", "43.2")
	assert.InDelta(t, 43.2, requirePathValue[float64](t, "/MCU/Temperature", fakeConn), 0.0001)
}

func TestMQTTToDBusE2E_PhaseCurrentToPowerUsesVictronVoltages(t *testing.T) {
	ev, _, fakeConn := newMQTTToDBusTestEV(t)

	ev.mqttReceived("SmartEVSE/EVCurrentL1", "100")
	ev.mqttReceived("SmartEVSE/EVCurrentL2", "150")
	ev.mqttReceived("SmartEVSE/EVCurrentL3", "200")

	assert.InDelta(t, 2300.0, requirePathValue[float64](t, "/Ac/L1/Power", fakeConn), 0.0001)
	assert.InDelta(t, 3450.0, requirePathValue[float64](t, "/Ac/L2/Power", fakeConn), 0.0001)
	assert.InDelta(t, 4600.0, requirePathValue[float64](t, "/Ac/L3/Power", fakeConn), 0.0001)
}

func TestMQTTToDBusE2E_InvalidNumericPayloadIsIgnored(t *testing.T) {
	ev, _, fakeConn := newMQTTToDBusTestEV(t)

	ev.mqttReceived("SmartEVSE/MaxCurrent", "320")
	before := requirePathValue[float64](t, "/MaxCurrent", fakeConn)
	ev.mqttReceived("SmartEVSE/MaxCurrent", "NaN-not-a-number")
	// After an invalid payload, no new emission should have changed the value.
	// Re-emit a valid value to confirm the last real emission is still 32.0.
	assert.InDelta(t, 32.0, before, 0.0001)
	assert.Equal(t, before, requirePathValue[float64](t, "/MaxCurrent", fakeConn))
}
