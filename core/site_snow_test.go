package core

import (
	"testing"
	"time"

	"github.com/benbjohnson/clock"
	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/core/metrics"
	"github.com/evcc-io/evcc/db"
	"github.com/evcc-io/evcc/db/settings"
	"github.com/evcc-io/evcc/util"
	optimizer "github.com/evcc-io/optimizer/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSnowClearStep(t *testing.T) {
	tc := []struct {
		name      string
		count     int
		pv, fcst  float64
		wantCount int
		wantOff   bool
	}{
		{"no forecast", 2, 0, 0, 2, false},
		{"below 100 Wh does not count", 2, 90, 99.9, 2, false},
		{"below 100 Wh does not reset", 3, 0, 50, 3, false},
		{"exactly 100 Wh counts", 0, 100, 100, 1, false},
		{"exactly 70 % counts", 0, 70, 100, 1, false},
		{"just below 70 % resets", 3, 69.9, 100, 0, false},
		{"nothing produced resets", 2, 0, 400, 0, false},
		{"above the forecast counts", 1, 500, 400, 2, false},
		{"fourth in a row turns off", 3, 300, 400, 4, true},
		{"third in a row does not", 2, 300, 400, 3, false},
	}

	for _, tc := range tc {
		t.Run(tc.name, func(t *testing.T) {
			count, off := snowClearStep(tc.count, tc.pv, tc.fcst)
			assert.Equal(t, tc.wantCount, count)
			assert.Equal(t, tc.wantOff, off)
		})
	}

	// a sequence: two free, a gap in the dusk, a miss, then four free
	var count int
	var off bool
	for i, in := range [][2]float64{{300, 400}, {300, 400}, {0, 50}, {100, 400}, {300, 400}, {300, 400}, {300, 400}, {300, 400}} {
		count, off = snowClearStep(count, in[0], in[1])
		assert.Equal(t, i == 7, off, "step %d", i)
	}
	assert.Equal(t, 4, count)
}

// Contract: the optimizer plans without solar yield while the switch is on, and
// the request is untouched while it is off.
func TestSnowCoverOptimizerInput(t *testing.T) {
	keepSettings(t)
	noSettingsDB(t)

	site := &Site{log: util.NewLogger("test")}
	newReq := func() optimizer.OptimizationInput {
		return optimizer.OptimizationInput{TimeSeries: optimizer.TimeSeries{
			Dt: quarterHours(4),
			Ft: []float32{100, 250, 400, 350},
			Gt: []float32{200, 200, 200, 200},
		}}
	}

	// off: inert
	req := newReq()
	site.applyLmOptimizerInputs(&req, nil)
	assert.Equal(t, newReq(), req)

	// on: no solar yield, nothing else
	require.NoError(t, site.SetSnowCover(true))
	req = newReq()
	site.applyLmOptimizerInputs(&req, nil)
	assert.Equal(t, []float32{0, 0, 0, 0}, req.TimeSeries.Ft)
	assert.Equal(t, newReq().TimeSeries.Gt, req.TimeSeries.Gt)

	// off again
	require.NoError(t, site.SetSnowCover(false))
	req = newReq()
	site.applyLmOptimizerInputs(&req, nil)
	assert.Equal(t, newReq(), req)
}

// TestSnowCoverAutoOff: the switch rates every completed slot once and turns
// off after four free slots in a row, saved and published
func TestSnowCoverAutoOff(t *testing.T) {
	keepSettings(t)

	clk := clock.NewMock()
	clk.Set(time.Date(2026, 1, 10, 10, 0, 0, 0, time.UTC))

	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	t.Cleanup(func() { db.Instance = nil })
	require.NoError(t, metrics.SetupSchema())

	pv, err := metrics.NewCollector(metrics.PV, "pv", "", metrics.WithClock(clk))
	require.NoError(t, err)
	fc, err := metrics.NewCollector(metrics.Forecast, metrics.Forecast, "", metrics.WithClock(clk))
	require.NoError(t, err)

	pub := make(chan util.Param, 16)
	site := &Site{
		log:        util.NewLogger("test"),
		valueChan:  pub,
		collectors: map[string]*metrics.Collector{"pv": pv, metrics.Forecast: fc},
	}
	site.Meters.PVMetersRef = []string{"pv"}

	// slot sets the energies of the running slot in Wh
	slot := func(pvWh, fcstWh float64) {
		require.NoError(t, pv.SetEnergy(pvWh/1e3))
		require.NoError(t, fc.SetEnergy(fcstWh/1e3))
	}
	// next completes the running slot, the controller rates it twice
	next := func() {
		clk.Add(15 * time.Minute)
		slot(0, 0)
		site.updateSnowCover(clk.Now())
		site.updateSnowCover(clk.Now())
	}
	count := func() int { return site.snow().clear }

	// off: nothing is rated
	slot(0, 0)
	site.updateSnowCover(clk.Now())
	assert.Zero(t, count())
	assert.True(t, site.snow().slot.IsZero())

	require.NoError(t, site.SetSnowCover(true))
	assert.Equal(t, util.Param{Key: keys.SnowCover, Val: true}, <-pub)
	assert.Equal(t, util.Param{Key: keys.SnowCoverAuto, Val: false}, <-pub)

	// the first slot is not complete yet: tried again, nothing is marked
	site.updateSnowCover(clk.Now())
	assert.True(t, site.snow().slot.IsZero())

	slot(10, 300) // 10:00 snow
	next()
	assert.Equal(t, 0, count())
	assert.Equal(t, clk.Now().Add(-15*time.Minute), site.snow().slot)

	slot(250, 300) // 10:15 free
	next()
	assert.Equal(t, 1, count(), "rated once")

	slot(5, 50) // 10:30 dusk, does not count and does not reset
	next()
	assert.Equal(t, 1, count())

	slot(100, 300) // 10:45 below 70 %
	next()
	assert.Equal(t, 0, count())

	for i := range 4 {
		assert.True(t, site.GetSnowCover(), "after %d free slots", i)
		slot(300, 300) // 11:00 ... 11:45
		next()
	}

	assert.False(t, site.GetSnowCover(), "off after four free slots")
	v, err := settings.Bool(keys.SnowCover)
	require.NoError(t, err)
	assert.False(t, v)
	assert.Equal(t, util.Param{Key: keys.SnowCover, Val: false}, <-pub)
	assert.Equal(t, util.Param{Key: keys.SnowCoverAuto, Val: false}, <-pub)

	// off: further slots change nothing
	slot(0, 400)
	next()
	assert.Zero(t, count())
}

// TestSnowCoverStaysForComingSnow: snow the detection counted that is still to
// come keeps the switch on, however free the modules are now
func TestSnowCoverStaysForComingSnow(t *testing.T) {
	keepSettings(t)

	clk := clock.NewMock()
	clk.Set(time.Date(2026, 1, 10, 13, 0, 0, 0, time.UTC))

	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	t.Cleanup(func() { db.Instance = nil })
	require.NoError(t, metrics.SetupSchema())

	pv, err := metrics.NewCollector(metrics.PV, "pv", "", metrics.WithClock(clk))
	require.NoError(t, err)
	fc, err := metrics.NewCollector(metrics.Forecast, metrics.Forecast, "", metrics.WithClock(clk))
	require.NoError(t, err)

	site := &Site{
		log:        util.NewLogger("test"),
		valueChan:  make(chan util.Param, 64),
		collectors: map[string]*metrics.Collector{"pv": pv, metrics.Forecast: fc},
	}
	site.Meters.PVMetersRef = []string{"pv"}

	// sunny afternoon, snow forecast until 14:30
	site.setSnowCover(true, true)
	site.snow().seen = time.Date(2026, 1, 10, 14, 30, 0, 0, time.UTC)

	free := func() {
		require.NoError(t, pv.SetEnergy(0.3))
		require.NoError(t, fc.SetEnergy(0.3))
		clk.Add(15 * time.Minute)
		require.NoError(t, pv.SetEnergy(0))
		require.NoError(t, fc.SetEnergy(0))
		site.updateSnowCover(clk.Now())
	}

	for range 4 {
		free() // 13:00 ... 13:45
	}
	assert.True(t, site.GetSnowCover(), "snow still to come")
	assert.Zero(t, site.snow().clear)

	for range 4 {
		free() // 14:00 ... 14:45, the snow ends at 14:30
	}
	assert.False(t, site.GetSnowCover(), "off after four free slots once the snow is past")
}

// TestSnowCoverNeedsMeterAndForecast: without a pv meter or forecast collector nothing is rated
func TestSnowCoverNeedsMeterAndForecast(t *testing.T) {
	keepSettings(t)
	noSettingsDB(t)

	site := &Site{log: util.NewLogger("test"), collectors: map[string]*metrics.Collector{}}
	require.NoError(t, site.SetSnowCover(true))

	site.updateSnowCover(time.Now())
	assert.True(t, site.GetSnowCover())
	assert.True(t, site.snow().slot.IsZero())

	site.Meters.PVMetersRef = []string{"pv"}
	site.updateSnowCover(time.Now())
	assert.True(t, site.GetSnowCover())
	assert.True(t, site.snow().slot.IsZero())
}
