package core

// Custom extension: the loadpoint's side of load management. See core/lm and
// core/site_lm.go.

import (
	"sync"
	"time"

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
// priorities of package lm on top. A protected loadpoint that had to be shed
// stays off for the guard time set in the ui.
func (lp *Loadpoint) lmLimit(current float64) float64 {
	now := lp.clock.Now()

	if lm.Guarded(lp, now) > 0 {
		// asking for nothing while held off, lower priority loads may use the power
		lm.Forget(lp)
		return 0
	}

	var limited, requested, allowed float64
	var shed bool

	switchDevice := lp.chargerHasFeature(api.SwitchDevice)
	if switchDevice {
		limited, requested, allowed = lp.lmSwitchLimit(current)
		shed = current > 0 && limited == 0
	} else {
		limited, requested, allowed = lp.lmCurrentLimit(current)
		minCurrent := lp.effectiveMinCurrent()
		shed = current >= minCurrent && limited < minCurrent
	}

	// for the overview in the ui, see core/site_lm_status.go
	running := lp.enabled && !shed
	if lm.Record(lp, requested, allowed, running, now) && !switchDevice {
		lm.AddEvent(lm.Event{At: now, Type: lm.EventThrottled, Load: lp.GetTitle(), A: requested, B: allowed})
	}

	// only switching off a running load counts, not one that could not start
	if shed && lp.enabled {
		d := lm.Shed(lp, now)
		if d > 0 {
			lp.log.INFO.Printf("shed by load management, stays off for %s", d.Round(time.Second))
		}
		lm.AddEvent(lm.Event{At: now, Type: lm.EventShed, Load: lp.GetTitle(), A: d.Minutes(), B: lp.circuit.GetChargePower()})
	}

	return limited
}

// lmCurrentLimit caps a current controlled loadpoint. It also returns the power
// requested and allowed.
func (lp *Loadpoint) lmCurrentLimit(current float64) (limited, requested, allowed float64) {
	currentLimit := lm.ValidateCurrent(lp, lp.circuit, lp.actualMaxChargeCurrent(), current)

	activePhases := lp.ActivePhases()
	requested = currentToPower(current, activePhases)
	powerLimit := lm.ValidatePower(lp, lp.circuit, lp.chargePower, requested)
	currentLimitViaPower := powerToCurrent(powerLimit, activePhases)

	limited = lp.roundedCurrent(min(currentLimit, currentLimitViaPower))
	if minCurrent := lp.effectiveMinCurrent(); limited < minCurrent && current >= minCurrent {
		lp.log.DEBUG.Printf("circuit limit %.3gA below min current %.3gA", limited, minCurrent)
	}

	allowed = min(powerLimit, currentToPower(currentLimit, activePhases))

	return limited, requested, allowed
}

// lmSwitchLimit handles a switch device, which draws its full power or nothing:
// its current cannot be limited, so a partial budget is no budget. Upstream
// would switch a 3 kW heater on with 1.6 kW to spare because 1.6 kW is still
// above the minimum current, and the circuit then stays overloaded.
// It also returns the power requested and allowed.
func (lp *Loadpoint) lmSwitchLimit(current float64) (limited, requested, allowed float64) {
	if current <= 0 {
		// staying off asks for nothing, which also clears an earlier demand
		lm.ValidatePower(lp, lp.circuit, lp.chargePower, 0)
		return current, 0, 0
	}

	need := lp.lmSwitchPower()

	if allowed := lm.ValidatePower(lp, lp.circuit, lp.chargePower, need); allowed < need {
		// only worth a line when it actually switches off, not on every cycle it stays off
		if lp.enabled {
			lp.log.DEBUG.Printf("circuit allows %.0fW, switch needs %.0fW: off", allowed, need)
		}
		return 0, need, allowed
	}

	return current, need, need
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
