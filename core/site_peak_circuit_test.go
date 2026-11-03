package core

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/benbjohnson/clock"
	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/circuit"
	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/core/metrics"
	"github.com/evcc-io/evcc/core/peak"
	"github.com/evcc-io/evcc/db"
	"github.com/evcc-io/evcc/db/settings"
	"github.com/evcc-io/evcc/util"
	"github.com/evcc-io/evcc/util/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// circuitMeter is a meter with an energy counter
type circuitMeter struct {
	energy float64
	err    error
}

func (m *circuitMeter) CurrentPower() (float64, error) { return 0, nil }
func (m *circuitMeter) TotalEnergy() (float64, error)  { return m.energy, m.err }

// powerOnlyMeter has no energy counter
type powerOnlyMeter struct{}

func (powerOnlyMeter) CurrentPower() (float64, error) { return 0, nil }

// circuitSetup registers a main circuit that refers to the meter ref, and the
// meter itself, nil = no such meter
func circuitSetup(t *testing.T, ref string, m api.Meter) {
	t.Helper()

	config.Reset()
	t.Cleanup(config.Reset)

	c, err := circuit.New(util.NewLogger("test"), "main", 0, 0, nil, 0)
	require.NoError(t, err)

	other := map[string]any{}
	if ref != "" {
		other["meter"] = ref
	}
	require.NoError(t, config.Circuits().Add(config.NewStaticDevice(config.Named{Name: "main", Other: other}, api.Circuit(c))))

	if m != nil {
		require.NoError(t, config.Meters().Add(config.NewStaticDevice(config.Named{Name: ref}, m)))
	}
}

func peakCircuitSite(t *testing.T) (*Site, *clock.Mock) {
	t.Helper()

	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	t.Cleanup(func() { db.Instance = nil; _ = settings.Delete(keys.GridPeakMeter) })

	clk := clock.NewMock()
	clk.Set(time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC))

	site := &Site{log: util.NewLogger("test"), collectors: map[string]*metrics.Collector{}}
	site.custom.peakCircuit.clock = clk
	site.custom.peak.clock = clk
	site.peak().limit = 5000

	return site, clk
}

// cycle is one cycle: the circuit's reading, then the window
func cycle(site *Site, clk *clock.Mock, grid float64, gridMeter *float64) {
	clk.Add(30 * time.Second)
	site.updatePeakCircuit()
	site.setPeakGridEnergy(gridMeter, true)
	site.updatePeakWindow(grid, 0)
}

// Without a circuit meter, or one without energy, nothing is read or recorded and
// the window is metered as before.
func TestPeakCircuitInert(t *testing.T) {
	for name, tc := range map[string]struct {
		ref string
		m   api.Meter
	}{
		"circuit without meter":     {},
		"meter without energy":      {"shelly", powerOnlyMeter{}},
		"meter reference not found": {"gone", nil},
	} {
		t.Run(name, func(t *testing.T) {
			circuitSetup(t, tc.ref, tc.m)
			site, clk := peakCircuitSite(t)

			cycle(site, clk, 0, kWh(10))
			cycle(site, clk, 0, kWh(10.05))

			assert.Nil(t, site.peakCircuit().collector)
			assert.Equal(t, peak.SourceMeter, site.peak().window.Source)
			assert.InDelta(t, 180000, site.peak().window.DrawnWs, 0.001)

			var n int64
			require.NoError(t, db.Instance.Table("entities").Where(`"group" = ?`, metrics.GridPeak).Count(&n).Error)
			assert.Zero(t, n)
		})
	}

	t.Run("no circuit", func(t *testing.T) {
		config.Reset()
		t.Cleanup(config.Reset)
		site, clk := peakCircuitSite(t)
		cycle(site, clk, 0, kWh(10))
		assert.Nil(t, site.peakCircuit().collector)
	})
}

// The circuit meter's counter is the first source of the window, the grid
// meter's counter and the grid power fill in where it fails.
func TestPeakCircuitIsFirstSource(t *testing.T) {
	m := &circuitMeter{energy: 100}
	circuitSetup(t, "shelly", m)
	site, clk := peakCircuitSite(t)
	s := site.peak()

	// 0.05 kWh in 30s is 6 kW, whatever the grid meter says
	cycle(site, clk, 0, kWh(1))
	m.energy = 100.05
	cycle(site, clk, 0, kWh(1))
	assert.Equal(t, peak.SourceCircuit, s.window.Source)
	assert.InDelta(t, 180000, s.window.DrawnWs, 0.001)

	// a failed read: the grid meter's counter
	m.err = errors.New("offline")
	cycle(site, clk, 0, kWh(1))
	assert.Equal(t, peak.SourceMeter, s.window.Source)

	// no counter at all: the grid power
	cycle(site, clk, 2000, nil)
	assert.Equal(t, peak.SourcePower, s.window.Source)

	// readable again: a new baseline, then the counter
	m.err = nil
	m.energy = 100.1
	cycle(site, clk, 0, nil)
	m.energy = 100.125
	before := s.window.DrawnWs
	cycle(site, clk, 0, nil)
	assert.Equal(t, peak.SourceCircuit, s.window.Source)
	assert.InDelta(t, 90000, s.window.DrawnWs-before, 0.001)
}

// A reading below the last is a glitch for the window, a counter standing still
// is stale, as for the other counters.
func TestPeakCircuitGlitchAndStandstill(t *testing.T) {
	m := &circuitMeter{energy: 100}
	circuitSetup(t, "shelly", m)
	site, clk := peakCircuitSite(t)
	s := site.peak()

	cycle(site, clk, 0, nil)
	m.energy = 100.05
	cycle(site, clk, 0, nil)
	assert.InDelta(t, 180000, s.window.DrawnWs, 0.001)

	m.energy = 1 // a sensor reporting almost 0 for a moment
	cycle(site, clk, 1000, nil)
	assert.InDelta(t, 180000+30000, s.window.DrawnWs, 0.001, "the grid power counts meanwhile")

	m.energy = 100.075 // back at the old level: it was a glitch
	cycle(site, clk, 1000, nil)
	assert.Equal(t, peak.SourceCircuit, s.window.Source)
	assert.InDelta(t, 180000+90000, s.window.DrawnWs, 0.001)

	// the counter stands still while the grid power says it should move
	for range 30 {
		cycle(site, clk, 20000, nil)
		if s.window.Stale {
			break
		}
	}
	assert.True(t, s.window.Stale)
	assert.Equal(t, peak.SourcePower, s.window.Source)
}

// The collector records the counter per slot in its own group and starts afresh
// for another meter, in this run and after a restart.
func TestPeakCircuitCollector(t *testing.T) {
	a := &circuitMeter{energy: 100}
	circuitSetup(t, "shelly", a)
	site, clk := peakCircuitSite(t)

	slotEnergy := func() []float64 {
		var res []float64
		require.NoError(t, db.Instance.Table("meters").Order("ts").Pluck("energy", &res).Error)
		return res
	}
	read := func() { site.updatePeakCircuit(); clk.Add(time.Minute) }
	next := func(minute int) { clk.Set(time.Date(2026, 11, 3, 12, minute, 0, 0, time.UTC)) }

	read()
	a.energy = 101
	read()
	next(15)
	read()
	a.energy = 102
	read()
	next(30)
	read()
	assert.Equal(t, []float64{1, 1}, slotEnergy(), "full slots recorded")

	var groups []string
	require.NoError(t, db.Instance.Raw(`SELECT "group" FROM entities WHERE name = ?`, metrics.GridPeakName).Scan(&groups).Error)
	assert.Equal(t, []string{metrics.GridPeak}, groups)
	assert.Equal(t, "shelly", site.peakCircuit().ref)
	assert.Same(t, site.peakCircuit().collector, site.gridPeakCollector())

	// another meter: no jump between the counters
	b := &circuitMeter{energy: 5000}
	circuitSetup(t, "other", b)
	read()
	b.energy = 5000.5
	next(45)
	read()
	b.energy = 5001
	next(60)
	read()
	b.energy = 5001.7
	read()
	next(75)
	read()
	for _, e := range slotEnergy() {
		assert.Less(t, e, 5.0, "no jump between the counters")
	}
	assert.Equal(t, "other", site.peakCircuit().ref)

	// the next start finds another meter than the one recorded before
	circuitSetup(t, "shelly", a)
	site2 := &Site{log: util.NewLogger("test"), collectors: map[string]*metrics.Collector{}}
	site2.custom.peakCircuit.clock = clk
	a.energy = 90000
	site2.updatePeakCircuit()

	stored, err := settings.String(keys.GridPeakMeter)
	require.NoError(t, err)
	assert.Equal(t, "shelly", stored)
	require.NoError(t, site2.peakCircuit().collector.SetEnergyMeterTotal(90000.5))
	for _, e := range slotEnergy() {
		assert.Less(t, e, 5.0, "restored reading dropped for the other meter")
	}
}

// Without a circuit meter evcc's peak reads the grid meter's collector as before.
func TestGridPeakCollectorFallsBackToGridMeter(t *testing.T) {
	config.Reset()
	t.Cleanup(config.Reset)
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	t.Cleanup(func() { db.Instance = nil })

	c, err := metrics.NewCollector(metrics.Grid, "grid", "grid")
	require.NoError(t, err)

	site := &Site{log: util.NewLogger("test"), collectors: map[string]*metrics.Collector{"grid": c}}
	site.Meters.GridMeterRef = "grid"

	assert.Same(t, c, site.gridPeakCollector())
}

// The main circuit's meter is the grid meter itself: it is not read a second time,
// no collector, the source stays the grid meter.
func TestPeakCircuitSameAsGridMeter(t *testing.T) {
	m := &circuitMeter{energy: 100}
	circuitSetup(t, "shelly", m)
	site, clk := peakCircuitSite(t)
	site.Meters.GridMeterRef = "shelly"

	cycle(site, clk, 0, kWh(1))
	m.energy = 100.05
	cycle(site, clk, 0, kWh(1))

	assert.Nil(t, site.peakCircuit().collector)
	assert.Empty(t, site.peakCircuit().ref)
	assert.Equal(t, peak.SourceMeter, site.peak().window.Source)

	var n int64
	require.NoError(t, db.Instance.Table("entities").Where(`"group" = ?`, metrics.GridPeak).Count(&n).Error)
	assert.Zero(t, n)
}

// A meter whose recording cannot start is tried and logged once, not every cycle.
func TestPeakCircuitFailedStartNotRepeated(t *testing.T) {
	m := &circuitMeter{energy: 100}
	circuitSetup(t, "shelly", m)
	site, clk := peakCircuitSite(t)

	var buf bytes.Buffer
	site.log = testLogger(&buf)

	// no entities table: the collector cannot be created
	require.NoError(t, db.Instance.Migrator().DropTable("entities"))

	for range 4 {
		cycle(site, clk, 0, nil)
	}
	assert.Equal(t, 1, strings.Count(buf.String(), "ERROR"), buf.String())
	assert.Nil(t, site.peakCircuit().collector)
	assert.Equal(t, "shelly", site.peakCircuit().ref)

	// the window still uses the counter
	assert.Equal(t, peak.SourceCircuit, site.peak().window.Source)
}

// Without a database the meter is taken note of once and nothing is persisted again.
func TestPeakCircuitWithoutDatabaseOnce(t *testing.T) {
	m := &circuitMeter{energy: 100}
	circuitSetup(t, "shelly", m)
	site, clk := peakCircuitSite(t)
	db.Instance = nil

	cycle(site, clk, 0, nil)
	require.NoError(t, settings.Delete(keys.GridPeakMeter))
	for range 3 {
		cycle(site, clk, 0, nil)
	}

	_, err := settings.String(keys.GridPeakMeter)
	assert.ErrorIs(t, err, settings.ErrNotFound, "not set again every cycle")
	assert.Equal(t, "shelly", site.peakCircuit().ref)
}
