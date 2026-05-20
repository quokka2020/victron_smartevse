package victron

import (
	"reflect"
	"testing"

	"github.com/godbus/dbus/v5"
	"github.com/stretchr/testify/assert"
)

func TestGetValueMethodReturnsVariant(t *testing.T) {
	unit := NewUnitFormatterObject(1.23, "kWh", 3)
	position := NewEvPositionBusItem(EV_Position_AC_Output)

	cases := map[string]any{
		"UnitBusItem":       &unit,
		"EvPositionBusItem": &position,
	}

	expected := reflect.TypeOf(dbus.Variant{})
	for name, item := range cases {
		t.Run(name, func(t *testing.T) {
			m, ok := reflect.TypeOf(item).MethodByName("GetValue")
			if !ok {
				t.Fatalf("GetValue method not found")
			}
			assert.Equal(t, expected, m.Type.Out(0))
		})
	}
}
