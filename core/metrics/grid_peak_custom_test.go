package metrics

import (
	"testing"
	"time"

	"github.com/evcc-io/evcc/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The peak collector's slots are in no flow, no balance and no group the energy
// page reads: the groups of the page are all there is, its own group is not one.
func TestGridPeakInNoBalance(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, SetupSchema())
	t.Cleanup(func() { db.Instance = nil })

	grid, err := createEntity(Grid, Grid, Grid)
	require.NoError(t, err)
	home, err := createEntity(Home, Home, Home)
	require.NoError(t, err)

	ts := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	require.NoError(t, persist(grid, ts, 2, 0, nil, false))
	require.NoError(t, persist(home, ts, 2, 0, nil, false))

	before, err := QueryFlow(time.Time{}, time.Time{})
	require.NoError(t, err)

	peak, err := createEntity(GridPeak, GridPeakName, GridPeakName)
	require.NoError(t, err)
	require.NoError(t, persist(peak, ts, 5, 0, nil, false))

	after, err := QueryFlow(time.Time{}, time.Time{})
	require.NoError(t, err)
	assert.Equal(t, before, after, "the flow does not change")

	for _, g := range []string{Grid, Meter, Home, PV, Battery, Loadpoint, Consumer} {
		res, err := QueryEnergy(time.Time{}, time.Time{}, "15m", false, EnergyFilter{Group: g})
		require.NoError(t, err)
		for _, s := range res {
			assert.NotEqual(t, GridPeakName, s.Title, "group %s", g)
			assert.NotEqual(t, GridPeak, s.Group, "group %s", g)
		}
	}

	// grid energy stays as it was
	res, err := QueryEnergy(time.Time{}, time.Time{}, "15m", true, EnergyFilter{Group: Grid})
	require.NoError(t, err)
	require.Len(t, res, 1)
	assert.Equal(t, 2.0, res[0].Data[0].Energy)

	assert.NotContains(t, GroupOrder, GridPeak, "not a group of the energy page")
}
