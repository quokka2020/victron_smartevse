package victron

import (
	"errors"
	"testing"

	"github.com/godbus/dbus/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEvModeBusItemRejectsCallbackRejectedMode(t *testing.T) {
	item := NewEvModeBusItem(EV_Mode_Manual)
	item.callback = func(mode EV_Mode) error {
		if mode == EV_Mode_Scheduled {
			return errors.New("scheduled unsupported")
		}
		return nil
	}

	code, dbusErr := item.SetValue(dbus.MakeVariant(int32(EV_Mode_Scheduled)))
	require.NotNil(t, dbusErr)
	assert.Equal(t, -1, code)

	val, dbusErr := item.GetValue()
	require.Nil(t, dbusErr)
	assert.Equal(t, int32(EV_Mode_Manual), val.Value())
}
