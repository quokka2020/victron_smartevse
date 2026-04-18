package smartevse

import (
	"context"
	"fmt"
	"io"
	"log"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
	"victron_smartevse/victron"

	"github.com/hashicorp/mdns"
	"github.com/quokka2020/gohelpers/mqtthelper"
	"github.com/quokka2020/gohelpers/util"
)

var web = web_interface{}

type EvHandler struct {
	mqtt    *mqtthelper.Mqtt_Helper
	evs     []*SmartEVSE
	victron *victron.VictronHandler
}

type SmartEVSE struct {
	mqtt *mqtthelper.Mqtt_Helper

	Name       string
	IP         string
	SerialNr   int
	Version    string
	Prefix     string
	victron_ev *victron.Victron_EV_Charger

	current_min float64
	current     float64
	current_max float64
	charged     float64
	total       float64

	mode        string
	evplugstate string
	state       string

	session_time      int64
	last_session_time int64
}

var cfg_smartevse_ips = util.GetEnv("SMARTEVSE_IPS", "")

func NewEvHandler(mqtt *mqtthelper.Mqtt_Helper) (*EvHandler, error) {
	var err error
	handler := EvHandler{
		mqtt: mqtt,
	}

	err = handler.findSmartEVSEs()
	if err != nil {
		return nil, err
	}

	if len(handler.evs) > 1 {
		return nil, fmt.Errorf("Only capable to handle 1 smartevse, got %d", len(handler.evs))
	}

	for _, ev := range handler.evs {
		err = ev.load_info()
		if err != nil {
			return nil, err
		}
		log.Printf("loaded: %s %d - %s", ev.Name, ev.SerialNr, ev.Prefix)
	}

	for _, ev := range handler.evs {
		log.Printf("loaded: %s %d - %s", ev.Name, ev.SerialNr, ev.Prefix)
		ev.subscribe(mqtt)
	}

	return &handler, nil
}

func (handler *EvHandler) Close() error {
	return nil
}

func (handler *EvHandler) findSmartEVSEs() error {
	if cfg_smartevse_ips != "" {
		ips := strings.SplitSeq(cfg_smartevse_ips, ",")
		for ip := range ips {
			handler.evs = append(handler.evs, &SmartEVSE{
				mqtt: handler.mqtt,
				// Name: strings.Trim(entry.Host, ".local."),
				IP: ip,
			})
		}
		return nil
	}
	entriesCh := make(chan *mdns.ServiceEntry, 4)
	defer close(entriesCh)
	go func() {
		for entry := range entriesCh {
			if strings.HasPrefix(entry.Host, "SmartEVSE-") {
				handler.evs = append(handler.evs, &SmartEVSE{
					mqtt: handler.mqtt,
					Name: strings.Trim(entry.Host, ".local."),
					IP:   entry.AddrV4.String(),
				})
			}
		}
	}()

	params := mdns.QueryParam{
		Service:     "_http._tcp",
		DisableIPv6: true,
		Entries:     entriesCh,
		Logger:      log.New(io.Discard, "", log.LstdFlags),
	}

	ctx := context.Background()
	// Start the lookup
	err := mdns.QueryContext(ctx, &params)
	if err != nil {
		log.Printf("failed to lookup err:%v", err)
		return err
	}

	return nil
}

func (ev *SmartEVSE) load_info() error {
	raw, err := web.settings(ev.IP)
	if err != nil {
		return err
	}
	ev.SerialNr = raw.SerialNr
	ev.Version = raw.Version
	if raw.MQTT == nil {
		return fmt.Errorf("mqtt is not configured")
	}
	ev.Prefix = raw.MQTT.Prefix
	if ev.Name == "" {
		ev.Name = fmt.Sprintf("smartevse-%d", ev.SerialNr)
	}

	ev.current_min = raw.Settings.Current_Min
	ev.current = raw.Settings.Charge_Current / 10
	ev.current_max = raw.Settings.Current_Max

	ev.session_time = 0
	ev.last_session_time = 0
	return nil
}

func (ev *SmartEVSE) subscribe(mqtt *mqtthelper.Mqtt_Helper) {
	topic := fmt.Sprintf("%s/#", ev.Prefix)
	mqtt.AddStringSubscriptionFull(topic, ev.mqtt_received)
}

func (ev *SmartEVSE) find_sub(topic string) string {
	if len(topic) < len(ev.Prefix)+2 {
		return topic
	}
	return topic[len(ev.Prefix)+1:]
}

var float_subtopics = []string{
	"EVCurrentL1",
	"EVCurrentL2",
	"EVCurrentL3",
	"MaxCurrent",
	"ChargeCurrent",
	"EVChargePower",
	"EVEnergyCharged",
	"EVTotalEnergyCharged",
	"ESPTemp",
}
var test bool = false

func (ev *SmartEVSE) mqtt_received(topic string, value string) {
	if test {
		return
	}
	if ev.victron_ev == nil {
		log.Printf("No victron_ev yet, dropping topic:%s payload:%s", topic, value)
		return
	}
	sub := ev.find_sub(topic)
	if sub == topic {
		log.Printf("invalid topic %s", topic)
		return
	}
	update_state := false
	// string values
	switch sub {
	case "connected":
		ev.victron_ev.ChangeConnected(value == "online")
		return
	case "Access":
	case "State":
		ev.state = value
		update_state = true
	case "EVPlugState":
		ev.evplugstate = value
		update_state = true
	case "Error":
	case "Mode":
		ev.mode = value
		update_state = true
	}
	if update_state {
		if ev.evplugstate == "Disconnected" {
			ev.victron_ev.ChangeStatus(victron.EV_Status_Disconnected)
			ev.session_time = 0
			ev.last_session_time = 0
			ev.victron_ev.EnergyTime(ev.session_time)
		} else {
			switch ev.state {
			case "Charging":
				ev.victron_ev.ChangeStatus(victron.EV_Status_Charging)
			case "Charging Stopped":
				ev.victron_ev.ChangeStatus(victron.EV_Status_Connected)
			case "Connected to EV":
				ev.victron_ev.ChangeStatus(victron.EV_Status_Connected)
			case "Ready to Charge":
				ev.victron_ev.ChangeStatus(victron.EV_Status_Waiting_for_start)
			case "Solar":
				ev.victron_ev.ChangeStatus(victron.EV_Status_Waiting_for_sun)
			case "Smart":
				ev.victron_ev.ChangeStatus(victron.EV_Status_Charging)
			case "Stop Charging":
				ev.victron_ev.ChangeStatus(victron.EV_Status_Connected)
			default:
				log.Printf("Unmapped status. state:[%s] evplugstate:[%s] mode:[%s]", ev.state, ev.evplugstate, ev.mode)
				ev.victron_ev.ChangeStatus(victron.EV_Status_Connected)
			}
			if ev.mode == "Off" {
				ev.victron_ev.ChangeMode(victron.EV_Mode_Manual)
			} else if ev.state == "Smart" {
				ev.victron_ev.ChangeMode(victron.EV_Mode_Automatic)
			} else {
				ev.victron_ev.ChangeMode(victron.EV_Mode_Scheduled)
			}
		}
		return
	}

	if !slices.Contains(float_subtopics, sub) {
		return
	}
	// float values
	i, err := strconv.ParseFloat(value, 64)
	if err != nil {
		log.Printf("Got a non-number from %s with payload [%s] err:%v", topic, value, err)
		return
	}
	switch sub {
	case "EVCurrentL1":
		ev.victron_ev.ChangeCurrentL1(i / 10)
		if i/10 > 0 {
			now:=time.Now().Unix()
			if ev.last_session_time > 0 {
				ev.session_time += (now - ev.last_session_time)
				ev.victron_ev.EnergyTime(ev.session_time)
			}
			ev.last_session_time = now
		}
	case "EVCurrentL2":
		ev.victron_ev.ChangeCurrentL2(i / 10)
	case "EVCurrentL3":
		ev.victron_ev.ChangeCurrentL3(i / 10)
	case "MaxCurrent":
		ev.victron_ev.ChangeMaxCurrent(i / 10)
	case "ChargeCurrent":
		ev.victron_ev.ChangeChargeCurrent(i / 10)
	case "EVChargePower":
		ev.victron_ev.ChangeChargePower(i)
	case "EVEnergyCharged":
		ev.victron_ev.EnergyCharged(i / 1000)
	case "EVTotalEnergyCharged":
		ev.victron_ev.TotalCharged(i / 1000)
	case "ESPTemp":
		ev.victron_ev.Temperature(i)
	}
}

func (evse *SmartEVSE) mode_changed_callback(mode victron.EV_Mode) {
	log.Printf("Request to change mode to: %d", mode)
	topic := fmt.Sprintf("%s/Set/Mode", evse.Prefix)
	switch mode {
	case victron.EV_Mode_Manual:
		evse.mqtt.PublishFullTopic(topic, "Off")
	case victron.EV_Mode_Scheduled:
		evse.mqtt.PublishFullTopic(topic, "Solar")
	case victron.EV_Mode_Automatic:
		evse.mqtt.PublishFullTopic(topic, "Smart")
	default:
		log.Printf("Don't know what to do for mode:%d", mode)
	}
}

func (ev *EvHandler) Write_MainsMeter() {
	l1, l2, l3 := ev.victron.Grid()

	payload := fmt.Sprintf("%d:%d:%d", int32(math.RoundToEven(l1*10)), int32(math.RoundToEven(l2*10)), int32(math.RoundToEven(l3*10)))
	for _, smartevse := range ev.evs {
		topic := fmt.Sprintf("%s/Set/MainsMeter", smartevse.Prefix)
		ev.mqtt.PublishFullTopic(topic, payload)
	}
}

func (ev *EvHandler) Write_HomeBattery() {
	battery := ev.victron.BatteryCurrent()
	payload := fmt.Sprintf("%d", int32(math.RoundToEven(battery*10)))
	for _, smartevse := range ev.evs {
		topic := fmt.Sprintf("%s/Set/HomeBatteryCurrent", smartevse.Prefix)
		ev.mqtt.PublishFullTopic(topic, payload)
	}
}
