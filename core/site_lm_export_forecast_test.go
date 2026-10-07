package core

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/db/settings"
	"github.com/evcc-io/evcc/util"
	"github.com/evcc-io/evcc/util/homeassistant"
	optimizer "github.com/evcc-io/optimizer/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// exportCase builds a request and result with the given Wh per step, from start
func exportCase(start time.Time, dt int, wh ...float32) (requestDetails, optimizer.OptimizationInput, optimizer.OptimizationResult) {
	var details requestDetails
	var req optimizer.OptimizationInput

	for i := range wh {
		details.Timestamps = append(details.Timestamps, start.Add(time.Duration(i*dt)*time.Second))
		req.TimeSeries.Dt = append(req.TimeSeries.Dt, dt)
	}

	return details, req, optimizer.OptimizationResult{Status: optimizer.Optimal, GridExport: wh}
}

func TestExportForecastPower(t *testing.T) {
	start := time.Date(2026, 10, 7, 12, 0, 0, 0, time.Local)

	// 15 min steps: 250 Wh in 900 s are 1000 W
	got := exportForecast(exportCase(start, 900, 250, 125.4, 62.5))
	require.Len(t, got, 3)
	assert.Equal(t, exportRate{start, start.Add(15 * time.Minute), 1000}, got[0])
	assert.Equal(t, 502.0, got[1].Value, "rounded to whole W")
	assert.Equal(t, 250.0, got[2].Value)

	// hourly steps: 1000 Wh in 3600 s are 1000 W
	got = exportForecast(exportCase(start, 3600, 1000, 2000))
	require.Len(t, got, 2)
	assert.Equal(t, exportRate{start, start.Add(time.Hour), 1000}, got[0])
	assert.Equal(t, 2000.0, got[1].Value)

	// a shortened first step: the power of its own length, listed from the quarter hour it is in
	details, req, res := exportCase(start, 900, 250, 250)
	details.Timestamps[0] = start.Add(5*time.Minute + 23*time.Second)
	req.TimeSeries.Dt[0] = 577
	res.GridExport[0] = 100
	got = exportForecast(details, req, res)
	require.Len(t, got, 2)
	assert.Equal(t, 624.0, got[0].Value, "100 Wh in 577 s")
	assert.Equal(t, start, got[0].Start)
	assert.Equal(t, start.Add(15*time.Minute), got[0].End)

	// solver noise below zero is no export
	got = exportForecast(exportCase(start, 900, -0.0001))
	assert.Equal(t, 0.0, got[0].Value)
}

func TestExportForecastNoMatch(t *testing.T) {
	start := time.Date(2026, 10, 7, 12, 0, 0, 0, time.Local)

	assert.Nil(t, exportForecast(requestDetails{}, optimizer.OptimizationInput{}, optimizer.OptimizationResult{}))

	details, req, res := exportCase(start, 900, 1, 2, 3)
	res.GridExport = res.GridExport[:2]
	assert.Nil(t, exportForecast(details, req, res), "result shorter than the request")

	details, req, res = exportCase(start, 900, 1, 2, 3)
	details.Timestamps = details.Timestamps[:2]
	assert.Nil(t, exportForecast(details, req, res), "fewer timestamps")

	details, req, res = exportCase(start, 900, 1, 2, 3)
	req.TimeSeries.Dt[1] = 0
	assert.Nil(t, exportForecast(details, req, res), "step without duration")
}

// Hours at 0 W become one entry, hours with export and partial hours do not.
func TestExportForecastZeroHours(t *testing.T) {
	at := func(h, m int) time.Time { return time.Date(2026, 10, 7, h, m, 0, 0, time.Local) }

	// 20:30 to 23:00: the half hour before 21:00 stays, 21-22 and 22-23 are one entry each
	steps := make([]float32, 10)
	details, req, res := exportCase(at(20, 30), 900, steps...)
	got := exportForecast(details, req, res)
	require.Len(t, got, 4)
	assert.Equal(t, exportRate{at(20, 30), at(20, 45), 0}, got[0])
	assert.Equal(t, exportRate{at(20, 45), at(21, 0), 0}, got[1])
	assert.Equal(t, exportRate{at(21, 0), at(22, 0), 0}, got[2])
	assert.Equal(t, exportRate{at(22, 0), at(23, 0), 0}, got[3])

	// one step with export keeps the whole hour in steps
	steps = []float32{0, 0, 0, 0, 0, 0, 5, 0, 0, 0, 0, 0}
	details, req, res = exportCase(at(21, 0), 900, steps...)
	got = exportForecast(details, req, res)
	require.Len(t, got, 1+4+1) // hour 21, the four steps of 22, hour 23
	assert.Equal(t, exportRate{at(21, 0), at(22, 0), 0}, got[0])
	assert.Equal(t, 20.0, got[3].Value)
	assert.Equal(t, at(22, 30), got[3].Start)
	assert.Equal(t, exportRate{at(23, 0), at(24, 0), 0}, got[len(got)-1], "the last full hour of zeros")

	// a partial hour at the end of the horizon stays in steps
	details, req, res = exportCase(at(21, 0), 900, 0, 0, 0, 0, 0, 0)
	got = exportForecast(details, req, res)
	require.Len(t, got, 3)
	assert.Equal(t, exportRate{at(21, 0), at(22, 0), 0}, got[0])
	assert.Equal(t, at(22, 0), got[1].Start)
	assert.Equal(t, at(22, 15), got[1].End)

	// no entry spans more than an hour
	details, req, res = exportCase(at(0, 0), 900, make([]float32, 96)...)
	got = exportForecast(details, req, res)
	require.Len(t, got, 24)
	for _, r := range got {
		assert.Equal(t, time.Hour, r.End.Sub(r.Start))
	}

	// an hour of steps that do not end on it (a shortened step) is not merged
	details, req, res = exportCase(at(21, 0), 900, 0, 0, 0, 0)
	req.TimeSeries.Dt[3] = 600
	got = exportForecast(details, req, res)
	assert.Len(t, got, 4)
}

// 48 h at 15 min with pv during the day: the size of the attribute is only logged,
// the entity is excluded from the recorder
func TestExportForecastSize(t *testing.T) {
	start := time.Date(2026, 10, 7, 0, 0, 0, 0, time.Local)

	wh := make([]float32, 192)
	for i := range wh {
		if h := (i / 4) % 24; h >= 9 && h < 17 {
			wh[i] = float32(100 + 7*i)
		}
	}

	got := exportForecast(exportCase(start, 900, wh...))
	b, err := json.Marshal(got)
	require.NoError(t, err)
	t.Logf("%d steps -> %d entries, %d bytes", len(wh), len(got), len(b))
	assert.Less(t, len(got), len(wh), "nights are merged")
}

func TestExportForecastEntityValidation(t *testing.T) {
	noSettingsDB(t)
	keepSettings(t)

	site := &Site{log: util.NewLogger("test")}

	// outside the add-on there is no Home Assistant connection
	t.Setenv(homeassistant.SupervisorToken, "")
	assert.Error(t, site.SetLmExportForecast("sensor.evcc_einspeiseprognose"))
	assert.Empty(t, site.GetLmExportForecast())

	t.Setenv(homeassistant.SupervisorToken, "token")

	for _, entity := range []string{"number.x", "sensor.", "sensor.A", "sensor.a-b", "x", "sensor.a b"} {
		assert.Error(t, site.SetLmExportForecast(entity), entity)
	}
	assert.Empty(t, site.GetLmExportForecast())

	require.NoError(t, site.SetLmExportForecast("sensor.evcc_einspeiseprognose"))
	assert.Equal(t, "sensor.evcc_einspeiseprognose", site.GetLmExportForecast())

	// empty turns it off, also without a connection
	t.Setenv(homeassistant.SupervisorToken, "")
	require.NoError(t, site.SetLmExportForecast(""))
	assert.Empty(t, site.GetLmExportForecast())
}

// The entity is stored with the advanced settings, a missing key is empty.
func TestExportForecastEntityRestore(t *testing.T) {
	noSettingsDB(t)
	keepSettings(t)

	t.Setenv(homeassistant.SupervisorToken, "token")

	a := &Site{log: util.NewLogger("test")}
	require.NoError(t, a.SetLmAdvanced("hysteresis", 3))
	require.NoError(t, a.SetLmExportForecast("sensor.evcc_einspeiseprognose"))

	b := &Site{log: util.NewLogger("test")}
	b.restoreLmAdvanced()
	assert.Equal(t, "sensor.evcc_einspeiseprognose", b.GetLmExportForecast())
	assert.Equal(t, 3.0, b.peakHysteresis(), "the other settings stay")

	// stored before the setting existed
	require.NoError(t, settings.SetJson(keys.LmAdvanced, map[string]float64{"hysteresis": 3}))
	c := &Site{log: util.NewLogger("test")}
	c.restoreLmAdvanced()
	assert.Empty(t, c.GetLmExportForecast())
}

// haRecorder is a Home Assistant taking states by POST
type haRecorder struct {
	mu     sync.Mutex
	paths  []string
	bodies []map[string]any
	status int
}

func (h *haRecorder) handler(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)

	h.mu.Lock()
	defer h.mu.Unlock()

	h.paths = append(h.paths, r.Method+" "+r.URL.Path)

	var body map[string]any
	_ = json.Unmarshal(b, &body)
	h.bodies = append(h.bodies, body)

	if h.status != 0 {
		w.WriteHeader(h.status)
		return
	}
	w.WriteHeader(http.StatusCreated)
	_, _ = w.Write([]byte(`{}`))
}

// call returns the method, path and body of the i-th request
func (h *haRecorder) call(i int) (string, map[string]any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.paths[i], h.bodies[i]
}

func (h *haRecorder) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.paths)
}

// exportSite returns a site writing to the test server instead of the supervisor
func exportSite(t *testing.T, ha *haRecorder, entity string) *Site {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(ha.handler))
	t.Cleanup(srv.Close)

	site := &Site{log: util.NewLogger("test")}
	site.lms().adv.ExportForecastEntity = entity
	site.exportFc().connect = func() (*homeassistant.Connection, error) {
		conn, err := homeassistant.NewConnection(util.NewLogger("test"), srv.URL, "", false)
		if err != nil {
			return nil, err
		}
		conn.Client.Transport = http.DefaultTransport // no token against the test server
		return conn, nil
	}

	return site
}

func TestPublishExportForecast(t *testing.T) {
	noSettingsDB(t)
	keepSettings(t)

	start := time.Now().Truncate(time.Hour)
	details, req, res := exportCase(start, 900, 250, 125, 0, 0)

	t.Run("body", func(t *testing.T) {
		ha := new(haRecorder)
		site := exportSite(t, ha, "sensor.evcc_einspeiseprognose")

		site.publishExportForecast(details, req, res)
		site.exportFc().wg.Wait()

		require.Equal(t, 1, ha.count())
		path, body := ha.call(0)
		assert.Equal(t, "POST /api/states/sensor.evcc_einspeiseprognose", path)
		assert.NotEmpty(t, body["state"])

		attrs, ok := body["attributes"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "W", attrs["unit_of_measurement"])
		assert.Equal(t, "power", attrs["device_class"])
		assert.Equal(t, "evcc Einspeiseprognose", attrs["friendly_name"])

		_, err := time.Parse(time.RFC3339, attrs["updated"].(string))
		assert.NoError(t, err)

		fc, ok := attrs["forecast"].([]any)
		require.True(t, ok)
		require.Len(t, fc, 4)
		first := fc[0].(map[string]any)
		assert.Equal(t, 1000.0, first["value"])
		assert.Equal(t, start.Format(time.RFC3339), first["start"])
		assert.Equal(t, start.Add(15*time.Minute).Format(time.RFC3339), first["end"])
	})

	t.Run("state is the power of the current step", func(t *testing.T) {
		now := time.Now()
		cur := []exportRate{
			{now.Add(-time.Hour), now.Add(-time.Minute), 100},
			{now.Add(-time.Minute), now.Add(10 * time.Minute), 700},
		}
		assert.Equal(t, 700.0, exportCurrent(cur, now))
		assert.Equal(t, 0.0, exportCurrent(cur, now.Add(time.Hour)))
	})

	t.Run("unchanged list is not written again", func(t *testing.T) {
		ha := new(haRecorder)
		site := exportSite(t, ha, "sensor.evcc_einspeiseprognose")

		for range 3 {
			site.publishExportForecast(details, req, res)
			site.exportFc().wg.Wait()
		}
		assert.Equal(t, 1, ha.count())

		// a new plan is
		details2, req2, res2 := exportCase(start, 900, 250, 125, 50, 0)
		site.publishExportForecast(details2, req2, res2)
		site.exportFc().wg.Wait()
		assert.Equal(t, 2, ha.count())

		// a changed entity gets the plan again
		t.Setenv(homeassistant.SupervisorToken, "token")
		require.NoError(t, site.SetLmExportForecast("sensor.other"))
		site.publishExportForecast(details2, req2, res2)
		site.exportFc().wg.Wait()
		assert.Equal(t, 3, ha.count())
		path, _ := ha.call(2)
		assert.Equal(t, "POST /api/states/sensor.other", path)
	})

	t.Run("no entity writes nothing", func(t *testing.T) {
		ha := new(haRecorder)
		site := exportSite(t, ha, "")

		site.publishExportForecast(details, req, res)
		site.exportFc().wg.Wait()
		assert.Equal(t, 0, ha.count())
	})

	t.Run("failed write is tried again", func(t *testing.T) {
		ha := &haRecorder{status: http.StatusInternalServerError}
		site := exportSite(t, ha, "sensor.evcc_einspeiseprognose")

		site.publishExportForecast(details, req, res)
		site.exportFc().wg.Wait()
		assert.Equal(t, 1, ha.count())
		assert.Empty(t, site.exportFc().last)

		ha.mu.Lock()
		ha.status = 0
		ha.mu.Unlock()

		site.publishExportForecast(details, req, res)
		site.exportFc().wg.Wait()
		assert.Equal(t, 2, ha.count())
		assert.NotEmpty(t, site.exportFc().last)
	})

	t.Run("no connection is logged, not fatal", func(t *testing.T) {
		site := &Site{log: util.NewLogger("test")}
		site.lms().adv.ExportForecastEntity = "sensor.evcc_einspeiseprognose"
		t.Setenv(homeassistant.SupervisorToken, "")

		site.publishExportForecast(details, req, res)
		site.exportFc().wg.Wait()
		assert.Empty(t, site.exportFc().last)
		assert.False(t, site.exportFc().lastWarn.IsZero(), "warned")
	})
}

// The hook in optimizerUpdate: the export forecast follows a usable result only.
func TestLmOptimizerResultExport(t *testing.T) {
	start := time.Now().Truncate(time.Hour)

	for _, tc := range []struct {
		status optimizer.OptimizationResultStatus
		writes int
	}{
		{optimizer.Optimal, 1},
		{optimizer.Feasible, 1},
		{optimizer.Infeasible, 0},
		{optimizer.NotSolved, 0},
	} {
		t.Run(string(tc.status), func(t *testing.T) {
			details, req, res := exportCase(start, 900, 250, 125)
			res.Status = tc.status

			ha := new(haRecorder)
			site := exportSite(t, ha, "sensor.evcc_einspeiseprognose")

			site.lmOptimizerResult(nil, &req, details, &res)
			site.exportFc().wg.Wait()
			assert.Equal(t, tc.writes, ha.count())
		})
	}

	t.Run("without result", func(t *testing.T) {
		ha := new(haRecorder)
		site := exportSite(t, ha, "sensor.evcc_einspeiseprognose")
		site.lmOptimizerResult(nil, new(optimizer.OptimizationInput), requestDetails{}, nil)
		assert.Equal(t, 0, ha.count())
	})
}
