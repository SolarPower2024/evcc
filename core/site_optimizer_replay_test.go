package core

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/evcc-io/evcc/util"
	optimizer "github.com/evcc-io/optimizer/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLmOptimizerReplay sends a recorded optimizer request with the fork's
// inputs to a running optimizer and checks that its plan respects them. It only
// runs when OPTIMIZER_REPLAY names a file {"req": ..., "details": ...} as the
// optimize page shows it and OPTIMIZER_URI a running optimizer.
func TestLmOptimizerReplay(t *testing.T) {
	file, uri := os.Getenv("OPTIMIZER_REPLAY"), os.Getenv("OPTIMIZER_URI")
	if file == "" || uri == "" {
		t.Skip("OPTIMIZER_REPLAY and OPTIMIZER_URI not set")
	}

	b, err := os.ReadFile(file)
	require.NoError(t, err)

	var rec struct {
		Req     optimizer.OptimizationInput `json:"req"`
		Details struct {
			BatteryDetails []batteryDetail `json:"batteryDetails"`
		} `json:"details"`
	}
	require.NoError(t, json.Unmarshal(b, &rec))
	require.Len(t, rec.Details.BatteryDetails, len(rec.Req.Batteries))

	client, err := optimizer.NewClientWithResponses(uri, optimizer.WithHTTPClient(&http.Client{Timeout: 90 * time.Second}))
	require.NoError(t, err)

	solve := func(t *testing.T, req optimizer.OptimizationInput) (*optimizer.OptimizationResult, time.Duration) {
		start := time.Now()
		resp, err := client.PostOptimizeChargeScheduleWithResponse(context.Background(), req)
		elapsed := time.Since(start)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode(), string(resp.Body))
		require.NotNil(t, resp.JSON200)
		return resp.JSON200, elapsed
	}

	home := -1
	for i, d := range rec.Details.BatteryDetails {
		if d.Type == batteryTypeBattery {
			home = i
		}
	}
	require.GreaterOrEqual(t, home, 0, "no home battery in the recording")

	peakW := func(res *optimizer.OptimizationResult, dt []int) float64 {
		var peak float64
		for i, wh := range res.GridImport {
			peak = max(peak, float64(wh)/float64(dt[i])*3600)
		}
		return peak
	}

	// upstream request as recorded
	base, elapsed := solve(t, rec.Req)
	t.Logf("recorded request: %s, %d steps, %v, grid peak %.0f W", base.Status, len(rec.Req.TimeSeries.Dt), elapsed.Round(time.Millisecond), peakW(base, rec.Req.TimeSeries.Dt))

	// scenario runs the recorded request with the fork's inputs for the given
	// settings and checks the plan respects them
	// goalMayMiss: the goal may fall short while the peak limit, which is hard,
	// leaves no room for it
	scenario := func(t *testing.T, limit, reserve, start, stop float64, running bool, initialSoc float64, goalMayMiss bool) {
		site := &Site{log: util.NewLogger("replay")}
		setPeakShaving(site, limit, reserve)
		s := site.lms()
		s.socChargeEnabled, s.socChargeStart, s.socChargeStop, s.socChargeRunning = true, start, stop, running

		req := rec.Req
		batteries := make([]optimizerBattery, len(req.Batteries))
		for i := range req.Batteries {
			batteries[i] = optimizerBattery{cfg: req.Batteries[i], detail: rec.Details.BatteryDetails[i]}
		}
		batteries[home].cfg.ChargeFromGrid = true // as with a battery that has a charge mode
		if initialSoc > 0 {
			batteries[home].cfg.SInitial = batteries[home].cfg.SCapacity * float32(initialSoc) / 100
		}

		site.applyLmOptimizerInputs(&req, batteries)

		req.Batteries = make([]optimizer.BatteryConfig, len(batteries))
		for i, b := range batteries {
			req.Batteries[i] = b.cfg
		}
		bat := req.Batteries[home]

		res, elapsed := solve(t, req)
		t.Logf("%s, %v, limit %.0f W, grid peak %.0f W, min soc %.0f Wh", res.Status, elapsed.Round(time.Millisecond), limit, peakW(res, req.TimeSeries.Dt), bat.SMin)

		soc := res.Batteries[home].StateOfCharge
		require.NotEmpty(t, soc)

		for i, v := range soc {
			assert.GreaterOrEqual(t, v, bat.SMin-1, "soc below the minimum at step %d", i)
		}

		if running && bat.SGoal != nil {
			i := slotAfter(req.TimeSeries.Dt, site.gridChargeWindow())
			t.Logf("goal %.0f Wh at step %d, planned %.0f Wh", bat.SGoal[i], i, soc[i])
			if !goalMayMiss {
				assert.GreaterOrEqual(t, soc[i], bat.SGoal[i]-1, "goal not reached at step %d", i)
			}
		}

		// the hard limit holds unless the optimizer reports it could not be kept
		for i, wh := range res.GridImport {
			w := float64(wh) / float64(req.TimeSeries.Dt[i]) * 3600
			var over float64
			if i < len(res.GridImportOvershoot) {
				over = float64(res.GridImportOvershoot[i])
			}
			if w > limit+1 && over <= 0 {
				assert.Failf(t, "grid import above the limit", "step %d: %.0f W > %.0f W", i, w, limit)
			}
		}
		t.Logf("limit violations: %+v", res.LimitViolations)
	}

	// the settings of the installation: REPLAY_SETTINGS="limit,reserve,start,stop"
	if v := os.Getenv("REPLAY_SETTINGS"); v != "" {
		var limit, reserve, start, stop float64
		_, err := fmt.Sscanf(v, "%f,%f,%f,%f", &limit, &reserve, &start, &stop)
		require.NoError(t, err)
		t.Run("settings", func(t *testing.T) { scenario(t, limit, reserve, start, stop, false, 0, false) })
	}

	peak := peakW(base, rec.Req.TimeSeries.Dt)

	// nearly empty battery, grid charging running, room for the charging on top
	// of the recorded peak: the goal is reached within the window
	t.Run("charge", func(t *testing.T) {
		scenario(t, peak+float64(rec.Req.Batteries[home].CMax), 40, 30, 90, true, 25, false)
	})

	// the same with the limit below the recorded peak: the hard peak limit wins,
	// the goal may fall short
	t.Run("conflict", func(t *testing.T) {
		scenario(t, max(1000, 0.6*peak), 40, 30, 90, true, 25, true)
	})
}
