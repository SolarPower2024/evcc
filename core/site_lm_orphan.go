package core

// Custom extension: the circuit chosen for the battery that was deleted since. A
// name that no longer resolves must not widen what a feature touches (the
// battery grid charging without a circuit check): the assignment is removed with
// a warning, at the start and once per cycle, so the window ends with the next cycle.

import (
	"github.com/evcc-io/evcc/util/config"
)

// circuitExists reports whether a circuit of that name is configured
func circuitExists(name string) bool {
	_, err := config.Circuits().ByName(name)
	return err == nil
}

// dropOrphanCircuits removes the battery's circuit when it no longer exists.
func (site *Site) dropOrphanCircuits() {
	if ref := site.GetPeakShavingCircuit(); ref != "" && !circuitExists(ref) {
		site.log.WARN.Printf("peak shaving: battery circuit %s no longer exists, removed, the battery is not part of load management", ref)
		_ = site.SetPeakShavingCircuit("")
	}
}

// batteryCircuitOrphan reports whether the battery is assigned to a circuit
// that cannot be resolved. Until dropOrphanCircuits has removed the name, grid
// charging stays off: "no circuit" would otherwise read as "not in load
// management" and charge without the circuit's check.
func (site *Site) batteryCircuitOrphan() bool {
	if !site.circuitOrphan() {
		return false
	}

	ref := site.GetPeakShavingCircuit()
	site.lms().orphanWarn.Do(func() {
		site.log.WARN.Printf("load management: battery circuit %s does not exist, grid charging stays off until it is removed", ref)
	})
	return true
}

// circuitOrphan is batteryCircuitOrphan without the log, for readers that only ask
func (site *Site) circuitOrphan() bool {
	return site.GetPeakShavingCircuit() != "" && site.lmBatteryCircuit() == nil
}
