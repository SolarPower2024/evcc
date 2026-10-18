package core

// Custom extension: the battery's phases are asked for where they matter. With
// a current limit (A) on the battery's circuit or above it, the grid charge
// power is converted to a current by the phase count. Assuming three phases
// would let a single-phase battery charge with a third of the current it
// really draws, so without the setting grid charging stays off.

// batteryPhasesMissing reports whether the battery's phases are needed but not
// set: no value under Erweitert, and the battery's circuit or one above it has
// a current limit. A circuit with only a power limit needs no phases.
func (site *Site) batteryPhasesMissing() bool {
	if !site.phasesMissing() {
		return false
	}

	site.lms().phasesWarn.Do(func() {
				site.log.WARN.Println("load management: battery phases not set, needed for a circuit with a current limit - grid charging stays off until they are set under Lastmanagement-Details → Erweitert")
	})
	return true
}

// phasesMissing is batteryPhasesMissing without the log, for readers that only ask
func (site *Site) phasesMissing() bool {
	if site.advanced().Phases != nil {
		return false
	}

	for c := site.lmBatteryCircuit(); c != nil; c = c.GetParent() {
		if c.GetMaxCurrent() > 0 {
			return true
		}
	}

	return false
}
