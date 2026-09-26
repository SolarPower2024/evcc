package core

import (
	"context"
	"encoding/json"
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

	// the fork's inputs: peak limit below the recorded peak, reserve, running soc grid charging
	limit := max(1000, 0.6*peakW(base, rec.Req.TimeSeries.Dt))

	site := &Site{log: util.NewLogger("replay")}
	setPeakShaving(site, limit, 40)
	s := site.lms()
	s.socChargeEnabled, s.socChargeStart, s.socChargeStop, s.socChargeRunning = true, 30, 90, true

	req := rec.Req
	batteries := make([]optimizerBattery, len(req.Batteries))
	for i := range req.Batteries {
		batteries[i] = optimizerBattery{cfg: req.Batteries[i], detail: rec.Details.BatteryDetails[i]}
	}
	batteries[home].cfg.ChargeFromGrid = true // as with a battery that has a charge mode

	site.applyLmOptimizerInputs(&req, batteries)

	req.Batteries = make([]optimizer.BatteryConfig, len(batteries))
	for i, b := range batteries {
		req.Batteries[i] = b.cfg
	}

	bat := req.Batteries[home]
	require.NotNil(t, bat.SGoal)

	res, elapsed := solve(t, req)
	t.Logf("with fork inputs: %s, %v, limit %.0f W, grid peak %.0f W, min soc %.0f Wh, goal %.0f Wh", res.Status, elapsed.Round(time.Millisecond), limit, peakW(res, req.TimeSeries.Dt), bat.SMin, bat.SGoal[slotAfter(req.TimeSeries.Dt, site.gridChargeWindow())])

	soc := res.Batteries[home].StateOfCharge
	require.NotEmpty(t, soc)

	for i, v := range soc {
		assert.GreaterOrEqual(t, v, bat.SMin-1, "soc below the minimum at step %d", i)
	}

	goalIdx := slotAfter(req.TimeSeries.Dt, site.gridChargeWindow())
	assert.GreaterOrEqual(t, soc[goalIdx], bat.SGoal[goalIdx]-1, "goal not reached at step %d", goalIdx)

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
