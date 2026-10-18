package core

// Custom extension: circuits chosen for the fork's features that were deleted
// since. A name that no longer resolves must not widen what a feature touches
// (load management off lifting every circuit, the battery grid charging
// without a circuit check): the assignment is removed with a warning, at the
// start and once per cycle, so the window ends with the next cycle.

import (
	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/db/settings"
	"github.com/evcc-io/evcc/util/config"
)

// circuitExists reports whether a circuit of that name is configured
func circuitExists(name string) bool {
	_, err := config.Circuits().ByName(name)
	return err == nil
}

// dropOrphanCircuits removes the load management (peak) circuit and the
// battery's circuit when they no longer exist. A load management switched off
// for a circuit that is gone is switched on again.
func (site *Site) dropOrphanCircuits() {
	s := site.lms()

	s.mu.Lock()
	name, off := s.circuit, s.off
	s.mu.Unlock()

	changed := false

	if name != "" && !circuitExists(name) {
		s.mu.Lock()
		s.circuit = ""
		s.offPublished = false
		s.mu.Unlock()

		_ = settings.Delete(keys.LmCircuit)

		if off {
			site.log.WARN.Printf("load management: circuit %s no longer exists, removed, load management is on again", name)
			_ = site.SetLmEnabled(true)
		} else {
			site.log.WARN.Printf("load management: circuit %s no longer exists, removed", name)
		}
		changed = true
	}

	if changed {
		site.applyCircuitLimits()
	}
}
