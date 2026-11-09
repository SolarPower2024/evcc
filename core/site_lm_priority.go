package core

// Custom extension: one priority for pv surplus, planner and load management.
//
// Loadpoints are ranked by their upstream priority everywhere: pv surplus and
// shedding (and the planner, once it shares circuit capacity, see PR 2), so a
// loadpoint that gets surplus first is not the first to be shed. The battery has
// no upstream priority; it keeps a value of its own on the same 0-10 scale, set
// in the load management priorities.

// lmBatteryPriority is the battery's priority: the value set in the ui, else 0
func (site *Site) lmBatteryPriority() int {
	if prio, ok := site.lmPriorityLookup(site.lmBattery()); ok {
		return prio
	}
	return 0
}
