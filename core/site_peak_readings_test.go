package core

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"
	"time"

	"github.com/benbjohnson/clock"
	"github.com/evcc-io/evcc/core/peak"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
// returns a step of 30s as a cycle runs it: the hook of updateGridMeter, then
// the peak shaving cycle with the given values. Writes to the entity are
// recorded in writes.
func peakOutageSite(t *testing.T) (sc *scenario, s *peakState, step func(read bool, grid, battery float64) bool, writes *[]float64) {
	t.Helper()

	sc = newScenario(t)
	clk := clock.NewMock()

	writes = new([]float64)

	s = sc.site.peak()
	s.clock = clk
	s.set = func(v float64) error { *writes = append(*writes, v); return nil }

	step = func(read bool, grid, battery float64) bool {
		clk.Add(30 * time.Second)
		sc.site.setPeakGridEnergy(nil, read)
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

			assert.Equal(t, tc.charging, step(true, tc.grid, 0))
			assert.Equal(t, []float64{tc.setpoint}, *writes)

			// unavailable up to peak.MaxGap: nothing is metered or written
			*writes = nil
			start := s.window
			for range 4 {
				assert.Equal(t, tc.charging, step(false, tc.grid, tc.setpoint))
			}
			assert.Empty(t, *writes, "the setpoint stays")
			assert.Equal(t, start, s.window, "the stale value is not metered")
			assert.True(t, sc.site.peakShavingActive())

			// beyond it: free value once, no grid charging
			assert.False(t, step(false, tc.grid, tc.setpoint))
			assert.False(t, step(false, tc.grid, tc.setpoint))
			assert.Equal(t, []float64{peak.DefaultFreeValue}, *writes)
			assert.False(t, sc.site.peakShavingActive())
			assert.True(t, s.metersLost)

			// back: the setpoint again
			*writes = nil
			step(true, 3000, 0)
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
		step(false, 6000, 0)
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
		step(true, 6000, battery)
		battery = (*writes)[len(*writes)-1]
	}
	assert.Greater(t, battery, 3000.0, "the setpoint grows while the value is trusted")
	assert.True(t, sc.site.peakShavingActive())

	// over peak.MaxGap it counts as an outage: free value at once, as it began
	// with the standstill
	*writes = nil
	assert.False(t, step(true, 6000, battery))
	assert.Equal(t, []float64{peak.DefaultFreeValue}, *writes)
	assert.False(t, sc.site.peakShavingActive())
	assert.True(t, s.metersLost)

	// and stays one while it stands still
	*writes = nil
	for range 3 {
		step(true, 6000, battery)
	}
	assert.Empty(t, *writes)

	// a value that moves again ends it
	step(true, 4000, 0)
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
		step(true, 6000-battery, battery)
		battery = val(sc.peak)
	}
	assert.True(t, sc.site.peakShavingActive())
	assert.False(t, s.metersLost)

	// a grid power at a standstill with a battery that stands still as well
	sc, s, step, _ = peakOutageSite(t)
	for range 12 {
		step(true, 4000, 800)
	}
	assert.True(t, sc.site.peakShavingActive())
	assert.False(t, s.metersLost)
}
