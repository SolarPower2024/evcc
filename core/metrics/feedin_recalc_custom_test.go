package metrics

import (
	"testing"
	"time"

	"github.com/evcc-io/evcc/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRecalcFeedIn: the feed-in of the grid meter in the range is the balance of
// the slot, everything else stays, the preview changes nothing
func TestRecalcFeedIn(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, SetupSchema())
	require.NoError(t, db.Instance.AutoMigrate(new(tariffValue), new(eegPrice)))

	grid, err := createEntity(Grid, Grid, Grid)
	require.NoError(t, err)
	pv, err := createEntity(PV, "pv", "pv")
	require.NoError(t, err)
	bat, err := createEntity(Battery, "battery", "battery")
	require.NoError(t, err)
	home, err := createEntity(Home, "home", "home")
	require.NoError(t, err)
	lp1, err := createEntity(Loadpoint, "lp1", "lp1")
	require.NoError(t, err)
	lp2, err := createEntity(Loadpoint, "lp2", "lp2")
	require.NoError(t, err)
	eeg, err := createEntity(Meter, FeedInEeg, "EEG")
	require.NoError(t, err)

	local := time.Local
	time.Local = time.UTC
	t.Cleanup(func() { time.Local = local })

	day := func(d, h int) time.Time { return time.Date(2026, 9, d, h, 0, 0, 0, time.UTC) }
	price := func(v float64) *float64 { return &v }

	// feed-in stored as a copy of the draw (2)
	store := func(ts time.Time, pvE, gridIn, batIn, batOut, homeE, lpA, lpB float64) {
		require.NoError(t, persist(grid, ts, gridIn, gridIn, nil, false))
		require.NoError(t, persist(pv, ts, pvE, 0, nil, false))
		require.NoError(t, persist(bat, ts, batIn, batOut, nil, false))
		require.NoError(t, persist(home, ts, homeE, 0, nil, false))
		require.NoError(t, persist(lp1, ts, lpA, 0, nil, false))
		require.NoError(t, persist(lp2, ts, lpB, 0, nil, false))
		require.NoError(t, persist(eeg, ts, 1, 0, nil, false))
		require.NoError(t, PersistTariffs(ts, price(0.3), price(0.08), nil, nil))
		require.NoError(t, PersistEegPrice(ts, 0.12))
	}

	// 10 + 1 + 2 - 3 - 2 - 1 - 1 = 6
	store(day(9, 12), 10, 2, 2, 1, 3, 1, 1)
	store(day(10, 12), 10, 2, 2, 1, 3, 1, 1)
	// negative balance: 1 + 0 + 2 - 5 = -2 -> 0
	store(day(11, 12), 1, 2, 0, 0, 5, 0, 0)
	store(day(12, 12), 10, 2, 2, 1, 3, 1, 1)

	snapshot := func() map[string][2]float64 {
		all, err := QueryEnergy(day(1, 0), day(30, 0), "month", true)
		require.NoError(t, err)
		got := map[string][2]float64{}
		for _, s := range all {
			for _, d := range s.Data {
				got[s.Group] = [2]float64{d.Energy, d.ReturnEnergy}
			}
		}
		return got
	}
	before := snapshot()

	from, to := day(10, 0), day(12, 0)

	sum, err := FeedInRecalcSummary(from, to)
	require.NoError(t, err)
	assert.InDelta(t, 4, sum.OldEnergy, 1e-9)
	assert.InDelta(t, 6, sum.NewEnergy, 1e-9)
	// the EEG counter is 1 per slot, the rest at the standard price
	assert.InDelta(t, 2*(0.12)+2*0.08, sum.OldRevenue, 1e-9)
	assert.InDelta(t, 2*0.12+5*0.08, sum.NewRevenue, 1e-9, "the EEG counter stays, as in the history")
	assert.Zero(t, sum.Skipped)

	// asking changes nothing
	assert.Equal(t, before, snapshot())

	rows, err := RecalcFeedIn(from, to)
	require.NoError(t, err)
	assert.EqualValues(t, 2, rows)

	sum, err = FeedInRecalcSummary(from, to)
	require.NoError(t, err)
	assert.InDelta(t, 6, sum.NewEnergy, 1e-9)
	assert.InDelta(t, 6, sum.OldEnergy, 1e-9, "stored value is the balance now")

	// in a second run nothing changes
	rows, err = RecalcFeedIn(from, to)
	require.NoError(t, err)
	assert.Zero(t, rows)

	energy, revenue, err := FeedInSummary(from, to)
	require.NoError(t, err)
	assert.InDelta(t, 6, energy, 1e-9)
	assert.InDelta(t, 2*0.12+5*0.08, revenue, 1e-9, "revenue is the new feed-in at the price")

	// feed-in of the grid: 2 slots outside stay (2 each), the range has 6 and 0
	after := snapshot()
	assert.Equal(t, [2]float64{8, 2 + 6 + 0 + 2}, after[Grid])
	for _, g := range []string{PV, Battery, Home, Loadpoint, Meter} {
		assert.Equal(t, before[g], after[g], g)
	}
}

// TestRecalcFeedInNotToday: a range reaching into today or an invalid one is refused
func TestRecalcFeedInNotToday(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, SetupSchema())

	now := time.Now()
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())

	_, err := RecalcFeedIn(midnight.AddDate(0, 0, -1), midnight.Add(time.Hour))
	assert.ErrorIs(t, err, ErrFeedInRange)
	_, err = FeedInRecalcSummary(midnight.AddDate(0, 0, -1), midnight.AddDate(0, 0, 1))
	assert.ErrorIs(t, err, ErrFeedInRange)
	_, err = RecalcFeedIn(midnight, midnight.AddDate(0, 0, -1))
	assert.ErrorIs(t, err, ErrFeedInRange)

	_, err = RecalcFeedIn(midnight.AddDate(0, 0, -1), midnight)
	assert.NoError(t, err)
}

// TestBalanceFeedIn: the balance of one slot, never more than production and
// battery discharge, so the metering difference at night is no feed-in
func TestBalanceFeedIn(t *testing.T) {
	for _, tc := range []struct {
		name string
		s    flowSlot
		want float64
	}{
		{"surplus", flowSlot{PV: 3, GridIn: 0.2, Home: 1, BatIn: 0.5}, 1.7},
		{"loadpoint", flowSlot{PV: 3, Home: 1, Loadpoint: 1.5}, 0.5},
		{"battery to grid", flowSlot{BatOut: 2, Home: 0.5}, 1.5},
		{"deficit", flowSlot{PV: 1, GridIn: 0.5, Home: 2}, 0},
		{"night metering difference", flowSlot{GridIn: 0.115, Home: 0.112}, 0},
		{"capped by production", flowSlot{PV: 0.2, GridIn: 1, Home: 0.5}, 0.2},
	} {
		assert.InDelta(t, tc.want, balanceFeedIn(tc.s), 1e-9, tc.name)
	}
}

// TestRecalcFeedInWithoutHome: a slot without a home consumption row has no balance,
// its feed-in stays and the preview counts it
func TestRecalcFeedInWithoutHome(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, SetupSchema())
	require.NoError(t, db.Instance.AutoMigrate(new(tariffValue), new(eegPrice)))

	grid, err := createEntity(Grid, Grid, Grid)
	require.NoError(t, err)
	pv, err := createEntity(PV, "pv", "pv")
	require.NoError(t, err)
	home, err := createEntity(Home, "home", "home")
	require.NoError(t, err)

	ts := func(h int) time.Time { return time.Date(2026, 9, 10, h, 0, 0, 0, time.UTC) }

	// with home: pv 5, draw 1, home 3 -> 3; without home: would be 5 + 1 = 6
	require.NoError(t, persist(grid, ts(10), 1, 1, nil, false))
	require.NoError(t, persist(pv, ts(10), 5, 0, nil, false))
	require.NoError(t, persist(home, ts(10), 3, 0, nil, false))
	require.NoError(t, persist(grid, ts(11), 1, 1, nil, false))
	require.NoError(t, persist(pv, ts(11), 5, 0, nil, false))

	from, to := ts(0), ts(0).AddDate(0, 0, 1)

	sum, err := FeedInRecalcSummary(from, to)
	require.NoError(t, err)
	assert.Equal(t, 1, sum.Skipped)
	assert.InDelta(t, 2, sum.OldEnergy, 1e-9)
	assert.InDelta(t, 3+1, sum.NewEnergy, 1e-9, "the slot without home keeps its 1")

	rows, err := RecalcFeedIn(from, to)
	require.NoError(t, err)
	assert.EqualValues(t, 1, rows)

	var kept float64
	require.NoError(t, db.Instance.Raw(`SELECT return_energy FROM meters WHERE meter = ? AND ts = ?`, grid.Id, ts(11).Unix()).Scan(&kept).Error)
	assert.InDelta(t, 1, kept, 1e-9)
}
