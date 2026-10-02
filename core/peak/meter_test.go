package peak_test

import (
	"testing"
	"time"

	"github.com/evcc-io/evcc/core/peak"
	"github.com/stretchr/testify/assert"
)

// harness feeds a meter samples at a clock it controls, with a 5 kW limit
type harness struct {
	m   peak.Meter
	now time.Time
	set peak.Settings
	st  peak.State
}

// newHarness starts at the given minute of a window
func newHarness(minute int) *harness {
	return &harness{
		now: time.Date(2026, 9, 25, 10, minute, 0, 0, time.UTC),
		set: peak.Settings{Limit: 5000, Freeze: peak.DefaultFreeze, Cap: peak.DefaultCap},
	}
}

// update samples the grid power now, without a counter
func (h *harness) update(grid float64) {
	h.st = h.m.Update(peak.Sample{Now: h.now, GridPower: grid, Source: peak.SourcePower}, h.set)
}

// sample updates every 30s for the given duration
func (h *harness) sample(grid float64, d time.Duration) {
	for end := h.now.Add(d); h.now.Before(end); {
		h.now = h.now.Add(peak.Cycle)
		h.update(grid)
	}
}

// counterNow samples the grid power and the grid meter's counter in kWh now
func (h *harness) counterNow(grid, kWh float64) {
	h.st = h.m.Update(peak.Sample{Now: h.now, GridPower: grid, Energy: kWh, Source: peak.SourceMeter}, h.set)
}

// counter advances by 30s and samples with the counter
func (h *harness) counter(grid, kWh float64) {
	h.now = h.now.Add(peak.Cycle)
	h.counterNow(grid, kWh)
}

// TestMeterBudget verifies that energy not drawn earlier in the window
// allows more later, and the limits on that
func TestMeterBudget(t *testing.T) {
	h := newHarness(0)

	h.update(0)
	assert.Equal(t, 5000.0, h.st.Allowed)

	// 5 minutes without drawing anything
	h.sample(0, 5*time.Minute)
	assert.InDelta(t, 7500, h.st.Allowed, 0.001)

	// a 9kW spike needs only what exceeds the allowed power
	assert.Equal(t, 1500.0, peak.Setpoint(9000, 0, h.st.Allowed))

	// drawing exactly the allowed power keeps it where it is
	h.sample(7500, 2*time.Minute)
	assert.InDelta(t, 7500, h.st.Allowed, 0.001)

	// the cap: 10 minutes without drawing would allow 15kW, at most 2 x 5kW
	h = newHarness(0)
	h.update(0)
	h.sample(0, 10*time.Minute)
	assert.Equal(t, 10000.0, h.st.Allowed)
}

// TestMeterFreeze verifies that from the freeze minute on the allowed
// power no longer grows but still falls
func TestMeterFreeze(t *testing.T) {
	h := newHarness(0)
	h.set.Cap = 10

	h.update(0)
	h.sample(0, 12*time.Minute)
	assert.InDelta(t, 25000, h.st.Allowed, 0.001, "12 minutes unused leave 75kWmin for 3 minutes")

	// without the freeze it would be 37.5kW one minute later
	h.sample(0, time.Minute)
	assert.InDelta(t, 25000, h.st.Allowed, 0.001)

	// 40kW for a minute would leave 35kW for the last minute, still capped by the freeze
	h.sample(40000, time.Minute)
	assert.InDelta(t, 25000, h.st.Allowed, 0.001)

	// drawing more than allowed still lowers it: 60kW for 30s leaves 10kW for the last 30s
	h.sample(60000, 30*time.Second)
	assert.InDelta(t, 10000, h.st.Allowed, 0.001)

	// a new window starts on track again
	h.sample(5000, time.Minute)
	assert.Equal(t, 5000.0, h.st.Allowed)
}

// TestMeterUnmetered verifies that the part of a window evcc did not see
// counts at the limit, and that a sample from the previous window carries over
func TestMeterUnmetered(t *testing.T) {
	// evcc started 5 minutes into the window: nothing is known to be left over
	h := newHarness(5)
	h.update(0)
	assert.Equal(t, 5000.0, h.st.Allowed)

	h.sample(0, 5*time.Minute)
	assert.InDelta(t, 10000, h.st.Allowed, 0.001, "5 unused minutes for the last 5")

	// crossing into the next window, the 30s since the boundary are metered
	h.now = time.Date(2026, 9, 25, 10, 14, 50, 0, time.UTC)
	h.update(0)
	h.now = time.Date(2026, 9, 25, 10, 15, 20, 0, time.UTC)
	h.update(6000)

	assert.Equal(t, time.Date(2026, 9, 25, 10, 15, 0, 0, time.UTC), h.st.MeteredFrom)
	assert.InDelta(t, 6000*20, h.st.DrawnWs, 0.001)
	assert.InDelta(t, 6000, h.st.Avg, 0.001)

	// after a longer gap the next window is not metered from its start
	h.now = time.Date(2026, 9, 25, 10, 33, 0, 0, time.UTC)
	h.update(0)
	assert.Equal(t, h.now, h.st.MeteredFrom)
	assert.Equal(t, 5000.0, h.st.Allowed)
}

// TestMeterLateCounter verifies that a counter updating less often than
// evcc samples is followed, and that one which stopped is replaced by the grid
// power for the rest of the window
func TestMeterLateCounter(t *testing.T) {
	h := newHarness(0)

	// the counter moves every 90s only, by 0.15kWh = 6kW
	h.counter(6000, 10)
	h.counter(6000, 10)
	h.counter(6000, 10)
	assert.Zero(t, h.st.DrawnWs, "a late counter is not replaced right away")
	h.counter(6000, 10.15)
	assert.InDelta(t, 540000, h.st.DrawnWs, 0.001)

	// it stops: after 2 minutes the grid power takes over, including what was
	// drawn while it stood still
	for range 4 {
		h.counter(6000, 10.15)
	}
	assert.False(t, h.st.Stale)
	assert.InDelta(t, 540000, h.st.DrawnWs, 0.001)

	h.counter(6000, 10.15)
	assert.True(t, h.st.Stale)
	assert.InDelta(t, 540000+5*180000, h.st.DrawnWs, 0.001)

	// its catching up later is not counted a second time
	h.counter(6000, 10.5)
	assert.InDelta(t, 540000+6*180000, h.st.DrawnWs, 0.001)

	// the next window tries the counter again
	h.now = time.Date(2026, 9, 25, 10, 14, 50, 0, time.UTC)
	h.counterNow(6000, 11)
	h.counter(0, 11.05)
	assert.False(t, h.st.Stale)
	assert.Zero(t, h.st.DrawnWs, "the step across the boundary may hold the backlog, the grid power counts")

	h.counter(0, 11.1)
	assert.InDelta(t, 180000, h.st.DrawnWs, 0.001)
	assert.Equal(t, peak.SourceMeter, h.st.Source)
}

// TestMeterCompleted: a window metered from its start is handed out once it
// ends, with the demand including what the battery covered; one evcc joined
// late is not
func TestMeterCompleted(t *testing.T) {
	h := newHarness(5) // joined 5 minutes in
	h.update(0)
	h.sample(6000, 10*time.Minute) // up to 10:15, crossing into the next window
	assert.Nil(t, h.st.Completed, "not metered from its start")

	// 10:15 to 10:30 from its start, 4 kW drawn while the battery covers 2 kW
	for range 30 {
		h.now = h.now.Add(peak.Cycle)
		h.st = h.m.Update(peak.Sample{Now: h.now, GridPower: 4000, BatteryPower: 2000, Source: peak.SourcePower}, h.set)
	}

	c := h.st.Completed
	if assert.NotNil(t, c) {
		assert.Equal(t, time.Date(2026, 9, 25, 10, 15, 0, 0, time.UTC), c.Start)
		assert.InDelta(t, 4000, c.DrawnWs/peak.Window.Seconds(), 0.001)
		assert.InDelta(t, 6000, c.DemandWs/peak.Window.Seconds(), 0.001)
	}
}
