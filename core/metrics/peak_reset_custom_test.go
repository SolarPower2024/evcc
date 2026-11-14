package metrics

import (
	"testing"
	"time"

	"github.com/benbjohnson/clock"
	"github.com/evcc-io/evcc/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDailyPeakEnergySince: the slots before the reset do not count, the whole
// month does with a zero time (evcc's own query is the one used), the running
// slot only if it started after the reset
func TestDailyPeakEnergySince(t *testing.T) {
	clock := clock.NewMock()

	loc := time.Now().Location()
	day := func(d int, hour, min int) time.Time {
		return time.Date(2026, 11, d, hour, min, 0, 0, loc)
	}
	clock.Set(day(3, 18, 7))

	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, SetupSchema())

	col, err := NewCollector(Grid, "grid", "", WithClock(clock))
	require.NoError(t, err)

	e := col.entity
	require.NoError(t, persist(e, day(1, 10, 0), 5.0, 0, nil, false))
	require.NoError(t, persist(e, day(2, 9, 0), 4.0, 0, nil, false))
	require.NoError(t, persist(e, day(2, 14, 0), 2.0, 0, nil, false))
	require.NoError(t, persist(e, day(3, 12, 0), 1.0, 0, nil, false))
	require.NoError(t, persist(e, day(3, 12, 15), 1.5, 0, nil, false))

	// no reset: as evcc's DailyPeakEnergy
	all, err := col.DailyPeakEnergy()
	require.NoError(t, err)
	none, err := col.DailyPeakEnergySince(time.Time{})
	require.NoError(t, err)
	assert.Equal(t, all, none)

	// reset on the 2nd at 12:07: the 14:00 slot counts, the 9:00 one not
	days, err := col.DailyPeakEnergySince(day(2, 12, 7))
	require.NoError(t, err)
	require.Len(t, days, 3)
	assert.Nil(t, days[0])
	assert.InDelta(t, 2.0, *days[1], 1e-9)
	assert.InDelta(t, 1.5, *days[2], 1e-9)

	// a slot starting exactly at the reset counts, the one before does not
	days, err = col.DailyPeakEnergySince(day(3, 12, 15))
	require.NoError(t, err)
	assert.Nil(t, days[0])
	assert.Nil(t, days[1])
	assert.InDelta(t, 1.5, *days[2], 1e-9)

	// the running slot (18:00) started before a reset at 18:05: it does not count
	require.NoError(t, col.AddEnergy(nil, nil, 9000))
	clock.Add(5 * time.Minute)
	require.NoError(t, col.AddEnergy(nil, nil, 9000))

	days, err = col.DailyPeakEnergySince(day(3, 18, 5))
	require.NoError(t, err)
	assert.Nil(t, days[2])

	// after a reset before it, it does
	days, err = col.DailyPeakEnergySince(day(3, 18, 0))
	require.NoError(t, err)
	assert.InDelta(t, 0.75, *days[2], 1e-9)
}
