package victron_test

import (
	"testing"
	"victron_smartevse/internal/testhelper"
	"victron_smartevse/victron"

	"github.com/godbus/dbus/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateEvChargerWithDBusAbstraction(t *testing.T) {
	fakeConn := testhelper.NewFakeDBusConn()
	handler := victron.NewVictronHandlerWithConn(fakeConn)

	charger, err := handler.CreateEvCharger(1001, "v3.10.0", "192.168.1.10", 6, 16, 32, 6.7, 3255.5)
	require.NoError(t, err)
	require.NotNil(t, charger)

	assert.Contains(t, fakeConn.RequestNames, "com.victronenergy.evcharger.smartevse_1001")
	assert.Equal(t, "evcharger:1", fakeConn.Settings["smartevse_1001"])
	assert.NotEmpty(t, fakeConn.ExportedAll)
	assert.NotEmpty(t, fakeConn.Exported)

	charger.SetAcPower(4200)
	require.NotEmpty(t, fakeConn.Emitted)
	last := fakeConn.Emitted[len(fakeConn.Emitted)-1]
	assert.Equal(t, dbus.ObjectPath("/Ac/Power"), last.Path)
	assert.Equal(t, "com.victronenergy.BusItem.PropertiesChanged", last.Signal)
}
