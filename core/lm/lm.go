// Package lm adds priority-based load shedding on top of evcc's circuits.
//
// Upstream circuits serve requests first come, first served: whichever load asks
// first gets the remaining budget. This package puts a priority in front of that
// budget. A load whose request the circuit caps records the increase it asked
// for as unserved demand; loads with a lower priority then have that amount
// withheld from their own budget and give way on their next update, which frees
// the power for the higher-priority load one cycle later.
//
// An overload works the other way round: a load keeps what it draws as long as
// the loads below it draw enough to cover the excess, so shedding starts at the
// bottom rather than with whichever load evcc happens to update first.
//
// Nothing happens while all loads on a circuit share the same priority, so the
// behaviour is identical to upstream until priorities are actually configured.
package lm

import (
	"math"
	"sync"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/plugin"
)

// Load is a participant in the priority-based load management.
// Both loadpoints and the home battery implement it.
type Load interface {
	GetTitle() string

	// LmPriority is the shed priority, lower is shed first. It is deliberately
	// separate from the loadpoint priority, which governs pv surplus
	// distribution: the load that should get surplus first is not necessarily
	// the one that should keep power when the fuse is the constraint.
	LmPriority() int

	// what the load draws right now, i.e. what shedding it would free
	GetChargePower() float64
	GetMaxPhaseCurrent() float64
}

// Config is the site's load management configuration
type Config struct {
	Timeout     time.Duration `mapstructure:"timeout"`     // unserved demand expiry, 0 = default
	Battery     Battery       `mapstructure:"battery"`     // home battery participation
	PeakShaving PeakShaving   `mapstructure:"peakshaving"` // demand charge peak shaving
}

// PeakShaving configures the battery reserve used to cap the grid demand peak.
// The limit, the reserve soc and the target entity are runtime settings that
// live in the ui, not here - see core/site_peakshaving.go. Everything below is
// optional and only needed outside the Home Assistant add-on.
type PeakShaving struct {
	URI        string         `mapstructure:"uri"`        // Home Assistant URI, empty = the add-on's supervisor connection
	Insecure   bool           `mapstructure:"insecure"`   // allow self-signed certificates
	Set        *plugin.Config `mapstructure:"set"`        // full plugin override, takes precedence over the ui entity
	FreeValue  float64        `mapstructure:"freevalue"`  // written while above the reserve soc, 0 = default
	Hysteresis float64        `mapstructure:"hysteresis"` // soc band in %, 0 = default
}

const (
	// DefaultFreeValue signals the discharge controller that the battery may be
	// used without restriction, i.e. the soc is above the peak shaving reserve
	DefaultFreeValue = 10000.0

	// DefaultHysteresis keeps a fluctuating soc from flapping across the reserve
	DefaultHysteresis = 2.0

	// PeakWindow is the metering interval a demand charge is billed on
	PeakWindow = 15 * time.Minute
)

// Battery configures the home battery as a load management participant
type Battery struct {
	CircuitRef string        `mapstructure:"circuit"`  // circuit the battery draws from, empty = battery not managed
	Priority   int           `mapstructure:"priority"` // shed priority, lower is shed first
	Power      float64       `mapstructure:"power"`    // expected grid charge power in W, 0 = sum of maxchargepower
	Phases     int           `mapstructure:"phases"`   // phases for current accounting, 0 = default
	HoldOff    time.Duration `mapstructure:"holdoff"`  // wait before retrying after a shed, 0 = default
}

const (
	// DefaultTimeout is how long an unserved demand keeps reserving headroom.
	// It must outlast a full loadpoint round-robin, as evcc updates only one
	// loadpoint per cycle: interval x number of loadpoints. The default covers
	// 20 loadpoints at the default 30s interval. A load that is satisfied or
	// idle records a demand of zero on every visit, so a generous value costs
	// nothing; only a load that stops updating entirely would keep reserving.
	DefaultTimeout = 10 * time.Minute

	// DefaultHoldOff is how long battery grid charging stays off after load
	// management denied it. Without it the battery would flap: stopping frees
	// the power that made it start again.
	DefaultHoldOff = 5 * time.Minute

	// DefaultPhases is the assumed phase count for battery current accounting
	DefaultPhases = 3
)

// record is a load's unserved demand
type record struct {
	circuit api.Circuit
	prio    int
	power   float64 // unserved power in W
	current float64 // unserved current in A
	updated time.Time
}

var (
	mu      sync.Mutex
	reg     = make(map[Load]*record)
	timeout = DefaultTimeout
	lookup  func(Load) (int, bool)
)

// SetPriorityLookup installs a lookup whose answer takes precedence over a
// load's own LmPriority. The site uses it for the priorities set in the ui.
func SetPriorityLookup(f func(Load) (int, bool)) {
	mu.Lock()
	defer mu.Unlock()
	lookup = f
}

// Priority returns the effective shed priority of a load
func Priority(l Load) int {
	mu.Lock()
	f := lookup
	mu.Unlock()

	// called without holding mu, the lookup may take locks of its own
	if f != nil {
		if prio, ok := f(l); ok {
			return prio
		}
	}

	return l.LmPriority()
}

// SetTimeout sets how long an unserved demand keeps reserving headroom
func SetTimeout(d time.Duration) {
	mu.Lock()
	defer mu.Unlock()

	if d <= 0 {
		d = DefaultTimeout
	}
	timeout = d
}

// Forget drops a load's unserved demand. For a load that stops asking for power
// without passing through ValidatePower again, whose reservation would otherwise
// keep lower priority loads throttled until it expires.
func Forget(l Load) {
	mu.Lock()
	defer mu.Unlock()
	delete(reg, l)
}

// Reset drops all recorded demand. Intended for tests.
func Reset() {
	mu.Lock()
	defer mu.Unlock()
	clear(reg)
	lookup = nil
}

// competes reports whether loads on the two circuits draw through a shared
// limit, i.e. whether the circuits' parent chains intersect. In the common
// single-circuit setup this is always true.
func competes(a, b api.Circuit) bool {
	for ; a != nil; a = a.GetParent() {
		for c := b; c != nil; c = c.GetParent() {
			if a == c {
				return true
			}
		}
	}
	return false
}

// remember stores a load's unserved demand. A nil power or current leaves the
// respective value untouched, as the two are recorded by separate calls.
func remember(c api.Circuit, l Load, prio int, power, current *float64) {
	mu.Lock()
	defer mu.Unlock()

	now := time.Now()

	// drop demand of loads that stopped reporting
	for k, v := range reg {
		if now.Sub(v.updated) > timeout {
			delete(reg, k)
		}
	}

	r, ok := reg[l]
	if !ok {
		r = new(record)
		reg[l] = r
	}

	r.circuit = c
	r.prio = prio
	r.updated = now

	if power != nil {
		r.power = *power
	}
	if current != nil {
		r.current = *current
	}
}

// entry is a registered load with its record, copied out of the registry
type entry struct {
	load Load
	record
}

// snapshot returns the live records. Taken under the lock so that the loads'
// own methods can be called afterwards without holding it.
func snapshot() []entry {
	mu.Lock()
	defer mu.Unlock()

	now := time.Now()
	res := make([]entry, 0, len(reg))

	for l, r := range reg {
		if now.Sub(r.updated) <= timeout {
			res = append(res, entry{l, *r})
		}
	}

	return res
}

// below returns what the loads with a priority below prio draw on circuits
// competing with c, i.e. what shedding them could free. The calling load's own
// draw is passed in rather than queried, as it may hold its own lock.
func below(entries []entry, c api.Circuit, prio int, self Load, selfPower, selfCurrent float64) (float64, float64) {
	var power, current float64

	for _, e := range entries {
		if e.prio >= prio || !competes(c, e.circuit) {
			continue
		}

		if e.load == self {
			power += selfPower
			current += selfCurrent
			continue
		}

		power += e.load.GetChargePower()
		current += e.load.GetMaxPhaseCurrent()
	}

	return power, current
}

// headroom returns the power and current still free on the circuit, the
// tightest level of its parent chain. Unlimited levels do not count.
func headroom(c api.Circuit) (float64, float64) {
	power, current := math.Inf(1), math.Inf(1)

	for ; c != nil; c = c.GetParent() {
		if m := c.GetMaxPower(); m > 0 {
			power = min(power, m-c.GetChargePower())
		}
		if m := c.GetMaxCurrent(); m > 0 {
			current = min(current, m-c.GetMaxPhaseCurrent())
		}
	}

	return power, current
}

// reserved returns the power and current that must be left to loads with a
// higher priority than prio and are hence unavailable to the calling load.
//
// A reservation only counts while it can be met: the free headroom plus what
// the loads below the reserving one draw has to cover it. Otherwise shedding
// them would not let it run anyway, and the power would just sit idle.
func reserved(c api.Circuit, self Load, prio int, selfPower, selfCurrent float64) (float64, float64) {
	entries := snapshot()

	var power, current float64

	for _, e := range entries {
		if e.load == self || e.prio <= prio || !competes(c, e.circuit) {
			continue
		}

		freePower, freeCurrent := headroom(e.circuit)
		lowerPower, lowerCurrent := below(entries, e.circuit, e.prio, self, selfPower, selfCurrent)

		if e.power > 0 && freePower+lowerPower >= e.power {
			power += e.power
		}
		if e.current > 0 && freeCurrent+lowerCurrent >= e.current {
			current += e.current
		}
	}

	return power, current
}

// Reserved returns the power reserved for loads with a higher priority than the
// given load. Exposed for logging and diagnostics.
func Reserved(l Load, c api.Circuit) (float64, float64) {
	if c == nil || l == nil {
		return 0, 0
	}
	return reserved(c, l, Priority(l), l.GetChargePower(), l.GetMaxPhaseCurrent())
}

// unmet returns what a load has to be left once the circuit capped its request:
// the whole increase, not only the part that was capped. A load that switches
// on in full or not at all, like a battery or a heater, takes nothing of a
// partial budget, so lower priority loads must leave all of it free. Measured
// without any reserve.
func unmet(old, new, allowed float64) float64 {
	if allowed >= new {
		return 0
	}
	return max(0, new-old)
}

// ValidatePower caps a power request against the circuit while withholding the
// headroom reserved for higher-priority loads, and records what the circuit
// denied so that lower-priority loads give way.
func ValidatePower(l Load, c api.Circuit, old, new float64) float64 {
	if c == nil {
		return new
	}

	need := unmet(old, new, c.ValidatePower(old, new))
	remember(c, l, Priority(l), &need, nil)

	return PeekPower(l, c, old, new)
}

// ValidateCurrent caps a current request against the circuit while withholding
// the headroom reserved for higher-priority loads, and records what the circuit
// denied so that lower-priority loads give way.
func ValidateCurrent(l Load, c api.Circuit, old, new float64) float64 {
	if c == nil {
		return new
	}

	need := unmet(old, new, c.ValidateCurrent(old, new))
	remember(c, l, Priority(l), nil, &need)

	return PeekCurrent(l, c, old, new)
}

// PeekPower caps a power request like ValidatePower but records no demand.
// Use it for what-if probes such as phase scaling, where the requested value is
// a theoretical maximum rather than an actual need.
func PeekPower(l Load, c api.Circuit, old, new float64) float64 {
	if c == nil {
		return new
	}

	prio := Priority(l)
	reserve, _ := reserved(c, l, prio, old, 0)

	// ValidatePower caps at old + (maxPower - circuit power). Lowering old by the
	// reserve therefore caps at old + (maxPower - circuit power - reserve), which
	// is exactly the budget minus the withheld headroom. The reserve is applied at
	// every level of the parent chain, which over-reserves on nested circuits
	// whose limit is not the binding one - erring towards less power for the
	// lower-priority load.
	res := c.ValidatePower(old-reserve, new)

	// cutting into what the load draws right now: the loads below it go first
	if keep := min(old, new); res < keep {
		lower, _ := below(snapshot(), c, prio, l, old, 0)
		res = max(res, min(keep, c.ValidatePower(old-reserve+lower, keep)))
	}

	return res
}

// PeekCurrent caps a current request like ValidateCurrent but records no demand
func PeekCurrent(l Load, c api.Circuit, old, new float64) float64 {
	if c == nil {
		return new
	}

	prio := Priority(l)
	_, reserve := reserved(c, l, prio, 0, old)

	res := c.ValidateCurrent(old-reserve, new)

	// cutting into what the load draws right now: the loads below it go first
	if keep := min(old, new); res < keep {
		_, lower := below(snapshot(), c, prio, l, 0, old)
		res = max(res, min(keep, c.ValidateCurrent(old-reserve+lower, keep)))
	}

	return res
}
