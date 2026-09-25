package core

// Custom extension: loads that do not follow their load management limit, see
// core/lm/follow.go. After the cycles set under Lastmanagement-Details →
// Erweitert, load management no longer counts on such a load to give way.

import (
	"time"

	"github.com/evcc-io/evcc/core/lm"
)

// defaultFollowCycles is after how many cycles a load not following its limit
// is no longer counted on
const defaultFollowCycles = 3

func (site *Site) lmFollowCycles() int {
	if v := site.advanced().FollowCycles; v != nil {
		return int(*v)
	}
	return defaultFollowCycles
}

// checkLmFollowing runs once per cycle, after every load has been limited
func (site *Site) checkLmFollowing() {
	for _, c := range lm.CheckFollowing() {
		name := c.Load.GetTitle()

		if !c.Ignored {
			site.log.INFO.Printf("load management: %s follows its limit again", name)
			continue
		}

		site.log.WARN.Printf("load management: %s draws %.0fW but was allowed %.0fW, no longer counted on to give way", name, c.Power, c.Allowed)
		lm.AddEvent(lm.Event{At: time.Now(), Type: lm.EventNotFollowing, Load: name, A: c.Power, B: c.Allowed})
	}
}
