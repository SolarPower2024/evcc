package core

// Custom extension: separate current limits for 1p charging on loadpoints with
// phase switching. The regular min and max current stay the 3p limits, the 1p
// values only apply while charging on one phase, 0 = use the regular value.
//
// Phase scaling uses both: scaling up to 3p needs the 1p maximum exhausted and
// the 3p minimum reached, scaling down happens below the 3p minimum. After a
// switch the limits of the new phase count apply right away.
//
// Built after evcc PR 32505, with the same names, settings keys and config
// fields, so an upstream version can take over the values. Without phase
// switching the regular limits already are the 1p limits and the 1p values are
// ignored.

import (
	"cmp"
	"errors"

	"github.com/evcc-io/evcc/core/keys"
)

// phaseCurrents1p holds the 1p current limits, embedded in Loadpoint
type phaseCurrents1p struct {
	minCurrent1p float64 // 1p override for minCurrent, 0 = use minCurrent
	maxCurrent1p float64 // 1p override for maxCurrent, 0 = use maxCurrent
}

// restoreCurrents1p restores the 1p limits from the settings
func (lp *Loadpoint) restoreCurrents1p() {
	if v, err := lp.settings.Float(keys.MinCurrent1p); err == nil && v > 0 {
		lp.minCurrent1p = v
	}
	if v, err := lp.settings.Float(keys.MaxCurrent1p); err == nil && v > 0 {
		lp.maxCurrent1p = v
	}
}

// publishCurrents1p publishes the 1p limits
func (lp *Loadpoint) publishCurrents1p() {
	minCurrent, maxCurrent := lp.GetCurrents1p()
	lp.publish(keys.MinCurrent1p, minCurrent)
	lp.publish(keys.MaxCurrent1p, maxCurrent)
}

// currents1pPhases returns the active phases if 1p limits are set, else 0. Without
// them nothing is looked up and the regular limits apply as upstream.
func (lp *Loadpoint) currents1pPhases() int {
	if (lp.minCurrent1p > 0 || lp.maxCurrent1p > 0) && lp.hasPhaseSwitching() {
		return lp.activePhases()
	}
	return 0
}

// uses1pCurrent returns true if the 1p limits apply for the given phases
func (lp *Loadpoint) uses1pCurrent(phases int) bool {
	return phases == 1 && lp.hasPhaseSwitching()
}

// getMinCurrentFor returns the configured min current for the given phases
func (lp *Loadpoint) getMinCurrentFor(phases int) float64 {
	if lp.uses1pCurrent(phases) && lp.minCurrent1p > 0 {
		return lp.minCurrent1p
	}
	return lp.getMinCurrent()
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
