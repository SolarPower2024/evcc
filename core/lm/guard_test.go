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
