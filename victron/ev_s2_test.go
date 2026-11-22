package victron

import (
	"testing"
	"time"
	"victron_smartevse/internal/testhelper"

	"github.com/godbus/dbus/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateEvChargerRestoresPersistentS2RmSettings(t *testing.T) {
	fakeConn := testhelper.NewFakeDBusConn()
	fakeConn.Settings["smartevse_1001/ClassAndVrmInstance"] = "evcharger:1"
	fakeConn.Settings["smartevse_1001/"+s2MaxChargePowerSetting] = 7400.0
	fakeConn.Settings["smartevse_1001/"+s2RememberEvPhasesSetting] = int32(1)

	handler := NewHandlerWithConn(fakeConn)
	charger, err := handler.CreateEvCharger(1001, "v3.10.0", "192.168.1.10", 6, 16, 32, 0, 0)
	require.NoError(t, err)
	t.Cleanup(charger.Close)

	assert.Equal(t, 7400.0, charger.GetS2MaxChargePower())
	assert.True(t, charger.RememberEvPhases())
}

func TestEvChargerPersistsS2RmSettingsOnDBusWrites(t *testing.T) {
	fakeConn := testhelper.NewFakeDBusConn()
	handler := NewHandlerWithConn(fakeConn)
	charger, err := handler.CreateEvCharger(1001, "v3.10.0", "192.168.1.10", 6, 16, 32, 0, 0)
	require.NoError(t, err)
	t.Cleanup(charger.Close)

	_, dbusErr := charger.s2MaxChargePower.SetValue(dbus.MakeVariant(5000.0))
	require.Nil(t, dbusErr)
	stored, found := fakeConn.Setting("smartevse_1001", s2MaxChargePowerSetting)
	require.True(t, found)
	assert.Equal(t, 5000.0, stored)

	_, dbusErr = charger.s2RememberEvPhases.SetValue(dbus.MakeVariant(int32(1)))
	require.Nil(t, dbusErr)
	stored, found = fakeConn.Setting("smartevse_1001", s2RememberEvPhasesSetting)
	require.True(t, found)
	assert.Equal(t, int32(1), stored)
	assert.True(t, charger.RememberEvPhases())
}

func TestEvChargerSetEvPhasesClamps(t *testing.T) {
	charger := newEvChargerFields(nil, 6, 10, 16, 0, 0)
	assert.Equal(t, 1, charger.EvPhases())
	charger.SetEvPhases(3)
	assert.Equal(t, 3, charger.EvPhases())
	charger.SetEvPhases(0)
	charger.SetEvPhases(4)
	assert.Equal(t, 3, charger.EvPhases())
	charger.ResetEvPhases()
	assert.Equal(t, 1, charger.EvPhases())
}

func TestEvChargerS2ControlFlowsToCallbacks(t *testing.T) {
	fakeConn := testhelper.NewFakeDBusConn()
	handler := NewHandlerWithConn(fakeConn)
	handler.SetAcOutVoltages(230, 230, 230)
	charger, err := handler.CreateEvCharger(1001, "v3.10.0", "192.168.1.10", 6, 16, 32, 0, 0)
	require.NoError(t, err)
	t.Cleanup(charger.Close)
	charger.s2.publishInterval = time.Hour
	charger.s2.ombcBootstrapDelay = 0
	charger.SetMode(EV_Mode_Auto)
	charger.SetEvPhases(1)

	commands := []S2ChargeCommand{}
	releases := 0
	charger.SetS2ControlCallback(func(cmd S2ChargeCommand) { commands = append(commands, cmd) })
	charger.SetS2ReleaseCallback(func() { releases++ })

	ok, dbusErr := charger.s2.Connect("opportunity_loads", 15)
	require.Nil(t, dbusErr)
	require.True(t, ok)
	require.Nil(t, charger.s2.Message("opportunity_loads", `{"message_type":"HandshakeResponse","message_id":"`+handshakeResponseMessageID+`","selected_protocol_version":"0.0.2-beta"}`))
	assert.True(t, charger.s2Active.value)
	require.Nil(t, charger.s2.Message("opportunity_loads", `{"message_type":"SelectControlType","message_id":"fd941d1e-fd9b-4db0-b636-6c3a90df8bd6","control_type":"OPERATION_MODE_BASED_CONTROL"}`))

	require.Len(t, commands, 1)
	assert.Equal(t, S2ChargeCommand{Charge: false}, commands[0])
	assert.Equal(t, 1, charger.s2.model.phases)
	assert.Equal(t, 32, charger.s2.model.modes[len(charger.s2.model.modes)-1].currentA)

	step := charger.s2.model.nearestStep(13)
	require.Nil(t, charger.s2.Message("opportunity_loads", `{"message_type":"OMBC.Instruction","message_id":"7d0f5a39-3d4b-4bd6-9d0e-2b3f0b8a1c11","id":"7d0f5a39-3d4b-4bd6-9d0e-2b3f0b8a1c12","execution_time":"2025-01-01T00:00:00Z","operation_mode_id":"`+step.id+`","operation_mode_factor":1,"abnormal_condition":false}`))
	require.Len(t, commands, 2)
	assert.Equal(t, S2ChargeCommand{Charge: true, CurrentA: 13}, commands[1])
	assert.Equal(t, 13.0, charger.gxChargingCurrent.value)

	// Leaving Auto mode hands the charger back.
	charger.SetMode(EV_Mode_Manual)
	assert.Equal(t, 1, releases)
	assert.Equal(t, 0.0, charger.gxChargingCurrent.value)

	// Returning to Auto re-applies the active S2 operation mode.
	charger.SetMode(EV_Mode_Auto)
	require.Len(t, commands, 3)
	assert.Equal(t, S2ChargeCommand{Charge: true, CurrentA: 13}, commands[2])

	require.Nil(t, charger.s2.Disconnect("opportunity_loads"))
	assert.Equal(t, 2, releases)
	assert.False(t, charger.s2Active.value)
}
