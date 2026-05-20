package smartevse

import (
	"log"
	"victron_smartevse/victron"
)

func (ev *EvHandler) RegisterInVictron(vh *victron.VictronHandler) error {
	var err error
	ev.victron = vh
	for _, evse := range ev.evs {
		evse.victron_ev, err = vh.CreateEvCharger(evse.SerialNr, evse.Version, evse.IP, evse.current_min, evse.current, evse.current_max, evse.charged, evse.total)
		if err != nil {
			log.Printf("failed to create Ev charger err: %v", err)
			return err
		}
		evse.victron_ev.SetModeChangedCallback(evse.modeChangedCallback)
		evse.victron_ev.SetOverrideCurrentChangedCallback(evse.setOverrideCurrentChangedCallback)
		evse.victron_ev.SetStartStopChangedCallback(evse.startStopChangedCallback)
		evse.victron_ev.SetAutoStartChangedCallback(evse.autoStartChangedCallback)
	}

	return nil
}
