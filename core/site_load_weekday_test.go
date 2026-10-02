package core

import (
	"testing"
	"time"

	"github.com/evcc-io/evcc/core/metrics"
	"github.com/evcc-io/evcc/db"
	"github.com/evcc-io/evcc/tariff"
	"github.com/evcc-io/evcc/util"
	"github.com/jinzhu/now"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedHome writes the home energy of every slot of the last days, kWh per slot by weekday
func seedHome(t *testing.T, days int, kwh func(time.Weekday) float64) *metrics.Collector {
	t.Helper()
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	t.Cleanup(func() { db.Instance = nil }) // later tests must not write into this database
	require.NoError(t, metrics.SetupSchema())

	col, err := metrics.NewCollector(metrics.Home, metrics.Home, "")
	require.NoError(t, err)

	var id int
	require.NoError(t, db.Instance.Raw(`SELECT id FROM entities WHERE name = ?`, metrics.Home).Scan(&id).Error)

	for day := -days; day < 0; day++ {
		base := now.BeginningOfDay().AddDate(0, 0, day)
		v := kwh(base.Weekday())
		if v == 0 {
			continue
		}
		for slot := range 96 {
			ts := base.Add(time.Duration(slot) * tariff.SlotDuration).Unix()
			require.NoError(t, db.Instance.Exec(`INSERT INTO meters (meter, ts, energy) VALUES (?, ?, ?)`, id, ts, v).Error)
		}
	}

	return col
}

func TestHomeProfileByWeekday(t *testing.T) {
	today := time.Now().Weekday()
	tomorrow := (today + 1) % 7

	col := seedHome(t, 56, func(wd time.Weekday) float64 {
		switch wd {
		case today:
			return 2
		case tomorrow:
			return 3
		}
		return 1
	})

	site := &Site{log: util.NewLogger("test")}
	first := int(time.Now().Truncate(tariff.SlotDuration).Sub(now.BeginningOfDay()) / tariff.SlotDuration)

	res, err := site.homeProfileByWeekday(col, 2*96)
	require.NoError(t, err)

	for i, v := range res {
		want := 2000.0
		if (first+i)/96 == 1 {
			want = 3000
		}
		if (first+i)/96 == 2 {
			want = 1000
		}
		assert.Equal(t, want, v, "slot %d", i)
	}

	// the hook in upstream's homeProfile: per weekday only when chosen
	site.collectors = map[string]*metrics.Collector{metrics.Home: col}
	t.Cleanup(func() { _ = site.SetLmAdvanced("homeForecast", 0) })

	upstream, err := site.homeProfile(2 * 96)
	require.NoError(t, err)
	assert.NotEqual(t, res, upstream, "evcc's own forecast by default")

	require.NoError(t, site.SetLmAdvanced("homeForecast", 1))
	hooked, err := site.homeProfile(2 * 96)
	require.NoError(t, err)
	assert.Equal(t, res, hooked)
}

// A weekday without data takes the regular 28 day profile.
func TestHomeProfileByWeekdayFallback(t *testing.T) {
	today := time.Now().Weekday()

	col := seedHome(t, 28, func(wd time.Weekday) float64 {
		if wd == today {
			return 2
		}
		return 0
	})

	site := &Site{log: util.NewLogger("test")}
	res, err := site.homeProfileByWeekday(col, 2*96)
	require.NoError(t, err)

	for i, v := range res {
		assert.Equal(t, 2000.0, v, "slot %d", i)
	}
}

func TestHomeWeekdaySetting(t *testing.T) {
	site := &Site{log: util.NewLogger("test")}
	assert.False(t, site.homeWeekday(), "off by default")

	require.NoError(t, site.SetLmAdvanced("homeWeekday", 1))
	assert.True(t, site.homeWeekday())

	require.Error(t, site.SetLmAdvanced("homeWeekday", 2))
	require.NoError(t, site.SetLmAdvanced("homeWeekday", 0))
	assert.False(t, site.homeWeekday())
}
