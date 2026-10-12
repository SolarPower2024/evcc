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
)

// Load is a participant in the priority-based load management.
// Both loadpoints and the home battery implement it.
type Load interface {
	GetTitle() string

	// LmPriority is the shed priority, lower is shed first. For a loadpoint it
	// is its regular priority, which also ranks pv surplus; the battery has a
	// value of its own on the same scale.
	LmPriority() int

	// what the load draws right now, i.e. what shedding it would free
	GetChargePower() float64
	GetMaxPhaseCurrent() float64
}

const (
	// DefaultTimeout is how long an unserved demand keeps reserving headroom.
	// It must outlast a full loadpoint round-robin, as evcc updates only one
	// loadpoint per cycle: interval x number of loadpoints. The default covers
	// 20 loadpoints at the default 30s interval. A load that is satisfied or off
	// records a demand of zero on every visit, so a generous value costs nothing;
	// only a load that stops updating entirely would keep reserving.
	DefaultTimeout = 10 * time.Minute

	// DefaultHoldOff is how long battery grid charging stays off after load
	// management denied it. Without it the battery would flap: stopping frees
	// the power that made it start again.
	DefaultHoldOff = 5 * time.Minute

	// DefaultPhases is the assumed phase count for battery current accounting
	DefaultPhases = 3
)

// amount is a load's unserved demand in one unit
type amount struct {
	need float64 // everything the load asked for and did not get
	min  float64 // the part of it that is worth granting on its own, see Demand
}

// reserve returns how much of the available headroom the demand claims: all of
// the need if the headroom covers it, else as much as there is, but only when
// that is worth granting. A load that runs in full or not at all, like a
// heater or the battery, has its whole need as the minimum.
func (a amount) reserve(available float64) float64 {
	if a.need <= 0 || available <= 0 || available < a.min {
		return 0
	}
	return min(a.need, available)
}

// record is a load's unserved demand
type record struct {
	circuit api.Circuit
	prio    int
	power   amount // in W
	current amount // in A
	updated time.Time
}

// Manager is the load management of one site: the loads' unserved demand, the
// shed guard, the overview's status and which loads follow their limit. The
// site owns it, its loadpoints reach it through the site.
type Manager struct {
	mu      sync.Mutex
	reg     map[Load]*record
	timeout time.Duration
	lookup  func(Load) (int, bool)

	guard
	status
	follow
}

// New returns an empty load management
func New() *Manager {
	return &Manager{
		reg:     make(map[Load]*record),
		timeout: DefaultTimeout,
		guard:   guard{shedAt: make(map[Load]time.Time)},
		status:  status{decisions: make(map[Load]Decision)},
		follow:  follow{following: make(map[Load]*followState)},
	}
}

// SetPriorityLookup installs a lookup whose answer takes precedence over a
// load's own LmPriority. The site uses it for the priorities set in the ui.
func (m *Manager) SetPriorityLookup(f func(Load) (int, bool)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lookup = f
}

// Priority returns the effective shed priority of a load
func (m *Manager) Priority(l Load) int {
	m.mu.Lock()
	f := m.lookup
	m.mu.Unlock()

	// called without holding mu, the lookup may take locks of its own
	if f != nil {
		if prio, ok := f(l); ok {
			return prio
		}
	}

	return l.LmPriority()
}

// SetTimeout sets how long an unserved demand keeps reserving headroom
func (m *Manager) SetTimeout(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if d <= 0 {
		d = DefaultTimeout
	}
	m.timeout = d
}

// Forget drops a load's unserved demand. For a load that stops asking for power
// without passing through ValidatePower again, whose reservation would otherwise
// keep lower priority loads throttled until it expires.
func (m *Manager) Forget(l Load) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.reg, l)
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

// under reports whether a load on circuit c draws through the level, i.e.
// whether the level is c itself or one of its parents. Shedding such a load
// relieves the level, shedding any other does not.
func under(c, level api.Circuit) bool {
	for ; c != nil; c = c.GetParent() {
		if c == level {
			return true
		}
	}
	return false
}

// remember stores a load's unserved demand. A nil power or current leaves the
// respective value untouched, as the two are recorded by separate calls.
func (m *Manager) remember(c api.Circuit, l Load, prio int, power, current *amount) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()

	// drop demand of loads that stopped reporting
	for k, v := range m.reg {
		if now.Sub(v.updated) > m.timeout {
			delete(m.reg, k)
		}
	}

	r, ok := m.reg[l]
	if !ok {
		r = new(record)
		m.reg[l] = r
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
func (m *Manager) snapshot() []entry {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	res := make([]entry, 0, len(m.reg))

	for l, r := range m.reg {
		if now.Sub(r.updated) <= m.timeout {
			res = append(res, entry{l, *r})
		}
	}

	return res
}

// below returns what the loads with a priority below prio draw through the
// level, i.e. what shedding them frees on it. The calling load's own draw is
// passed in rather than queried, as it may hold its own lock.
func (m *Manager) below(entries []entry, level api.Circuit, prio int, self Load, selfPower, selfCurrent float64) (float64, float64) {
	var power, current float64

	for _, e := range entries {
		if e.prio >= prio || !under(e.circuit, level) {
			continue
		}

		if e.load == self {
			power += selfPower
			current += selfCurrent
			continue
		}

		// a load not following its limit would not give way either
		if m.ignored(e.load) {
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

// relief returns what shedding the loads below prio adds to the headroom of a
// request on circuit c. Each level of the parent chain counts the loads below
// it only: a load in a sibling circuit does not relieve an overloaded circuit,
// while one in a sibling of the next level up does relieve that level. Added to
// the request's old value it makes the circuit's own chain check come to the
// tightest level's answer. Without a limited level there is nothing to relieve.
func (m *Manager) relief(entries []entry, c api.Circuit, prio int, self Load, selfPower, selfCurrent float64) (float64, float64) {
	freePower, freeCurrent := headroom(c)
	power, current := math.Inf(1), math.Inf(1)

	for level := c; level != nil; level = level.GetParent() {
		lowerPower, lowerCurrent := m.below(entries, level, prio, self, selfPower, selfCurrent)

		if limit := level.GetMaxPower(); limit > 0 {
			power = min(power, limit-level.GetChargePower()-freePower+lowerPower)
		}
		if limit := level.GetMaxCurrent(); limit > 0 {
			current = min(current, limit-level.GetMaxPhaseCurrent()-freeCurrent+lowerCurrent)
		}
	}

	if math.IsInf(power, 1) {
		power = 0
	}
	if math.IsInf(current, 1) {
		current = 0
	}

	return power, current
}

// reserved returns the power and current that must be left to loads with a
// higher priority than prio and are hence unavailable to the calling load.
//
// A reservation only counts while it can be met: on every level of the
// reserving load's circuit chain the free headroom plus what the loads below it
// draw through that level has to cover it. Otherwise shedding them would not
// let it run anyway, and the power would just sit idle. A load that can run on
// part of its demand claims what there is as soon as that covers its minimum.
func (m *Manager) reserved(c api.Circuit, self Load, prio int, selfPower, selfCurrent float64) (float64, float64) {
	entries := m.snapshot()

	var power, current float64

	for _, e := range entries {
		if e.load == self || e.prio <= prio || !competes(c, e.circuit) {
			continue
		}

		freePower, freeCurrent := headroom(e.circuit)
		lowerPower, lowerCurrent := m.relief(entries, e.circuit, e.prio, self, selfPower, selfCurrent)

		power += e.power.reserve(freePower + lowerPower)
		current += e.current.reserve(freeCurrent + lowerCurrent)
	}

	return power, current
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

// Demand qualifies a load's request: what it tells load management beyond the
// amounts asked for. The zero value is the plain request.
type Demand struct {
	// Standing is the demand of a load that is on but draws nothing, like a
	// wallbox in state B whose car is full: it asks for its maximum every cycle
	// and takes nothing. The amount stands as its recorded demand whether the
	// request fits or not. Following what the circuit denies instead would hold
	// the lower loads back for one cycle, show the request as met the next
	// and let them take the power back, over and over.
	Standing float64

	// Regulated marks a load that runs on any power from its minimum up, like a
	// wallbox. Of its demand only Min is needed to get it going; as soon as the
	// headroom covers that, the lower loads leave what there is. Without it the
	// load needs the whole increase, like a heater or the battery, which take
	// nothing of a partial budget.
	Regulated bool

	// Min is the smallest increase that gets a regulated load going: its minimum
	// less what it draws now. Zero for one that runs above its minimum already.
	Min float64
}

// amount returns what to record for an unserved amount
func (d Demand) amount(unserved float64) amount {
	need := unserved
	if d.Standing > 0 {
		need = d.Standing
	}

	if !d.Regulated {
		return amount{need, need}
	}

	return amount{need, min(max(d.Min, 0), need)}
}

// ValidatePower caps a power request against the circuit while withholding the
// headroom reserved for higher-priority loads, and records what the circuit
// denied so that lower-priority loads give way.
func (m *Manager) ValidatePower(l Load, c api.Circuit, old, new float64) float64 {
	return m.RequestPower(l, c, old, new, Demand{})
}

// RequestPower is ValidatePower for a load that qualifies its request
func (m *Manager) RequestPower(l Load, c api.Circuit, old, new float64, d Demand) float64 {
	if c == nil {
		return new
	}

	capped := c.ValidatePower(old, new)
	need := d.amount(unmet(old, new, capped))
	m.remember(c, l, m.Priority(l), &need, nil)

	return m.peekPower(l, c, old, new, &capped)
}

// ValidateCurrent caps a current request against the circuit while withholding
// the headroom reserved for higher-priority loads, and records what the circuit
// denied so that lower-priority loads give way.
func (m *Manager) ValidateCurrent(l Load, c api.Circuit, old, new float64) float64 {
	return m.RequestCurrent(l, c, old, new, Demand{})
}

// RequestCurrent is ValidateCurrent for a load that qualifies its request
func (m *Manager) RequestCurrent(l Load, c api.Circuit, old, new float64, d Demand) float64 {
	if c == nil {
		return new
	}

	capped := c.ValidateCurrent(old, new)
	need := d.amount(unmet(old, new, capped))
	m.remember(c, l, m.Priority(l), nil, &need)

	return m.peekCurrent(l, c, old, new, &capped)
}

// PeekPower caps a power request like ValidatePower but records no demand.
// Use it for what-if probes such as phase scaling, where the requested value is
// a theoretical maximum rather than an actual need.
func (m *Manager) PeekPower(l Load, c api.Circuit, old, new float64) float64 {
	if c == nil {
		return new
	}
	return m.peekPower(l, c, old, new, nil)
}

// peekPower is PeekPower reusing the circuit's answer without a reserve when
// the caller already has it, so the circuit is not asked (and logs) twice
func (m *Manager) peekPower(l Load, c api.Circuit, old, new float64, capped *float64) float64 {
	prio := m.Priority(l)
	reserve, _ := m.reserved(c, l, prio, old, 0)

	// ValidatePower caps at old + (maxPower - circuit power). Lowering old by the
	// reserve therefore caps at old + (maxPower - circuit power - reserve), which
	// is exactly the budget minus the withheld headroom. The reserve is applied at
	// every level of the parent chain, which over-reserves on nested circuits
	// whose limit is not the binding one - erring towards less power for the
	// lower-priority load.
	var res float64
	if reserve == 0 && capped != nil {
		res = *capped
	} else {
		res = c.ValidatePower(old-reserve, new)
	}

	// cutting into what the load draws right now: the loads below it go first, as
	// far as shedding them relieves the level that is short
	if keep := min(old, new); res < keep {
		lower, _ := m.relief(m.snapshot(), c, prio, l, old, 0)
		res = max(res, min(keep, c.ValidatePower(old-reserve+lower, keep)))
	}

	return res
}

// PeekCurrent caps a current request like ValidateCurrent but records no demand
func (m *Manager) PeekCurrent(l Load, c api.Circuit, old, new float64) float64 {
	if c == nil {
		return new
	}
	return m.peekCurrent(l, c, old, new, nil)
}

// peekCurrent is PeekCurrent reusing the circuit's answer, see peekPower
func (m *Manager) peekCurrent(l Load, c api.Circuit, old, new float64, capped *float64) float64 {
	prio := m.Priority(l)
	_, reserve := m.reserved(c, l, prio, 0, old)

	var res float64
	if reserve == 0 && capped != nil {
		res = *capped
	} else {
		res = c.ValidateCurrent(old-reserve, new)
	}

	// cutting into what the load draws right now: the loads below it go first, as
	// far as shedding them relieves the level that is short
	if keep := min(old, new); res < keep {
		_, lower := m.relief(m.snapshot(), c, prio, l, 0, old)
		res = max(res, min(keep, c.ValidateCurrent(old-reserve+lower, keep)))
	}

	return res
}
