package lm_test

import (
	"testing"
	"time"

	"github.com/evcc-io/evcc/core/lm"
	"github.com/stretchr/testify/assert"
)

func TestShedGuard(t *testing.T) {
	m := lm.New()

	protected, other := &testLoad{title: "heater"}, &testLoad{title: "other"}

	guard := 5 * time.Minute
	m.SetGuardLookup(func(l lm.Load) time.Duration {
		if l == protected {
			return guard
		}
		return 0
	})

	now := time.Now()

	// a load that is not protected is not held off
	assert.Zero(t, m.Shed(other, now))
	assert.Zero(t, m.Guarded(other, now))

	assert.Equal(t, guard, m.Shed(protected, now))
	assert.Equal(t, guard, m.Guarded(protected, now))
	assert.Equal(t, time.Second, m.Guarded(protected, now.Add(guard-time.Second)))
	assert.Zero(t, m.Guarded(protected, now.Add(guard)), "runs again once the guard is over")

	// the guard is over for good, not just at that moment
	assert.Zero(t, m.Guarded(protected, now))

	// a changed duration applies to a running guard
	m.Shed(protected, now)
	guard = 10 * time.Minute
	assert.Equal(t, 4*time.Minute, m.Guarded(protected, now.Add(6*time.Minute)))

	// lifting the protection releases it right away
	guard = 0
	assert.Zero(t, m.Guarded(protected, now.Add(time.Minute)))
}

// TestResumed: a shed load reports once when it runs again, with or without a
// guard; a load that was not shed never does
func TestResumed(t *testing.T) {
	m := lm.New()
	l, other := &testLoad{title: "heater"}, &testLoad{title: "other"}
	m.SetGuardLookup(func(lm.Load) time.Duration { return 0 })

	now := time.Now()

	_, ok := m.Resumed(l, now)
	assert.False(t, ok, "never shed")

	m.Shed(l, now)
	_, ok = m.Resumed(other, now)
	assert.False(t, ok, "another load")

	d, ok := m.Resumed(l, now.Add(12*time.Minute))
	assert.True(t, ok)
	assert.Equal(t, 12*time.Minute, d)

	_, ok = m.Resumed(l, now.Add(13*time.Minute))
	assert.False(t, ok, "once per shed")
}

// TestReleaseGuards: the loads held off are counted and may run again at once
func TestReleaseGuards(t *testing.T) {
	m := lm.New()
	a, b, free := &testLoad{title: "a"}, &testLoad{title: "b"}, &testLoad{title: "free"}
	m.SetGuardLookup(func(l lm.Load) time.Duration {
		if l == free {
			return 0
		}
		return 5 * time.Minute
	})

	now := time.Now()
	m.Shed(a, now)
	m.Shed(b, now.Add(-4*time.Minute))
	m.Shed(free, now)

	assert.Equal(t, 2, m.GuardedCount(now))
	assert.Equal(t, 1, m.GuardedCount(now.Add(2*time.Minute)), "b is over")

	assert.Equal(t, 2, m.ReleaseGuards(now))
	assert.Zero(t, m.Guarded(a, now))
	assert.Zero(t, m.Guarded(b, now))
	assert.Zero(t, m.GuardedCount(now))
	assert.Zero(t, m.ReleaseGuards(now), "nothing left to lift")

	// a shed that follows is held again
	m.Shed(a, now)
	assert.Equal(t, 5*time.Minute, m.Guarded(a, now))

	// "switched on again" still comes after a release
	_, ok := m.Resumed(free, now.Add(time.Minute))
	assert.True(t, ok)
}
