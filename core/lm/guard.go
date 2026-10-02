package lm

// Shed guard: a load that load management had to shed stays off for a while.
// Without it a load flaps whenever the demand hovers around the limit: shedding
// it frees the power that lets it start again on the next visit. Which loads are
// protected and for how long is set in the ui, see core/site_lm_guard.go.

import (
	"sync"
	"time"
)

// guard is the Manager's shed guard state
type guard struct {
	guardMu     sync.Mutex
	guardLookup func(Load) time.Duration
	shedAt      map[Load]time.Time
}

// SetGuardLookup installs the lookup returning how long a load stays off after
// it was shed, 0 for a load that is not protected
func (m *Manager) SetGuardLookup(f func(Load) time.Duration) {
	m.guardMu.Lock()
	defer m.guardMu.Unlock()
	m.guardLookup = f
}

// guardDuration is called without holding guardMu, the lookup may take locks of
// its own
func (m *Manager) guardDuration(l Load) time.Duration {
	m.guardMu.Lock()
	f := m.guardLookup
	m.guardMu.Unlock()

	if f == nil {
		return 0
	}
	return f(l)
}

// Shed records that load management switched l off at now and returns how long
// it stays off, 0 if it is not protected
func (m *Manager) Shed(l Load, now time.Time) time.Duration {
	d := m.guardDuration(l)
	if d <= 0 {
		return 0
	}

	m.guardMu.Lock()
	defer m.guardMu.Unlock()
	m.shedAt[l] = now

	return d
}

// Guarded returns how long l stays off at now, 0 once it may run again. The
// duration is looked up on every call, so changing it or lifting the protection
// in the ui applies to a running guard right away.
func (m *Manager) Guarded(l Load, now time.Time) time.Duration {
	m.guardMu.Lock()
	at, ok := m.shedAt[l]
	m.guardMu.Unlock()

	if !ok {
		return 0
	}

	if left := at.Add(m.guardDuration(l)).Sub(now); left > 0 {
		return left
	}

	m.guardMu.Lock()
	delete(m.shedAt, l)
	m.guardMu.Unlock()

	return 0
}
