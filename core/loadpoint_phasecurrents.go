package core

// Custom extension: phase switching settings of loadpoints with 1p/3p switching.
//
// Separate current limits for 1p charging: the regular min and max current stay
// the 3p limits, the 1p values only apply while charging on one phase, 0 = use
// the regular value. Phase scaling uses both: scaling up to 3p needs the 1p
// maximum exhausted and the 3p minimum reached, scaling down happens below the
// 3p minimum. After a switch the limits of the new phase count apply right away.
// On 1p the min never exceeds the 1p max, see getMinCurrentFor.
// Built after evcc PR 32505, with the same names, settings keys and config
// fields, so an upstream version can take over the values. Without phase
// switching the regular limits already are the 1p limits and the 1p values are
// ignored.
//
// Separate delays for switching phases: how long the surplus has to allow 3p
// before scaling up, and how long it has to be short of the 3p minimum before
// scaling down. 0 = the enable and disable delay, as upstream. Starting and
// stopping charging keep the enable and disable delay.

import (
	"cmp"
	"errors"
	"sync/atomic"
	"time"

	"github.com/evcc-io/evcc/core/keys"
)

// phaseSwitchSettings holds the phase switching settings, embedded in Loadpoint
type phaseSwitchSettings struct {
	minCurrent1p      float64       // 1p override for minCurrent, 0 = use minCurrent
	maxCurrent1p      float64       // 1p override for maxCurrent, 0 = use maxCurrent
	phaseScale3pDelay time.Duration // delay before scaling up, 0 = enable delay
	phaseScale1pDelay time.Duration // delay before scaling down, 0 = disable delay

	currents1pConflict atomic.Bool // the 1p min exceeds the 1p max, warned once
}

// restorePhaseSwitch restores the phase switching settings
func (lp *Loadpoint) restorePhaseSwitch() {
	if v, err := lp.settings.Float(keys.MinCurrent1p); err == nil && v > 0 {
		lp.minCurrent1p = v
	}
	if v, err := lp.settings.Float(keys.MaxCurrent1p); err == nil && v > 0 {
		lp.maxCurrent1p = v
	}
	if v, err := lp.settings.Int(keys.PhaseScale3pDelay); err == nil && v > 0 {
		lp.phaseScale3pDelay = time.Duration(v)
	}
	if v, err := lp.settings.Int(keys.PhaseScale1pDelay); err == nil && v > 0 {
		lp.phaseScale1pDelay = time.Duration(v)
	}
}

// publishPhaseSwitch publishes the phase switching settings
func (lp *Loadpoint) publishPhaseSwitch() {
	minCurrent, maxCurrent := lp.GetCurrents1p()
	lp.publish(keys.MinCurrent1p, minCurrent)
	lp.publish(keys.MaxCurrent1p, maxCurrent)

	up, down := lp.GetPhaseDelays()
	lp.publish(keys.PhaseScale3pDelay, up)
	lp.publish(keys.PhaseScale1pDelay, down)
}

// currents1pPhases returns the active phases if 1p limits are set, else 0. Without
// them nothing is looked up and the regular limits apply as upstream.
func (lp *Loadpoint) currents1pPhases() int {
	if (lp.minCurrent1p > 0 || lp.maxCurrent1p > 0) && lp.hasPhaseSwitching() {
		return lp.activePhases()
	}
	return 0
}

// min1pCurrentOr returns the effective 1p min current, or minCurrent as upstream
// when no 1p limits are set (then nothing else is looked up)
func (lp *Loadpoint) min1pCurrentOr(minCurrent float64) float64 {
	if lp.minCurrent1p == 0 && lp.maxCurrent1p == 0 {
		return minCurrent
	}
	return lp.effectiveMinCurrentFor(1)
}

// minCurrentScaleUp returns the current set before scaling up: the min of the
// new phase count, but never below the active min, which setLimit refuses
func (lp *Loadpoint) minCurrentScaleUp(phases int) float64 {
	return max(lp.effectiveMinCurrentFor(phases), lp.effectiveMinCurrent())
}

// uses1pCurrent returns true if the 1p limits apply for the given phases
func (lp *Loadpoint) uses1pCurrent(phases int) bool {
	return phases == 1 && lp.hasPhaseSwitching()
}

// getMinCurrentFor returns the configured min current for the given phases.
// On 1p it never exceeds the 1p max: a min or max current changed after the 1p
// values were set is not checked against them, and the 1p max usually is a
// wiring or unbalanced load limit, so it wins over the min.
func (lp *Loadpoint) getMinCurrentFor(phases int) float64 {
	// without 1p values the regular limits apply unchanged, as upstream
	if !lp.uses1pCurrent(phases) || lp.minCurrent1p == 0 && lp.maxCurrent1p == 0 {
		return lp.getMinCurrent()
	}

	minCurrent := cmp.Or(lp.minCurrent1p, lp.getMinCurrent())
	maxCurrent := lp.getMaxCurrentFor(phases)

	if minCurrent <= maxCurrent || maxCurrent <= 0 {
		lp.currents1pConflict.Store(false)
		return minCurrent
	}

	if !lp.currents1pConflict.Swap(true) {
		lp.log.WARN.Printf("1p min current %.3gA exceeds 1p max current %.3gA, charging at %.3gA on 1p", minCurrent, maxCurrent, maxCurrent)
	}

	return maxCurrent
}

// getMaxCurrentFor returns the configured max current for the given phases
func (lp *Loadpoint) getMaxCurrentFor(phases int) float64 {
	if lp.uses1pCurrent(phases) && lp.maxCurrent1p > 0 {
		return lp.maxCurrent1p
	}
	return lp.getMaxCurrent()
}

// GetCurrents1p returns the 1p min/max current (0 = use min/max current)
func (lp *Loadpoint) GetCurrents1p() (float64, float64) {
	lp.RLock()
	defer lp.RUnlock()
	return lp.minCurrent1p, lp.maxCurrent1p
}

// SetCurrents1p sets the 1p min/max current (0 = use min/max current)
func (lp *Loadpoint) SetCurrents1p(minCurrent, maxCurrent float64) error {
	lp.Lock()
	defer lp.Unlock()

	if minCurrent < 0 || maxCurrent < 0 {
		return errors.New("current must not be negative")
	}

	// unset values fall back to the regular limits
	if cmp.Or(minCurrent, lp.minCurrent) > cmp.Or(maxCurrent, lp.maxCurrent) {
		return errors.New("1p min current must be smaller or equal than 1p max current")
	}

	if minCurrent != lp.minCurrent1p {
		lp.log.DEBUG.Println("set 1p min current:", minCurrent)
		lp.minCurrent1p = minCurrent
		lp.publish(keys.MinCurrent1p, minCurrent)
		lp.settings.SetFloat(keys.MinCurrent1p, minCurrent)
	}

	if maxCurrent != lp.maxCurrent1p {
		lp.log.DEBUG.Println("set 1p max current:", maxCurrent)
		lp.maxCurrent1p = maxCurrent
		lp.publish(keys.MaxCurrent1p, maxCurrent)
		lp.settings.SetFloat(keys.MaxCurrent1p, maxCurrent)
	}

	return nil
}

// projectPhaseSwitch1p extends evcc's projectPhaseSwitch by the 1p minimum: after a
// pending scale down to 1p the loadpoint runs at the 1p minimum, which is returned too
func (lp *Loadpoint) projectPhaseSwitch1p(sitePower, minCurrent float64) (float64, int, float64) {
	sitePower, phases := lp.projectPhaseSwitch(sitePower, minCurrent)
	if lp.hasPhaseSwitching() && !lp.phaseTimer.IsZero() {
		if min1p := lp.effectiveMinCurrentFor(1); min1p != minCurrent {
			sitePower -= Voltage * (minCurrent - min1p)
			minCurrent = min1p
		}
	}
	return sitePower, phases, minCurrent
}

// phaseScaleDelay returns the delay before switching to the given phases: the
// phase delay if set, else the enable delay for 3p and the disable delay for 1p
func (lp *Loadpoint) phaseScaleDelay(phases int) time.Duration {
	up, down := lp.GetPhaseDelays()

	if phases == 1 {
		if down > 0 {
			return down
		}
		return lp.GetDisableDelay()
	}

	if up > 0 {
		return up
	}
	return lp.GetEnableDelay()
}

// GetPhaseDelays returns the delays before scaling up and down (0 = enable/disable delay)
func (lp *Loadpoint) GetPhaseDelays() (time.Duration, time.Duration) {
	lp.RLock()
	defer lp.RUnlock()
	return lp.phaseScale3pDelay, lp.phaseScale1pDelay
}

// SetPhaseDelays sets the delays before scaling up and down (0 = enable/disable delay)
func (lp *Loadpoint) SetPhaseDelays(up, down time.Duration) error {
	lp.Lock()
	defer lp.Unlock()

	if up < 0 || down < 0 {
		return errors.New("delay must not be negative")
	}

	if up != lp.phaseScale3pDelay {
		lp.log.DEBUG.Println("set phase scale 3p delay:", up)
		lp.phaseScale3pDelay = up
		lp.publish(keys.PhaseScale3pDelay, up)
		lp.settings.SetInt(keys.PhaseScale3pDelay, int64(up))
	}

	if down != lp.phaseScale1pDelay {
		lp.log.DEBUG.Println("set phase scale 1p delay:", down)
		lp.phaseScale1pDelay = down
		lp.publish(keys.PhaseScale1pDelay, down)
		lp.settings.SetInt(keys.PhaseScale1pDelay, int64(down))
	}

	return nil
}
