package smartevse

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/quokka2020/gohelpers/util"
)

type webInterface struct {
	sync.Mutex
	client *http.Client
}

type Mqtt struct {
	Prefix string `json:"topic_prefix"`
}

type Settings struct {
	CurrentMin      float64 `json:"current_min"`
	CurrentMax      float64 `json:"current_max"`
	ChargeCurrent   float64 `json:"charge_current"`
	OverrideCurrent float64 `json:"override_current"`
	EnableC2        string  `json:"enable_C2"`
}

type Evse struct {
	Access     int    `json:"access"`
	State      string `json:"state"`
	NrOfPhases int    `json:"nrofphases"`
}

type EvMeter struct {
	TotalWh   float64 `json:"total_wh"`
	ChargedWh float64 `json:"charged_wh"`
}

type Raw struct {
	SerialNr int       `json:"serialnr"`
	Version  string    `json:"version"`
	Mode     string    `json:"mode"`
	ModeId   int       `json:"mode_id"`
	MQTT     *Mqtt     `json:"mqtt"`
	Settings *Settings `json:"settings"`
	Evse     *Evse     `json:"evse"`
	EvMeter  *EvMeter  `json:"ev_meter"`
}

func (web *webInterface) init() {
	web.Lock()
	defer web.Unlock()
	if web.client != nil {
		return
	}
	tr := &http.Transport{
		ResponseHeaderTimeout: 10 * time.Second,
		DisableKeepAlives:     true,
		MaxIdleConns:          5,
		IdleConnTimeout:       20 * time.Second,
		DisableCompression:    true,
		// TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	web.client = &http.Client{
		Transport: tr,
		Timeout:   5 * time.Second,
	}
}

func (web *webInterface) get(evHost, urlPath string, v any) error {
	web.init()

	url := fmt.Sprintf("http://%s/%s", evHost, urlPath)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Add("Content-Type", "application/json")
	req.Header.Add("Accept", "application/json")
	requestStart := time.Now()

	resp, err := web.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if util.Verbose() {
		log.Printf("sfc-api %s in %s: Just received %d", url, time.Since(requestStart), resp.StatusCode)
	}
	if resp.StatusCode == http.StatusOK {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return err
		}
		err = json.Unmarshal(body, v)
		if err != nil {
			return err
		}
		return nil
	}
	return fmt.Errorf("unexpected response from %s status:%d", url, resp.StatusCode)
}

func (web *webInterface) settings(ev string) (Raw, error) {
	result := Raw{}
	err := web.get(ev, "settings", &result)
	return result, err
}
