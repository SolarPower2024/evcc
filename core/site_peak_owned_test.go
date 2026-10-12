package core

import (
	"testing"

	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stored returns the value of a setting in the database, empty if it is not there
func stored(t *testing.T, key string) string {
	t.Helper()

	var v []string
	require.NoError(t, db.Instance.Table("settings").Select("value").Where("key = ?", key).Scan(&v).Error)

	if len(v) == 0 {
		return ""
	}

	return v[0]
}

// TestPeakHeldMarksStoredAtOnce verifies that the marks of what evcc holds are in
// the database right away, not with the next flush of the settings: a crash
// within the minute would lose them, and a start would not hand the battery back.
func TestPeakHeldMarksStoredAtOnce(t *testing.T) {
	sc, _ := newOmni(t) // keeps the settings in memory, the database follows
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	t.Cleanup(func() { db.Instance = nil })

	sc.site.lms().socChargeEnabled = true

	require.True(t, sc.cycle(20, 1000, 0))
	assert.Equal(t, "true", stored(t, keys.PeakShavingOwned), "manual control")
	assert.Equal(t, "true", stored(t, keys.PeakShavingProtOwned), "peak shaving of Omnibattery")

	// and the release
	require.False(t, sc.cycle(90, 1000, 0))
	assert.Equal(t, "false", stored(t, keys.PeakShavingOwned))

	require.NoError(t, sc.site.SetPeakShaving(false))
	assert.Equal(t, "false", stored(t, keys.PeakShavingProtOwned))
}

// TestPeakHeldMarkStoredBYD is the same for the setpoint of the battery type BYD
func TestPeakHeldMarkStoredBYD(t *testing.T) {
	sc, _ := newOmni(t)
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	t.Cleanup(func() { db.Instance = nil })

	sc.site.peak().batteryType = batteryTypeBYD
	require.NoError(t, sc.site.rebuildPeakSetter())

	sc.cycle(25, 9000, 0)
	assert.Equal(t, "true", stored(t, keys.PeakShavingOwned))

	require.NoError(t, sc.site.SetPeakShaving(false))
	assert.Equal(t, "false", stored(t, keys.PeakShavingOwned))
}
