package victron

import (
	"log"
	"reflect"
	"time"

	"github.com/godbus/dbus/v5"
)

type Handler struct {
	dbusConn    DBusConn
	stopChannel chan struct{}
	services    []*Service

	acInL1I       LastFloat
	acInL2I       LastFloat
	acInL3I       LastFloat
	acInL1V       LastFloat
	acInL2V       LastFloat
	acInL3V       LastFloat
	acInConnected LastFloat

	acOutL1V LastFloat
	acOutL2V LastFloat
	acOutL3V LastFloat

	dcV LastFloat
	dcI LastFloat
}

func NewHandler() (*Handler, error) {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil, err
	}
	return NewHandlerWithConn(conn), nil
}

func NewHandlerWithConn(conn DBusConn) *Handler {
	handler := Handler{
		dbusConn:    conn,
		stopChannel: make(chan struct{}),
		services:    []*Service{},
	}
	return &handler
}

func (h *Handler) Close() error {
	for _, service := range h.services {
		_ = service.Close()
	}
	select {
	case h.stopChannel <- struct{}{}:
	default: // channel already full
	}
	return h.dbusConn.Close()
}

func (h *Handler) ListNames() {
	var s []string
	err := h.dbusConn.BusObject().Call("org.freedesktop.DBus.ListNames", 0).Store(&s)
	if err != nil {
		log.Printf("Failed to get list of owned names: %v", err)
	}

	log.Println("Currently owned names on the session bus:")
	for _, v := range s {
		log.Println(v)
	}
}

func (h *Handler) Listen() {
	var err error
	defer log.Printf("Stop Listen")
	err = h.dbusConn.AddMatchSignal(
		dbus.WithMatchObjectPath("/"),
		dbus.WithMatchInterface("com.victronenergy.BusItem"),
		dbus.WithMatchSender("com.victronenergy.vebus.ttyS4"),
	)
	if err != nil {
		panic(err)
	}

	signals := make(chan *dbus.Signal, 10)
	h.dbusConn.Signal(signals)
	defer h.dbusConn.RemoveSignal(signals)

	for {
		select {
		case message := <-signals:
			if message == nil {
				continue
			}
			if len(message.Body) == 1 {
				if m, ok := message.Body[0].(map[string]map[string]dbus.Variant); ok {
					h.handleDbusSignalMessage(m)
				}
			}
		case <-h.stopChannel:
			return
		case <-time.After(3 * time.Second):
			log.Printf("timeout")
		}
	}
}

func (h *Handler) Grid() (float64, float64, float64) {
	return h.acInL1I.lastValue, h.acInL2I.lastValue, h.acInL3I.lastValue
}

func (h *Handler) BatteryCurrent() float64 {
	avgAcVoltage := (h.acOutL1V.lastValue + h.acOutL2V.lastValue + h.acOutL3V.lastValue) / 3
	if avgAcVoltage == 0 {
		// no voltages received yet
		return 0
	}
	batteryPower := h.dcV.lastValue * h.dcI.lastValue
	return batteryPower / avgAcVoltage
}

func (h *Handler) SetAcOutVoltages(l1, l2, l3 float64) {
	h.acOutL1V.lastValue = l1
	h.acOutL2V.lastValue = l2
	h.acOutL3V.lastValue = l3
}

func (h *Handler) SetAcInVoltages(l1, l2, l3 float64) {
	h.acInL1V.lastValue = l1
	h.acInL2V.lastValue = l2
	h.acInL3V.lastValue = l3
}

func (h *Handler) handleDbusSignalMessage(msg map[string]map[string]dbus.Variant) {
	h.acInL1I.Change(value(msg, "/Ac/ActiveIn/L1/I"))
	h.acInL2I.Change(value(msg, "/Ac/ActiveIn/L2/I"))
	h.acInL3I.Change(value(msg, "/Ac/ActiveIn/L3/I"))
	h.acInL1V.Change(value(msg, "/Ac/ActiveIn/L1/V"))
	h.acInL2V.Change(value(msg, "/Ac/ActiveIn/L2/V"))
	h.acInL3V.Change(value(msg, "/Ac/ActiveIn/L3/V"))
	h.acInConnected.Change(value(msg, "/Ac/ActiveIn/Connected"))

	h.acOutL1V.Change(value(msg, "/Ac/Out/L1/V"))
	h.acOutL2V.Change(value(msg, "/Ac/Out/L2/V"))
	h.acOutL3V.Change(value(msg, "/Ac/Out/L3/V"))

	h.dcI.Change(value(msg, "/Dc/0/Current"))
	h.dcV.Change(value(msg, "/Dc/0/Voltage"))

}

func value(msg map[string]map[string]dbus.Variant, key string) *float64 {
	m := msg[key]
	if m == nil {
		// log.Printf("key %s not found %v", key, msg )
		return nil
	}
	v, vf := m["Value"]
	if !vf {
		log.Printf("key %s has no value", key)
		return nil
	}
	if value, ok := v.Value().(float64); ok {
		return &value
	} else if value, ok := v.Value().(int32); ok {
		return new(float64(value))
	} else if value, ok := v.Value().(uint32); ok {
		return new(float64(value))
	}
	log.Printf("not float but %v", reflect.TypeOf(v.Value()))
	return nil
}

type LastFloat struct {
	lastValue float64
}

func (last *LastFloat) Change(n *float64) {
	if n != nil {
		last.lastValue = *n
	}
}
func (last *LastFloat) Get() float64 {
	return last.lastValue
}
