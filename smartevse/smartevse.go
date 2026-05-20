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
	autoIdtag   string
	access      string

	session_time      float64
	last_session_time float64
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

	for _, ev := range handler.evs {
		err = ev.loadInfo()
		if err != nil {
			return nil, err
		}
		log.Printf("loaded: %s %d - %s", ev.Name, ev.SerialNr, ev.Prefix)
	}

	handler.evs = dedupeBySerial(handler.evs)

	for _, ev := range handler.evs {
		ev.subscribe(mqtt)
	}

	return &handler, nil
}

func dedupeBySerial(evs []*SmartEVSE) []*SmartEVSE {
	seen := map[int]string{}
	result := make([]*SmartEVSE, 0, len(evs))

	for _, ev := range evs {
		if prevIP, exists := seen[ev.SerialNr]; exists {
			log.Printf("Duplicate SmartEVSE serial %d detected (%s and %s); keeping the first entry", ev.SerialNr, prevIP, ev.IP)
			continue
		}
		seen[ev.SerialNr] = ev.IP
		result = append(result, ev)
	}

	return result
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

func (ev *SmartEVSE) loadInfo() error {
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
	if raw.Ocpp != nil && raw.Ocpp.AutoAuthIdtag != "" {
		ev.autoIdtag = raw.Ocpp.AutoAuthIdtag
		log.Printf("RFID tag configured: %s", ev.autoIdtag)
	}

	if raw.EvMeter != nil {
		ev.total = raw.EvMeter.Total_Wh / 1000
		ev.charged = raw.EvMeter.Charged_Wh / 1000
	}

	ev.session_time = 0
	ev.last_session_time = 0
	return nil
}

func (ev *SmartEVSE) subscribe(mqtt *mqtthelper.Mqtt_Helper) {
	topic := fmt.Sprintf("%s/#", ev.Prefix)
	mqtt.AddStringSubscriptionFull(topic, ev.mqttReceived)
}

func (ev *SmartEVSE) findSub(topic string) string {
	if len(topic) < len(ev.Prefix)+2 {
		return topic
	}
	return topic[len(ev.Prefix)+1:]
}

var floatSubtopics = []string{
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

func (ev *SmartEVSE) mqttReceived(topic string, value string) {
	if ev.victron_ev == nil {
		log.Printf("No victron_ev yet, dropping topic:%s payload:%s", topic, value)
		return
	}
	sub := ev.findSub(topic)
	if sub == topic {
		log.Printf("invalid topic %s", topic)
		return
	}
	updateState := false

	// string values
	switch sub {
	case "connected":
		ev.victron_ev.SetConnected(value == "online")
		return
	case "Access":
		ev.access = value
	case "State":
		ev.state = value
		updateState = true
	case "EVPlugState":
		ev.evplugstate = value
		updateState = true
	case "Error":
	case "Mode":
		ev.mode = value
		updateState = true
	}
	if updateState {
		if ev.evplugstate == "Disconnected" {
			ev.victron_ev.SetStatus(victron.EV_Status_Disconnected)
			ev.session_time = 0
			ev.last_session_time = 0
			ev.victron_ev.SetSessionTime(ev.session_time)
		} else {
			switch ev.state {
			case "Charging":
				ev.victron_ev.SetStatus(victron.EV_Status_Charging)
			case "Charging Stopped":
				ev.victron_ev.SetStatus(victron.EV_Status_Connected)
			case "Connected to EV":
				ev.victron_ev.SetStatus(victron.EV_Status_Connected)
			case "Ready to Charge":
				ev.victron_ev.SetStatus(victron.EV_Status_Waiting_for_start)
			case "Solar":
				ev.victron_ev.SetStatus(victron.EV_Status_Waiting_for_sun)
			case "Smart":
				ev.victron_ev.SetStatus(victron.EV_Status_Charging)
			case "Stop Charging":
				ev.victron_ev.SetStatus(victron.EV_Status_Connected)
			default:
				log.Printf("Unmapped status. state:[%s] evplugstate:[%s] mode:[%s]", ev.state, ev.evplugstate, ev.mode)
				ev.victron_ev.SetStatus(victron.EV_Status_Connected)
			}
			if ev.mode == "Off" {
				ev.victron_ev.SetMode(victron.EV_Mode_Manual)
			} else if ev.mode == "Smart" {
				ev.victron_ev.SetMode(victron.EV_Mode_Auto)
			} else {
				ev.victron_ev.SetMode(victron.EV_Mode_Scheduled)
			}
		}
		return
	}

	if !slices.Contains(floatSubtopics, sub) {
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
			ev.setSessionTime()
		}
	case "EVCurrentL2":
		ev.victron_ev.ChangeCurrentL2(i / 10)
		if i/10 > 0 {
			ev.setSessionTime()
		}
	case "EVCurrentL3":
		ev.victron_ev.ChangeCurrentL3(i / 10)
		if i/10 > 0 {
			ev.setSessionTime()
		}
	case "MaxCurrent":
		ev.victron_ev.SetMaxCurrent(i / 10)
	case "ChargeCurrent":
		ev.victron_ev.SetChargeCurrent(i / 10)
	case "EVChargePower":
		ev.victron_ev.SetAcPower(i)
	case "EVEnergyCharged":
		ev.victron_ev.SetSessionEnergy(i / 1000)
	case "EVTotalEnergyCharged":
		ev.victron_ev.SetTotalEnergy(i / 1000)
	case "ESPTemp":
		ev.victron_ev.SetTemperature(i)
	}
}

func (ev *SmartEVSE) setSessionTime() {
	now := time.Now().Unix()
	if ev.last_session_time > 0 {
		ev.session_time += float64(now) - ev.last_session_time
		ev.victron_ev.SetSessionTime(ev.session_time)
	}
	ev.last_session_time = float64(now)
}

func (evse *SmartEVSE) modeChangedCallback(mode victron.EV_Mode) {
	log.Printf("Request to change mode to: %d", mode)
	topic := fmt.Sprintf("%s/Set/Mode", evse.Prefix)
	switch mode {
	case victron.EV_Mode_Manual:
		evse.mqtt.PublishFullTopic(topic, "Off")
	case victron.EV_Mode_Scheduled:
		evse.mqtt.PublishFullTopic(topic, "Solar")
	case victron.EV_Mode_Auto:
		evse.mqtt.PublishFullTopic(topic, "Smart")
	default:
		log.Printf("Don't know what to do for mode:%d", mode)
	}
}

func (evse *SmartEVSE) setOverrideCurrentChangedCallback(value, _, max float64) {
	if value == max {
		value = 0 // 0 means no override, so if the value is the same as max, we can set it to 0 to disable the override
	}
	log.Printf("Request to change override current to: %f", value)
	topic := fmt.Sprintf("%s/Set/CurrentOverride", evse.Prefix)
	payload := fmt.Sprintf("%d", int32(math.RoundToEven(value*10)))
	evse.mqtt.PublishFullTopic(topic, payload)
}

func (evse *SmartEVSE) startStopChangedCallback(mode victron.EV_StartStop) {
	log.Printf("Request to change start/stop to: %d", mode)
	if evse.autoIdtag == "" {
		log.Printf("No RFID configured, can't start/stop")
		return
	}
	if (evse.access == "Deny" && mode == victron.EV_StartStop_Start) ||
		(evse.access == "Allow" && mode == victron.EV_StartStop_Stop) {
		topic := fmt.Sprintf("%s/Set/RFID", evse.Prefix)
		payload := evse.autoIdtag
		evse.mqtt.PublishFullTopic(topic, payload)
	}
}

func (evse *SmartEVSE) autoStartChangedCallback(mode victron.EV_AutoStart) {
	log.Printf("Request to change autostart to: %d", mode)
	if evse.autoIdtag == "" {
		log.Printf("No RFID configured, can't auto start/stop")
	}
	settings, _ := web.settings(evse.IP)
	if settings.Ocpp != nil {
		if settings.Ocpp.AutoAuth != mode.ToString() {
			err := web.setOcppAutoStart(evse.IP, int32(mode))
			if err != nil {
				log.Printf("Failed to update auto start/stop")
			} else {
				log.Printf("Updated auto start/stop")
			}
		}
	}
}

func (ev *EvHandler) WriteMainsmeter() {
	l1, l2, l3 := ev.victron.Grid()
	//log.Printf("Grid L1:%f L2:%f L3:%f", l1, l2, l3)

	payload := fmt.Sprintf("%d:%d:%d", int32(math.RoundToEven(l1*10)), int32(math.RoundToEven(l2*10)), int32(math.RoundToEven(l3*10)))
	for _, smartevse := range ev.evs {
		topic := fmt.Sprintf("%s/Set/MainsMeter", smartevse.Prefix)
		ev.mqtt.PublishFullTopic(topic, payload)
	}
}

func (ev *EvHandler) WriteHomebattery() {
	battery := ev.victron.BatteryCurrent()
	//log.Printf("Battery current:%f", battery)

	payload := fmt.Sprintf("%d", int32(math.RoundToEven(battery*10)))
	for _, smartevse := range ev.evs {
		topic := fmt.Sprintf("%s/Set/HomeBatteryCurrent", smartevse.Prefix)
		ev.mqtt.PublishFullTopic(topic, payload)
	}
}
