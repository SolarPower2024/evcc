package core

// Custom extension: the optimizer's hold on a Marstek.
//
// evcc writes no discharge power to a Marstek, Omnibattery regulates it. While
// the optimizer in automatic mode holds the battery (see site_optimizer_gate.go)
// and peak shaving is on, the soc threshold of Omnibattery's peak shaving goes to
// its maximum: below the threshold Omnibattery only discharges for a peak above
// the limit and still charges from pv surplus. The value before is kept,
// persisted, and written back when peak shaving is released (switched off, type
// or entity changed, shutdown, or at the next start after a crash). While peak
// shaving stays on, the end of the hold writes the reserve as every cycle does.
// Without peak shaving nothing here applies, the hold is evcc's battery mode.

import (
	"strconv"

	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/db/settings"
)

// omniHoldThreshold is the soc threshold written while the optimizer holds
const omniHoldThreshold = 100.0

// omniThreshold returns the soc threshold to write: the reserve, or the maximum
// while the optimizer holds. Before raising it the first time it reads and keeps
// the value in the entity. ok is false if that read failed, the next cycle tries
// again. Called with s.out held.
func (site *Site) omniThreshold(reserve float64) (threshold float64, hold, ok bool) {
	if !site.optimizerHolds() {
		return reserve, false, true
	}

	s := site.peak()

	s.mu.Lock()
	raised, entity := s.holdSoc > 0, s.protSoc
	s.mu.Unlock()

	if !raised {
		conn, err := site.haConnection()
		if !site.logWrite("marstek connect", "connect", err) {
			return 0, true, false
		}

		_, state, err := numberState(conn, entity)
		if !site.logWrite("peak shaving soc threshold read", "read "+entity, err) {
			return 0, true, false
		}

		// nothing sensible to give back: the reserve, which every cycle writes
		before, err := strconv.ParseFloat(state, 64)
		if err != nil || before <= 0 {
			before = reserve
		}

		site.setHoldSoc(before)
		site.log.DEBUG.Printf("peak shaving soc threshold: optimizer holds, %.0f%% kept", before)
	}

	return omniHoldThreshold, true, true
}

// setHoldSoc keeps the threshold before the hold raised it, 0 = not raised
func (site *Site) setHoldSoc(soc float64) {
	s := site.peak()

	s.mu.Lock()
	changed := s.holdSoc != soc
	s.holdSoc = soc
	s.mu.Unlock()

	if changed {
		settings.SetFloat(keys.PeakShavingHoldSoc, soc)
		site.persistHeld()
	}
}

// restoreOmniThreshold writes back the threshold the optimizer's hold raised and
// reports whether nothing is left raised. Without the setter (entity removed)
// there is nothing to write to. Called with s.out held.
func (site *Site) restoreOmniThreshold() bool {
	s := site.peak()

	s.mu.Lock()
	before, set := s.holdSoc, s.protSocSet
	s.mu.Unlock()

	if before <= 0 {
		return true
	}

	if set != nil && !site.omniWriteNumber("peak shaving soc threshold", "%", set, before) {
		return false
	}

	site.setHoldSoc(0)

	return true
}
