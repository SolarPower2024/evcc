package core

// Custom extension: one priority for pv surplus, planner and load management.
//
// Loadpoints are ranked by their upstream priority everywhere: pv surplus and
// shedding (and the planner, once it shares circuit capacity, see PR 2), so a
// loadpoint that gets surplus first is not the first to be shed. The battery has
// no upstream priority; it keeps a value of its own on the same 0-10 scale, set
// in the load management priorities.

import (
	"maps"

	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/core/loadpoint"
	"github.com/evcc-io/evcc/db/settings"
	"github.com/evcc-io/evcc/util/config"
)

// unifyLmPriorities takes the load management priorities of the loadpoints over
// into their upstream priority, once. Afterwards only the battery's own value
// is kept in the load management priorities.
func (site *Site) unifyLmPriorities() {
	if done, _ := settings.Bool(keys.LmPrioritiesUnified); done {
		return
	}

	s := site.lms()

	s.mu.Lock()
	prios := maps.Clone(s.prios)
	s.mu.Unlock()

	for _, dev := range config.Loadpoints().Devices() {
		lp, ok := dev.Instance().(*Loadpoint)
		if !ok {
			continue
		}

		name := dev.Config().Name
		prio, ok := prios[name]
		if !ok && lp.LmPrio_ != 0 {
			prio, ok = lp.LmPrio_, true // lmpriority from the stored loadpoint config
		}
		delete(prios, name)

		if ok && prio != lp.GetPriority() {
			site.log.INFO.Printf("load management: %s priority %d taken over as loadpoint priority (was %d), it now also ranks pv surplus and plans", lp.GetTitle(), prio, lp.GetPriority())
			lp.SetPriority(prio)
		}
	}

	s.mu.Lock()
	s.prios = prios
	s.mu.Unlock()

	if err := settings.SetJson(keys.LmPriorities, prios); err != nil {
		site.log.ERROR.Printf("load management: priorities: %v", err)
		return
	}

	settings.SetBool(keys.LmPrioritiesUnified, true)
}

// lmBatteryPriority is the battery's priority: the value set in the ui, else 0
func (site *Site) lmBatteryPriority() int {
	if prio, ok := site.lmPriorityLookup(site.lmBattery()); ok {
		return prio
	}
	return 0
}

// dropOldLmPriority removes the old lmpriority key from the stored loadpoint
// configs. The ui only merges into a stored config and could never remove it.
// Runs after unifyLmPriorities has taken the value over.
func (site *Site) dropOldLmPriority() {
	for _, dev := range config.Loadpoints().Devices() {
		cd, ok := dev.(config.ConfigurableDevice[loadpoint.API])
		if !ok {
			continue // yaml loadpoint, only the user can edit evcc.yaml
		}

		conf := cd.Config()
		if _, ok := conf.Other["lmpriority"]; !ok {
			continue
		}

		delete(conf.Other, "lmpriority")
		if err := cd.Update(conf.Other, cd.Instance()); err != nil {
			site.log.ERROR.Printf("load management: %s: removing old lmpriority: %v", conf.Name, err)
			continue
		}

		site.log.INFO.Printf("load management: %s: old lmpriority removed from the stored loadpoint config", conf.Name)
	}
}
