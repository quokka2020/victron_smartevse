package victron

import (
	"log"
	"reflect"
	"time"

	"github.com/godbus/dbus/v5"
)

type VictronHandler struct {
	dbusconn     DBusConn
	stop_channel chan struct{}
	services     []*Service

	grid_l1_i      LastFloat
	grid_l2_i      LastFloat
	grid_l3_i      LastFloat
	grid_l1_v      LastFloat
	grid_l2_v      LastFloat
	grid_l3_v      LastFloat
	grid_connected LastFloat

	consumption_l1_i LastFloat
	consumption_l2_i LastFloat
	consumption_l3_i LastFloat
	consumption_l1_v LastFloat
	consumption_l2_v LastFloat
	consumption_l3_v LastFloat

	battery_v LastFloat
	battery_i LastFloat
}

func NewVictronHandler() (*VictronHandler, error) {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil, err
	}
	return NewVictronHandlerWithConn(conn), nil
}

func NewVictronHandlerWithConn(conn DBusConn) *VictronHandler {
	handler := VictronHandler{
		dbusconn:     conn,
		stop_channel: make(chan struct{}),
		services:     []*Service{},
	}
	return &handler
}

func (handler *VictronHandler) Close() error {
	for _, service := range handler.services {
		service.Close()
	}
	select {
	case handler.stop_channel <- struct{}{}:
	default: // channel already full
	}
	return handler.dbusconn.Close()
}

func (handler *VictronHandler) ListNames() {
	var s []string
	err := handler.dbusconn.BusObject().Call("org.freedesktop.DBus.ListNames", 0).Store(&s)
	if err != nil {
		log.Printf("Failed to get list of owned names: %v", err)
	}

	log.Println("Currently owned names on the session bus:")
	for _, v := range s {
		log.Println(v)
	}
}

func (handler *VictronHandler) Listen() {
	var err error
	defer log.Printf("Stop Listen")
	err = handler.dbusconn.AddMatchSignal(
		// dbus.WithMatchObjectPath("/Ac/Grid/L1/Current"),
		dbus.WithMatchObjectPath("/"),
		dbus.WithMatchInterface("com.victronenergy.BusItem"),
		// dbus.WithMatchSender("com.victronenergy.system"),
		dbus.WithMatchSender("com.victronenergy.vebus.ttyS4"),
	)
	if err != nil {
		panic(err)
	}

	// Grab media player keys.
	// bus := handler.dbusconn.Object("org.gnome.SettingsDaemon", "/org/gnome/SettingsDaemon/MediaKeys")
	// call := bus.Call("org.gnome.SettingsDaemon.MediaKeys.GrabMediaPlayerKeys", 0, "test app", uint(0))
	// err = call.Err
	// if err != nil {
	// 	panic(err)
	// }

	signals := make(chan *dbus.Signal, 10)
	handler.dbusconn.Signal(signals)
	defer handler.dbusconn.RemoveSignal(signals)

	for {
		select {
		case message := <-signals:
			//log.Printf("Name:%s Path:%s Body:%d", message.Name, message.Path, len(message.Body))
			//for i, val := range message.Body {
			//	log.Printf("%d: %v %v", i, reflect.TypeOf(val), val)
			//}
			if len(message.Body) == 1 {
				if m, ok := message.Body[0].(map[string]map[string]dbus.Variant); ok {
					handler.handle_dbus_signal_message(m)

					//for kk, mm := range m {
					//	for kkk, mmm := range mm {
					//		log.Printf(".  kk=%s kkk=%s v=:%v", kk, kkk, mmm)
					//	}
					//}
				}
			}
		case <-handler.stop_channel:
			return
		case <-time.After(3 * time.Second):
			log.Printf("timeout")
		}
	}
}

func (h *VictronHandler) Grid() (float64, float64, float64) {
	return h.grid_l1_i.lastValue, h.grid_l2_i.lastValue, h.grid_l3_i.lastValue

	//if h.grid_connected.lastValue != 0 {
	//	return h.grid_l1_i.lastValue, h.grid_l2_i.lastValue, h.grid_l3_i.lastValue
	//}
	//
	//return h.Consumption()
}

func (h *VictronHandler) Consumption() (float64, float64, float64) {
	return h.consumption_l1_i.lastValue, h.consumption_l2_i.lastValue, h.consumption_l3_i.lastValue
}

func (h *VictronHandler) SetConsumptionVoltages(l1, l2, l3 float64) {
	h.consumption_l1_v.lastValue = l1
	h.consumption_l2_v.lastValue = l2
	h.consumption_l3_v.lastValue = l3
}

func (h *VictronHandler) BatteryCurrent() float64 {
	avg_v := (h.consumption_l1_v.lastValue + h.consumption_l2_v.lastValue + h.consumption_l3_v.lastValue) / 3
	battery_p := h.battery_v.lastValue * h.battery_i.lastValue
	// log.Printf("avg_v:%f battery_p:%f b_i:%f b_b:%f l1:%f l2:%f l3:%f",avg_v,battery_p, h.battery_v.lastValue, h.battery_i.lastValue,h.grid_l1_v.lastValue,h.grid_l2_v.lastValue,h.grid_l3_v.lastValue)
	return battery_p / avg_v
}

func (handler *VictronHandler) handle_dbus_signal_message(msg map[string]map[string]dbus.Variant) {
	handler.grid_l1_i.Change(value(msg, "/Ac/ActiveIn/L1/I"))
	handler.grid_l2_i.Change(value(msg, "/Ac/ActiveIn/L2/I"))
	handler.grid_l3_i.Change(value(msg, "/Ac/ActiveIn/L3/I"))
	handler.grid_l1_v.Change(value(msg, "/Ac/ActiveIn/L1/V"))
	handler.grid_l2_v.Change(value(msg, "/Ac/ActiveIn/L2/V"))
	handler.grid_l3_v.Change(value(msg, "/Ac/ActiveIn/L3/V"))
	handler.grid_connected.Change(value(msg, "/Ac/ActiveIn/Connected"))

	handler.consumption_l1_i.Change(value(msg, "/Ac/Out/L1/I"))
	handler.consumption_l2_i.Change(value(msg, "/Ac/Out/L2/I"))
	handler.consumption_l3_i.Change(value(msg, "/Ac/Out/L3/I"))
	handler.consumption_l1_v.Change(value(msg, "/Ac/Out/L1/V"))
	handler.consumption_l2_v.Change(value(msg, "/Ac/Out/L2/V"))
	handler.consumption_l3_v.Change(value(msg, "/Ac/Out/L3/V"))

	handler.battery_i.Change(value(msg, "/Dc/0/Current"))
	handler.battery_v.Change(value(msg, "/Dc/0/Voltage"))

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
		float := float64(value)
		return &float
	} else if value, ok := v.Value().(uint32); ok {
		float := float64(value)
		return &float
	}
	log.Printf("not float but %v", reflect.TypeOf(v.Value()))
	return nil
}

type LastFloat struct {
	lastValue float64
}

func (last *LastFloat) Change(n *float64) {
	if n != nil {
		// log.Printf("Change value: %f", *n)
		last.lastValue = *n
		// } else {
		// 	log.Printf("No value")
	}
}
func (last *LastFloat) Get() float64 {
	return last.lastValue
}
