package smartevse

import (
	"log"
	"victron_smartevse/victron"
)

func (handler *EvHandler) RegisterInVictron(vh *victron.Handler) error {
	var err error
	handler.victron = vh
	for _, evse := range handler.evs {
		evse.victronEvcs, err = vh.CreateEvCharger(evse.SerialNr, evse.Version, evse.IP, evse.currentMin, evse.current, evse.currentMax, evse.charged, evse.total)
		if err != nil {
			log.Printf("failed to create Ev charger err: %v", err)
			return err
		}
		evse.victronEvcs.SetModeChangedCallback(evse.modeChangedCallback)
		evse.victronEvcs.SetOverrideCurrentChangedCallback(evse.setOverrideCurrentChangedCallback)
		evse.victronEvcs.SetStartStopChangedCallback(evse.startStopChangedCallback)
		// AutoStart and Position are internal Victron state, managed by internal callbacks
		evse.syncVictronState()

		// Wait for all topics before subsribing
		if handler.mqtt != nil {
			evse.subscribe(handler.mqtt)
		}
	}

	return nil
}
