package smartevse

import (
	"log"
	"time"
	"victron_smartevse/victron"
)

const (
	// phaseActiveCurrentA is the per-phase current above which a phase counts
	// as charging when detecting the number of phases used by the EV.
	phaseActiveCurrentA = 1.0
	// phaseDetectionStableFor is how long a phase count must be observed
	// before it is committed to the S2 resource manager.
	phaseDetectionStableFor = 20 * time.Second
)

// s2State holds the SmartEVSE side of S2 control.
type s2State struct {
	// command is the last instruction from the S2 CEM, nil when not controlled.
	command *victron.S2ChargeCommand
	// paused is true when the S2 Standby mode paused the charger (as opposed to
	// the user), so release knows to resume it.
	paused bool
	// userPaused is true when the user stopped charging via /StartStop; S2
	// charge commands are not applied until the user starts again.
	userPaused bool

	phaseCurrents   [3]float64
	candidatePhases int
	candidateSince  time.Time
	now             func() time.Time
}

func (smartEvse *SmartEVSE) s2Now() time.Time {
	if smartEvse.s2.now != nil {
		return smartEvse.s2.now()
	}
	return time.Now()
}

// s2ControlCallback drives the charger for the operation mode selected by the
// S2 CEM. Charging uses Smart mode with a CurrentOverride, Standby pauses.
func (smartEvse *SmartEVSE) s2ControlCallback(cmd victron.S2ChargeCommand) {
	command := cmd
	smartEvse.s2.command = &command
	smartEvse.applyS2Command()
}

func (smartEvse *SmartEVSE) applyS2Command() {
	cmd := smartEvse.s2.command
	if cmd == nil {
		return
	}
	if !cmd.Charge {
		if !smartEvse.isPaused() {
			smartEvse.s2.paused = true
			smartEvse.requestRunningMode("Pause")
		}
		return
	}

	if smartEvse.s2.userPaused {
		log.Printf("S2 %s: charge %.0fA requested but charging is stopped by the user", smartEvse.Name, cmd.CurrentA)
		return
	}
	smartEvse.s2.paused = false
	smartEvse.setManagedAutoControl(true, cmd.CurrentA)
	if smartEvse.isPaused() {
		smartEvse.requestRunningMode(smartEvse.targetStrategyForManagedMode())
	}
}

// s2ReleaseCallback returns the charger to normal operation when S2 control
// ends (CEM disconnect, not-controllable, or leaving Auto mode).
func (smartEvse *SmartEVSE) s2ReleaseCallback() {
	log.Printf("S2 %s: control released", smartEvse.Name)
	smartEvse.s2.command = nil
	wasS2Paused := smartEvse.s2.paused
	smartEvse.s2.paused = false
	smartEvse.clearManagedAutoControl()
	if wasS2Paused && smartEvse.isPaused() && !smartEvse.s2.userPaused {
		smartEvse.requestRunningMode(smartEvse.targetStrategyForManagedMode())
	}
}

func (smartEvse *SmartEVSE) s2Controlled() bool {
	return smartEvse.s2.command != nil
}

func (smartEvse *SmartEVSE) updatePhaseCurrent(index int, currentA float64) {
	if index < 0 || index >= len(smartEvse.s2.phaseCurrents) {
		return
	}
	smartEvse.s2.phaseCurrents[index] = currentA
	smartEvse.detectPhases()
}

func (smartEvse *SmartEVSE) detectPhases() {
	count := 0
	for _, current := range smartEvse.s2.phaseCurrents {
		if current > phaseActiveCurrentA {
			count++
		}
	}
	if count == 0 {
		smartEvse.s2.candidatePhases = 0
		return
	}
	now := smartEvse.s2Now()
	if count != smartEvse.s2.candidatePhases {
		smartEvse.s2.candidatePhases = count
		smartEvse.s2.candidateSince = now
		return
	}
	if now.Sub(smartEvse.s2.candidateSince) < phaseDetectionStableFor {
		return
	}
	if smartEvse.victronEvcs != nil && smartEvse.victronEvcs.EvPhases() != count {
		log.Printf("S2 %s: detected EV charging on %d phase(s)", smartEvse.Name, count)
		smartEvse.victronEvcs.SetEvPhases(count)
	}
}

func (smartEvse *SmartEVSE) resetPhasesOnUnplug() {
	smartEvse.s2.candidatePhases = 0
	smartEvse.s2.phaseCurrents = [3]float64{}
	if smartEvse.victronEvcs == nil || smartEvse.victronEvcs.RememberEvPhases() {
		return
	}
	smartEvse.victronEvcs.ResetEvPhases()
}

// handleS2PlugChange starts new Auto-mode sessions in Standby instead of
// applying the previous S2 command. Manual-mode AutoStart remains unchanged.
func (smartEvse *SmartEVSE) handleS2PlugChange(previousPlugState string) bool {
	if smartEvse.evPlugState == "Disconnected" && previousPlugState != "Disconnected" {
		smartEvse.resetPhasesOnUnplug()
	}
	if previousPlugState == "Connected" || smartEvse.evPlugState != "Connected" {
		return smartEvse.s2Controlled()
	}
	if smartEvse.effectiveManagedMode() != victron.EV_Mode_Auto {
		return false
	}
	smartEvse.s2.userPaused = false
	smartEvse.s2.command = nil
	smartEvse.s2.paused = true
	if !smartEvse.isPaused() {
		smartEvse.requestRunningMode("Pause")
	}
	if smartEvse.managedAutoControlActive {
		smartEvse.clearManagedAutoControl()
	}
	smartEvse.victronEvcs.ResetS2OnPlugIn()
	return true
}
