package core

// Custom extension: the loadpoint's side of load management. See core/lm and
// core/site_lm.go.

import (
	"sync"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/lm"
)

var _ lm.Load = (*Loadpoint)(nil)

// LmPriority returns the loadpoint's load management shed priority, configured
// as `lmpriority`. Lower is shed first, the default 0 puts every loadpoint on
// the same level, which is upstream's first come, first served behaviour.
//
// This is deliberately not the loadpoint's `priority`: that one distributes pv
// surplus, where the answer to "who goes first" is usually the opposite of what
// it should be when a fuse forces a load to be dropped.
func (lp *Loadpoint) LmPriority() int {
	return lp.LmPrio
}

// lmLimit caps the requested current against the loadpoint's circuit, with the
// priorities of package lm on top
func (lp *Loadpoint) lmLimit(current float64) float64 {
	if lp.chargerHasFeature(api.SwitchDevice) {
		return lp.lmSwitchLimit(current)
	}

	currentLimit := lm.ValidateCurrent(lp, lp.circuit, lp.actualMaxChargeCurrent(), current)

	activePhases := lp.ActivePhases()
	powerLimit := lm.ValidatePower(lp, lp.circuit, lp.chargePower, currentToPower(current, activePhases))
	currentLimitViaPower := powerToCurrent(powerLimit, activePhases)

	limited := lp.roundedCurrent(min(currentLimit, currentLimitViaPower))
	if minCurrent := lp.effectiveMinCurrent(); limited < minCurrent && current >= minCurrent {
		lp.log.DEBUG.Printf("circuit limit %.3gA below min current %.3gA", limited, minCurrent)
	}

	return limited
}

// lmSwitchLimit handles a switch device, which draws its full power or nothing:
// its current cannot be limited, so a partial budget is no budget. Upstream
// would switch a 3 kW heater on with 1.6 kW to spare because 1.6 kW is still
// above the minimum current, and the circuit then stays overloaded.
func (lp *Loadpoint) lmSwitchLimit(current float64) float64 {
	if current <= 0 {
		// staying off asks for nothing, which also clears an earlier demand
		lm.ValidatePower(lp, lp.circuit, lp.chargePower, 0)
		return current
	}

	need := lp.lmSwitchPower()

	if allowed := lm.ValidatePower(lp, lp.circuit, lp.chargePower, need); allowed < need {
		// only worth a line when it actually switches off, not on every cycle it stays off
		if lp.enabled {
			lp.log.DEBUG.Printf("circuit allows %.0fW, switch needs %.0fW: off", allowed, need)
		}
		return 0
	}

	return current
}

// ratedPower is implemented by switch devices with a configured power
type ratedPower interface {
	RatedPower() float64
}

// switch power last measured while on, by loadpoint
var (
	switchMu    sync.Mutex
	switchPower = make(map[*Loadpoint]float64)
)

// lmSwitchPower returns what the switch draws when on: the measurement while it
// draws, else the configured power, else the last measurement, else the
// nominal maximum current.
func (lp *Loadpoint) lmSwitchPower() float64 {
	switchMu.Lock()
	defer switchMu.Unlock()

	if lp.chargePower > 0 {
		switchPower[lp] = lp.chargePower
		return lp.chargePower
	}

	if c, ok := api.Cap[ratedPower](lp.charger); ok {
		if p := c.RatedPower(); p > 0 {
			return p
		}
	}

	if p := switchPower[lp]; p > 0 {
		return p
	}

	return currentToPower(lp.effectiveMaxCurrent(), 1)
}
