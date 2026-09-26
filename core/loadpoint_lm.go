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

// LmPriority returns the loadpoint's load management shed priority: its upstream
// priority, which also ranks pv surplus and the planner, see
// core/site_lm_planner.go. Lower is shed first, equal priorities are upstream's
// first come, first served behaviour. The yaml `lmpriority` is only taken over
// once into the priority.
func (lp *Loadpoint) LmPriority() int {
	return lp.EffectivePriority()
}

// lmCircuit is the loadpoint's circuit as setLimit sees it. setLimit keeps
// upstream's calculation; the requests pass the priorities of package lm, a
// switch device asks for its whole power or nothing, and a protected loadpoint
// held off after a shed gets nothing. It lives for one setLimit call.
type lmCircuit struct {
	api.Circuit
	lp  *Loadpoint
	now time.Time

	guarded, switchDevice bool

	// what was asked and allowed, for the overview
	requested, allowedPower, allowedCurrent float64
}

// lmCircuit returns the circuit for one setLimit call
func (lp *Loadpoint) lmCircuit() *lmCircuit {
	c := &lmCircuit{
		Circuit:      lp.circuit,
		lp:           lp,
		now:          lp.clock.Now(),
		switchDevice: lp.chargerHasFeature(api.SwitchDevice),
	}

	if lm.Guarded(lp, c.now) > 0 {
		// asking for nothing while held off, lower priority loads may use the power
		lm.Forget(lp)
		c.guarded = true
	}

	return c
}

// ValidateCurrent caps the current with the priorities on top. A switch device
// cannot be limited, its power decides alone.
func (c *lmCircuit) ValidateCurrent(old, new float64) float64 {
	switch {
	case c.guarded:
		return 0
	case c.switchDevice:
		return new
	}

	c.allowedCurrent = lm.ValidateCurrent(c.lp, c.Circuit, old, new)
	return c.allowedCurrent
}

// ValidatePower caps the power with the priorities on top. A switch device draws
// its full power or nothing, so a partial budget is no budget: upstream would
// switch a 3 kW heater on with 1.6 kW to spare, as that is still above the
// minimum current, and the circuit would then stay overloaded.
func (c *lmCircuit) ValidatePower(old, new float64) float64 {
	switch {
	case c.guarded:
		return 0

	case !c.switchDevice:
		c.requested = new
		c.allowedPower = lm.ValidatePower(c.lp, c.Circuit, old, new)
		return c.allowedPower

	case new <= 0:
		// staying off asks for nothing, which also clears an earlier demand
		lm.ValidatePower(c.lp, c.Circuit, old, 0)
		return new
	}

	c.requested = c.lp.lmSwitchPower()
	c.allowedPower = lm.ValidatePower(c.lp, c.Circuit, old, c.requested)

	if c.allowedPower < c.requested {
		// only worth a line when it actually switches off, not on every cycle it stays off
		if c.lp.enabled {
			c.lp.log.DEBUG.Printf("circuit allows %.0fW, switch needs %.0fW: off", c.allowedPower, c.requested)
		}
		return 0
	}

	return new
}

// done records the result of setLimit's circuit check for the overview and
// starts the shed guard when a running load was switched off
func (c *lmCircuit) done(current, limited float64) {
	if c.guarded {
		return
	}

	lp := c.lp
	allowed := c.allowedPower

	var shed bool
	if c.switchDevice {
		shed = current > 0 && limited == 0
	} else {
		allowed = min(allowed, currentToPower(c.allowedCurrent, lp.ActivePhases()))
		minCurrent := lp.effectiveMinCurrent()
		shed = current >= minCurrent && limited < minCurrent
	}

	// for the overview in the ui, see core/site_lm_status.go
	running := lp.enabled && !shed
	if lm.Record(lp, c.requested, allowed, running, c.now) && !c.switchDevice {
		lm.AddEvent(lm.Event{At: c.now, Type: lm.EventThrottled, Load: lp.GetTitle(), A: c.requested, B: allowed})
	}

	// only switching off a running load counts, not one that could not start
	if shed && lp.enabled {
		d := lm.Shed(lp, c.now)
		if d > 0 {
			lp.log.INFO.Printf("shed by load management, stays off for %s", d.Round(time.Second))
		}
		lm.AddEvent(lm.Event{At: c.now, Type: lm.EventShed, Load: lp.GetTitle(), A: d.Minutes(), B: c.GetChargePower()})
	}
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
