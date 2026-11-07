package core

import (
	"testing"
	"time"

	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/db/settings"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setPeakLimit sets evcc's limit and what the next cycle takes from it
func setPeakLimit(site *Site, limit float64) {
	site.gridPeakBaseline = limit
	s := site.peak()
	s.limit, s.baseline = limit, limit
}

func TestPeakLimitFor(t *testing.T) {
	assert.Equal(t, 9000.0, peakLimitFor(9000, 0), "no peak yet this month")
	assert.Equal(t, 9000.0, peakLimitFor(9000, 6320), "peak below the limit")
	assert.Equal(t, 12300.0, peakLimitFor(9000, 12345), "rounded down to 100 W")
	assert.Equal(t, 9000.0, peakLimitFor(9000, 9050), "never below the limit")
	assert.Equal(t, maxPeakLimit, peakLimitFor(9000, 30000), "capped")
	assert.Equal(t, 25000.0, peakLimitFor(25000, 30000), "a limit above the cap stays")
	assert.Zero(t, peakLimitFor(0, 6320), "no limit, no peak shaving")
	assert.Equal(t, minPeakLimit, peakLimitFor(1000, 0), "the battery's floor")
}

// The limit in evcc's dialog and the one set here are one: the battery takes
// either at once, with the month's peak on top, and a new month starts at the limit.
func TestPeakLimitIsEvccs(t *testing.T) {
	site, clk := peakWindowSite(t, 0)
	s := site.peak()
	setPeakShaving(site, 9000, 20)

	require.NoError(t, site.SetPeakShavingLimit(8000))
	assert.Equal(t, 8000.0, site.GetGridPeakBaseline())
	assert.Equal(t, 8000.0, site.GetPeakShavingLimit())

	require.NoError(t, site.SetGridPeakBaseline(7000))
	site.updatePeakLimit()
	assert.Equal(t, 7000.0, site.GetPeakShavingLimit())

	s.months = []peakMonth{{Month: clk.Now().Local().Format("2006-01"), Peak: 11820}}
	site.updatePeakLimit()
	assert.Equal(t, 11800.0, site.GetPeakShavingLimit())
	assert.Equal(t, 7000.0, site.GetGridPeakBaseline(), "evcc's limit stays")

	clk.Set(clk.Now().AddDate(0, 1, 0))
	site.updatePeakLimit()
	assert.Equal(t, 7000.0, site.GetPeakShavingLimit())
}

// Without a limit the battery keeps no reserve instead of covering all of the demand
func TestPeakLimitNone(t *testing.T) {
	site, _ := peakWindowSite(t, 0)
	setPeakShaving(site, 9000, 20)

	on, _, _ := site.peakShavingConfigured()
	assert.True(t, on)

	require.NoError(t, site.SetGridPeakBaseline(0))
	site.updatePeakLimit()

	on, limit, _ := site.peakShavingConfigured()
	assert.False(t, on)
	assert.Zero(t, limit)
}

// The switch does not depend on the limit: the card then says that one is missing
func TestPeakSwitchWithoutLimit(t *testing.T) {
	sc := newScenario(t)
	require.NoError(t, sc.site.SetPeakShaving(false))
	require.NoError(t, sc.site.SetGridPeakBaseline(0))
	sc.site.updatePeakLimit()

	require.NoError(t, sc.site.SetPeakShaving(true))
	assert.True(t, sc.site.GetPeakShaving())
}

// A limit raised in evcc's dialog counts for the month's savings like one set here
func TestPeakLimitRaisesBaseline(t *testing.T) {
	site, clk := peakWindowSite(t, 0)
	s := site.peak()
	setPeakShaving(site, 5000, 20)
	s.months = []peakMonth{{Month: clk.Now().Local().Format("2006-01"), Baseline: 5000}}

	require.NoError(t, site.SetGridPeakBaseline(6000))
	site.updatePeakLimit()
	assert.Equal(t, 6000.0, s.months[0].Baseline)

	require.NoError(t, site.SetGridPeakBaseline(4000))
	site.updatePeakLimit()
	assert.Equal(t, 6000.0, s.months[0].Baseline, "lowering keeps it")

	// a new month starts with the limit now
	clk.Set(clk.Now().AddDate(0, 1, 0).Add(-time.Hour))
	m, _ := s.peakMonthOf(clk.Now())
	assert.Equal(t, 4000.0, m.Baseline)
}

func TestMigratePeakLimit(t *testing.T) {
	for _, tc := range []struct {
		name     string
		stored   map[string]string
		baseline float64
		want     float64
	}{
		{"taken over", map[string]string{keys.PeakShaving: "true", keys.PeakShavingLimit: "9000"}, 0, 9000},
		{"evcc's limit stays", map[string]string{keys.PeakShaving: "true", keys.PeakShavingLimit: "9000"}, 7000, 7000},
		{"following: the limit set by hand", map[string]string{keys.PeakShaving: "true", keys.PeakShavingLimit: "11300", keys.PeakFollow: "true", keys.PeakFollowBase: "9000", keys.PeakFollowBuffer: "500"}, 0, 9000},
		{"not following: the old base does not count", map[string]string{keys.PeakShaving: "true", keys.PeakShavingLimit: "8000", keys.PeakFollow: "false", keys.PeakFollowBase: "9000"}, 0, 8000},
		{"peak shaving off: the loads stay unlimited", map[string]string{keys.PeakShaving: "false", keys.PeakShavingLimit: "9000"}, 0, 0},
		{"nothing stored: the default that was in use", map[string]string{keys.PeakShaving: "true"}, 0, 5000},
		{"invalid: the default", map[string]string{keys.PeakShaving: "true", keys.PeakShavingLimit: "0"}, 0, 5000},
		{"nothing stored, peak shaving off", map[string]string{}, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			site, _ := restoreTestSite(t)
			for k, v := range tc.stored {
				settings.SetString(k, v)
			}
			site.gridPeakBaseline = tc.baseline

			site.migratePeakLimit()

			assert.Equal(t, tc.want, site.GetGridPeakBaseline())
			for _, k := range []string{keys.PeakShavingLimit, keys.PeakFollow, keys.PeakFollowBase, keys.PeakFollowBuffer} {
				_, err := settings.String(k)
				assert.Error(t, err, "%s removed", k)
			}
		})
	}
}
