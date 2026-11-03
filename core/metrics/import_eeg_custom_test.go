package metrics

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/evcc-io/evcc/db"
	"github.com/stretchr/testify/require"
)

// The import from the energy community comes with the split, per bucket, and
// changes nothing else; without a counter the field is 0.
func TestQueryFeedInSplitImportEeg(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, SetupSchema())
	require.NoError(t, db.Instance.AutoMigrate(new(tariffValue), new(eegPrice)))
	t.Cleanup(func() { db.Instance = nil })

	loc := time.UTC
	local := time.Local
	time.Local = loc
	t.Cleanup(func() { time.Local = local })
	slot := func(n int) time.Time {
		return time.Date(2026, 10, 10, 12, 0, 0, 0, loc).Add(time.Duration(n) * 15 * time.Minute)
	}
	from, to := slot(0), slot(8)

	grid, err := createEntity(Grid, Grid, Grid)
	require.NoError(t, err)
	require.NoError(t, persist(grid, slot(0), 3, 0, nil, false))
	require.NoError(t, persist(grid, slot(1), 1, 2, nil, false))

	// no counter: nothing to show
	res, err := QueryFeedInSplit(from, to, "15m")
	require.NoError(t, err)
	require.Len(t, res, 1)
	require.Zero(t, res[0].ImportEeg)

	imp, err := createEntity(Meter, ImportEeg, ImportEeg)
	require.NoError(t, err)
	require.NoError(t, persist(imp, slot(0), 0.4, 0, nil, false))
	require.NoError(t, persist(imp, slot(1), 0.6, 0, nil, false))
	// a slot the grid has no row for is not part of the chart
	require.NoError(t, persist(imp, slot(5), 9, 0, nil, false))

	res, err = QueryFeedInSplit(from, to, "15m")
	require.NoError(t, err)
	require.Len(t, res, 2)
	require.InDelta(t, 0.4, res[0].ImportEeg, 1e-9)
	require.InDelta(t, 0.6, res[1].ImportEeg, 1e-9)
	require.InDelta(t, 2, res[1].Export, 1e-9, "export as before")
	require.Zero(t, res[1].Eeg)

	// the route sends it as importEeg
	b, err := json.Marshal(res[1])
	require.NoError(t, err)
	require.Contains(t, string(b), `"importEeg":0.6`)

	res, err = QueryFeedInSplit(from, to, "day")
	require.NoError(t, err)
	require.Len(t, res, 1)
	require.InDelta(t, 1.0, res[0].ImportEeg, 1e-9)

	// the counter is a monitoring meter: it is in no flow
	before, err := QueryFlow(time.Time{}, time.Time{})
	require.NoError(t, err)
	require.NoError(t, db.Instance.Where("meter = ?", imp.Id).Delete(new(meter)).Error)
	after, err := QueryFlow(time.Time{}, time.Time{})
	require.NoError(t, err)
	require.Equal(t, before, after)
}
