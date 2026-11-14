package metrics

import (
	"testing"
	"time"

	"github.com/evcc-io/evcc/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestClearFeedIn: only the feed-in of the grid meter in the range is set to 0,
// the draw, the other groups and the slots outside stay, and the revenue follows
func TestClearFeedIn(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, SetupSchema())
	require.NoError(t, db.Instance.AutoMigrate(new(tariffValue), new(eegPrice)))

	grid, err := createEntity(Grid, Grid, Grid)
	require.NoError(t, err)
	bat, err := createEntity(Battery, "battery", "battery")
	require.NoError(t, err)
	home, err := createEntity(Home, "home", "home")
	require.NoError(t, err)
	eeg, err := createEntity(Meter, FeedInEeg, "EEG")
	require.NoError(t, err)

	loc := time.UTC
	local := time.Local
	time.Local = loc
	t.Cleanup(func() { time.Local = local })

	day := func(d, h int) time.Time { return time.Date(2026, 9, d, h, 0, 0, 0, loc) }
	price := func(v float64) *float64 { return &v }

	for _, d := range []int{9, 10, 11, 12} {
		ts := day(d, 12)
		require.NoError(t, persist(grid, ts, 2, 3, nil, false)) // draw 2, feed-in 3
		require.NoError(t, persist(bat, ts, 1, 4, nil, false))
		require.NoError(t, persist(home, ts, 5, 0, nil, false))
		require.NoError(t, persist(eeg, ts, 1, 0, nil, false))
		require.NoError(t, PersistTariffs(ts, price(0.3), price(0.08), nil, nil))
		require.NoError(t, PersistEegPrice(ts, 0.12))
	}

	// the days 10 and 11 including, as the ui sends them
	from, to := day(10, 0), day(12, 0)

	energy, revenue, err := FeedInSummary(from, to)
	require.NoError(t, err)
	assert.InDelta(t, 6, energy, 1e-9)
	assert.InDelta(t, 2*(1*0.12+2*0.08), revenue, 1e-9, "1 kWh each day at the EEG price, 2 at the standard")

	// asking changes nothing
	energy, _, err = FeedInSummary(from, to)
	require.NoError(t, err)
	assert.InDelta(t, 6, energy, 1e-9)

	rows, err := ClearFeedIn(from, to)
	require.NoError(t, err)
	assert.EqualValues(t, 2, rows)

	energy, revenue, err = FeedInSummary(from, to)
	require.NoError(t, err)
	assert.Zero(t, energy)
	assert.Zero(t, revenue)

	// the revenue in the history is 0 for the range, as before outside
	series, err := QueryEnergy(from, to, "day", true, EnergyFilter{Group: Grid})
	require.NoError(t, err)
	require.NotEmpty(t, series)
	for _, s := range series {
		for _, d := range s.Data {
			assert.Zero(t, d.ReturnEnergy)
			if d.ReturnCost != nil {
				assert.Zero(t, *d.ReturnCost)
			}
			assert.Positive(t, d.Energy, "the draw stays")
		}
	}

	// every group as stored, outside the range the feed-in too
	all, err := QueryEnergy(day(1, 0), day(30, 0), "month", true)
	require.NoError(t, err)

	got := map[string][2]float64{}
	for _, s := range all {
		for _, d := range s.Data {
			got[s.Group] = [2]float64{d.Energy, d.ReturnEnergy}
		}
	}
	assert.Equal(t, [2]float64{8, 6}, got[Grid], "draw 4 x 2, feed-in 2 x 3 left")
	assert.Equal(t, [2]float64{4, 16}, got[Battery])
	assert.Equal(t, [2]float64{20, 0}, got[Home])
	assert.Equal(t, [2]float64{2, 0}, got[Meter], "the EEG counter of the range is 0 too")

	// the range must be valid
	_, err = ClearFeedIn(to, from)
	assert.Error(t, err)
	_, _, err = FeedInSummary(time.Time{}, to)
	assert.Error(t, err)
}
