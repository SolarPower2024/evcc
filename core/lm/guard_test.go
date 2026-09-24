package lm_test

import (
	"testing"
	"time"

	"github.com/evcc-io/evcc/core/lm"
	"github.com/stretchr/testify/assert"
)

func TestShedGuard(t *testing.T) {
	lm.Reset()
	t.Cleanup(lm.Reset)

	protected, other := &testLoad{title: "heater"}, &testLoad{title: "other"}

	guard := 5 * time.Minute
	lm.SetGuardLookup(func(l lm.Load) time.Duration {
		if l == protected {
			return guard
		}
		return 0
	})

	now := time.Now()

	// a load that is not protected is not held off
	assert.Zero(t, lm.Shed(other, now))
	assert.Zero(t, lm.Guarded(other, now))

	assert.Equal(t, guard, lm.Shed(protected, now))
	assert.Equal(t, guard, lm.Guarded(protected, now))
	assert.Equal(t, time.Second, lm.Guarded(protected, now.Add(guard-time.Second)))
	assert.Zero(t, lm.Guarded(protected, now.Add(guard)), "runs again once the guard is over")

	// the guard is over for good, not just at that moment
	assert.Zero(t, lm.Guarded(protected, now))

	// a changed duration applies to a running guard
	lm.Shed(protected, now)
	guard = 10 * time.Minute
	assert.Equal(t, 4*time.Minute, lm.Guarded(protected, now.Add(6*time.Minute)))

	// lifting the protection releases it right away
	guard = 0
	assert.Zero(t, lm.Guarded(protected, now.Add(time.Minute)))
}
