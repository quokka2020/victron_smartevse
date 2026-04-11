package victron

import (
	"testing"

	"github.com/godbus/dbus/v5"
	"github.com/stretchr/testify/assert"
)

func TestServiceWrapperGetText(t *testing.T) {
	service := Service{
		bus_items: map[string]BusItem{
			"/Ac/L1/Current": NewAnyBusItem("1"),
			"/Ac/L2/Current": NewAnyBusItem("A"),
		},
	}
	wrapper := service_wrapper{
		service: &service,
	}
	res, _ := wrapper.GetText()
	expected := map[string]string{
		"Ac/L1/Current": "1",
		"Ac/L2/Current": "A",
	}
	assert.Equal(t, expected, res)
	part := part_service_wrapper{
		service: &service,
		path:    "/Ac/",
	}
	res, _ = part.GetText()
	expected = map[string]string{
		"L1/Current": "1",
		"L2/Current": "A",
	}
	assert.Equal(t, expected, res)

}

func TestServiceWrapperGetItemsValueIsNotNestedVariant(t *testing.T) {
	service := Service{
		bus_items: map[string]BusItem{
			"/ProductName": NewAnyBusItem("SmartEVSE"),
			"/ProductId":   NewAnyBusItem(int32(0xFFFF)),
		},
	}
	wrapper := service_wrapper{service: &service}

	res, err := wrapper.GetItems()
	assert.Nil(t, err)

	assert.Equal(t, "SmartEVSE", res["/ProductName"]["Value"].Value())
	assert.Equal(t, int32(0xFFFF), res["/ProductId"]["Value"].Value())

	_, nested := res["/ProductName"]["Value"].Value().(dbus.Variant)
	assert.False(t, nested)
}

func TestPartServiceWrapperGetValueIsNotNestedVariant(t *testing.T) {
	service := Service{
		bus_items: map[string]BusItem{
			"/Ac/L1/Power": NewAnyBusItem(float64(2300)),
		},
	}
	part := part_service_wrapper{service: &service, path: "/Ac/"}

	res, err := part.GetValue()
	assert.Nil(t, err)

	assert.Equal(t, float64(2300), res["L1/Power"].Value())
	_, nested := res["L1/Power"].Value().(dbus.Variant)
	assert.False(t, nested)
}
