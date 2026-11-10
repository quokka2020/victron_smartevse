package victron

import (
	"testing"
	"victron_smartevse/internal/testhelper"

	"github.com/godbus/dbus/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateEvChargerRestoresPersistentAutoStartAndPosition(t *testing.T) {
	fakeConn := testhelper.NewFakeDBusConn()
	fakeConn.Settings["smartevse_1001/ClassAndVrmInstance"] = "evcharger:1"
	fakeConn.Settings["smartevse_1001/AutoStart"] = int32(EV_AutoStart_Disabled)
	fakeConn.Settings["smartevse_1001/Position"] = int32(EV_Position_AC_Input)

	handler := NewVictronHandlerWithConn(fakeConn)
	charger, err := handler.CreateEvCharger(1001, "v3.10.0", "192.168.1.10", 6, 16, 32, 6.7, 3255.5)
	require.NoError(t, err)
	require.NotNil(t, charger)

	assert.Equal(t, EV_AutoStart_Disabled, charger.AutoStart())
	assert.Equal(t, EV_Position_AC_Input, charger.Position())
}

func TestEvChargerPersistsAutoStartAndPositionOnDBusWrites(t *testing.T) {
	fakeConn := testhelper.NewFakeDBusConn()
	handler := NewVictronHandlerWithConn(fakeConn)
	charger, err := handler.CreateEvCharger(1001, "v3.10.0", "192.168.1.10", 6, 16, 32, 6.7, 3255.5)
	require.NoError(t, err)
	require.NotNil(t, charger)

	result, dbusErr := charger.autostart.SetValue(dbus.MakeVariant(int32(EV_AutoStart_Disabled)))
	require.Nil(t, dbusErr)
	assert.Equal(t, 0, result)
	storedAutoStart, found := fakeConn.Setting("smartevse_1001", "AutoStart")
	require.True(t, found)
	assert.Equal(t, int32(EV_AutoStart_Disabled), storedAutoStart)
	assert.Equal(t, EV_AutoStart_Disabled, charger.AutoStart())

	result, dbusErr = charger.position.SetValue(dbus.MakeVariant(int32(EV_Position_AC_Input)))
	require.Nil(t, dbusErr)
	assert.Equal(t, 0, result)
	storedPosition, found := fakeConn.Setting("smartevse_1001", "Position")
	require.True(t, found)
	assert.Equal(t, int32(EV_Position_AC_Input), storedPosition)
	assert.Equal(t, EV_Position_AC_Input, charger.Position())
}
