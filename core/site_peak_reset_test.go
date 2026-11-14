package core

import (
	"testing"
	"time"

	"github.com/benbjohnson/clock"
	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/core/metrics"
	"github.com/evcc-io/evcc/db"
	"github.com/evcc-io/evcc/db/settings"
	"github.com/evcc-io/evcc/util"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// resetSite is a site with a grid meter whose slots are in the database: a spike
// of 12 kW at 12:00, 4 kW afterwards, the clock at 13:05
func resetSite(t *testing.T) (*Site, *metrics.Collector, *clock.Mock) {
	t.Helper()

	t.Cleanup(func() {
		_ = settings.Delete(keys.GridPeakBaseline)
		_ = settings.Delete(keys.PeakResetAt)
		_ = settings.Delete(keys.PeakMonths)
	})

	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, metrics.SetupSchema())

	clk := clock.NewMock()
	clk.Set(time.Date(2026, 10, 24, 12, 0, 0, 0, time.Local))

	col, err := metrics.NewCollector(metrics.Grid, "grid", "grid", metrics.WithClock(clk))
	require.NoError(t, err)

	for _, power := range []float64{12000, 12000, 4000, 4000} {
		require.NoError(t, col.AddEnergy(nil, nil, power))
		clk.Add(15 * time.Minute)
	}
	require.NoError(t, col.AddEnergy(nil, nil, 4000))
	clk.Add(5 * time.Minute)

	site := &Site{
		log:        util.NewLogger("foo"),
		Meters:     MetersConfig{GridMeterRef: "grid"},
		collectors: map[string]*metrics.Collector{"grid": col},
	}
	site.custom.peak.clock = clk
	require.NoError(t, site.SetGridPeakBaseline(8000))

	// the statistics saw the spike
	site.custom.peak.months = []peakMonth{{
		Month: clk.Now().Local().Format("2006-01"), Peak: 12000, PeakAt: time.Date(2026, 10, 24, 12, 0, 0, 0, time.Local),
		Demand: 12000, DemandAt: time.Date(2026, 10, 24, 12, 0, 0, 0, time.Local), Interventions: 2,
	}}
	site.updateGridPeak()
	site.updatePeakLimit()

	return site, col, clk
}

// TestResetMonthPeak: after the reset only later quarter hours count, for evcc's
// limit and for the statistics, and the measurements stay
func TestResetMonthPeak(t *testing.T) {
	site, col, clk := resetSite(t)

	assert.Equal(t, 12000.0, site.GetGridPeakTarget(), "the spike counts")
	assert.Equal(t, 12000.0, site.GetPeakShavingLimit())

	before, err := col.DailyPeakEnergy()
	require.NoError(t, err)

	require.NoError(t, site.ResetMonthPeak())

	assert.Equal(t, 8000.0, site.GetGridPeakTarget(), "evcc's limit again")
	assert.Equal(t, 8000.0, site.GetPeakShavingLimit(), "the battery's limit with it")

	m := site.custom.peak.months[0]
	assert.Zero(t, m.Peak)
	assert.Zero(t, m.Demand)
	assert.True(t, m.PeakAt.IsZero() && m.DemandAt.IsZero())
	assert.Equal(t, 2, m.Interventions, "counts of the month stay")

	var saved []peakMonth
	require.NoError(t, settings.Json(keys.PeakMonths, &saved))
	assert.Zero(t, saved[0].Peak, "stored")

	// the metrics are as they were
	after, err := col.DailyPeakEnergy()
	require.NoError(t, err)
	assert.Equal(t, before, after)
	assert.InDelta(t, 3.0, *after[len(after)-1], 1e-9, "12 kW over a quarter hour")

	// the quarter hours before the reset are left out in the days that are published
	days := site.peakAfterReset(col, after)
	require.Len(t, days, len(after))
	for _, d := range days {
		assert.Nil(t, d)
	}

	// the quarter hour that follows counts: 9 kW
	for range 3 {
		require.NoError(t, col.AddEnergy(nil, nil, 9000))
		clk.Add(15 * time.Minute)
	}
	site.updateGridPeak()
	assert.InDelta(t, 9000, site.GetGridPeakTarget(), 1e-6)

	// the statistics' window that started before the reset is not taken in
	at := site.peakResetAt()
	site.custom.peak.recordPeakWindow(at.Add(-5*time.Minute), 12000*900, 12000*900)
	assert.Zero(t, site.custom.peak.months[0].Peak)
	site.custom.peak.recordPeakWindow(at.Add(10*time.Minute), 7000*900, 7000*900)
	assert.InDelta(t, 7000, site.custom.peak.months[0].Peak, 1e-6)
}

// TestResetMonthPeakRestart: the reset survives a restart
func TestResetMonthPeakRestart(t *testing.T) {
	site, _, clk := resetSite(t)
	require.NoError(t, site.ResetMonthPeak())

	restarted := &Site{log: util.NewLogger("foo")}
	restarted.restorePeakReset()
	assert.True(t, restarted.peakResetAt().Equal(clk.Now()), "%v", restarted.peakResetAt())
}

// TestResetMonthPeakLastMonth: a reset of an earlier month has no effect
func TestResetMonthPeakLastMonth(t *testing.T) {
	site, col, _ := resetSite(t)

	s := site.peak()
	s.resetAt = time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local)
	site.updateGridPeak()
	assert.Equal(t, 12000.0, site.GetGridPeakTarget())

	energy, err := col.DailyPeakEnergy()
	require.NoError(t, err)
	assert.Equal(t, energy, site.peakAfterReset(col, energy))
}
