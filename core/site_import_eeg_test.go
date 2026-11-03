package core

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/benbjohnson/clock"
	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/core/metrics"
	"github.com/evcc-io/evcc/db"
	"github.com/evcc-io/evcc/db/settings"
	"github.com/evcc-io/evcc/util"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Without a counter nothing is read, recorded or persisted.
func TestImportEegInertWithoutCounter(t *testing.T) {
	db.Instance = nil

	site := &Site{log: util.NewLogger("test")}
	site.updateImportEeg()

	require.Nil(t, site.importEeg().collector)
	require.Empty(t, site.GetImportEegEntity())
}

// The counter is recorded per slot as a monitoring meter, in no balance, and does
// not touch the feed-in counter.
func TestImportEegRecordsCounter(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	t.Cleanup(func() { db.Instance = nil })

	site := &Site{log: util.NewLogger("test")}
	require.NoError(t, site.applyImportEegEntity("", false))

	counter := 100.0
	s := site.importEeg()
	s.entity, s.get = "sensor.eeg_import", func() (float64, error) { return counter, nil }

	site.updateImportEeg()
	counter += 1.5
	site.updateImportEeg()

	var groups []string
	require.NoError(t, db.Instance.Raw(`SELECT "group" FROM entities WHERE name = ?`, metrics.ImportEeg).Scan(&groups).Error)
	require.Equal(t, []string{metrics.Meter}, groups)
	require.Nil(t, site.eeg().collector)
}

// A changed counter must not count the jump between the old and the new one.
func TestImportEegChangedCounterDropsReading(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	t.Cleanup(func() { db.Instance = nil })

	clk := clock.NewMock()
	clk.Set(time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC))

	site := &Site{log: util.NewLogger("test")}
	site.importEeg().clock = clk

	read := func(v float64) {
		require.NoError(t, site.importEeg().collector.SetEnergyMeterTotal(v))
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

	require.NoError(t, site.applyImportEegEntity("", false))
	read(100)
	read(101)
	slot(15)
	read(101)
	read(102)
	slot(30)
	read(102)
	require.Equal(t, []float64{1, 1}, energy())

	require.NoError(t, site.applyImportEegEntity("", true))
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

// The setting is checked like the feed-in counter, turns off with an empty value
// and is read back after a restart, also without a reachable Home Assistant.
func TestImportEegSetting(t *testing.T) {
	sc, ha := newOmni(t)
	site := sc.site
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	t.Cleanup(func() { db.Instance = nil })
	t.Cleanup(func() { _ = settings.Delete(keys.ImportEegEntity) })

	ha.states["sensor.eeg_import"] = &haEntity{"12.5", map[string]any{"unit_of_measurement": "kWh"}}
	ha.states["sensor.eeg_power"] = &haEntity{"3", map[string]any{"unit_of_measurement": "W"}}
	ha.states["switch.x"] = &haEntity{"on", map[string]any{"unit_of_measurement": "kWh"}}

	assert.Error(t, site.SetImportEegEntity("switch.x"), "not a sensor")
	assert.Error(t, site.SetImportEegEntity("sensor.eeg_power"), "not an energy counter")
	assert.Error(t, site.SetImportEegEntity("sensor.missing"), "unknown to Home Assistant")
	assert.Empty(t, site.GetImportEegEntity())

	require.NoError(t, site.SetImportEegEntity("sensor.eeg_import"))
	assert.Equal(t, "sensor.eeg_import", site.GetImportEegEntity())
	stored, err := settings.String(keys.ImportEegEntity)
	require.NoError(t, err)
	assert.Equal(t, "sensor.eeg_import", stored)

	// the cycle records it
	site.updateImportEeg()
	require.NotNil(t, site.importEeg().collector)

	// after a restart
	again := &Site{log: util.NewLogger("test")}
	again.custom.peak.conn = site.peak().conn
	again.restoreImportEeg()
	assert.Equal(t, "sensor.eeg_import", again.GetImportEegEntity())

	// off
	require.NoError(t, site.SetImportEegEntity(""))
	assert.Empty(t, site.GetImportEegEntity())
	stored, err = settings.String(keys.ImportEegEntity)
	require.NoError(t, err)
	assert.Empty(t, stored)

	// a restart without a reachable Home Assistant keeps the name
	settings.SetString(keys.ImportEegEntity, "sensor.eeg_import")
	offline := &Site{log: util.NewLogger("test")}
	offline.restoreImportEeg()
	assert.Equal(t, "sensor.eeg_import", offline.GetImportEegEntity())
}

// A failing read is warned once, its return is logged once.
func TestImportEegReadWarnsOnce(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	t.Cleanup(func() { db.Instance = nil })

	var buf bytes.Buffer
	site := &Site{log: testLogger(&buf)}
	require.NoError(t, site.applyImportEegEntity("", false))

	failing := true
	s := site.importEeg()
	s.entity, s.get = "sensor.eeg_import", func() (float64, error) {
		if failing {
			return 0, assert.AnError
		}
		return 1, nil
	}

	for range 3 {
		site.updateImportEeg()
	}
	assert.Equal(t, 1, strings.Count(buf.String(), "WARN"))

	failing = false
	site.updateImportEeg()
	site.updateImportEeg()
	assert.Equal(t, 1, strings.Count(buf.String(), "readable again"))
}
