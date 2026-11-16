package core

// Custom extension: the power the charge planner plans with, see site_planpower.go.
// The planner takes its duration from GetPlanRequiredDuration, which gets the
// loadpoint's maximum power. PlanPower lowers it to what the circuits and the peak
// limit let the loadpoint draw, so the plan starts early enough where load
// management holds the car below the wall box's power. EffectiveMaxPower stays as
// it is: control, the optimizer and the site read it too.

// PlanPower returns the power a plan for this loadpoint is made with and what
// limits it: "peak", "circuit" or "" if nothing does. It starts from maxPower and
// takes the lowest of
//   - every limit of the loadpoint's circuit and its parents below the main
//     circuit, a current counted on the phases the loadpoint charges on,
//   - what the main circuit leaves: the lower of the peak limit and its own limit,
//     less the home draw expected until the plan time.
//
// It never goes below the minimum power: load management regulates anyway. Without
// a site (tests), limits or circuits maxPower is returned unchanged.
//
// It reads the loadpoint through its public methods, so it has to be called without
// the loadpoint's lock; of the site only its own cache is read, never the site's lock.
func (lp *Loadpoint) PlanPower(maxPower float64) (float64, string) {
	site, ok := lp.site.(*Site)
	if !ok || site == nil || maxPower <= 0 {
		return maxPower, ""
	}

	power, reason := maxPower, ""

	phases := lp.MaxActivePhases()
	for c := lp.GetCircuit(); c != nil && c.GetParent() != nil; c = c.GetParent() {
		if p := circuitLimit(c, phases); p > 0 && p < power {
			power, reason = p, planReasonCircuit
		}
	}

	if room, why := site.planRoom(lp.EffectivePlanTime()); room > 0 && room < power {
		power, reason = room, why
	}

	if minPower := min(lp.EffectiveMinPower(), maxPower); power < minPower {
		power = minPower
	}
	if power >= maxPower {
		return maxPower, ""
	}

	return power, reason
}
