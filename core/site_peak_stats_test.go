package core

import (
	"testing"
	"time"

	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/db/settings"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPeakMonthsWindows verifies that each completed quarter hour goes into its
// month, with and without the battery
func TestPeakMonthsWindows(t *testing.T) {
	site, clk := peakWindowSite(t, 0)
	s := site.peak()

	step := func(grid, battery float64, d time.Duration) {
		for end := clk.Now().Add(d); clk.Now().Before(end); {
			clk.Add(30 * time.Second)
			site.updatePeakWindow(grid, battery)
		}
	}

	// 10:00 to 10:15: 4kW from the grid while the battery discharges 1kW
	site.updatePeakWindow(4000, 1000)
	step(4000, 1000, 15*time.Minute)
	require.Len(t, s.months, 1)
	assert.InDelta(t, 4000, s.months[0].Peak, 0.001)
	assert.InDelta(t, 5000, s.months[0].Demand, 0.001)

	// 10:15 to 10:30: 6kW, 2kW of it charge the battery
	step(6000, -2000, 15*time.Minute)

	m := s.months[0]
	assert.Equal(t, "2026-09", m.Month)
	assert.InDelta(t, 6000, m.Peak, 0.001)
	assert.Equal(t, time.Date(2026, 9, 25, 10, 15, 0, 0, time.UTC), m.PeakAt.UTC())
	assert.InDelta(t, 5000, m.Demand, 0.001, "4kW without the battery is below the first window")
	assert.Equal(t, time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC), m.DemandAt.UTC())
	assert.True(t, s.monthsDirty)

	// the battery charging from pv does not count as demand
	step(1000, -3000, 15*time.Minute)
	assert.InDelta(t, 5000, s.months[0].Demand, 0.001)
}

// TestPeakMonthsPartialWindow verifies that a window evcc did not see from its
// start is left out
func TestPeakMonthsPartialWindow(t *testing.T) {
	site, clk := peakWindowSite(t, 5)
	s := site.peak()

	site.updatePeakWindow(9000, 0)
	for range 20 {
		clk.Add(30 * time.Second)
		site.updatePeakWindow(9000, 0)
	}
	assert.Empty(t, s.months, "10:05 to 10:15 was not seen from its start")

	for range 30 {
		clk.Add(30 * time.Second)
		site.updatePeakWindow(3000, 0)
	}
	require.Len(t, s.months, 1)
	assert.InDelta(t, 3000, s.months[0].Peak, 0.001)
}

// TestPeakMonthsInterventions verifies that each covered peak is counted once
// and the statistics are saved
func TestPeakMonthsInterventions(t *testing.T) {
	sc := newScenario(t)
	sc.site.peak().limit = 5000

	sc.cycle(20, 8000, 0) // peak starts
	sc.cycle(20, 8000, 3000)
	sc.cycle(20, 4000, 0) // over
	sc.cycle(20, 9000, 0) // the next one

	s := sc.site.peak()
	require.Len(t, s.months, 1)
	assert.Equal(t, 2, s.months[0].Interventions)
	assert.False(t, s.monthsDirty, "saved at the end of the cycle")

	var saved []peakMonth
	require.NoError(t, settings.Json(keys.PeakMonths, &saved))
	assert.Equal(t, 2, saved[0].Interventions)

	// restored after a restart
	site := &Site{log: sc.site.log}
	site.restorePeakMonths()
	assert.Equal(t, saved, site.peak().months)
}

// TestPeakMonthsKept verifies that the oldest months are dropped
func TestPeakMonthsKept(t *testing.T) {
	var s peakState

	start := time.Date(2024, 1, 15, 12, 0, 0, 0, time.Local)
	for i := range 30 {
		s.recordPeakWindow(start.AddDate(0, i, 0), 1000, 1000)
	}

	require.Len(t, s.months, peakMonthsKept)
	assert.Equal(t, "2026-06", s.months[0].Month)
	assert.Equal(t, "2024-07", s.months[peakMonthsKept-1].Month)
}
