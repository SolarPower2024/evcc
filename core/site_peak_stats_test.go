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

// TestPeakBaseline verifies that a new month takes the limit set by hand and that
// setting the limit raises the month's baseline, lowering it does not
func TestPeakBaseline(t *testing.T) {
	site, clk := peakWindowSite(t, 0)
	s := site.peak()
	s.limit = 7500

	// a new month starts with the limit set by hand
	site.updatePeakWindow(4000, 0)
	for range 31 {
		clk.Add(30 * time.Second)
		site.updatePeakWindow(4000, 0)
	}
	require.Len(t, s.months, 1)
	assert.Equal(t, 7500.0, s.months[0].Baseline)

	require.NoError(t, site.SetPeakShavingLimit(9000))
	assert.Equal(t, 9000.0, s.months[0].Baseline)
	assert.False(t, s.monthsDirty, "saved")

	require.NoError(t, site.SetPeakShavingLimit(6000))
	assert.Equal(t, 6000.0, s.limit)
	assert.Equal(t, 9000.0, s.months[0].Baseline, "lowering the limit keeps the baseline")

	// the next month starts with the current limit, the old one stays
	clk.Set(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	require.NoError(t, site.SetPeakShavingLimit(6500))
	require.Len(t, s.months, 2)
	assert.Equal(t, "2026-10", s.months[0].Month)
	assert.Equal(t, 6500.0, s.months[0].Baseline)
	assert.Equal(t, 9000.0, s.months[1].Baseline)

	// lowered on the first of a month: the new month starts with the new limit
	clk.Set(time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC))
	require.NoError(t, site.SetPeakShavingLimit(5000))
	require.Len(t, s.months, 3)
	assert.Equal(t, 5000.0, s.months[0].Baseline)

	var saved []peakMonth
	require.NoError(t, settings.Json(keys.PeakMonths, &saved))
	assert.Equal(t, s.months, saved)
}

// TestPeakBaselineFollow verifies that following the peak counts the base, not
// the raised limit
func TestPeakBaselineFollow(t *testing.T) {
	site, clk := peakWindowSite(t, 0)
	s := site.peak()
	s.limit = 5000
	s.followBuffer = defaultPeakFollowBuffer
	s.months = []peakMonth{{Month: "2026-09", Peak: 12000, Baseline: 5000}}

	require.NoError(t, site.SetPeakFollow(true))
	assert.Equal(t, 11500.0, s.limit, "raised to the month's peak")
	assert.Equal(t, 5000.0, s.manualPeakLimit())

	// a new month while the limit is raised
	clk.Set(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	assert.Equal(t, 5000.0, s.peakMonthOf(clk.Now()).Baseline)

	// set by hand: the base
	require.NoError(t, site.SetPeakShavingLimit(6000))
	assert.Equal(t, 6000.0, s.months[0].Baseline)
	assert.Equal(t, "2026-10", s.months[0].Month)
}

// TestPeakFillBaselines verifies that only months without a baseline are filled
// and that an entry from before the baseline existed is still read
func TestPeakFillBaselines(t *testing.T) {
	site, _ := peakWindowSite(t, 0)
	s := site.peak()
	s.limit = 7000

	old := `[{"month":"2026-09","peak":5000,"peakAt":"2026-09-25T10:00:00Z","demand":6000,"demandAt":"2026-09-25T10:00:00Z","interventions":2},` +
		`{"month":"2026-08","peak":4000,"peakAt":"2026-08-05T10:00:00Z","demand":4000,"demandAt":"2026-08-05T10:00:00Z","interventions":0,"baseline":4500}]`
	settings.SetString(keys.PeakMonths, old)

	site.restorePeakMonths()
	require.Len(t, s.months, 2)
	assert.Zero(t, s.months[0].Baseline)
	assert.Equal(t, 2, s.months[0].Interventions)

	site.fillPeakBaselines()
	assert.Equal(t, 7000.0, s.months[0].Baseline)
	assert.Equal(t, 4500.0, s.months[1].Baseline, "a stored baseline stays")
	assert.False(t, s.monthsDirty)

	var saved []peakMonth
	require.NoError(t, settings.Json(keys.PeakMonths, &saved))
	assert.Equal(t, s.months, saved)

	// fixed from now on
	s.limit = 8000
	site.fillPeakBaselines()
	assert.Equal(t, 7000.0, s.months[0].Baseline)

	// without a limit nothing is filled
	s.months = []peakMonth{{Month: "2026-09"}}
	s.limit = 0
	site.fillPeakBaselines()
	assert.Zero(t, s.months[0].Baseline)
}
