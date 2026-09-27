package victron

import (
	"testing"

	"github.com/godbus/dbus/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEvChargerExposesS2RmSettingsPaths(t *testing.T) {
	ev := newEvChargerFields(nil, 6, 10, 16, 0, 0)
	ev.initModifyableItems()

	maxPowerItem, hasMax := ev.modifyable_items["/S2/0/RmSettings/MaxChargePower"]
	rememberItem, hasRemember := ev.modifyable_items["/S2/0/RmSettings/RememberEvPhases"]
	activeItem, hasActive := ev.modifyable_items["/S2/0/Active"]

	require.True(t, hasMax)
	require.True(t, hasRemember)
	require.True(t, hasActive)

	code, dbusErr := maxPowerItem.SetValue(dbus.MakeVariant(9300.0))
	require.Nil(t, dbusErr)
	assert.Equal(t, 0, code)
	assert.Equal(t, 9300.0, ev.GetS2MaxChargePower())

	code, dbusErr = rememberItem.SetValue(dbus.MakeVariant(true))
	require.Nil(t, dbusErr)
	assert.Equal(t, 0, code)

	val, dbusErr := rememberItem.GetValue()
	require.Nil(t, dbusErr)
	remember, ok := val.Value().(bool)
	require.True(t, ok)
	assert.True(t, remember)

	val, dbusErr = activeItem.GetValue()
	require.Nil(t, dbusErr)
	active, ok := val.Value().(bool)
	require.True(t, ok)
	assert.False(t, active)

	ev.setS2Active(true)
	val, dbusErr = activeItem.GetValue()
	require.Nil(t, dbusErr)
	active, ok = val.Value().(bool)
	require.True(t, ok)
	assert.True(t, active)
}

func TestBoolBusItemAcceptsBoolAndIntegerValues(t *testing.T) {
	item := NewBoolBusItem(false)

	code, dbusErr := item.SetValue(dbus.MakeVariant(true))
	require.Nil(t, dbusErr)
	assert.Equal(t, 0, code)

	val, dbusErr := item.GetValue()
	require.Nil(t, dbusErr)
	assert.Equal(t, true, val.Value())

	code, dbusErr = item.SetValue(dbus.MakeVariant(int32(0)))
	require.Nil(t, dbusErr)
	assert.Equal(t, 0, code)

	val, dbusErr = item.GetValue()
	require.Nil(t, dbusErr)
	assert.Equal(t, false, val.Value())

	code, dbusErr = item.SetValue(dbus.MakeVariant("bad"))
	require.NotNil(t, dbusErr)
	assert.Equal(t, -1, code)
}
