package smartevse

import (
	"encoding/json"
	"testing"
	"time"

	"victron_smartevse/victron"

	"github.com/godbus/dbus/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testS2Interface interface {
	Connect(string, int32) (bool, *dbus.Error)
	Message(string, string) *dbus.Error
}

type s2CaptureConn struct {
	*fakeDBusConn
	s2 testS2Interface
}

func (c *s2CaptureConn) ExportAll(v any, path dbus.ObjectPath, iface string) error {
	if iface == "com.victronenergy.S2" {
		c.s2 = v.(testS2Interface)
	}
	return c.fakeDBusConn.ExportAll(v, path, iface)
}

func newS2TestEV(t *testing.T, configure ...func(*SmartEVSE)) (*SmartEVSE, *victron.EvCharger, *fakeDBusConn, *[]publishedMessage) {
	t.Helper()
	solar := func(ev *SmartEVSE) {
		ev.mode = "Solar"
		ev.activeStrategyMode = "Solar"
		ev.managedMode = victron.EV_Mode_Auto
		ev.evPlugState = "Connected"
	}
	evse, charger, fakeConn := newMQTTToDBusTestEV(t, append([]func(*SmartEVSE){solar}, configure...)...)
	return evse, charger, fakeConn, capturePublishedMessages(evse)
}

func publishedTo(messages []publishedMessage, topic string) []string {
	payloads := []string{}
	for _, m := range messages {
		if m.topic == topic {
			payloads = append(payloads, m.payload)
		}
	}
	return payloads
}

func TestS2ControlChargeStandbyAndRelease(t *testing.T) {
	evse, charger, _, messages := newS2TestEV(t)

	evse.s2ControlCallback(victron.S2ChargeCommand{Charge: true, CurrentA: 10})
	assert.Equal(t, []string{"100"}, publishedTo(*messages, "SmartEVSE/Set/CurrentOverride"))
	assert.Equal(t, []string{"Smart"}, publishedTo(*messages, "SmartEVSE/Set/Mode"))
	assert.Equal(t, victron.EV_Mode_Auto, charger.Mode())

	evse.s2ControlCallback(victron.S2ChargeCommand{Charge: false})
	assert.Equal(t, []string{"Smart", "Pause"}, publishedTo(*messages, "SmartEVSE/Set/Mode"))
	assert.True(t, evse.s2.paused)
	assert.Equal(t, victron.EV_Mode_Auto, charger.Mode())

	evse.s2ReleaseCallback()
	assert.Equal(t, []string{"100", "0"}, publishedTo(*messages, "SmartEVSE/Set/CurrentOverride"))
	assert.Equal(t, []string{"Smart", "Pause", "Solar"}, publishedTo(*messages, "SmartEVSE/Set/Mode"))
	assert.False(t, evse.s2Controlled())
	assert.Equal(t, victron.EV_Mode_Auto, charger.Mode())
}

func TestS2ControlResumesFromStandbyWithNewCurrent(t *testing.T) {
	evse, _, _, messages := newS2TestEV(t)

	evse.s2ControlCallback(victron.S2ChargeCommand{Charge: false})
	evse.s2ControlCallback(victron.S2ChargeCommand{Charge: true, CurrentA: 7})
	assert.Equal(t, []string{"Pause", "Smart"}, publishedTo(*messages, "SmartEVSE/Set/Mode"))
	assert.Equal(t, []string{"70"}, publishedTo(*messages, "SmartEVSE/Set/CurrentOverride"))
	assert.False(t, evse.s2.paused)
}

func TestS2ControlRespectsUserStop(t *testing.T) {
	evse, _, _, messages := newS2TestEV(t)

	evse.startStopChangedCallback(victron.EvStartStopStop)
	evse.s2ControlCallback(victron.S2ChargeCommand{Charge: true, CurrentA: 10})
	assert.Equal(t, []string{"Pause"}, publishedTo(*messages, "SmartEVSE/Set/Mode"))
	assert.Empty(t, publishedTo(*messages, "SmartEVSE/Set/CurrentOverride"))

	// Release must not resume charging the user stopped.
	evse.s2ReleaseCallback()
	assert.Equal(t, []string{"Pause"}, publishedTo(*messages, "SmartEVSE/Set/Mode"))

	evse.s2ControlCallback(victron.S2ChargeCommand{Charge: true, CurrentA: 10})
	evse.startStopChangedCallback(victron.EvStartStopStart)
	assert.Equal(t, []string{"Pause", "Smart"}, publishedTo(*messages, "SmartEVSE/Set/Mode"))
	assert.Equal(t, []string{"0", "100"}, publishedTo(*messages, "SmartEVSE/Set/CurrentOverride"))
}

func TestAutoPlugInPausesUntilS2ChargeCommand(t *testing.T) {
	for _, autoStart := range []victron.EV_AutoStart{victron.EV_AutoStart_Enabled, victron.EV_AutoStart_Disabled} {
		t.Run(autoStart.ToString(), func(t *testing.T) {
			evse, charger, fakeConn, messages := newS2TestEV(t)
			charger.SetAutoStart(autoStart)
			evse.s2ControlCallback(victron.S2ChargeCommand{Charge: true, CurrentA: 12})
			evse.mqttReceived("SmartEVSE/EVPlugState", "Disconnected")
			evse.mqttReceived("SmartEVSE/EVPlugState", "Connected")

			assert.Equal(t, []string{"Smart", "Pause"}, publishedTo(*messages, "SmartEVSE/Set/Mode"))
			assert.Equal(t, []string{"120", "0"}, publishedTo(*messages, "SmartEVSE/Set/CurrentOverride"))
			assert.Equal(t, int32(victron.EV_Status_Waiting_for_start), requirePathValue[int32](t, "/Status", fakeConn))
			assert.Equal(t, victron.EV_Mode_Auto, charger.Mode())

			evse.s2ControlCallback(victron.S2ChargeCommand{Charge: true, CurrentA: 7})
			assert.Equal(t, []string{"Smart", "Pause", "Smart"}, publishedTo(*messages, "SmartEVSE/Set/Mode"))
			assert.Equal(t, []string{"120", "0", "70"}, publishedTo(*messages, "SmartEVSE/Set/CurrentOverride"))
		})
	}
}

func TestAutoPlugInWithoutS2StaysPaused(t *testing.T) {
	evse, _, _, messages := newS2TestEV(t)
	evse.mqttReceived("SmartEVSE/EVPlugState", "Disconnected")
	evse.mqttReceived("SmartEVSE/EVPlugState", "Connected")
	assert.Equal(t, []string{"Pause"}, publishedTo(*messages, "SmartEVSE/Set/Mode"))
	assert.False(t, evse.s2Controlled())
}

func TestAutoPlugInReportsStandbyToConnectedS2(t *testing.T) {
	conn := &s2CaptureConn{fakeDBusConn: newFakeDBusConn()}
	handler := victron.NewHandlerWithConn(conn)
	handler.SetAcOutVoltages(230, 230, 230)
	evse := &SmartEVSE{
		Prefix: "SmartEVSE", SerialNr: 1001, currentMin: 6, current: 16, currentMax: 16,
		mode: "Solar", activeStrategyMode: "Solar", managedMode: victron.EV_Mode_Auto,
		managedModeKnown: true, evPlugState: "Connected",
	}
	require.NoError(t, (&EvHandler{evs: []*SmartEVSE{evse}}).RegisterInVictron(handler))
	t.Cleanup(func() {
		evse.victronEvcs.Close()
		_ = handler.Close()
	})
	messages := capturePublishedMessages(evse)
	require.NotNil(t, conn.s2)
	ok, dbusErr := conn.s2.Connect("dynamic_ess", 15)
	require.True(t, ok)
	require.Nil(t, dbusErr)
	require.Nil(t, conn.s2.Message("dynamic_ess", `{"message_type":"HandshakeResponse","message_id":"c56a0000-0000-4000-8000-000000000001","selected_protocol_version":"0.0.2-beta"}`))
	require.Nil(t, conn.s2.Message("dynamic_ess", `{"message_type":"SelectControlType","message_id":"c56a0000-0000-4000-8000-000000000002","control_type":"OPERATION_MODE_BASED_CONTROL"}`))

	var description map[string]any
	for _, event := range conn.Emitted {
		if event.Signal == "com.victronenergy.S2.Message" {
			var payload map[string]any
			require.NoError(t, json.Unmarshal([]byte(event.Values[1].(string)), &payload))
			if payload["message_type"] == "OMBC.SystemDescription" {
				description = payload
			}
		}
	}
	require.NotNil(t, description)
	var chargeID, standbyID string
	for _, value := range description["operation_modes"].([]any) {
		mode := value.(map[string]any)
		switch mode["diagnostic_label"] {
		case "10A":
			chargeID = mode["id"].(string)
		case "Standby":
			standbyID = mode["id"].(string)
		}
	}
	require.NotEmpty(t, chargeID)
	require.NotEmpty(t, standbyID)
	instruction := `{"message_type":"OMBC.Instruction","message_id":"c56a0000-0000-4000-8000-000000000003","operation_mode_id":"` + chargeID + `","operation_mode_factor":1}`
	require.Nil(t, conn.s2.Message("dynamic_ess", instruction))
	assert.Equal(t, "Smart", evse.mode)

	evse.mqttReceived("SmartEVSE/EVPlugState", "Disconnected")
	evse.mqttReceived("SmartEVSE/EVPlugState", "Connected")
	assert.Equal(t, "Pause", evse.mode)
	assert.Equal(t, "0", publishedTo(*messages, "SmartEVSE/Set/CurrentOverride")[1])
	assert.Equal(t, 0.0, requirePathValue[float64](t, "/GxAutoMode/ChargingCurrent", conn.fakeDBusConn))
	var status map[string]any
	for _, event := range conn.Emitted {
		if event.Signal == "com.victronenergy.S2.Message" {
			var payload map[string]any
			require.NoError(t, json.Unmarshal([]byte(event.Values[1].(string)), &payload))
			if payload["message_type"] == "OMBC.Status" {
				status = payload
			}
		}
	}
	require.NotNil(t, status)
	assert.Equal(t, standbyID, status["active_operation_mode_id"])
	assert.Equal(t, chargeID, status["previous_operation_mode_id"])
	assert.True(t, evse.s2Controlled())
}

func TestS2PhaseDetectionFromMeasuredCurrents(t *testing.T) {
	now := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	evse, charger, _, _ := newS2TestEV(t, func(ev *SmartEVSE) {
		ev.s2.now = func() time.Time { return now }
	})
	require.Equal(t, 1, charger.EvPhases())

	evse.mqttReceived("SmartEVSE/EVCurrentL1", "100")
	evse.mqttReceived("SmartEVSE/EVCurrentL2", "100")
	evse.mqttReceived("SmartEVSE/EVCurrentL3", "100")
	assert.Equal(t, 1, charger.EvPhases(), "not committed before the stable period")

	now = now.Add(21 * time.Second)
	evse.mqttReceived("SmartEVSE/EVCurrentL1", "101")
	assert.Equal(t, 3, charger.EvPhases())

	evse.mqttReceived("SmartEVSE/EVPlugState", "Disconnected")
	assert.Equal(t, 1, charger.EvPhases(), "reset to one phase on unplug")
}

func TestS2PhaseDetectionRemembersPhasesWhenConfigured(t *testing.T) {
	fakeConn := newFakeDBusConn()
	fakeConn.Settings["smartevse_1001/ClassAndVrmInstance"] = "evcharger:1"
	fakeConn.Settings["smartevse_1001/S2RememberEvPhases"] = int32(1)
	now := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	evse, charger, _ := newMQTTToDBusTestEVWithConn(t, fakeConn, func(ev *SmartEVSE) {
		ev.evPlugState = "Connected"
		ev.s2.now = func() time.Time { return now }
	})
	require.True(t, charger.RememberEvPhases())

	evse.mqttReceived("SmartEVSE/EVCurrentL1", "80")
	evse.mqttReceived("SmartEVSE/EVCurrentL2", "80")
	now = now.Add(time.Minute)
	evse.mqttReceived("SmartEVSE/EVCurrentL2", "80")
	assert.Equal(t, 2, charger.EvPhases())

	evse.mqttReceived("SmartEVSE/EVPlugState", "Disconnected")
	assert.Equal(t, 2, charger.EvPhases())
}
