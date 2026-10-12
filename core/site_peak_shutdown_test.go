package core

import (
	"net/http"
	"testing"
	"time"

	"github.com/evcc-io/evcc/util"
	"github.com/evcc-io/evcc/util/homeassistant"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// shutdownBudget sets the time the shutdown function may take for the test
func shutdownBudget(t *testing.T, d time.Duration) {
	t.Helper()

	prev := peakShutdownBudget
	peakShutdownBudget = d
	t.Cleanup(func() { peakShutdownBudget = prev })
}

// TestPeakReleasedOnShutdown verifies that stopping evcc hands back what the fork
// holds: the grid charge power and the manual control of a Marstek, the limit of
// Omnibattery's peak shaving (it stays on, with the peak limit instead of the
// allowed power of the window), the charge power and the setpoint of a BYD. The
// cycle writes nothing any more afterwards.
func TestPeakReleasedOnShutdown(t *testing.T) {
	t.Run("marstek", func(t *testing.T) {
		sc, ha := charging(t)
		ha.set(haProtLimit, "6700") // the window raised it, the peak limit is 5000

		sc.site.releasePeakOnShutdown()
		assert.Equal(t, []string{
			"set_value " + haChargeNum + " 0",
			"turn_off " + haSwitch,
			"set_value " + haProtLimit + " 5000",
		}, ha.take())
		assert.False(t, sc.site.peakOwned())
		assert.True(t, sc.site.peak().protOwned, "the peak shaving of Omnibattery stays on")
		assert.Equal(t, "on", ha.get(haProtSwitch))

		sc.cycle(20, 1000, 0)
		sc.cycle(25, 8000, 0)
		assert.Empty(t, ha.take(), "the cycle writes nothing any more")
	})

	t.Run("byd", func(t *testing.T) {
		sc, ha := newOmni(t)
		sc.site.lms().socChargeEnabled = true

		s := sc.site.peak()
		s.batteryType = batteryTypeBYD
		require.NoError(t, sc.site.rebuildPeakSetter())
		require.NoError(t, sc.site.rebuildChargeSetter())

		require.True(t, sc.cycle(20, 1000, 0))
		require.True(t, sc.site.peakOwned())
		ha.take()

		sc.site.releasePeakOnShutdown()
		assert.Equal(t, []string{
			"set_value " + haChargeNum + " 0",
			"set_value " + haDischargeNum + " 10000",
		}, ha.take())
		assert.False(t, sc.site.peakOwned())

		sc.cycle(20, 1000, 0)
		sc.cycle(25, 8000, 0)
		assert.Empty(t, ha.take(), "the cycle writes nothing any more")
	})

	t.Run("nothing held", func(t *testing.T) {
		sc, ha := newOmni(t)
		sc.site.releasePeakOnShutdown()
		assert.Empty(t, ha.take())

		sc, ha = newOmni(t)
		sc.site.peak().batteryType = batteryTypeBYD
		require.NoError(t, sc.site.rebuildPeakSetter())
		sc.site.releasePeakOnShutdown()
		assert.Empty(t, ha.take())
	})
}

// TestPeakShutdownRegisteredOnce verifies that the settings restore registers the
// hand back with evcc's shutdown functions, once
func TestPeakShutdownRegisteredOnce(t *testing.T) {
	var registered []func()

	prev := registerShutdown
	registerShutdown = func(cb func()) { registered = append(registered, cb) }
	t.Cleanup(func() { registerShutdown = prev })

	sc, ha := charging(t)
	sc.site.restorePeakSettings()
	sc.site.restorePeakSettings()

	require.Len(t, registered, 1)

	registered[0]()
	assert.False(t, sc.site.peakOwned())
	assert.Contains(t, ha.take(), "turn_off "+haSwitch)
}

// TestPeakShutdownDoesNotBlock verifies that the stop is not held up by a Home
// Assistant that does not answer or refuses the call (the supervisor does once the
// host shuts down): the hand back returns within its budget, what was not handed
// back stays marked as held, and the refusal is no error.
func TestPeakShutdownDoesNotBlock(t *testing.T) {
	shutdownBudget(t, 100*time.Millisecond)

	t.Run("no answer", func(t *testing.T) {
		sc, _ := charging(t)
		h := hangHA(sc)
		sc.site.peak().conn.Client.Timeout = time.Second // longer than the budget
		h.hang.Store(true)

		start := time.Now()
		sc.site.releasePeakOnShutdown()
		assert.Less(t, time.Since(start), 900*time.Millisecond)
		assert.True(t, sc.site.peakOwned(), "still held, released with the next start")
	})

	t.Run("refused", func(t *testing.T) {
		sc, ha := charging(t)
		ha.fail["switch/turn_off"] = http.StatusBadRequest

		sc.site.releasePeakOnShutdown()
		assert.True(t, sc.site.peakOwned(), "still held, released with the next start")

		s := sc.site.peak()
		s.mu.Lock()
		defer s.mu.Unlock()
		assert.Empty(t, s.writeErr, "expected while stopping: no error of an output")
	})
}

// timedOut lets the first call of a cycle time out, as one to a Home Assistant
// that is slow does, and checks that the guard then refuses the calls. The
// scenario's Home Assistant answers again afterwards.
func timedOut(t *testing.T, sc *scenario) {
	t.Helper()

	h := hangHA(sc)
	conn := sc.site.peak().conn

	h.hang.Store(true)
	_, _, err := numberState(conn, haChargeNum)
	require.Error(t, err)
	h.hang.Store(false)

	_, _, err = numberState(conn, haChargeNum)
	require.ErrorIs(t, err, errHAHung)
}

// TestPeakReleasedAfterTimeout verifies that the hand back is not refused by the
// guard when evcc stops after a call of the running cycle timed out: the shutdown
// function runs beside the cycle, which ends the refusals only at its end.
func TestPeakReleasedAfterTimeout(t *testing.T) {
	t.Run("marstek", func(t *testing.T) {
		sc, ha := charging(t)
		timedOut(t, sc)

		sc.site.releasePeakOnShutdown()
		assert.Equal(t, []string{
			"set_value " + haChargeNum + " 0",
			"turn_off " + haSwitch,
		}, ha.take())
		assert.False(t, sc.site.peakOwned())
		assert.Equal(t, "off", ha.get(haSwitch))
	})

	t.Run("byd", func(t *testing.T) {
		sc, ha := newOmni(t)
		sc.site.lms().socChargeEnabled = true

		s := sc.site.peak()
		s.batteryType = batteryTypeBYD
		require.NoError(t, sc.site.rebuildPeakSetter())
		require.NoError(t, sc.site.rebuildChargeSetter())

		require.True(t, sc.cycle(20, 1000, 0))
		require.True(t, sc.site.peakOwned())
		ha.take()

		timedOut(t, sc)

		sc.site.releasePeakOnShutdown()
		assert.Equal(t, []string{
			"set_value " + haChargeNum + " 0",
			"set_value " + haDischargeNum + " 10000",
		}, ha.take())
		assert.False(t, sc.site.peakOwned())
	})

	t.Run("guard of a connection built while stopping", func(t *testing.T) {
		t.Setenv(homeassistant.SupervisorToken, "token")

		site := &Site{log: util.NewLogger("test")}
		site.haStop()

		g, ok := site.peak().conn.Client.Transport.(*haGuard)
		require.True(t, ok)
		assert.True(t, g.stopping)
	})
}
