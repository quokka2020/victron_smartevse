package victron

import (
	"testing"

	"github.com/godbus/dbus/v5"
	"github.com/stretchr/testify/assert"
)

func TestBatteryCurrentWithoutVoltagesIsZero(t *testing.T) {
	h := NewVictronHandlerWithConn(nil)
	h.battery_v.lastValue = 52
	h.battery_i.lastValue = 10

	assert.Equal(t, 0.0, h.BatteryCurrent())

	h.SetConsumptionVoltages(230, 230, 230)
	assert.InDelta(t, 520.0/230.0, h.BatteryCurrent(), 0.0001)
}

func TestSetCurrentLimitsRejectsValueAboveNewMax(t *testing.T) {
	item := NewMinMaxUnitBusItem(16, 6, 32, "A", 0)
	item.setBounds(6, 10, 10)

	code, dbusErr := item.SetValue(dbus.MakeVariant(float64(16)))
	assert.NotNil(t, dbusErr)
	assert.Equal(t, -1, code)

	code, dbusErr = item.SetValue(dbus.MakeVariant(float64(8)))
	assert.Nil(t, dbusErr)
	assert.Equal(t, 0, code)
}
