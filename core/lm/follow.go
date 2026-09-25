package lm

// Loads that do not follow their limit. In an overload, load management counts
// on the loads below a priority to give way. A load that keeps drawing more than
// it was allowed never does, and the circuit would stay overloaded. After the set
// number of cycles such a load is no longer counted on, so the next load up the
// priority order is shed instead. See core/site_lm_follow.go.

import (
	"sync"

	"github.com/evcc-io/evcc/api"
)

var (
	followMu     sync.Mutex
	followLookup func() int
	following    = make(map[Load]*followState)
)

type followState struct {
	cycles  int // cycles in a row above its limit on an overloaded circuit
	ignored bool
}

// FollowChange is a load that stopped or started following its limit again
type FollowChange struct {
	Load    Load
	Ignored bool    // true: no longer counted on, false: follows again
	Power   float64 // what it draws in W
	Allowed float64 // what it was allowed in W
}

// SetFollowCycles installs the lookup returning after how many cycles a load
// not following its limit is no longer counted on, 0 = never
func SetFollowCycles(f func() int) {
	followMu.Lock()
	defer followMu.Unlock()
	followLookup = f
}

// followTolerance is what a load may draw above its limit without counting as
// not following: measurement, voltage and ramping, not disobedience
func followTolerance(allowed float64) float64 {
	return max(300, allowed*0.1)
}

// overloaded reports whether a limited level of c's parent chain is above its limit
func overloaded(c api.Circuit) bool {
	for ; c != nil; c = c.GetParent() {
		if m := c.GetMaxPower(); m > 0 && c.GetChargePower() > m {
			return true
		}
		if m := c.GetMaxCurrent(); m > 0 && c.GetMaxPhaseCurrent() > m {
			return true
		}
	}
	return false
}

// ignored reports whether l is no longer counted on to give way
func ignored(l Load) bool {
	followMu.Lock()
	defer followMu.Unlock()

	s, ok := following[l]
	return ok && s.ignored
}

// CheckFollowing compares what each load draws with what it was last allowed.
// Called once per cycle, it returns the loads whose state changed. A load counts
// a cycle while it draws more than allowed and its circuit is overloaded; it is
// counted on again as soon as it draws what it was allowed.
func CheckFollowing() []FollowChange {
	followMu.Lock()
	f := followLookup
	followMu.Unlock()

	n := 0
	if f != nil {
		n = f()
	}

	// the loads' own methods are called without holding followMu, as below()
	// takes it while a load may hold its own lock
	type sample struct {
		load           Load
		power, allowed float64
		over, counts   bool
	}

	var samples []sample
	for _, e := range snapshot() {
		d, ok := LastDecision(e.load)
		if !ok {
			continue
		}

		power := e.load.GetChargePower()
		over := power > d.Allowed+followTolerance(d.Allowed)
		samples = append(samples, sample{e.load, power, d.Allowed, over, over && overloaded(e.circuit)})
	}

	followMu.Lock()
	defer followMu.Unlock()

	var res []FollowChange
	seen := make(map[Load]bool, len(samples))

	for _, s := range samples {
		seen[s.load] = true

		st, ok := following[s.load]
		if !ok {
			st = new(followState)
			following[s.load] = st
		}

		switch {
		case !s.over:
			st.cycles = 0
			if st.ignored {
				st.ignored = false
				res = append(res, FollowChange{s.load, false, s.power, s.allowed})
			}

		case s.counts:
			st.cycles++
			if n > 0 && st.cycles >= n && !st.ignored {
				st.ignored = true
				res = append(res, FollowChange{s.load, true, s.power, s.allowed})
			}

		default:
			// above its limit, but the circuit copes: nothing to count
			st.cycles = 0
		}

		if n <= 0 && st.ignored {
			st.ignored = false
			res = append(res, FollowChange{s.load, false, s.power, s.allowed})
		}
	}

	for l := range following {
		if !seen[l] {
			delete(following, l)
		}
	}

	return res
}

func resetFollowing() {
	followMu.Lock()
	defer followMu.Unlock()
	clear(following)
	followLookup = nil
}
