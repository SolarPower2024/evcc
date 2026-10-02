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

// loadpointCustom is the fork's state of a loadpoint, one embedded field in
// upstream's Loadpoint
type loadpointCustom struct {
	phaseSwitchSettings // 1p currents and phase delays, see core/loadpoint_phasecurrents.go

	lmOwn *lm.Manager // load management of a loadpoint without a site (tests)

	switchMu    sync.Mutex
	switchPower float64 // switch power last measured while on
}

// lmm returns the load management of the loadpoint's site. A loadpoint
// without a site, in tests, keeps one of its own.
func (lp *Loadpoint) lmm() *lm.Manager {
	if s, ok := lp.site.(*Site); ok && s != nil {
		return s.lmm()
	}
	if lp.lmOwn == nil {
		lp.lmOwn = lm.New()
	}
	return lp.lmOwn
}

// LmPriority returns the loadpoint's load management shed priority: its upstream
// priority, which also ranks pv surplus, see core/site_lm_priority.go. Lower is
// shed first, equal priorities are upstream's first come, first served
// behaviour.
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

	if lp.lmm().Guarded(lp, c.now) > 0 {
		// asking for nothing while held off, lower priority loads may use the power
		lp.lmm().Forget(lp)
		c.guarded = true
	}

	return c
}

// ValidateCurrent caps the current with the priorities on top. A switch device
// cannot be limited: it asks for the current its power draws on its phases and
// gets all or nothing, as for the power below.
func (c *lmCircuit) ValidateCurrent(old, new float64) float64 {
	switch {
	case c.guarded:
		return 0

	case !c.switchDevice:
		c.allowedCurrent = c.lp.lmm().ValidateCurrent(c.lp, c.Circuit, old, new)
		return c.allowedCurrent

	case new <= 0:
		// staying off asks for nothing, which also clears an earlier demand
		c.lp.lmm().ValidateCurrent(c.lp, c.Circuit, old, 0)
		return new
	}

	// what the switch draws, not the nominal current offered to it
	phases := c.lp.ActivePhases()
	old = powerToCurrent(c.lp.chargePower, phases)
	need := powerToCurrent(c.lp.lmSwitchPower(), phases)

	if allowed := c.lp.lmm().ValidateCurrent(c.lp, c.Circuit, old, need); allowed < need {
		if c.lp.enabled {
			c.lp.log.DEBUG.Printf("circuit allows %.3gA, switch needs %.3gA: off", allowed, need)
		}
		return 0
	}

	return new
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
		c.allowedPower = c.lp.lmm().ValidatePower(c.lp, c.Circuit, old, new)
		return c.allowedPower

	case new <= 0:
		// staying off asks for nothing, which also clears an earlier demand
		c.lp.lmm().ValidatePower(c.lp, c.Circuit, old, 0)
		return new
	}

	c.requested = c.lp.lmSwitchPower()
	c.allowedPower = c.lp.lmm().ValidatePower(c.lp, c.Circuit, old, c.requested)

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
	if c.lp.lmm().Record(lp, c.requested, allowed, running, c.now) && !c.switchDevice {
		c.lp.lmm().AddEvent(lm.Event{At: c.now, Type: lm.EventThrottled, Load: lp.GetTitle(), A: c.requested, B: allowed})
	}

	// only switching off a running load counts, not one that could not start
	if shed && lp.enabled {
		d := c.lp.lmm().Shed(lp, c.now)
		if d > 0 {
			lp.log.INFO.Printf("shed by load management, stays off for %s", d.Round(time.Second))
		}
		c.lp.lmm().AddEvent(lm.Event{At: c.now, Type: lm.EventShed, Load: lp.GetTitle(), A: d.Minutes(), B: c.GetChargePower()})
	}
}

// ratedPower is implemented by switch devices with a configured power
type ratedPower interface {
	RatedPower() float64
}

// lmSwitchPower returns what the switch draws when on: the measurement while it
// draws, else the configured power, else the last measurement, else the
// nominal maximum current.
func (lp *Loadpoint) lmSwitchPower() float64 {
	lp.switchMu.Lock()
	defer lp.switchMu.Unlock()

	if lp.chargePower > 0 {
		lp.switchPower = lp.chargePower
		return lp.chargePower
	}

	if c, ok := api.Cap[ratedPower](lp.charger); ok {
		if p := c.RatedPower(); p > 0 {
			return p
		}
	}

	if p := lp.switchPower; p > 0 {
		return p
	}

	return currentToPower(lp.effectiveMaxCurrent(), 1)
}
