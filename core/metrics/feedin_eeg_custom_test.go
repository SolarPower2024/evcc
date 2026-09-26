package metrics

import (
	"testing"
	"time"

	"github.com/evcc-io/evcc/db"
	"github.com/stretchr/testify/require"
)

func TestQueryFeedInSplit(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, SetupSchema())
	require.NoError(t, db.Instance.AutoMigrate(new(tariffValue), new(eegPrice)))

	grid, err := createEntity(Grid, Grid, Grid)
	require.NoError(t, err)
	eeg, err := createEntity(Meter, FeedInEeg, "EEG")
	require.NoError(t, err)

	loc := time.UTC
	slot := func(month time.Month, n int) time.Time {
		return time.Date(2026, month, 10, 12, 0, 0, 0, loc).Add(time.Duration(n) * 15 * time.Minute)
	}
	feedin := func(v float64) *float64 { return &v }

	// August: counter not configured yet, all export is standard
	require.NoError(t, persist(grid, slot(8, 0), 0, 2, nil, false))
	require.NoError(t, PersistTariffs(slot(8, 0), nil, feedin(0.08), nil, nil))

	// September
	for _, s := range []struct {
		n              int
		export, eegKWh float64
		price          *float64 // eeg
		feedin         *float64
	}{
		{0, 3, 1, feedin(0.12), feedin(0.08)},   // 1 eeg, 2 standard
		{1, 1, 1.5, feedin(0.12), feedin(0.08)}, // counter above export: standard clamped at 0
		{2, 2, 0.5, nil, nil},                   // no prices: energy only
		{3, 0, 0.2, feedin(0.12), feedin(0.08)}, // export 0 (coarse grid counter)
	} {
		ts := slot(9, s.n)
		require.NoError(t, persist(grid, ts, 0, s.export, nil, false))
		require.NoError(t, persist(eeg, ts, s.eegKWh, 0, nil, false))
		if s.price != nil {
			require.NoError(t, PersistEegPrice(ts, *s.price))
		}
		if s.feedin != nil {
			require.NoError(t, PersistTariffs(ts, nil, s.feedin, nil, nil))
		}
	}

	res, err := QueryFeedInSplit(time.Date(2026, 1, 1, 0, 0, 0, 0, loc), time.Date(2027, 1, 1, 0, 0, 0, 0, loc), "month")
	require.NoError(t, err)
	require.Len(t, res, 2)

	aug := res[0]
	require.Equal(t, "2026-08", aug.Period)
	require.InDelta(t, 2, aug.Export, 1e-9)
	require.InDelta(t, 0, aug.Eeg, 1e-9)
	require.InDelta(t, 2, aug.Standard, 1e-9)
	require.InDelta(t, 0.16, aug.StandardRevenue, 1e-9)

	sep := res[1]
	require.Equal(t, "2026-09", sep.Period)
	require.InDelta(t, 6, sep.Export, 1e-9)
	require.InDelta(t, 1+1.5+0.5+0.2, sep.Eeg, 1e-9)
	require.InDelta(t, 2+0+1.5+0, sep.Standard, 1e-9)
	require.InDelta(t, (1+1.5+0.2)*0.12, sep.EegRevenue, 1e-9)
	require.InDelta(t, 1+1.5+0.2, sep.EegPriced, 1e-9)
	require.InDelta(t, 2*0.08, sep.StandardRevenue, 1e-9)
	require.InDelta(t, 2, sep.StandardPriced, 1e-9)

	// per day
	res, err = QueryFeedInSplit(time.Date(2026, 9, 1, 0, 0, 0, 0, loc), time.Date(2026, 10, 1, 0, 0, 0, 0, loc), "day")
	require.NoError(t, err)
	require.Len(t, res, 1)
	require.Equal(t, "2026-09-10", res[0].Period)
}

func TestPersistFeedInEegPrice(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, db.Instance.AutoMigrate(new(eegPrice)))

	ts := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	require.NoError(t, PersistEegPrice(ts, 0))
	require.NoError(t, PersistEegPrice(ts, 0.12))

	var res []eegPrice
	require.NoError(t, db.Instance.Find(&res).Error)
	require.Len(t, res, 1)
	require.Zero(t, res[0].Price, "0 is a valid price and kept")
}
