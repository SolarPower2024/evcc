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
// clock of its own. The loads a and b are on it, the circuit measures what the
// loads draw.
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

// measure lets the circuit measure what the loads draw
func (r *startRig) measure() {
	r.t.Helper()
	require.NoError(r.t, r.c.Update(r.loads))
}

func (r *startRig) advance(d time.Duration) { r.now = r.now.Add(d) }

// TestStartReserveBlocksSecondLoad: the circuit measures 12.9 A, the wallbox is
// granted 18 A and draws nothing yet. The heater, which needs 8.7 A, does not
// get them, the circuit counts 30.9 A.
func TestStartReserveBlocksSecondLoad(t *testing.T) {
	r := newStartRig(t, 35, 0)
	r.base.current = 12.9
	r.measure()

	assert.Equal(t, 18.0, r.m.ValidateCurrent(r.a, r.c, 0, 18))

	// 35 - 12.9 - 18 = 4.1 A are left, less than the heater needs: a load that
	// takes all or nothing stays off
	got := r.m.ValidateCurrent(r.b, r.c, 0, 8.7)
	assert.InDelta(t, 4.1, got, 1e-9)
	assert.Less(t, got, 8.7)

	_, cur := r.m.StartReserve(r.b, r.c)
	assert.Equal(t, 18.0, cur)

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

	assert.Equal(t, 20.0, r.m.ValidateCurrent(r.a, r.c, 0, 20))
	assert.Equal(t, 15.0, r.m.ValidateCurrent(r.b, r.c, 0, 20))
}

// TestStartReserveEndsWhenDrawing: once the wallbox draws, the circuit measures
// it and the reserve is gone
func TestStartReserveEndsWhenDrawing(t *testing.T) {
	r := newStartRig(t, 35, 0)
	r.base.current = 12.9
	r.measure()

	r.m.ValidateCurrent(r.a, r.c, 0, 18)

	// 17.5 A drawn: less than 1 A of the grant remains
	r.advance(15 * time.Second)
	r.a.current = 17.5
	r.measure()

	_, cur := r.m.StartReserve(r.b, r.c)
	assert.Zero(t, cur)
	assert.Equal(t, 4.6, round1(r.m.ValidateCurrent(r.b, r.c, 0, 8.7)), "only the measurement counts: 35 - 30.4")
}

func round1(f float64) float64 { return float64(int(f*10+0.5)) / 10 }

// TestStartReserveShrinksWhileRamping: the reserve is the rest not yet drawn
func TestStartReserveShrinksWhileRamping(t *testing.T) {
	r := newStartRig(t, 35, 0)
	r.m.ValidateCurrent(r.a, r.c, 0, 18)

	r.advance(15 * time.Second)
	r.a.current = 6
	r.measure()

	_, cur := r.m.StartReserve(r.b, r.c)
	assert.Equal(t, 12.0, cur)
}

// TestStartReserveExpires: a car that never draws blocks for a minute only
func TestStartReserveExpires(t *testing.T) {
	r := newStartRig(t, 35, 0)
	r.base.current = 12.9
	r.measure()

	r.m.ValidateCurrent(r.a, r.c, 0, 18)
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
		r.m.ValidateCurrent(r.a, r.c, 18, 18)
		assert.Less(t, r.m.ValidateCurrent(r.b, r.c, 0, 8.7), 8.7)
		r.advance(12 * time.Second)
	}

	// 61 s after the first grant
	r.m.ValidateCurrent(r.a, r.c, 18, 18)
	assert.Equal(t, 8.7, r.m.ValidateCurrent(r.b, r.c, 0, 8.7))

	// a higher grant starts it anew
	assert.Equal(t, 8.7, r.m.ValidateCurrent(r.b, r.c, 0, 8.7))
	r.a.current = 0
	r.m.ValidateCurrent(r.a, r.c, 18, 22)
	assert.Less(t, r.m.ValidateCurrent(r.b, r.c, 0, 8.7), 8.7)
}

// TestStartReserveRaise: raising from 10 to 16 A reserves the 6 A the car does
// not draw yet
func TestStartReserveRaise(t *testing.T) {
	r := newStartRig(t, 35, 0)
	r.a.current = 10
	r.measure()

	r.m.ValidateCurrent(r.a, r.c, 10, 10)
	r.advance(5 * time.Minute)
	assert.Equal(t, 16.0, r.m.ValidateCurrent(r.a, r.c, 10, 16))

	_, cur := r.m.StartReserve(r.b, r.c)
	assert.Equal(t, 6.0, cur)
}

// TestStartReserveKeepsOwnDraw: a load keeps what it draws, the reserve limits
// increases only
func TestStartReserveKeepsOwnDraw(t *testing.T) {
	r := newStartRig(t, 35, 0)
	r.base.current = 12.9
	r.b.current = 8.7
	r.measure()

	assert.Equal(t, 13.0, r.m.ValidateCurrent(r.a, r.c, 0, 13))

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

	r.m.ValidateCurrent(r.a, r.c, 0, 18)
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

	assert.Equal(t, 30.0, m.ValidateCurrent(a, c1, 0, 30))
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

	assert.Equal(t, 30.0, m.ValidateCurrent(a, n.garage, 0, 30))
	assert.Equal(t, 5.0, m.ValidateCurrent(b, n.heating, 0, 20))
}

// TestStartReserveClearedOnOff: switching off ends the reserve
func TestStartReserveClearedOnOff(t *testing.T) {
	r := newStartRig(t, 35, 0)
	r.m.ValidateCurrent(r.a, r.c, 0, 18)

	_, cur := r.m.StartReserve(r.b, r.c)
	assert.Equal(t, 18.0, cur)

	r.m.ValidateCurrent(r.a, r.c, 0, 0)

	_, cur = r.m.StartReserve(r.b, r.c)
	assert.Zero(t, cur)
}

// TestStartReserveClearedOnForget: a load that is forgotten reserves nothing
func TestStartReserveClearedOnForget(t *testing.T) {
	r := newStartRig(t, 35, 0)
	r.m.ValidateCurrent(r.a, r.c, 0, 18)
	r.m.Forget(r.a)

	_, cur := r.m.StartReserve(r.b, r.c)
	assert.Zero(t, cur)
}

// TestStartReservePeekDoesNotNote: a what-if request reserves nothing
func TestStartReservePeekDoesNotNote(t *testing.T) {
	r := newStartRig(t, 35, 0)
	assert.Equal(t, 18.0, r.m.PeekCurrent(r.a, r.c, 0, 18))

	_, cur := r.m.StartReserve(r.b, r.c)
	assert.Zero(t, cur)
}

// TestStartReserveDeniedIsNoGrant: a request the circuit cannot serve in full
// for a load that takes all or nothing is no grant
func TestStartReserveDeniedIsNoGrant(t *testing.T) {
	r := newStartRig(t, 35, 0)
	r.base.current = 30
	r.measure()

	assert.Less(t, r.m.ValidateCurrent(r.a, r.c, 0, 18), 18.0)

	_, cur := r.m.StartReserve(r.b, r.c)
	assert.Zero(t, cur)
}

// TestStartReserveRegulatedPartialGrant: a regulated load that gets going on
// part of its request reserves that part
func TestStartReserveRegulatedPartialGrant(t *testing.T) {
	r := newStartRig(t, 35, 0)
	r.base.current = 25
	r.measure()

	got := r.m.RequestCurrent(r.a, r.c, 0, 18, lm.Demand{Regulated: true, Min: 6})
	assert.Equal(t, 10.0, got)

	_, cur := r.m.StartReserve(r.b, r.c)
	assert.Equal(t, 10.0, cur)

	// below its minimum it does not start and reserves nothing
	r2 := newStartRig(t, 35, 0)
	r2.base.current = 32
	r2.measure()
	assert.Equal(t, 3.0, r2.m.RequestCurrent(r2.a, r2.c, 0, 18, lm.Demand{Regulated: true, Min: 6}))

	_, cur = r2.m.StartReserve(r2.b, r2.c)
	assert.Zero(t, cur)
}

// TestStartReservePower is the same for a limit in W
func TestStartReservePower(t *testing.T) {
	r := newStartRig(t, 0, 20000)
	r.base.power = 4000
	r.measure()

	assert.Equal(t, 12000.0, r.m.ValidatePower(r.a, r.c, 0, 12000))

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
	r.m.ValidatePower(r.a, r.c, 0, 12000)

	assert.Less(t, r.m.ValidatePower(r.b, r.c, 0, 9000), 9000.0)

	r.advance(61 * time.Second)
	assert.Equal(t, 9000.0, r.m.ValidatePower(r.b, r.c, 0, 9000))

	// the same grant again does not block anew
	r.m.ValidatePower(r.a, r.c, 12000, 12000)
	assert.Equal(t, 9000.0, r.m.ValidatePower(r.b, r.c, 0, 9000))
}

// TestStartReserveKeepsOwnDrawPower
func TestStartReserveKeepsOwnDrawPower(t *testing.T) {
	r := newStartRig(t, 0, 20000)
	r.base.power = 4000
	r.b.power = 5000
	r.measure()

	assert.Equal(t, 11000.0, r.m.ValidatePower(r.a, r.c, 0, 11000))
	assert.Equal(t, 5000.0, r.m.ValidatePower(r.b, r.c, 5000, 5000))
}

// TestStartReserveBatteryAsLoad: the battery's grid charge counts as a load with
// a positive charge power
func TestStartReserveBatteryAsLoad(t *testing.T) {
	r := newStartRig(t, 0, 10000)
	battery := &testLoad{title: "battery", circuit: r.c}
	r.loads = append(r.loads, battery)
	r.measure()

	assert.Equal(t, 8000.0, r.m.ValidatePower(battery, r.c, 0, 8000))
	assert.Equal(t, 2000.0, r.m.ValidatePower(r.a, r.c, 0, 5000))

	// it is full and never draws: after a minute the circuit is free again
	r.advance(61 * time.Second)
	assert.Equal(t, 5000.0, r.m.ValidatePower(r.a, r.c, 0, 5000))
}
