package core

import (
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/benbjohnson/clock"
	"github.com/evcc-io/evcc/db"
	"github.com/evcc-io/evcc/util"
	"github.com/evcc-io/evcc/util/homeassistant"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hangingHA is a Home Assistant that answers like fakeHA, or, while it hangs,
// takes the connection and does not answer: the call waits for the timeout of
// the client.
type hangingHA struct {
	base     http.RoundTripper
	hang     atomic.Bool
	requests atomic.Int32
}

func (h *hangingHA) RoundTrip(req *http.Request) (*http.Response, error) {
	h.requests.Add(1)

	if h.hang.Load() {
		<-req.Context().Done()
		return nil, req.Context().Err()
	}

	return h.base.RoundTrip(req)
}

// hangHA puts a Home Assistant that can hang below the guard of the scenario's
// connection and shortens the timeout of the client. It answers until hang is set.
func hangHA(sc *scenario) *hangingHA {
	conn := sc.site.peak().conn
	g := conn.Client.Transport.(*haGuard)

	h := &hangingHA{base: g.base}
	g.base = h
	conn.Client.Timeout = 20 * time.Millisecond

	return h
}

// TestHAGuardOneTimeoutPerCycle verifies that a Home Assistant that does not
// answer costs one timeout per cycle: the first call times out, the rest of the
// cycle is not called (grid charging and Omnibattery's peak shaving, each of them
// would run into a timeout of its own), and the next cycle asks again.
func TestHAGuardOneTimeoutPerCycle(t *testing.T) {
	sc, ha := newOmni(t)
	sc.site.lms().socChargeEnabled = true
	h := hangHA(sc)
	h.hang.Store(true)

	require.True(t, sc.cycle(20, 1000, 0))
	assert.Equal(t, int32(1), h.requests.Load(), "one request in the cycle")
	assert.Empty(t, ha.take())

	sc.cycle(20, 1000, 0)
	assert.Equal(t, int32(2), h.requests.Load(), "the next cycle asks again")

	// back: everything is done in the cycle
	h.hang.Store(false)
	sc.cycle(20, 1000, 0)
	assert.Equal(t, append([]string{
		"turn_on " + haSwitch,
		"select_option " + haSelect + " Charge",
		"set_value " + haChargeNum + " 4000",
	}, protOn("30", "5000")...), ha.take())
	assert.True(t, sc.site.peakOwned())
	assert.True(t, sc.site.peak().protOwned)
}

// TestHAGuardOneTimeoutPerCycleBYD is the same for the battery type BYD, which
// writes the charge power in the middle of the cycle.
func TestHAGuardOneTimeoutPerCycleBYD(t *testing.T) {
	sc, ha := newOmni(t)
	sc.site.lms().socChargeEnabled = true

	s := sc.site.peak()
	s.batteryType = batteryTypeBYD
	require.NoError(t, sc.site.rebuildChargeSetter())

	h := hangHA(sc)
	h.hang.Store(true)

	require.True(t, sc.cycle(20, 1000, 0))
	assert.Equal(t, int32(1), h.requests.Load(), "the charge power and the peak shaving: one request")

	h.hang.Store(false)
	sc.cycle(20, 1000, 0)
	assert.Contains(t, ha.take(), "set_value "+haChargeNum+" 4000")
}

// TestHAGuardEegCounter verifies the EEG counter, read in the middle of the cycle:
// it is not read after a timeout in the same cycle, and a timeout of its own is
// not asked again for a minute.
func TestHAGuardEegCounter(t *testing.T) {
	sc, _ := newOmni(t) // keeps the settings in memory, the collector needs a database
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	t.Cleanup(func() { db.Instance = nil })

	site := sc.site
	h := hangHA(sc)

	require.NoError(t, site.applyFeedInEegEntity("", false))

	conn := site.peak().conn
	clk := clock.NewMock()
	clk.Set(time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC))

	s := site.eeg()
	s.clock = clk
	s.entity, s.get = "sensor.eeg", func() (float64, error) { return conn.GetFloatState("sensor.eeg") }

	// cycle: the counter times out, Omnibattery's peak shaving at the end is not called
	h.hang.Store(true)
	site.updateFeedInEeg()
	site.updateBatteryModePeakAware(false, false, sc.rate)
	assert.Equal(t, int32(1), h.requests.Load(), "the counter timed out, nothing else was called")

	// the next cycles: no read of the counter for a minute, Omnibattery asks
	clk.Add(15 * time.Second)
	site.updateFeedInEeg()
	assert.Equal(t, int32(1), h.requests.Load(), "the counter is not read again")
	site.updateBatteryModePeakAware(false, false, sc.rate)
	assert.Equal(t, int32(2), h.requests.Load())

	clk.Add(time.Minute)
	site.updateFeedInEeg()
	assert.Equal(t, int32(3), h.requests.Load(), "read again after a minute")
}

// TestHAGuardRefusedCallsAreNotLogged verifies that a call refused after a timeout
// is no error of its own: the call that timed out reported it.
func TestHAGuardRefusedCallsAreNotLogged(t *testing.T) {
	sc, _ := newOmni(t)
	sc.site.lms().socChargeEnabled = true
	h := hangHA(sc)
	h.hang.Store(true)

	sc.cycle(20, 1000, 0)

	s := sc.site.peak()
	s.mu.Lock()
	defer s.mu.Unlock()

	assert.Len(t, s.writeErr, 1, "only the call that timed out")
}

// TestHAGuardInstalled verifies that the connection built for the add-on has the
// guard: the tests above set it up by hand.
func TestHAGuardInstalled(t *testing.T) {
	t.Setenv(homeassistant.SupervisorToken, "token")

	site := &Site{log: util.NewLogger("test")}

	conn, err := site.haConnection()
	require.NoError(t, err)
	assert.IsType(t, &haGuard{}, conn.Client.Transport)
}

// TestNumberSetterReadFailure verifies that an entity that cannot be read is not
// written to: the range is unknown, and an entity Home Assistant has not loaded
// answers a call with success and does nothing.
func TestNumberSetterReadFailure(t *testing.T) {
	sc, ha := newOmni(t)

	ha.mu.Lock()
	delete(ha.states, haDischargeNum)
	ha.mu.Unlock()

	set, err := sc.site.numberSetter(haDischargeNum, true, nil)
	require.NoError(t, err)

	assert.Error(t, set(1000))
	assert.Empty(t, ha.take(), "nothing written")

	// timeout
	h := hangHA(sc)
	h.hang.Store(true)

	assert.Error(t, set(1000))
	assert.Equal(t, int32(1), h.requests.Load(), "a read, no write")
}

// TestOmniReleaseHangs verifies that a release that cannot be read keeps what
// evcc holds: the next cycle tries again.
func TestOmniReleaseHangs(t *testing.T) {
	sc, ha := charging(t)

	h := hangHA(sc)
	h.hang.Store(true)

	// the charge ends: the charge power to zero, then the switch
	sc.cycle(90, 1000, 0)
	assert.Equal(t, int32(1), h.requests.Load())
	assert.True(t, sc.site.peakOwned(), "still held")
	assert.True(t, sc.site.peak().protOwned, "still held")

	h.hang.Store(false)
	sc.cycle(90, 1000, 0)
	assert.False(t, sc.site.peakOwned())
	assert.Equal(t, "off", ha.get(haSwitch))
}
