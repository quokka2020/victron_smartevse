package smartevse

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDedupeBySerialKeepsOnlyUniqueSerials(t *testing.T) {
	evs := []*SmartEVSE{
		{SerialNr: 1001, IP: "192.168.1.10"},
		{SerialNr: 1002, IP: "192.168.1.11"},
		{SerialNr: 1001, IP: "192.168.1.12"},
	}

	result := dedupeBySerial(evs)

	if assert.Len(t, result, 2) {
		assert.Equal(t, 1001, result[0].SerialNr)
		assert.Equal(t, "192.168.1.10", result[0].IP)
		assert.Equal(t, 1002, result[1].SerialNr)
	}
}
