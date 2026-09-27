package metrics

import (
	"testing"
	"time"

	"github.com/evcc-io/evcc/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSlots(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, SetupSchema())

	c, err := NewCollector(Battery, "batt", "")
	require.NoError(t, err)

	start := time.Date(2026, 9, 1, 12, 0, 0, 0, time.Local)
	soc := 50.0
	require.NoError(t, persist(c.entity, start.Add(15*time.Minute), 0, 0.3, &soc, false))
	require.NoError(t, persist(c.entity, start, 0.5, 0, &soc, false))
	require.NoError(t, persist(c.entity, start.Add(30*time.Minute), 9, 0, nil, true)) // recovered

	res, err := c.Slots(start.Add(-time.Hour))
	require.NoError(t, err)
	require.Len(t, res, 2)
	assert.Equal(t, start, res[0].Start, "oldest first")
	assert.Equal(t, 0.5, res[0].Energy)
	assert.Equal(t, 0.3, res[1].ReturnEnergy)
	assert.Equal(t, 50.0, *res[1].Soc)
}
