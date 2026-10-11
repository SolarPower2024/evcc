package core

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"
	"time"

	"github.com/benbjohnson/clock"
	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/peak"
	"github.com/evcc-io/evcc/util"
	"github.com/evcc-io/evcc/util/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

// readingsFunc returns the declaration of a method of Site in one of evcc's files
func readingsFunc(t *testing.T, file, name string) *ast.FuncDecl {
	t.Helper()

	f, err := parser.ParseFile(token.NewFileSet(), filepath.Join("..", filepath.FromSlash(file)), nil, 0)
	require.NoError(t, err)

	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == name && fd.Recv != nil && fd.Body != nil {
			return fd
		}
	}

	require.Failf(t, "function not found", "%s in %s", name, file)
	return nil
}

// TestPeakGridHookReportsRead pins the hook in updateGridMeter: peak shaving gets
// the grid power as read only if the power was read, which is set where evcc
// takes the value over. Without it evcc keeps the old value for a meter that is
// not available, and peak shaving would count it as a reading.
func TestPeakGridHookReportsRead(t *testing.T) {
	fd := readingsFunc(t, "core/site.go", "updateGridMeter")

	var (
		hook     *ast.CallExpr
		declared bool
		assigned []*ast.AssignStmt
		success  *ast.IfStmt // the branch taking the power over
	)

	ast.Inspect(fd.Body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CallExpr:
			if sel, ok := n.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "setPeakGridEnergy" {
				hook = n
			}

		case *ast.ValueSpec:
			for _, id := range n.Names {
				declared = declared || id.Name == "powerRead"
			}

		case *ast.AssignStmt:
			for _, l := range n.Lhs {
				if id, ok := l.(*ast.Ident); ok && id.Name == "powerRead" {
					assigned = append(assigned, n)
				}
			}

		case *ast.IfStmt:
			// if res, err := ...CurrentPower...; err == nil {
			if cond, ok := n.Cond.(*ast.BinaryExpr); ok && cond.Op == token.EQL && n.Init != nil {
				if x, ok := cond.X.(*ast.Ident); ok && x.Name == "err" {
					ast.Inspect(n.Init, func(m ast.Node) bool {
						if id, ok := m.(*ast.Ident); ok && id.Name == "CurrentPower" {
							success = n
						}
						return true
					})
				}
			}
		}

		return true
	})

	require.NotNil(t, hook, "updateGridMeter no longer calls setPeakGridEnergy")
	require.Len(t, hook.Args, 2, "setPeakGridEnergy takes the counter and whether the power was read")

	arg, ok := hook.Args[1].(*ast.Ident)
	require.True(t, ok && arg.Name == "powerRead", "the hook is not given powerRead")
	assert.True(t, declared, "powerRead is not declared")

	require.NotNil(t, success, "the branch reading the grid power moved")
	require.Len(t, assigned, 1, "powerRead is set in one place only")

	// set to true, and inside the branch taking the power over
	rhs, ok := assigned[0].Rhs[0].(*ast.Ident)
	assert.True(t, ok && rhs.Name == "true", "powerRead is not set to true")
	assert.True(t, assigned[0].Pos() > success.Body.Pos() && assigned[0].End() < success.Body.End(),
		"powerRead is set outside the branch that takes the power over")

	// reported after the branch, not before it ran
	assert.Greater(t, hook.Pos(), success.End(), "the hook runs before the power is read")
}

// peakOutageSite sets up a scenario below the reserve on a mock clock, and
// returns a step of 30s as a cycle runs it: the hooks of updateGridMeter and
// collectMeters, then the peak shaving cycle with the given values. Writes to the entity are
// recorded in writes.
func peakOutageSite(t *testing.T) (sc *scenario, s *peakState, step func(gridRead, batteryRead bool, grid, battery float64) bool, writes *[]float64) {
	t.Helper()

	sc = newScenario(t)
	clk := clock.NewMock()

	writes = new([]float64)

	s = sc.site.peak()
	s.clock = clk
	s.set = func(v float64) error { *writes = append(*writes, v); return nil }

	step = func(gridRead, batteryRead bool, grid, battery float64) bool {
		clk.Add(30 * time.Second)
		sc.site.setPeakGridEnergy(nil, gridRead)
		sc.site.setPeakMeterRead("battery", 0, readError(batteryRead))
		return sc.cycle(20, grid, battery)
	}

	return sc, s, step, writes
}

// TestPeakGridUnreadIsAnOutage: a grid meter that has no power (a Home Assistant
// sensor unavailable) is no failure for evcc, which keeps the last value. For
// peak shaving it is an outage like a failing meter: the setpoint stays, after
// peak.MaxGap the free value is written once and grid charging pauses.
func TestPeakGridUnreadIsAnOutage(t *testing.T) {
	for _, tc := range []struct {
		name     string
		grid     float64
		setpoint float64
		charging bool // grid charging runs: a peak pauses it
	}{
		// the old value is above the limit: the setpoint would grow cycle by cycle
		{"old value above the limit", 6000, 1000, false},
		// below it: the old value would keep grid charging going for good
		{"old value below the limit", 3000, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sc, s, step, writes := peakOutageSite(t)

			assert.Equal(t, tc.charging, step(true, true, tc.grid, 0))
			assert.Equal(t, []float64{tc.setpoint}, *writes)

			// unavailable up to peak.MaxGap: nothing is metered or written
			*writes = nil
			start := s.window
			for range 4 {
				assert.Equal(t, tc.charging, step(false, true, tc.grid, tc.setpoint))
			}
			assert.Empty(t, *writes, "the setpoint stays")
			assert.Equal(t, start, s.window, "the stale value is not metered")
			assert.True(t, sc.site.peakShavingActive())

			// beyond it: free value once, no grid charging
			assert.False(t, step(false, true, tc.grid, tc.setpoint))
			assert.False(t, step(false, true, tc.grid, tc.setpoint))
			assert.Equal(t, []float64{peak.DefaultFreeValue}, *writes)
			assert.False(t, sc.site.peakShavingActive())
			assert.True(t, s.metersLost)

			// back: the setpoint again
			*writes = nil
			step(true, true, 3000, 0)
			assert.Equal(t, []float64{0}, *writes)
			assert.False(t, s.metersLost)
			assert.True(t, sc.site.peakShavingActive())
		})
	}
}

// TestPeakGridUnreadIgnoredWhenOff: with peak shaving off an unavailable grid meter
// changes nothing and logs nothing, the window is metered as before
func TestPeakGridUnreadIgnoredWhenOff(t *testing.T) {
	sc, s, step, writes := peakOutageSite(t)
	s.enabled = false

	for range 8 {
		step(false, true, 6000, 0)
	}

	assert.Empty(t, *writes)
	assert.Empty(t, s.read.lost)
	assert.False(t, sc.site.peakShavingActive())
	assert.Positive(t, s.window.DrawnWs, "the window is metered")
}

// TestPeakGridFrozenIsAnOutage: a grid power that is a number but stands still
// while the battery moves is no reading (a template sensor or an mqtt topic that
// does not expire). Without the check the setpoint grows by what the stale value
// exceeds the allowed power, every cycle.
func TestPeakGridFrozenIsAnOutage(t *testing.T) {
	sc, s, step, writes := peakOutageSite(t)

	// the grid power stays at 6000W, the battery follows the setpoint
	var battery float64
	for range 5 {
		step(true, true, 6000, battery)
		battery = (*writes)[len(*writes)-1]
	}
	assert.Greater(t, battery, 3000.0, "the setpoint grows while the value is trusted")
	assert.True(t, sc.site.peakShavingActive())

	// over peak.MaxGap it counts as an outage: free value at once, as it began
	// with the standstill
	*writes = nil
	assert.False(t, step(true, true, 6000, battery))
	assert.Equal(t, []float64{peak.DefaultFreeValue}, *writes)
	assert.False(t, sc.site.peakShavingActive())
	assert.True(t, s.metersLost)

	// and stays one while it stands still
	*writes = nil
	for range 3 {
		step(true, true, 6000, battery)
	}
	assert.Empty(t, *writes)

	// a value that moves again ends it
	step(true, true, 4000, 0)
	assert.False(t, s.metersLost)
	assert.Empty(t, s.read.lost)
	assert.True(t, sc.site.peakShavingActive())
}

// TestPeakGridFollowingBatteryIsNoOutage: the grid power that follows the battery
// is a reading, also when the battery moves, and so is a value that stands still
// while the battery does
func TestPeakGridFollowingBatteryIsNoOutage(t *testing.T) {
	sc, s, step, _ := peakOutageSite(t)

	// demand 6000W, the battery covers what exceeds the allowed power
	var battery float64
	for range 12 {
		step(true, true, 6000-battery, battery)
		battery = val(sc.peak)
	}
	assert.True(t, sc.site.peakShavingActive())
	assert.False(t, s.metersLost)

	// a grid power at a standstill with a battery that stands still as well
	sc, s, step, _ = peakOutageSite(t)
	for range 12 {
		step(true, true, 4000, 800)
	}
	assert.True(t, sc.site.peakShavingActive())
	assert.False(t, s.metersLost)
}

// readError returns the result of a power read that worked or did not
func readError(ok bool) error {
	if ok {
		return nil
	}
	return api.ErrNotAvailable
}

// TestPeakMeterHookReportsRead pins the hook in collectMeters: it is given the
// result of the power read right where evcc has it, before evcc turns a failed
// read into 0 W without a mark, and for every meter.
func TestPeakMeterHookReportsRead(t *testing.T) {
	fd := readingsFunc(t, "core/site.go", "collectMeters")

	// the read is in the function run for each meter
	var body *ast.BlockStmt
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		if fl, ok := n.(*ast.FuncLit); ok && body == nil {
			body = fl.Body
		}
		return body == nil
	})
	require.NotNil(t, body, "collectMeters no longer reads each meter in a function of its own")

	callee := func(s ast.Stmt) string {
		var e ast.Expr
		switch s := s.(type) {
		case *ast.ExprStmt:
			e = s.X
		case *ast.AssignStmt:
			e = s.Rhs[0]
		default:
			return ""
		}

		if c, ok := e.(*ast.CallExpr); ok {
			switch f := c.Fun.(type) {
			case *ast.SelectorExpr:
				return f.Sel.Name
			case *ast.IndexListExpr:
				if sel, ok := f.X.(*ast.SelectorExpr); ok {
					return sel.Sel.Name
				}
			}
		}
		return ""
	}

	for i, s := range body.List {
		if callee(s) != "RetryWithData" {
			continue
		}

		// power, err := backoff.RetryWithData(meter.CurrentPower, ...)
		read := s.(*ast.AssignStmt)
		require.Len(t, read.Lhs, 2)
		errName := read.Lhs[1].(*ast.Ident).Name

		require.Less(t, i+1, len(body.List))
		hook, ok := body.List[i+1].(*ast.ExprStmt)
		require.True(t, ok && callee(hook) == "setPeakMeterRead", "setPeakMeterRead does not directly follow the power read")

		args := hook.X.(*ast.CallExpr).Args
		require.Len(t, args, 3)
		for j, want := range []string{"key", "i", errName} {
			id, ok := args[j].(*ast.Ident)
			assert.True(t, ok && id.Name == want, "argument %d of setPeakMeterRead is not %s", j+1, want)
		}

		return
	}

	require.Fail(t, "the power read in collectMeters moved")
}

// TestPeakMeterHookRecordsBatteryReads: collectMeters tells peak shaving which
// battery meters could not be read, and only those
func TestPeakMeterHookRecordsBatteryReads(t *testing.T) {
	ctrl := gomock.NewController(t)

	meter := func(err error) config.Device[api.Meter] {
		m := api.NewMockMeter(ctrl)
		m.EXPECT().CurrentPower().Return(0.0, err).AnyTimes()
		return config.NewStaticDevice(config.Named{Name: "bat"}, api.Meter(m))
	}

	good, bad := meter(nil), meter(api.ErrNotAvailable)
	site := &Site{log: util.NewLogger("test")}
	s := site.peak()

	site.collectMeters("battery", []config.Device[api.Meter]{good, bad})
	assert.Equal(t, map[int]bool{0: false, 1: true}, s.read.batteryFailed)

	// the next cycle reads both
	site.collectMeters("battery", []config.Device[api.Meter]{good, good})
	assert.Equal(t, map[int]bool{0: false, 1: false}, s.read.batteryFailed)

	// other meters are not peak shaving's business
	s.read.batteryFailed = nil
	site.collectMeters("pv", []config.Device[api.Meter]{bad})
	assert.Nil(t, s.read.batteryFailed)
}

// TestPeakBatteryUnreadIsAnOutage: a battery power that cannot be read stays 0 in
// evcc, and the setpoint, which adds the battery power back, jumps between the
// value with and without it (3000, 0, 3107, 0 ...). It counts as an outage: the
// setpoint stays, after peak.MaxGap the free value is written and grid charging
// pauses.
func TestPeakBatteryUnreadIsAnOutage(t *testing.T) {
	sc, s, step, writes := peakOutageSite(t)

	// a peak 3 kW above the limit, the battery covers it
	step(true, true, 8000, 0)
	assert.Equal(t, []float64{3000}, *writes)

	// the battery follows, its power reads 0: the grid shows 5000W
	*writes = nil
	for range 4 {
		step(true, false, 5000, 0)
	}
	assert.Empty(t, *writes, "the setpoint stays")
	assert.True(t, sc.site.peakShavingActive())

	// beyond peak.MaxGap: free value once
	step(true, false, 5000, 0)
	step(true, false, 5000, 0)
	assert.Equal(t, []float64{peak.DefaultFreeValue}, *writes)
	assert.False(t, sc.site.peakShavingActive())
	assert.True(t, s.metersLost)
	assert.True(t, sc.site.peakPausesGridCharge())

	// readable again: the setpoint from the real battery power
	*writes = nil
	step(true, true, 5000, 3000)
	require.Len(t, *writes, 1)
	assert.InDelta(t, 3000, (*writes)[0], 150)
	assert.False(t, s.metersLost)
	assert.True(t, sc.site.peakShavingActive())
}

// TestPeakBatteryUnreadOnceKeepsRunning: a read that fails now and then, with
// values in between, never reaches peak.MaxGap and hands nothing back
func TestPeakBatteryUnreadOnceKeepsRunning(t *testing.T) {
	sc, s, step, writes := peakOutageSite(t)

	step(true, true, 8000, 0)
	for range 6 {
		step(true, false, 5000, 0)
		step(true, true, 5000, 3000)
	}

	assert.True(t, sc.site.peakShavingActive())
	assert.False(t, s.metersLost)
	for _, v := range *writes {
		assert.Less(t, v, peak.DefaultFreeValue)
	}
}

// TestPeakBatteryUnreadWithTwoBatteries: one battery that cannot be read is
// enough, the sum of the battery power is wrong then
func TestPeakBatteryUnreadWithTwoBatteries(t *testing.T) {
	site := &Site{log: util.NewLogger("test")}
	s := site.peak()

	site.setPeakMeterRead("battery", 0, nil)
	site.setPeakMeterRead("battery", 1, api.ErrNotAvailable)
	cause, _ := s.outage(time.Now(), 5000, 0)
	assert.Equal(t, outageBatteryUnread, cause)

	site.setPeakMeterRead("battery", 1, nil)
	cause, _ = s.outage(time.Now(), 5000, 0)
	assert.Empty(t, cause)
}
