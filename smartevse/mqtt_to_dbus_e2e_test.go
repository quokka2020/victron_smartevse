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

func newMQTTToDBusTestEV(t *testing.T, configure ...func(*SmartEVSE)) (*SmartEVSE, *victron.EvCharger, *fakeDBusConn) {
	t.Helper()
	return newMQTTToDBusTestEVWithConn(t, newFakeDBusConn(), configure...)
}

func newMQTTToDBusTestEVWithConn(t *testing.T, fakeConn *fakeDBusConn, configure ...func(*SmartEVSE)) (*SmartEVSE, *victron.EvCharger, *fakeDBusConn) {
	t.Helper()

	vh := victron.NewHandlerWithConn(fakeConn)
	vh.SetAcOutVoltages(230, 230, 230)
	vh.SetAcInVoltages(250, 250, 250) // Set AC Input voltages to 250V for testing purposes
	t.Cleanup(func() {
		_ = vh.Close()
	})

	ev := &SmartEVSE{
		Prefix:             "SmartEVSE",
		IP:                 "192.168.1.10",
		SerialNr:           1001,
		Version:            "v3.10.0",
		currentMin:         6,
		current:            16,
		currentMax:         32,
		mode:               "Smart",
		activeStrategyMode: "Smart",
		managedMode:        victron.EV_Mode_Manual,
		managedModeKnown:   true,
	}
	for _, fn := range configure {
		fn(ev)
	}

	handler := &EvHandler{evs: []*SmartEVSE{ev}}
	require.NoError(t, handler.RegisterInVictron(vh))
	require.NotNil(t, ev.victronEvcs)

	return ev, ev.victronEvcs, fakeConn
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
		ev.evPlugState = "Connected"
	})

	assert.Equal(t, int32(victron.EV_Mode_Auto), requirePathValue[int32](t, "/Mode", fakeConn))
	assert.Equal(t, "Auto", requirePathText(t, "/Mode", fakeConn))
	assert.Equal(t, int32(victron.EvStartStopStart), requirePathValue[int32](t, "/StartStop", fakeConn))
	assert.Equal(t, int32(victron.EV_AutoStart_Enabled), requirePathValue[int32](t, "/AutoStart", fakeConn))
	assert.Equal(t, int32(victron.EV_Status_Waiting_for_sun), requirePathValue[int32](t, "/Status", fakeConn))
	assert.Equal(t, "Waiting for sun", requirePathText(t, "/Status", fakeConn))
}

func TestMQTTToDBusE2E_RegisterInVictronRestoresPersistentAutoStart(t *testing.T) {
	fakeConn := newFakeDBusConn()
	fakeConn.Settings["smartevse_1001/ClassAndVrmInstance"] = "evcharger:1"
	fakeConn.Settings["smartevse_1001/AutoStart"] = int32(victron.EV_AutoStart_Disabled)

	vh := victron.NewHandlerWithConn(fakeConn)
	t.Cleanup(func() {
		_ = vh.Close()
	})

	ev := &SmartEVSE{
		Prefix:             "SmartEVSE",
		IP:                 "192.168.1.10",
		SerialNr:           1001,
		Version:            "v3.10.0",
		currentMin:         6,
		current:            16,
		currentMax:         32,
		mode:               "Smart",
		activeStrategyMode: "Smart",
		managedMode:        victron.EV_Mode_Manual,
		managedModeKnown:   true,
	}

	handler := &EvHandler{evs: []*SmartEVSE{ev}}
	require.NoError(t, handler.RegisterInVictron(vh))

	assert.Equal(t, int32(victron.EV_AutoStart_Disabled), requirePathValue[int32](t, "/AutoStart", fakeConn))
	assert.Equal(t, "Disabled", requirePathText(t, "/AutoStart", fakeConn))
}

func TestMQTTToDBusE2E_RestoresModeAcrossRestart(t *testing.T) {
	conn := newFakeDBusConn()
	first, charger, _ := newMQTTToDBusTestEVWithConn(t, conn)
	require.NoError(t, first.modeChangedCallback(victron.EV_Mode_Auto))
	assert.Equal(t, victron.EV_Mode_Auto, charger.Mode())
	charger.Close()

	restarted, restoredCharger, _ := newMQTTToDBusTestEVWithConn(t, conn, func(ev *SmartEVSE) {
		ev.mode = "Smart"
		ev.activeStrategyMode = "Smart"
	})
	assert.Equal(t, victron.EV_Mode_Auto, restoredCharger.Mode())
	for i := 0; i < 2; i++ {
		restarted.mqttReceived("SmartEVSE/Mode", "Smart")
		assert.Equal(t, victron.EV_Mode_Auto, restoredCharger.Mode())
	}
	stored, _ := conn.Setting("smartevse_1001", "Mode")
	assert.Equal(t, int32(victron.EV_Mode_Auto), stored)

	restarted.mqttReceived("SmartEVSE/Mode", "Solar")
	restarted.mqttReceived("SmartEVSE/Mode", "Smart")
	assert.Equal(t, victron.EV_Mode_Manual, restoredCharger.Mode())
	stored, _ = conn.Setting("smartevse_1001", "Mode")
	assert.Equal(t, int32(victron.EV_Mode_Manual), stored)
}

func TestMQTTToDBusE2E_FirstModeUsesSmartEVSEStrategy(t *testing.T) {
	ev, charger, conn := newMQTTToDBusTestEV(t, func(ev *SmartEVSE) {
		ev.mode = "Solar"
		ev.activeStrategyMode = "Solar"
		ev.managedMode = victron.EV_Mode_Auto
	})
	assert.Equal(t, victron.EV_Mode_Auto, charger.Mode())
	assert.Equal(t, victron.EV_Mode_Auto, ev.effectiveManagedMode())
	stored, _ := conn.Setting("smartevse_1001", "Mode")
	assert.Equal(t, int32(victron.EV_Mode_Auto), stored)
}

func TestMQTTToDBusE2E_RestoresScheduledMode(t *testing.T) {
	conn := newFakeDBusConn()
	conn.Settings["smartevse_1001/Mode"] = int32(victron.EV_Mode_Scheduled)
	ev, charger, _ := newMQTTToDBusTestEVWithConn(t, conn, func(ev *SmartEVSE) {
		ev.mode = "Solar"
		ev.activeStrategyMode = "Solar"
	})
	assert.Equal(t, victron.EV_Mode_Scheduled, charger.Mode())
	ev.mqttReceived("SmartEVSE/Mode", "Solar")
	assert.Equal(t, victron.EV_Mode_Scheduled, charger.Mode())
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
	assert.Equal(t, int32(victron.EvStartStopStart), requirePathValue[int32](t, "/StartStop", fakeConn))

	ev.mqttReceived("SmartEVSE/Mode", "Pause")
	assert.Equal(t, int32(victron.EvStartStopStop), requirePathValue[int32](t, "/StartStop", fakeConn))
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
		ev.evPlugState = "Connected"
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

	ev.startStopChangedCallback(victron.EvStartStopStop)
	require.Len(t, *messages, 1)
	assert.Equal(t, publishedMessage{topic: "SmartEVSE/Set/Mode", payload: "Pause"}, (*messages)[0])
	assert.Equal(t, int32(victron.EvStartStopStop), requirePathValue[int32](t, "/StartStop", fakeConn))

	ev.startStopChangedCallback(victron.EvStartStopStart)
	require.Len(t, *messages, 2)
	assert.Equal(t, publishedMessage{topic: "SmartEVSE/Set/Mode", payload: "Smart"}, (*messages)[1])
	assert.Equal(t, int32(victron.EvStartStopStart), requirePathValue[int32](t, "/StartStop", fakeConn))
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

		ev.startStopChangedCallback(victron.EvStartStopStop)
		ev.mqttReceived("SmartEVSE/EVPlugState", "Disconnected")
		ev.mqttReceived("SmartEVSE/EVPlugState", "Connected")

		require.Len(t, *messages, 2)
		assert.Equal(t, publishedMessage{topic: "SmartEVSE/Set/Mode", payload: "Pause"}, (*messages)[0])
		assert.Equal(t, publishedMessage{topic: "SmartEVSE/Set/Mode", payload: "Smart"}, (*messages)[1])
		assert.Equal(t, int32(victron.EvStartStopStart), requirePathValue[int32](t, "/StartStop", fakeConn))
	})

	t.Run("Disabled keeps pause on reconnect", func(t *testing.T) {
		fakeConn := newFakeDBusConn()
		fakeConn.Settings["smartevse_1001/AutoStart"] = int32(victron.EV_AutoStart_Disabled)
		ev, _, fakeConn := newMQTTToDBusTestEVWithConn(t, fakeConn)
		messages := capturePublishedMessages(ev)

		ev.startStopChangedCallback(victron.EvStartStopStop)
		ev.mqttReceived("SmartEVSE/EVPlugState", "Disconnected")
		ev.mqttReceived("SmartEVSE/EVPlugState", "Connected")

		require.Len(t, *messages, 1)
		assert.Equal(t, publishedMessage{topic: "SmartEVSE/Set/Mode", payload: "Pause"}, (*messages)[0])
		assert.Equal(t, int32(victron.EvStartStopStop), requirePathValue[int32](t, "/StartStop", fakeConn))
		assert.Equal(t, int32(victron.EV_AutoStart_Disabled), requirePathValue[int32](t, "/AutoStart", fakeConn))
	})
}

func TestMQTTToDBusE2E_AutoStartDisabledPausesWhenVehicleIsPluggedIn(t *testing.T) {
	fakeConn := newFakeDBusConn()
	fakeConn.Settings["smartevse_1001/AutoStart"] = int32(victron.EV_AutoStart_Disabled)
	ev, _, fakeConn := newMQTTToDBusTestEVWithConn(t, fakeConn)
	messages := capturePublishedMessages(ev)

	ev.mqttReceived("SmartEVSE/EVPlugState", "Disconnected")
	ev.mqttReceived("SmartEVSE/EVPlugState", "Connected")

	require.Len(t, *messages, 1)
	assert.Equal(t, publishedMessage{topic: "SmartEVSE/Set/Mode", payload: "Pause"}, (*messages)[0])
	assert.Equal(t, int32(victron.EvStartStopStop), requirePathValue[int32](t, "/StartStop", fakeConn))
	assert.Equal(t, int32(victron.EV_AutoStart_Disabled), requirePathValue[int32](t, "/AutoStart", fakeConn))
	assert.Equal(t, int32(victron.EV_Status_Waiting_for_start), requirePathValue[int32](t, "/Status", fakeConn))
}

func TestMQTTToDBusE2E_AutoStartDisabledPausesWhenLegacyOffModeIsReportedOnPlugIn(t *testing.T) {
	fakeConn := newFakeDBusConn()
	fakeConn.Settings["smartevse_1001/AutoStart"] = int32(victron.EV_AutoStart_Disabled)
	ev, _, fakeConn := newMQTTToDBusTestEVWithConn(t, fakeConn, func(ev *SmartEVSE) {
		ev.mode = "Off"
		ev.activeStrategyMode = "Smart"
	})
	messages := capturePublishedMessages(ev)

	ev.mqttReceived("SmartEVSE/EVPlugState", "Disconnected")
	ev.mqttReceived("SmartEVSE/EVPlugState", "Connected")

	require.Len(t, *messages, 1)
	assert.Equal(t, publishedMessage{topic: "SmartEVSE/Set/Mode", payload: "Pause"}, (*messages)[0])
	assert.Equal(t, int32(victron.EvStartStopStop), requirePathValue[int32](t, "/StartStop", fakeConn))
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

func TestMQTTToDBusE2E_PhaseCurrentToPowerUsesCorrectVoltageByPosition(t *testing.T) {
	t.Run("AC_Output position uses AcOut voltages (230V)", func(t *testing.T) {
		ev, _, fakeConn := newMQTTToDBusTestEV(t)

		ev.mqttReceived("SmartEVSE/EVCurrentL1", "100")
		ev.mqttReceived("SmartEVSE/EVCurrentL2", "150")
		ev.mqttReceived("SmartEVSE/EVCurrentL3", "200")

		assert.InDelta(t, 2300.0, requirePathValue[float64](t, "/Ac/L1/Power", fakeConn), 0.0001)
		assert.InDelta(t, 3450.0, requirePathValue[float64](t, "/Ac/L2/Power", fakeConn), 0.0001)
		assert.InDelta(t, 4600.0, requirePathValue[float64](t, "/Ac/L3/Power", fakeConn), 0.0001)
	})

	t.Run("AC_Input position uses AcIn voltages (250V)", func(t *testing.T) {
		fakeConn := newFakeDBusConn()
		// Pre-populate position setting before creating the EV
		fakeConn.Settings["smartevse_1001/ClassAndVrmInstance"] = "evcharger:1"
		fakeConn.Settings["smartevse_1001/Position"] = int32(victron.EV_Position_AC_Input)
		ev, _, fakeConn := newMQTTToDBusTestEVWithConn(t, fakeConn)

		ev.mqttReceived("SmartEVSE/EVCurrentL1", "100")
		ev.mqttReceived("SmartEVSE/EVCurrentL2", "150")
		ev.mqttReceived("SmartEVSE/EVCurrentL3", "200")

		assert.InDelta(t, 2500.0, requirePathValue[float64](t, "/Ac/L1/Power", fakeConn), 0.0001)
		assert.InDelta(t, 3750.0, requirePathValue[float64](t, "/Ac/L2/Power", fakeConn), 0.0001)
		assert.InDelta(t, 5000.0, requirePathValue[float64](t, "/Ac/L3/Power", fakeConn), 0.0001)
	})
}

func TestMQTTToDBusE2E_InvalidNumericPayloadIsIgnored(t *testing.T) {
	ev, _, fakeConn := newMQTTToDBusTestEV(t)

	ev.mqttReceived("SmartEVSE/MaxCurrent", "320")
	before := requirePathValue[float64](t, "/MaxCurrent", fakeConn)
	ev.mqttReceived("SmartEVSE/MaxCurrent", "NaN-not-a-number")
	assert.InDelta(t, 32.0, before, 0.0001)
	assert.Equal(t, before, requirePathValue[float64](t, "/MaxCurrent", fakeConn))
}

func TestMQTTToDBusE2E_MaxCurrentUpdatesSetCurrentBounds(t *testing.T) {
	ev, victronEV, fakeConn := newMQTTToDBusTestEV(t)

	ev.mqttReceived("SmartEVSE/MaxCurrent", "100")

	min, current, max := victronEV.CurrentLimits()
	assert.InDelta(t, 6.0, min, 0.0001)
	assert.InDelta(t, 10.0, current, 0.0001, "current must be clamped to the new max")
	assert.InDelta(t, 10.0, max, 0.0001)
	assert.InDelta(t, 10.0, requirePathValue[float64](t, "/MaxCurrent", fakeConn), 0.0001)
	assert.InDelta(t, 10.0, requirePathValue[float64](t, "/SetCurrent", fakeConn), 0.0001)
}

func TestMQTTToDBusE2E_ScheduledModeMapsToSolar(t *testing.T) {
	ev, _, fakeConn := newMQTTToDBusTestEV(t)
	messages := capturePublishedMessages(ev)

	require.NoError(t, ev.modeChangedCallback(victron.EV_Mode_Scheduled))
	require.Len(t, *messages, 1)
	assert.Equal(t, publishedMessage{topic: "SmartEVSE/Set/Mode", payload: "Solar"}, (*messages)[0])
	assert.Equal(t, int32(victron.EV_Mode_Scheduled), requirePathValue[int32](t, "/Mode", fakeConn))

	// the SmartEVSE confirming Solar must not flip /Mode over to Auto
	ev.mqttReceived("SmartEVSE/Mode", "Solar")
	assert.Equal(t, int32(victron.EV_Mode_Scheduled), requirePathValue[int32](t, "/Mode", fakeConn))
	assert.Equal(t, "Scheduled", requirePathText(t, "/Mode", fakeConn))
}
