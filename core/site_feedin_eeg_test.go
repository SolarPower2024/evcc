package core

import (
	"testing"
	"time"

	"github.com/benbjohnson/clock"
	"github.com/evcc-io/evcc/core/metrics"
	"github.com/evcc-io/evcc/db"
	"github.com/evcc-io/evcc/tariff"
	"github.com/evcc-io/evcc/util"
	"github.com/stretchr/testify/require"
)

// Without a counter nothing is read, recorded or persisted: upstream behaviour.
func TestFeedInEegInertWithoutCounter(t *testing.T) {
	db.Instance = nil

	site := &Site{log: util.NewLogger("test")}
	site.updateFeedInEeg()

	require.Nil(t, site.eeg().collector)
	require.Empty(t, site.GetFeedInEegEntity())
}

func TestFeedInEegRecordsCounterAndPrice(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	t.Cleanup(func() { db.Instance = nil })

	eeg, err := tariff.NewFixedFromConfig(map[string]any{"price": 0.0})
	require.NoError(t, err)

	site := &Site{log: util.NewLogger("test"), tariffs: &tariff.Tariffs{FeedInEeg: eeg}}
	require.NoError(t, site.applyFeedInEegEntity("", false))

	counter := 100.0
	s := site.eeg()
	s.entity, s.get = "sensor.eeg", func() (float64, error) { return counter, nil }

	site.updateFeedInEeg()
	counter += 1.5
	site.updateFeedInEeg()

	// price of the running slot persisted once, 0 included
	var count int64
	require.NoError(t, db.Instance.Table("tariffs_eeg").Count(&count).Error)
	require.Equal(t, int64(1), count)

	now := time.Now()
	site.persistFeedInEegPrice(now)
	site.persistFeedInEegPrice(now.Add(15 * time.Minute))
	require.NoError(t, db.Instance.Table("tariffs_eeg").Count(&count).Error)
	require.Equal(t, int64(2), count)

	// the collector is a monitoring-only meter
	var groups []string
	require.NoError(t, db.Instance.Raw(`SELECT "group" FROM entities WHERE name = ?`, metrics.FeedInEeg).Scan(&groups).Error)
	require.Equal(t, []string{metrics.Meter}, groups)
}

// A changed counter must not count the jump between the old and the new one.
func TestFeedInEegChangedCounterDropsReading(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	t.Cleanup(func() { db.Instance = nil })

	clk := clock.NewMock()
	clk.Set(time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC))

	site := &Site{log: util.NewLogger("test")}
	site.eeg().clock = clk

	read := func(v float64) {
		require.NoError(t, site.eeg().collector.SetEnergyMeterTotal(v))
		clk.Add(time.Minute)
	}
	slot := func(minute int) {
		clk.Set(time.Date(2026, 9, 10, 12, minute, 0, 0, time.UTC))
	}
	energy := func() []float64 {
		var res []float64
		require.NoError(t, db.Instance.Table("meters").Order("ts").Pluck("energy", &res).Error)
		return res
	}

	// first counter, one full slot recorded
	require.NoError(t, site.applyFeedInEegEntity("", false))
	read(100)
	read(101)
	slot(15)
	read(101)
	read(102)
	slot(30)
	read(102)
	require.Equal(t, []float64{1, 1}, energy())

	// new counter: its first slot is incomplete, the one after is recorded
	require.NoError(t, site.applyFeedInEegEntity("", true))
	read(5000)
	read(5000.5)
	slot(45)
	read(5001)
	read(5001.7)
	slot(60)
	read(5001.7)

	e := energy()
	require.Len(t, e, 3)
	require.InDelta(t, 0.7, e[2], 1e-9, "no jump between the counters")
}
