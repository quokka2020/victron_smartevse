package smartevse

import (
	"embed"
	"encoding/json"
	"fmt"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
)

//go:embed testdata
var testdata embed.FS

func TestParseSettings(t *testing.T) {
	var err error
	raw := Smartevse_raw{}
	data, err := testdata.ReadFile("testdata/raw.json")
	assert.Nil(t, err)
	err = json.Unmarshal(data, &raw)
	assert.Nil(t, err)

	assert.Equal(t, 9999, raw.SerialNr)
	assert.Equal(t, 2, raw.ModeId)

	assert.Equal(t, "v3.10.0", raw.Version)
	assert.NotNil(t, raw.MQTT)
	assert.Equal(t, "SmartEVSE", raw.MQTT.Prefix)

	assert.Equal(t, 6.0, raw.Settings.Current_Min)
	assert.Equal(t, 60.0, raw.Settings.Charge_Current)
	assert.Equal(t, 13.0, raw.Settings.Current_Max)
	assert.Equal(t, 3255525.0, raw.EvMeter.Total_Wh)
	assert.Equal(t, 6721.0, raw.EvMeter.Charged_Wh)

}

func TestV1(t *testing.T) {
	l1 := float64(14.2234)
	l2 := float64(21.4489)
	l3 := float64(20)
	val := fmt.Sprintf("%d:%d:%d", int32(math.RoundToEven(l1*10)), int32(math.RoundToEven(l2*10)), int32(math.RoundToEven(l3*10)))
	assert.Equal(t, "142:214:200", val)
}

func TestV2(t *testing.T) {
	l1 := float64(-14.2234)
	l2 := float64(21.4489)
	l3 := float64(-20)
	val := fmt.Sprintf("%d:%d:%d", int32(math.RoundToEven(l1*10)), int32(math.RoundToEven(l2*10)), int32(math.RoundToEven(l3*10)))
	assert.Equal(t, "-142:214:-200", val)
}
