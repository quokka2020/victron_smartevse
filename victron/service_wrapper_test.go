package victron

import (
	"testing"

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
