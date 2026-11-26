package victron

import (
	"fmt"
	"math"
	"strings"
)

const (
	// s2NominalVoltage is the voltage used to translate charge currents into
	// advertised power. Opportunity Loads' EVCS quirks reverse-calculate the
	// current with end_of_range/230, so this must stay 230.
	s2NominalVoltage = 230.0
	// s2MaxChargeSteps keeps the full-mesh transition count within the S2
	// schema limit of 1000 transitions: (steps+1)*steps <= 1000.
	s2MaxChargeSteps  = 31
	s2StandbyLabel    = "Standby"
	s2DefaultMinA     = 6
	s2DefaultEVPhases = 1
	s2DefaultMaxPhase = 3
)

// S2ChargeCommand is the charger behaviour requested by the active S2 OMBC
// operation mode.
type S2ChargeCommand struct {
	Charge   bool
	CurrentA float64
}

type s2OperationMode struct {
	id       string
	label    string
	currentA int // 0 for Standby
}

func (m s2OperationMode) isStandby() bool { return m.currentA == 0 }

func (m s2OperationMode) command() S2ChargeCommand {
	if m.isStandby() {
		return S2ChargeCommand{}
	}
	return S2ChargeCommand{Charge: true, CurrentA: float64(m.currentA)}
}

// s2ChargeModel is the OMBC model advertised to the CEM: a Standby mode and
// one fixed-power mode per charge current step. Dynamic ESS always instructs
// with factor 1.0 and picks the mode closest to its setpoint, so ranged modes
// would make it charge at full power; fixed steps give it real granularity.
type s2ChargeModel struct {
	phases    int
	modes     []s2OperationMode // modes[0] is Standby, followed by ascending steps
	minOnID   string
	minOffID  string
	signature string
}

func buildS2ChargeModel(resourceID string, phases int, minCurrent, maxCurrent, maxChargePower float64) s2ChargeModel {
	if phases < 1 || phases > s2DefaultMaxPhase {
		phases = s2DefaultEVPhases
	}
	minA := int(math.Ceil(minCurrent - 1e-9))
	if minA <= 0 {
		minA = s2DefaultMinA
	}
	maxA := int(math.Floor(maxCurrent + 1e-9))
	if maxChargePower > 0 {
		powerCapA := int(math.Floor(maxChargePower/(float64(phases)*s2NominalVoltage) + 1e-9))
		if powerCapA < maxA {
			maxA = powerCapA
		}
	}
	if maxA < minA {
		maxA = minA
	}

	step := 1
	for stepCount(minA, maxA, step) > s2MaxChargeSteps {
		step++
	}
	currents := make([]int, 0, stepCount(minA, maxA, step))
	for c := minA; c < maxA; c += step {
		currents = append(currents, c)
	}
	currents = append(currents, maxA)

	model := s2ChargeModel{
		phases:   phases,
		minOnID:  deterministicUUID(resourceID + ":ombc-timer-min-on"),
		minOffID: deterministicUUID(resourceID + ":ombc-timer-min-off"),
	}
	model.modes = append(model.modes, s2OperationMode{
		id:    deterministicUUID(resourceID + ":ombc-standby"),
		label: s2StandbyLabel,
	})
	labels := make([]string, 0, len(currents))
	for _, c := range currents {
		model.modes = append(model.modes, s2OperationMode{
			id:       deterministicUUID(fmt.Sprintf("%s:ombc-charge-%dA", resourceID, c)),
			label:    fmt.Sprintf("%dA", c),
			currentA: c,
		})
		labels = append(labels, fmt.Sprintf("%d", c))
	}
	model.signature = fmt.Sprintf("phases=%d steps=%s", phases, strings.Join(labels, ","))
	return model
}

func stepCount(minA, maxA, step int) int {
	return (maxA-minA+step-1)/step + 1
}

func (m s2ChargeModel) standby() s2OperationMode { return m.modes[0] }

func (m s2ChargeModel) find(id string) (s2OperationMode, bool) {
	for _, mode := range m.modes {
		if mode.id == id {
			return mode, true
		}
	}
	return s2OperationMode{}, false
}

// nearestStep returns the charge step closest to currentA; ties resolve to the
// lower step.
func (m s2ChargeModel) nearestStep(currentA float64) s2OperationMode {
	best := m.modes[1]
	bestDelta := math.Abs(float64(best.currentA) - currentA)
	for _, mode := range m.modes[2:] {
		delta := math.Abs(float64(mode.currentA) - currentA)
		if delta < bestDelta {
			best = mode
			bestDelta = delta
		}
	}
	return best
}

func (m s2ChargeModel) powerRanges(mode s2OperationMode) []map[string]any {
	power := float64(mode.currentA) * s2NominalVoltage
	ranges := make([]map[string]any, 0, m.phases)
	for phase := 1; phase <= m.phases; phase++ {
		ranges = append(ranges, map[string]any{
			"commodity_quantity": fmt.Sprintf("ELECTRIC.POWER.L%d", phase),
			"start_of_range":     power,
			"end_of_range":       power,
		})
	}
	return ranges
}

// transitionTimers returns the timers started and the timers blocking a
// transition between two modes. Charge steps can change freely among each
// other; switching charging on or off is protected by min-on/min-off timers.
func (m s2ChargeModel) transitionTimers(from, to s2OperationMode) (start, blocking []string) {
	switch {
	case from.isStandby() && !to.isStandby():
		return []string{m.minOnID}, []string{m.minOffID}
	case !from.isStandby() && to.isStandby():
		return []string{m.minOffID}, []string{m.minOnID}
	default:
		return []string{}, []string{}
	}
}

func (m s2ChargeModel) systemDescription(resourceID string, minOnMs, minOffMs int64) map[string]any {
	modes := make([]map[string]any, 0, len(m.modes))
	for _, mode := range m.modes {
		modes = append(modes, map[string]any{
			"id":                      mode.id,
			"diagnostic_label":        mode.label,
			"power_ranges":            m.powerRanges(mode),
			"abnormal_condition_only": false,
		})
	}
	transitions := make([]map[string]any, 0, len(m.modes)*(len(m.modes)-1))
	for _, from := range m.modes {
		for _, to := range m.modes {
			if from.id == to.id {
				continue
			}
			start, blocking := m.transitionTimers(from, to)
			transitions = append(transitions, map[string]any{
				"id":                      deterministicUUID(resourceID + ":ombc-transition:" + from.id + ":" + to.id),
				"from":                    from.id,
				"to":                      to.id,
				"start_timers":            start,
				"blocking_timers":         blocking,
				"abnormal_condition_only": false,
			})
		}
	}
	return map[string]any{
		"message_type":    "OMBC.SystemDescription",
		"message_id":      newMessageID(),
		"valid_from":      nowRFC3339(),
		"operation_modes": modes,
		"transitions":     transitions,
		"timers": []map[string]any{
			{"id": m.minOnID, "diagnostic_label": "min-on", "duration": minOnMs},
			{"id": m.minOffID, "diagnostic_label": "min-off", "duration": minOffMs},
		},
	}
}
