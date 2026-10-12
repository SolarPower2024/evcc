package core

import (
	"time"

	"github.com/evcc-io/evcc/core/keys"
)

// Custom extension: tells the ui that the charger switches a heater in stages,
// see charger/switchstages.go. Its mode buttons then read Off/Smart/On as for
// upstream's switch devices, without the switch device feature, which would
// drop the current settings and change load management.
//
// Without a power sensor the heater's power is an assumption (stages switched
// on times the stage power), while its own thermostat may have cut out. Control,
// circuits and load management keep using it. The home consumption, the energy
// flow, the energy history and the sessions count only measured power, so the
// heater's real draw shows up in the home consumption instead. The energy flow
// leaves such a loadpoint out.

// stagedCharger is implemented by charger.SwitchStages
type stagedCharger interface {
	Stages() int
}

// powerEstimator is implemented by charger.SwitchStages
type powerEstimator interface {
	PowerEstimated() bool
}

// publishStages publishes if the charger switches in stages and if its power is assumed
func (lp *Loadpoint) publishStages() {
	_, ok := lp.charger.(stagedCharger)
	lp.publish(keys.ChargerStages, ok)
	lp.publish(keys.ChargePowerEstimated, lp.powerEstimated())
}

// powerEstimated reports if the charge power is assumed rather than measured:
// the charger is its own meter and estimates. A loadpoint meter measures.
func (lp *Loadpoint) powerEstimated() bool {
	if lp.chargeMeter == nil {
		return false
	}
	pe, ok := lp.chargeMeter.source.(powerEstimator)
	return ok && pe.PowerEstimated()
}

// meteredPower returns the power for home consumption, energy and sessions: 0
// for an assumed power
func (lp *Loadpoint) meteredPower(power float64) float64 {
	if lp.powerEstimated() {
		return 0
	}
	return power
}

// publishChargePower publishes the measured charge power, an assumed one apart
func (lp *Loadpoint) publishChargePower(power float64) {
	var estimate float64
	if lp.powerEstimated() {
		power, estimate = 0, power
	}
	lp.publish(keys.ChargePower, power)
	lp.publish(keys.ChargePowerEstimate, estimate)
}

// stageRunner is implemented by charger.SwitchStages
type stageRunner interface {
	RunningCurrent(phases int) float64
}

// holdStage keeps a heater in stages on the stages it runs while the pv surplus
// falls short of them, as a charger that switches off below its minimum waits
// for the loadpoint's disable threshold and delay. The charger itself steps down
// at once, so a surplus hovering around a stage boundary would toggle the
// relays: the stage goes only once the grid import has stayed at the disable
// threshold for the disable delay. Imports below the threshold are tolerated.
// The circuit limit in setLimit applies afterwards, at once.
func (lp *Loadpoint) holdStage(target, sitePower, threshold float64) float64 {
	c, ok := lp.charger.(stageRunner)
	if !ok {
		return target
	}

	running := c.RunningCurrent(lp.ActivePhases())
	if !lp.enabled || target+1e-6 >= running {
		lp.stageTimer = time.Time{}
		return target
	}

	if sitePower < threshold {
		lp.stageTimer = time.Time{}
		return running
	}

	now, delay := lp.clock.Now(), lp.GetDisableDelay()
	if lp.stageTimer.IsZero() {
		lp.log.DEBUG.Printf("stages short of %.3gA: %.0fW >= %.0fW disable threshold, down in %v", running, sitePower, threshold, delay)
		lp.stageTimer = now
	}

	if now.Sub(lp.stageTimer) < delay {
		return running
	}

	lp.log.DEBUG.Println("stages down: disable delay elapsed")
	lp.stageTimer = time.Time{}

	return target
}

// holdRunningStages keeps a heater in stages on the stages it runs while the pv
// surplus falls short of its first stage and the loadpoint's disable timer runs.
// evcc would return the minimum, one stage, and drop the heater at once; the
// timer decides, and when it has elapsed evcc switches the heater off. An import
// below the disable threshold resets that timer and is tolerated, as in
// holdStage. The circuit limit in setLimit applies afterwards, at once.
func (lp *Loadpoint) holdRunningStages(minCurrent, maxCurrent float64) float64 {
	c, ok := lp.charger.(stageRunner)
	if !ok {
		return minCurrent
	}

	// the disable timer takes over from the stage timer
	lp.stageTimer = time.Time{}

	return max(minCurrent, min(c.RunningCurrent(lp.ActivePhases()), maxCurrent))
}

// stagePhaser is implemented by charger.SwitchStages
type stagePhaser interface {
	StagePhaseCurrent() (float64, bool)
}

// stagePhaseCurrent returns the current one stage of a heater draws on its phase,
// 0 unless the heater has one stage per phase
func (lp *Loadpoint) stagePhaseCurrent() float64 {
	if c, ok := lp.charger.(stagePhaser); ok {
		if current, ok := c.StagePhaseCurrent(); ok {
			return current
		}
	}
	return 0
}
