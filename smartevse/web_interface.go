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

type web_interface struct {
	sync.Mutex
	client *http.Client
}

type Smartevse_raw_mqtt struct {
	Prefix string `json:"topic_prefix"`
}

type Smartevse_raw_settings struct {
	Current_Min    float64 `json:"current_min"`
	Current_Max    float64 `json:"current_max"`
	Charge_Current float64 `json:"charge_current"`
}

type Smartevse_ev_meter struct {
	Total_Wh   float64 `json:"total_wh"`
	Charged_Wh float64 `json:"charged_wh"`
}

type SmartevseOcpp struct {
	Mode          string `json:"mode"`
	BackendUrl    string `json:"backend_url"`
	CbId          string `json:"cb_id"`
	AuthKey       string `json:"auth_key"`
	AutoAuth      string `json:"auto_auth"`
	AutoAuthIdtag string `json:"auto_auth_idtag"`
	Status        string `json:"status"`
}

type Smartevse_raw struct {
	SerialNr int                     `json:"serialnr"`
	Version  string                  `json:"version"`
	ModeId   int                     `json:"mode_id"`
	MQTT     *Smartevse_raw_mqtt     `json:"mqtt"`
	Settings *Smartevse_raw_settings `json:"settings"`
	EvMeter  *Smartevse_ev_meter     `json:"ev_meter"`
	Ocpp     *SmartevseOcpp          `json:"ocpp"`
}

func (h *web_interface) init() {
	h.Lock()
	defer h.Unlock()
	if h.client != nil {
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
	h.client = &http.Client{
		Transport: tr,
		Timeout:   5 * time.Second,
	}
}

func (web *web_interface) get(ev_ip, urlpart string, v any) error {
	web.init()

	url := fmt.Sprintf("http://%s/%s", ev_ip, urlpart)
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
	if util.Verbose() {
		log.Printf("sfc-api %s in %s: Just received %d", url, time.Since(requestStart), resp.StatusCode)
	}
	if resp.StatusCode == http.StatusOK {
		defer resp.Body.Close()
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
	return fmt.Errorf("not logged in %s status:%d", url, resp.StatusCode)
}

func (web *web_interface) post(ev_ip, urlpart string, v any) error {
	web.init()

	url := fmt.Sprintf("http://%s/%s", ev_ip, urlpart)
	req, err := http.NewRequest("POST", url, nil)
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
	if util.Verbose() {
		log.Printf("sfc-api %s in %s: Just received %d", url, time.Since(requestStart), resp.StatusCode)
	}
	if resp.StatusCode == http.StatusOK {
		defer resp.Body.Close()
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
	return fmt.Errorf("not logged in %s status:%d", url, resp.StatusCode)
}

func (web *web_interface) settings(ev string) (Smartevse_raw, error) {
	result := Smartevse_raw{}
	err := web.get(ev, "settings", &result)
	return result, err
}

func (web *web_interface) setOcppAutoStart(ev string, autostart int32) error {
	query := fmt.Sprintf(
		"settings?ocpp_update=1&ocpp_auto_auth=%d",
		autostart,
	)

	result := Smartevse_raw{}
	return web.post(ev, query, &result)
}
