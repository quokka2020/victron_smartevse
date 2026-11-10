package smartevse

import (
	"testing"

	"victron_smartevse/victron"

	"github.com/godbus/dbus/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type publishedMessage struct {
	topic   string
	payload string
}

func newMQTTToDBusTestEV(t *testing.T, configure ...func(*SmartEVSE)) (*SmartEVSE, *victron.Victron_EV_Charger, *fakeDBusConn) {
	t.Helper()

	fakeConn := newFakeDBusConn()
	vh := victron.NewVictronHandlerWithConn(fakeConn)
	vh.SetConsumptionVoltages(230, 230, 230)
	t.Cleanup(func() {
		_ = vh.Close()
	})

	ev := &SmartEVSE{
		Prefix:             "SmartEVSE",
		IP:                 "192.168.1.10",
		SerialNr:           1001,
		Version:            "v3.10.0",
		current_min:        6,
		current:            16,
		current_max:        32,
		mode:               "Smart",
		activeStrategyMode: "Smart",
		managedMode:        victron.EV_Mode_Manual,
		managedModeKnown:   true,
		autoStart:          victron.EV_AutoStart_Enabled,
	}
	for _, fn := range configure {
		fn(ev)
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

func capturePublishedMessages(ev *SmartEVSE) *[]publishedMessage {
	messages := []publishedMessage{}
	ev.publish = func(topic, payload string) {
		messages = append(messages, publishedMessage{topic: topic, payload: payload})
	}
	return &messages
}

func TestMQTTToDBusE2E_InitialSyncUsesConfiguredStrategyAndAutostart(t *testing.T) {
	_, _, fakeConn := newMQTTToDBusTestEV(t, func(ev *SmartEVSE) {
		ev.mode = "Solar"
		ev.activeStrategyMode = "Solar"
		ev.managedMode = victron.EV_Mode_Auto
		ev.state = "Charging Stopped - No Power Available"
		ev.evplugstate = "Connected"
	})

	assert.Equal(t, int32(victron.EV_Mode_Auto), requirePathValue[int32](t, "/Mode", fakeConn))
	assert.Equal(t, "Auto", requirePathText(t, "/Mode", fakeConn))
	assert.Equal(t, int32(victron.EV_StartStop_Start), requirePathValue[int32](t, "/StartStop", fakeConn))
	assert.Equal(t, int32(victron.EV_AutoStart_Enabled), requirePathValue[int32](t, "/AutoStart", fakeConn))
	assert.Equal(t, int32(victron.EV_Status_Waiting_for_sun), requirePathValue[int32](t, "/Status", fakeConn))
	assert.Equal(t, "Waiting for sun", requirePathText(t, "/Status", fakeConn))
}

func TestMQTTToDBusE2E_RegisterInVictronRestoresPersistentAutoStart(t *testing.T) {
	fakeConn := newFakeDBusConn()
	fakeConn.Settings["smartevse_1001/ClassAndVrmInstance"] = "evcharger:1"
	fakeConn.Settings["smartevse_1001/AutoStart"] = int32(victron.EV_AutoStart_Disabled)

	vh := victron.NewVictronHandlerWithConn(fakeConn)
	vh.SetConsumptionVoltages(230, 230, 230)
	t.Cleanup(func() {
		_ = vh.Close()
	})

	ev := &SmartEVSE{
		Prefix:             "SmartEVSE",
		IP:                 "192.168.1.10",
		SerialNr:           1001,
		Version:            "v3.10.0",
		current_min:        6,
		current:            16,
		current_max:        32,
		mode:               "Smart",
		activeStrategyMode: "Smart",
		managedMode:        victron.EV_Mode_Manual,
		managedModeKnown:   true,
		autoStart:          victron.EV_AutoStart_Enabled,
	}
	handler := &EvHandler{evs: []*SmartEVSE{ev}}
	require.NoError(t, handler.RegisterInVictron(vh))

	assert.Equal(t, victron.EV_AutoStart_Disabled, ev.autoStart)
	assert.Equal(t, int32(victron.EV_AutoStart_Disabled), requirePathValue[int32](t, "/AutoStart", fakeConn))
	assert.Equal(t, "Disabled", requirePathText(t, "/AutoStart", fakeConn))
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
	assert.Equal(t, int32(victron.EV_Mode_Manual), requirePathValue[int32](t, "/Mode", fakeConn))
	assert.Equal(t, "Manual", requirePathText(t, "/Mode", fakeConn))
	assert.Equal(t, int32(victron.EV_StartStop_Start), requirePathValue[int32](t, "/StartStop", fakeConn))

	ev.mqttReceived("SmartEVSE/Mode", "Pause")
	assert.Equal(t, int32(victron.EV_StartStop_Stop), requirePathValue[int32](t, "/StartStop", fakeConn))
	assert.Equal(t, "Disable charging", requirePathText(t, "/StartStop", fakeConn))
	assert.Equal(t, int32(victron.EV_Status_Waiting_for_start), requirePathValue[int32](t, "/Status", fakeConn))

	ev.mqttReceived("SmartEVSE/EVPlugState", "Disconnected")
	assert.Equal(t, int32(victron.EV_Status_Disconnected), requirePathValue[int32](t, "/Status", fakeConn))
	assert.Equal(t, "Disconnected", requirePathText(t, "/Status", fakeConn))

	ev.mqttReceived("SmartEVSE/connected", "offline")
	assert.Equal(t, int32(0), requirePathValue[int32](t, "/Connected", fakeConn))
}

func TestMQTTToDBusE2E_UnpluggedOffModeDoesNotResetVictronAutoMode(t *testing.T) {
	ev, _, fakeConn := newMQTTToDBusTestEV(t, func(ev *SmartEVSE) {
		ev.mode = "Solar"
		ev.activeStrategyMode = "Solar"
		ev.managedMode = victron.EV_Mode_Auto
		ev.managedModeKnown = true
		ev.evplugstate = "Connected"
		ev.state = "Connected to EV"
	})

	ev.mqttReceived("SmartEVSE/Mode", "Off")
	ev.mqttReceived("SmartEVSE/EVPlugState", "Disconnected")

	assert.Equal(t, int32(victron.EV_Mode_Auto), requirePathValue[int32](t, "/Mode", fakeConn))
	assert.Equal(t, "Auto", requirePathText(t, "/Mode", fakeConn))
	assert.Equal(t, int32(victron.EV_Status_Disconnected), requirePathValue[int32](t, "/Status", fakeConn))
	assert.Equal(t, "Disconnected", requirePathText(t, "/Status", fakeConn))
	assert.Equal(t, "Solar", ev.activeStrategyMode)
	assert.Equal(t, "Off", ev.mode)
}

func TestMQTTToDBusE2E_ModeMappingFromHardware(t *testing.T) {
	tests := []struct {
		name                     string
		mqttMode                 string
		managedAutoControlActive bool
		expectedMode             victron.EV_Mode
		expectedText             string
	}{
		{name: "Smart to Manual", mqttMode: "Smart", expectedMode: victron.EV_Mode_Manual, expectedText: "Manual"},
		{name: "Solar to Auto", mqttMode: "Solar", expectedMode: victron.EV_Mode_Auto, expectedText: "Auto"},
		{name: "Smart with Managed Auto Control to Auto", mqttMode: "Smart", managedAutoControlActive: true, expectedMode: victron.EV_Mode_Auto, expectedText: "Auto"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev, _, fakeConn := newMQTTToDBusTestEV(t, func(ev *SmartEVSE) {
				ev.managedModeKnown = false
				ev.managedAutoControlActive = tt.managedAutoControlActive
			})

			ev.mqttReceived("SmartEVSE/EVPlugState", "Connected")
			ev.mqttReceived("SmartEVSE/State", "Connected to EV")
			ev.mqttReceived("SmartEVSE/Mode", tt.mqttMode)

			assert.Equal(t, int32(tt.expectedMode), requirePathValue[int32](t, "/Mode", fakeConn))
			assert.Equal(t, tt.expectedText, requirePathText(t, "/Mode", fakeConn))
		})
	}
}

func TestMQTTToDBusE2E_StartStopUsesPauseMode(t *testing.T) {
	ev, _, fakeConn := newMQTTToDBusTestEV(t)
	messages := capturePublishedMessages(ev)

	ev.startStopChangedCallback(victron.EV_StartStop_Stop)
	require.Len(t, *messages, 1)
	assert.Equal(t, publishedMessage{topic: "SmartEVSE/Set/Mode", payload: "Pause"}, (*messages)[0])
	assert.Equal(t, int32(victron.EV_StartStop_Stop), requirePathValue[int32](t, "/StartStop", fakeConn))

	ev.startStopChangedCallback(victron.EV_StartStop_Start)
	require.Len(t, *messages, 2)
	assert.Equal(t, publishedMessage{topic: "SmartEVSE/Set/Mode", payload: "Smart"}, (*messages)[1])
	assert.Equal(t, int32(victron.EV_StartStop_Start), requirePathValue[int32](t, "/StartStop", fakeConn))
}

func TestMQTTToDBusE2E_AutoModeSwitchesBetweenSolarAndSmartOnManagedAutoControl(t *testing.T) {
	ev, _, fakeConn := newMQTTToDBusTestEV(t)
	messages := capturePublishedMessages(ev)

	require.NoError(t, ev.modeChangedCallback(victron.EV_Mode_Auto))
	require.Len(t, *messages, 1)
	assert.Equal(t, publishedMessage{topic: "SmartEVSE/Set/Mode", payload: "Solar"}, (*messages)[0])
	assert.Equal(t, int32(victron.EV_Mode_Auto), requirePathValue[int32](t, "/Mode", fakeConn))

	ev.mqttReceived("SmartEVSE/Mode", "Solar")
	ev.setManagedAutoControl(true, 12)
	require.Len(t, *messages, 3)
	assert.Equal(t, publishedMessage{topic: "SmartEVSE/Set/CurrentOverride", payload: "120"}, (*messages)[1])
	assert.Equal(t, publishedMessage{topic: "SmartEVSE/Set/Mode", payload: "Smart"}, (*messages)[2])

	ev.mqttReceived("SmartEVSE/Mode", "Smart")
	assert.Equal(t, int32(victron.EV_Mode_Auto), requirePathValue[int32](t, "/Mode", fakeConn))
	assert.Equal(t, "Auto", requirePathText(t, "/Mode", fakeConn))

	ev.setManagedAutoControl(true, 32)
	require.Len(t, *messages, 5)
	assert.Equal(t, publishedMessage{topic: "SmartEVSE/Set/CurrentOverride", payload: "320"}, (*messages)[3])
	assert.Equal(t, publishedMessage{topic: "SmartEVSE/Set/Mode", payload: "Smart"}, (*messages)[4])
	assert.Equal(t, 32.0, ev.overrideCurrent)
	assert.Equal(t, int32(victron.EV_Mode_Auto), requirePathValue[int32](t, "/Mode", fakeConn))
	assert.Equal(t, "Auto", requirePathText(t, "/Mode", fakeConn))

	ev.clearManagedAutoControl()
	require.Len(t, *messages, 7)
	assert.Equal(t, publishedMessage{topic: "SmartEVSE/Set/CurrentOverride", payload: "0"}, (*messages)[5])
	assert.Equal(t, publishedMessage{topic: "SmartEVSE/Set/Mode", payload: "Solar"}, (*messages)[6])
	assert.Equal(t, 0.0, ev.overrideCurrent)
	assert.Equal(t, int32(victron.EV_Mode_Auto), requirePathValue[int32](t, "/Mode", fakeConn))
	assert.Equal(t, "Auto", requirePathText(t, "/Mode", fakeConn))
}

func TestMQTTToDBusE2E_SetCurrentDoesNotActivateManagedAutoControl(t *testing.T) {
	ev, _, fakeConn := newMQTTToDBusTestEV(t)
	messages := capturePublishedMessages(ev)

	require.NoError(t, ev.modeChangedCallback(victron.EV_Mode_Auto))
	require.Len(t, *messages, 1)
	assert.Equal(t, publishedMessage{topic: "SmartEVSE/Set/Mode", payload: "Solar"}, (*messages)[0])

	ev.setOverrideCurrentChangedCallback(12, 6, 32)

	require.Len(t, *messages, 2)
	assert.Equal(t, publishedMessage{topic: "SmartEVSE/Set/CurrentOverride", payload: "120"}, (*messages)[1])
	assert.Equal(t, int32(victron.EV_Mode_Auto), requirePathValue[int32](t, "/Mode", fakeConn))
	assert.Equal(t, "Auto", requirePathText(t, "/Mode", fakeConn))
	assert.False(t, ev.managedAutoControlActive)
	assert.Equal(t, 12.0, ev.overrideCurrent)
}

func TestMQTTToDBusE2E_AutoStartReconnectBehavior(t *testing.T) {
	t.Run("Enabled clears pause on reconnect", func(t *testing.T) {
		ev, _, fakeConn := newMQTTToDBusTestEV(t)
		messages := capturePublishedMessages(ev)

		ev.startStopChangedCallback(victron.EV_StartStop_Stop)
		ev.mqttReceived("SmartEVSE/EVPlugState", "Disconnected")
		ev.mqttReceived("SmartEVSE/EVPlugState", "Connected")

		require.Len(t, *messages, 2)
		assert.Equal(t, publishedMessage{topic: "SmartEVSE/Set/Mode", payload: "Pause"}, (*messages)[0])
		assert.Equal(t, publishedMessage{topic: "SmartEVSE/Set/Mode", payload: "Smart"}, (*messages)[1])
		assert.Equal(t, int32(victron.EV_StartStop_Start), requirePathValue[int32](t, "/StartStop", fakeConn))
	})

	t.Run("Disabled keeps pause on reconnect", func(t *testing.T) {
		ev, _, fakeConn := newMQTTToDBusTestEV(t)
		messages := capturePublishedMessages(ev)

		ev.autoStartChangedCallback(victron.EV_AutoStart_Disabled)
		ev.startStopChangedCallback(victron.EV_StartStop_Stop)
		ev.mqttReceived("SmartEVSE/EVPlugState", "Disconnected")
		ev.mqttReceived("SmartEVSE/EVPlugState", "Connected")

		require.Len(t, *messages, 1)
		assert.Equal(t, publishedMessage{topic: "SmartEVSE/Set/Mode", payload: "Pause"}, (*messages)[0])
		assert.Equal(t, int32(victron.EV_StartStop_Stop), requirePathValue[int32](t, "/StartStop", fakeConn))
		assert.Equal(t, int32(victron.EV_AutoStart_Disabled), requirePathValue[int32](t, "/AutoStart", fakeConn))
	})
}

func TestMQTTToDBusE2E_AutoStartDisabledPausesWhenVehicleIsPluggedIn(t *testing.T) {
	ev, _, fakeConn := newMQTTToDBusTestEV(t)
	messages := capturePublishedMessages(ev)

	ev.mqttReceived("SmartEVSE/EVPlugState", "Disconnected")
	ev.autoStartChangedCallback(victron.EV_AutoStart_Disabled)
	ev.mqttReceived("SmartEVSE/EVPlugState", "Connected")

	require.Len(t, *messages, 1)
	assert.Equal(t, publishedMessage{topic: "SmartEVSE/Set/Mode", payload: "Pause"}, (*messages)[0])
	assert.Equal(t, int32(victron.EV_StartStop_Stop), requirePathValue[int32](t, "/StartStop", fakeConn))
	assert.Equal(t, int32(victron.EV_AutoStart_Disabled), requirePathValue[int32](t, "/AutoStart", fakeConn))
	assert.Equal(t, int32(victron.EV_Status_Waiting_for_start), requirePathValue[int32](t, "/Status", fakeConn))
}

func TestMQTTToDBusE2E_AutoStartDisabledPausesWhenLegacyOffModeIsReportedOnPlugIn(t *testing.T) {
	ev, _, fakeConn := newMQTTToDBusTestEV(t, func(ev *SmartEVSE) {
		ev.mode = "Off"
		ev.activeStrategyMode = "Smart"
	})
	messages := capturePublishedMessages(ev)

	ev.mqttReceived("SmartEVSE/EVPlugState", "Disconnected")
	ev.autoStartChangedCallback(victron.EV_AutoStart_Disabled)
	ev.mqttReceived("SmartEVSE/EVPlugState", "Connected")

	require.Len(t, *messages, 1)
	assert.Equal(t, publishedMessage{topic: "SmartEVSE/Set/Mode", payload: "Pause"}, (*messages)[0])
	assert.Equal(t, int32(victron.EV_StartStop_Stop), requirePathValue[int32](t, "/StartStop", fakeConn))
	assert.Equal(t, int32(victron.EV_Status_Waiting_for_start), requirePathValue[int32](t, "/Status", fakeConn))
}

func TestMQTTToDBusE2E_FloatTopicScaling(t *testing.T) {
	ev, _, fakeConn := newMQTTToDBusTestEV(t)

	ev.mqttReceived("SmartEVSE/MaxCurrent", "320")
	assert.InDelta(t, 32.0, requirePathValue[float64](t, "/MaxCurrent", fakeConn), 0.0001)

	ev.mqttReceived("SmartEVSE/ChargeCurrent", "165")
	assert.InDelta(t, 16.5, requirePathValue[float64](t, "/Current", fakeConn), 0.0001)

	ev.mqttReceived("SmartEVSE/CurrentOverride", "85")
	assert.Equal(t, 8.5, ev.overrideCurrent)

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
	assert.InDelta(t, 32.0, before, 0.0001)
	assert.Equal(t, before, requirePathValue[float64](t, "/MaxCurrent", fakeConn))
}
