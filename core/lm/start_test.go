package lm_test

import (
	"testing"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/circuit"
	"github.com/evcc-io/evcc/core/lm"
	"github.com/evcc-io/evcc/util"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// startRig is a circuit of 35 A (or 20 kW) with a base load and a manager on a
// clock of its own. The loads a and b are on it, the circuit adds up what the
// loads report.
type startRig struct {
	t     *testing.T
	m     *lm.Manager
	c     api.Circuit
	now   time.Time
	loads []api.CircuitLoad
	base  *testLoad
	a, b  *testLoad
}

func newStartRig(t *testing.T, maxCurrent, maxPower float64) *startRig {
	t.Helper()

	c, err := circuit.New(util.NewLogger("test"), "test", maxCurrent, maxPower, nil, 0)
	require.NoError(t, err)

	r := &startRig{t: t, m: lm.New(), c: c, now: time.Date(2026, 11, 14, 13, 6, 24, 0, time.UTC)}
	r.m.SetClock(func() time.Time { return r.now })

	r.base = &testLoad{title: "house", circuit: c}
	r.a = &testLoad{title: "wallbox", circuit: c}
	r.b = &testLoad{title: "heater", circuit: c}
	r.loads = []api.CircuitLoad{r.base, r.a, r.b}
	r.measure()

	return r
}

// measure lets the circuit add up what the loads report
func (r *startRig) measure() {
	r.t.Helper()
	require.NoError(r.t, r.c.Update(r.loads))
}

func (r *startRig) advance(d time.Duration) { r.now = r.now.Add(d) }

// grant asks for a current and notes what was granted, as a loadpoint does
func (r *startRig) grant(l *testLoad, old, new float64) float64 {
	got := r.m.ValidateCurrent(l, r.c, old, new)
	r.m.NoteStart(l, r.c, got, 0)
	return got
}

// grantPower is grant for power
func (r *startRig) grantPower(l *testLoad, old, new float64) float64 {
	got := r.m.ValidatePower(l, r.c, old, new)
	r.m.NoteStart(l, r.c, 0, got)
	return got
}

func reserveCurrent(m *lm.Manager, l lm.Load, c api.Circuit) float64 {
	_, cur := m.StartReserve(l, c)
	return cur
}

// TestStartReserveBlocksSecondLoad: the circuit measures 12.9 A, the wallbox is
// granted 18 A and draws nothing yet. Of the 8.7 A the heater needs only 4.1 A
// are left, the circuit counts 30.9 A.
func TestStartReserveBlocksSecondLoad(t *testing.T) {
	r := newStartRig(t, 35, 0)
	r.base.current = 12.9
	r.measure()

	assert.Equal(t, 18.0, r.grant(r.a, 0, 18))

	got := r.m.ValidateCurrent(r.b, r.c, 0, 8.7)
	assert.InDelta(t, 4.1, got, 1e-9)
	assert.Equal(t, 18.0, reserveCurrent(r.m, r.b, r.c))

	// without the reserve it would have fitted
	r2 := newStartRig(t, 35, 0)
	r2.base.current = 12.9
	r2.measure()
	assert.Equal(t, 8.7, r2.m.ValidateCurrent(r2.b, r2.c, 0, 8.7))
}

// TestStartReserveSameCycle: two requests one after the other without a new
// measurement share the gap only once
func TestStartReserveSameCycle(t *testing.T) {
	r := newStartRig(t, 35, 0)

	assert.Equal(t, 20.0, r.grant(r.a, 0, 20))
	assert.Equal(t, 15.0, r.grant(r.b, 0, 20))
}

// TestStartReserveRequestDoesNotNote: a request alone reserves nothing, only the
// grant the load notes (a battery probe, a what-if, a loadpoint that ends up off)
func TestStartReserveRequestDoesNotNote(t *testing.T) {
	r := newStartRig(t, 35, 0)
	assert.Equal(t, 18.0, r.m.ValidateCurrent(r.a, r.c, 0, 18))
	assert.Equal(t, 18.0, r.m.PeekCurrent(r.a, r.c, 0, 18))
	assert.Equal(t, 9000.0, r.m.ValidatePower(r.a, r.c, 0, 9000))

	pow, cur := r.m.StartReserve(r.b, r.c)
	assert.Zero(t, pow)
	assert.Zero(t, cur)
}

// TestStartReserveEndsWhenDrawing: once the wallbox draws, the circuit measures
// it and the reserve is gone
func TestStartReserveEndsWhenDrawing(t *testing.T) {
	r := newStartRig(t, 35, 0)
	r.base.current = 12.9
	r.measure()

	r.grant(r.a, 0, 18)

	// 17.5 A drawn: less than 1 A of the grant remains
	r.advance(15 * time.Second)
	r.a.current = 17.5
	r.measure()

	assert.Zero(t, reserveCurrent(r.m, r.b, r.c))
	assert.InDelta(t, 4.6, r.m.ValidateCurrent(r.b, r.c, 0, 8.7), 1e-9, "only the measurement counts: 35 - 30.4")
}

// TestStartReserveShrinksWhileRamping: the reserve is the rest not yet drawn
func TestStartReserveShrinksWhileRamping(t *testing.T) {
	r := newStartRig(t, 35, 0)
	r.grant(r.a, 0, 18)

	r.advance(15 * time.Second)
	r.a.current = 6
	r.measure()

	assert.Equal(t, 12.0, reserveCurrent(r.m, r.b, r.c))
}

// TestStartReserveExpires: a car that never draws blocks for a minute only
func TestStartReserveExpires(t *testing.T) {
	r := newStartRig(t, 35, 0)
	r.base.current = 12.9
	r.measure()

	r.grant(r.a, 0, 18)
	assert.Less(t, r.m.ValidateCurrent(r.b, r.c, 0, 8.7), 8.7)

	r.advance(59 * time.Second)
	assert.Less(t, r.m.ValidateCurrent(r.b, r.c, 0, 8.7), 8.7)

	r.advance(2 * time.Second)
	assert.Equal(t, 8.7, r.m.ValidateCurrent(r.b, r.c, 0, 8.7))
}

// TestStartReserveNotExtendedByRepeat: the same grant in every cycle does not
// start the minute anew, a car that is full and never draws does not block
func TestStartReserveNotExtendedByRepeat(t *testing.T) {
	r := newStartRig(t, 35, 0)
	r.base.current = 12.9
	r.measure()

	for range 5 {
		r.grant(r.a, 0, 18)
		assert.Less(t, r.m.ValidateCurrent(r.b, r.c, 0, 8.7), 8.7)
		r.advance(12 * time.Second)
	}

	// 60 s after the first grant
	r.grant(r.a, 0, 18)
	assert.Equal(t, 8.7, r.m.ValidateCurrent(r.b, r.c, 0, 8.7))

	// a grant above the highest one starts it anew
	r.grant(r.a, 0, 22)
	assert.Less(t, r.m.ValidateCurrent(r.b, r.c, 0, 8.7), 8.7)
}

// TestStartReserveNotRestartedBySwing: an offer going down and back up, as with
// pv surplus, does not start the minute anew
func TestStartReserveNotRestartedBySwing(t *testing.T) {
	r := newStartRig(t, 35, 0)
	r.base.current = 12.9
	r.measure()

	r.grant(r.a, 0, 16)
	r.advance(61 * time.Second)

	for _, cur := range []float64{13, 16, 12, 16} {
		r.grant(r.a, 0, cur)
		assert.Equal(t, 8.7, r.m.ValidateCurrent(r.b, r.c, 0, 8.7), "offer %gA", cur)
		r.advance(15 * time.Second)
	}

	// off and on again is a new start
	r.m.NoteStart(r.a, r.c, 0, 0)
	r.grant(r.a, 0, 16)
	assert.Less(t, r.m.ValidateCurrent(r.b, r.c, 0, 8.7), 8.7)
}

// TestStartReserveRaise: raising from 10 to 16 A reserves the 6 A the car does
// not draw yet
func TestStartReserveRaise(t *testing.T) {
	r := newStartRig(t, 35, 0)
	r.a.current = 10
	r.measure()

	r.grant(r.a, 10, 10)
	r.advance(5 * time.Minute)
	assert.Equal(t, 16.0, r.grant(r.a, 10, 16))

	assert.Equal(t, 6.0, reserveCurrent(r.m, r.b, r.c))
}

// TestStartReserveKeepsOwnDraw: a load keeps what it draws, the reserve limits
// increases only
func TestStartReserveKeepsOwnDraw(t *testing.T) {
	r := newStartRig(t, 35, 0)
	r.base.current = 12.9
	r.b.current = 8.7
	r.measure()

	assert.Equal(t, 13.0, r.grant(r.a, 0, 13))

	// 35 - 21.6 - 13 = 0.4 A are left, the heater keeps its 8.7 A
	assert.Equal(t, 8.7, r.m.ValidateCurrent(r.b, r.c, 8.7, 8.7))
	assert.Equal(t, 8.7, r.m.PeekCurrent(r.b, r.c, 8.7, 8.7))

	// and may rise by that much only
	assert.InDelta(t, 9.1, r.m.ValidateCurrent(r.b, r.c, 8.7, 12), 1e-9)
}

// TestStartReserveIgnoresPriority: it is about the limit, not about order
func TestStartReserveIgnoresPriority(t *testing.T) {
	r := newStartRig(t, 35, 0)
	r.base.current = 12.9
	r.measure()
	r.a.prio, r.b.prio = 0, 5

	r.grant(r.a, 0, 18)
	assert.Less(t, r.m.ValidateCurrent(r.b, r.c, 0, 8.7), 8.7)
}

// TestStartReserveOtherCircuit: a sibling circuit without a shared limit is not
// affected
func TestStartReserveOtherCircuit(t *testing.T) {
	log := util.NewLogger("test")
	c1, err := circuit.New(log, "one", 35, 0, nil, 0)
	require.NoError(t, err)
	c2, err := circuit.New(log, "two", 35, 0, nil, 0)
	require.NoError(t, err)

	m := lm.New()
	a := &testLoad{title: "wallbox", circuit: c1}
	b := &testLoad{title: "heater", circuit: c2}

	m.NoteStart(a, c1, m.ValidateCurrent(a, c1, 0, 30), 0)
	assert.Equal(t, 30.0, m.ValidateCurrent(b, c2, 0, 30))
}

// TestStartReserveSharedParent: loads on sibling circuits below a limited parent
// share its limit
func TestStartReserveSharedParent(t *testing.T) {
	n := newNested(t, [2]float64{35, 0}, [2]float64{})
	m := lm.New()

	a := n.load("wallbox", 0, n.garage, 0, 0)
	b := n.load("heater", 0, n.heating, 0, 0)
	n.update(t, a, b)

	m.NoteStart(a, n.garage, m.ValidateCurrent(a, n.garage, 0, 30), 0)
	assert.Equal(t, 5.0, m.ValidateCurrent(b, n.heating, 0, 20))
}

// TestStartReserveOwnSubCircuitFree: the reserve counts on the levels the
// starting load draws through only. Main 35 A, garage 32 A with the wallbox,
// heating 16 A with the heater: the wallbox is granted 30 A, so the main fuse
// has 5 A left for the heater, not the 0 A that taking the 30 A off the
// heating's own 16 A would leave.
func TestStartReserveOwnSubCircuitFree(t *testing.T) {
	log := util.NewLogger("test")
	main, err := circuit.New(log, "main", 35, 0, nil, 0)
	require.NoError(t, err)

	mk := func(title string, limit float64) *level {
		c, err := circuit.New(log, title, limit, 0, nil, 0)
		require.NoError(t, err)
		l := &level{Circuit: c, parent: main}
		main.RegisterChild(l)
		return l
	}
	garage, heating := mk("garage", 32), mk("heating", 16)

	a := &testLoad{title: "wallbox", circuit: garage.Circuit}
	b := &testLoad{title: "heater", circuit: heating.Circuit}
	require.NoError(t, main.Update([]api.CircuitLoad{a, b}))

	m := lm.New()
	m.NoteStart(a, garage, m.ValidateCurrent(a, garage, 0, 30), 0)
	assert.Equal(t, 5.0, m.ValidateCurrent(b, heating, 0, 10))
}

// startMeter is a circuit meter with phase currents
type startMeter struct{ current float64 }

func (m *startMeter) CurrentPower() (float64, error) { return 3 * 230 * m.current, nil }
func (m *startMeter) Currents() (float64, float64, float64, error) {
	return m.current, m.current, m.current, nil
}

// TestStartReserveMeteredWithoutPhaseCurrents: a charger without phase currents
// reports its offer as current while it draws nothing yet. A circuit with a
// meter measures what flows, so the reserve counts the whole grant there.
func TestStartReserveMeteredWithoutPhaseCurrents(t *testing.T) {
	meter := &startMeter{current: 12.9}
	c, err := circuit.New(util.NewLogger("test"), "main", 35, 0, meter, 0)
	require.NoError(t, err)
	require.NoError(t, c.Update(nil))

	m := lm.New()
	a := &testLoad{title: "wallbox", circuit: c}
	b := &testLoad{title: "heater", circuit: c}

	m.NoteStart(a, c, m.ValidateCurrent(a, c, 0, 18), 0)
	a.current = 18 // the offer, no power yet

	assert.Equal(t, 18.0, reserveCurrent(m, b, c))
	assert.Less(t, m.ValidateCurrent(b, c, 0, 8.7), 8.7)

	// drawing: the meter sees it
	a.power, meter.current = 12000, 30.9
	require.NoError(t, c.Update(nil))
	assert.Zero(t, reserveCurrent(m, b, c))
}

// TestStartReserveClearedOnOff: switching off ends the reserve
func TestStartReserveClearedOnOff(t *testing.T) {
	r := newStartRig(t, 35, 0)
	r.grant(r.a, 0, 18)
	assert.Equal(t, 18.0, reserveCurrent(r.m, r.b, r.c))

	r.m.NoteStart(r.a, r.c, 0, 0)
	assert.Zero(t, reserveCurrent(r.m, r.b, r.c))
}

// TestStartReserveClearedOnForget: a load that is forgotten reserves nothing
func TestStartReserveClearedOnForget(t *testing.T) {
	r := newStartRig(t, 35, 0)
	r.grant(r.a, 0, 18)
	r.m.Forget(r.a)

	assert.Zero(t, reserveCurrent(r.m, r.b, r.c))
}

// TestStartReservePower is the same for a limit in W
func TestStartReservePower(t *testing.T) {
	r := newStartRig(t, 0, 20000)
	r.base.power = 4000
	r.measure()

	assert.Equal(t, 12000.0, r.grantPower(r.a, 0, 12000))

	// 20000 - 4000 - 12000 = 4000 W are left
	assert.Equal(t, 4000.0, r.m.ValidatePower(r.b, r.c, 0, 9000))

	pow, _ := r.m.StartReserve(r.b, r.c)
	assert.Equal(t, 12000.0, pow)

	// drawn: only the measurement counts
	r.a.power = 11900
	r.measure()
	pow, _ = r.m.StartReserve(r.b, r.c)
	assert.Zero(t, pow)
}

// TestStartReservePowerExpires
func TestStartReservePowerExpires(t *testing.T) {
	r := newStartRig(t, 0, 20000)
	r.grantPower(r.a, 0, 12000)

	assert.Less(t, r.m.ValidatePower(r.b, r.c, 0, 9000), 9000.0)

	r.advance(61 * time.Second)
	assert.Equal(t, 9000.0, r.m.ValidatePower(r.b, r.c, 0, 9000))

	// the same grant again does not block anew
	r.grantPower(r.a, 12000, 12000)
	assert.Equal(t, 9000.0, r.m.ValidatePower(r.b, r.c, 0, 9000))
}

// TestStartReserveKeepsOwnDrawPower
func TestStartReserveKeepsOwnDrawPower(t *testing.T) {
	r := newStartRig(t, 0, 20000)
	r.base.power = 4000
	r.b.power = 5000
	r.measure()

	assert.Equal(t, 11000.0, r.grantPower(r.a, 0, 11000))
	assert.Equal(t, 5000.0, r.m.ValidatePower(r.b, r.c, 5000, 5000))
}
