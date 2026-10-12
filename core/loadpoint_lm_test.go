package core

import (
	"testing"
	"time"

	"github.com/benbjohnson/clock"
	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/circuit"
	"github.com/evcc-io/evcc/core/lm"
	"github.com/evcc-io/evcc/util"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// lmSwitch is a switch device charger with an optional configured power
type lmSwitch struct {
	api.Charger
	rated float64
}

func (s *lmSwitch) Features() []api.Feature { return []api.Feature{api.SwitchDevice} }
func (s *lmSwitch) RatedPower() float64     { return s.rated }

type lmMeter struct{ power float64 }

func (m *lmMeter) CurrentPower() (float64, error) { return m.power, nil }

// newSwitchLoadpoint returns a switch loadpoint on a 10 kW circuit metered at
// the given grid power
func newSwitchLoadpoint(t *testing.T, rated, grid float64) (*Loadpoint, *lmMeter, api.Circuit) {
	t.Helper()
	Voltage = 230

	m := &lmMeter{power: grid}
	c, err := circuit.New(util.NewLogger("test"), "main", 0, 10000, m, 0)
	require.NoError(t, err)
	require.NoError(t, c.Update(nil))

	lp := &Loadpoint{
		log:        util.NewLogger("lp"),
		clock:      clock.NewMock(),
		circuit:    c,
		charger:    &lmSwitch{rated: rated},
		maxCurrent: 16,
	}

	return lp, m, c
}

func TestSwitchAllOrNothing(t *testing.T) {
	for _, tc := range []struct {
		name     string
		rated    float64 // configured power, 0 = not set
		grid     float64 // grid power with the switch off
		charging float64 // measured switch power, 0 = off
		wantOn   bool
	}{
		// switching on with the configured power
		{"3 kW fits into 4 kW", 3000, 6000, 0, true},
		{"3 kW does not fit into 1.6 kW", 3000, 8410, 0, false},
		{"exactly fits", 3000, 7000, 0, true},
		// without a configured power the nominal 16 A = 3680 W are assumed
		{"no power set, 3680 W do not fit into 3.5 kW", 0, 6500, 0, false},
		{"no power set, 3680 W fit into 4 kW", 0, 6000, 0, true},
		// running: the measurement counts, the 6 A minimum does not
		{"running within the limit", 3000, 9000, 3000, true},
		{"running into an overload", 3000, 11410, 3000, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lp, _, _ := newSwitchLoadpoint(t, tc.rated, tc.grid)
			lp.chargePower = tc.charging

			got := lp.lmLimit(16)
			assert.Equal(t, tc.wantOn, got > 0, "limit %.3gA", got)
		})
	}
}

// lmPhaseMeter is a circuit meter with phase currents
type lmPhaseMeter struct{ current float64 }

func (m *lmPhaseMeter) CurrentPower() (float64, error) { return 3 * Voltage * m.current, nil }
func (m *lmPhaseMeter) Currents() (float64, float64, float64, error) {
	return m.current, m.current, m.current, nil
}

// TestSwitchCurrentLimit: a circuit limited by current only (a fuse) keeps a
// switch off whose current does not fit, and sheds a running one in an over
// current, as upstream does for every load
func TestSwitchCurrentLimit(t *testing.T) {
	for _, tc := range []struct {
		name     string
		grid     float64 // phase current with the switch off
		charging float64 // measured switch power, 0 = off
		wantOn   bool
	}{
		// 3 kW on one phase = 13.04 A
		{"fits next to 2.9 A", 2.9, 0, true},
		{"does not fit next to 3.1 A", 3.1, 0, false},
		{"running within the fuse", 2, 3000, true},
		{"running into an over current", 4, 3000, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			Voltage = 230

			m := &lmPhaseMeter{current: tc.grid + tc.charging/230}
			c, err := circuit.New(util.NewLogger("test"), "fuse", 16, 0, m, 0)
			require.NoError(t, err)
			require.NoError(t, c.Update(nil))

			lp := &Loadpoint{
				log:        util.NewLogger("lp"),
				clock:      clock.NewMock(),
				circuit:    c,
				charger:    &lmSwitch{rated: 3000},
				maxCurrent: 16,
				phases:     1,
			}
			lp.chargePower = tc.charging
			lp.enabled = tc.charging > 0

			got := lp.lmLimit(16)
			assert.Equal(t, tc.wantOn, got > 0, "limit %.3gA", got)
		})
	}
}

func TestSwitchPowerSources(t *testing.T) {
	lp, _, _ := newSwitchLoadpoint(t, 0, 0)

	// nothing known: nominal max current on one phase
	assert.Equal(t, 3680.0, lp.lmSwitchPower())

	// measured while on, remembered once off
	lp.chargePower = 2800
	assert.Equal(t, 2800.0, lp.lmSwitchPower())
	lp.chargePower = 0
	assert.Equal(t, 2800.0, lp.lmSwitchPower())

	// a configured power wins over the remembered measurement
	lp.charger = &lmSwitch{rated: 3000}
	assert.Equal(t, 3000.0, lp.lmSwitchPower())

	// ... but not over an actual measurement
	lp.chargePower = 2950
	assert.Equal(t, 2950.0, lp.lmSwitchPower())
}

// guardFor protects the given loadpoints for d
func guardFor(d time.Duration, lps ...*Loadpoint) {
	lps[0].lmm().SetGuardLookup(func(l lm.Load) time.Duration {
		for _, lp := range lps {
			if l == lp {
				return d
			}
		}
		return 0
	})
}

// cycle runs one limit check with the switch drawing its power while on
func switchCycle(t *testing.T, lp *Loadpoint, m *lmMeter, c api.Circuit, base float64) bool {
	t.Helper()

	if lp.enabled {
		lp.chargePower = 3000
	} else {
		lp.chargePower = 0
	}
	m.power = base + lp.chargePower
	require.NoError(t, c.Update(nil))

	on := lp.lmLimit(16) > 0
	lp.enabled = on

	return on
}

// TestShedGuardSwitch: a protected heater shed in an overload stays off for the
// guard time, although the power is back right after
func TestShedGuardSwitch(t *testing.T) {
	for _, tc := range []struct {
		name      string
		protected bool
	}{
		{"protected", true},
		{"not protected", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lp, m, c := newSwitchLoadpoint(t, 3000, 0)
			clk := lp.clock.(*clock.Mock)

			if tc.protected {
				guardFor(5*time.Minute, lp)
			}

			lp.enabled = true
			require.True(t, switchCycle(t, lp, m, c, 5000), "runs")

			// the base load jumps: 12 kW + 3 kW heater on a 10 kW circuit
			require.False(t, switchCycle(t, lp, m, c, 12000), "shed")

			// the base load is back down, 3 kW would fit again
			clk.Add(10 * time.Second)
			if !tc.protected {
				assert.True(t, switchCycle(t, lp, m, c, 5000), "back on in the next cycle")
				return
			}

			assert.False(t, switchCycle(t, lp, m, c, 5000), "held off")
			clk.Add(4*time.Minute + 49*time.Second)
			assert.False(t, switchCycle(t, lp, m, c, 5000), "still held off just before the end")
			clk.Add(time.Second)
			assert.True(t, switchCycle(t, lp, m, c, 5000), "back on after 5 minutes")
		})
	}
}

// TestShedGuardOnlyAfterShed: a switch that could not start because there was
// no room was not shed and may start as soon as there is
func TestShedGuardOnlyAfterShed(t *testing.T) {
	lp, m, c := newSwitchLoadpoint(t, 3000, 0)
	guardFor(5*time.Minute, lp)

	assert.False(t, switchCycle(t, lp, m, c, 8000), "no room to start")
	assert.True(t, switchCycle(t, lp, m, c, 5000), "starts right away")
}

// TestShedGuardWallbox: a wallbox pushed below its minimum current is shed and
// held off like a switch, while being throttled is not a shed
func TestShedGuardWallbox(t *testing.T) {
	lp, m, c := newSwitchLoadpoint(t, 0, 0)
	lp.charger = &lmWallbox{}
	lp.phases = 3
	lp.minCurrent = 6
	lp.enabled = true
	clk := lp.clock.(*clock.Mock)

	guardFor(5*time.Minute, lp)

	wallbox := func(base float64) float64 {
		m.power = base + lp.chargePower
		require.NoError(t, c.Update(nil))
		return lp.lmLimit(16)
	}

	// 16 A on three phases, throttled to what fits: not a shed
	lp.chargePower = 11040
	assert.InDelta(t, 13.0, wallbox(1000), 0.5, "throttled")
	assert.Zero(t, lp.lmm().Guarded(lp, clk.Now()))

	// not even the 6 A minimum fits anymore: shed
	lp.chargePower = 4140
	assert.Less(t, wallbox(7000), 6.0, "shed")
	assert.Equal(t, 5*time.Minute, lp.lmm().Guarded(lp, clk.Now()))

	// power is back, the guard holds it off
	lp.enabled, lp.chargePower = false, 0
	assert.Zero(t, wallbox(1000), "held off")

	clk.Add(5 * time.Minute)
	assert.InDelta(t, 13.0, wallbox(1000), 0.5, "back after the guard, 9 kW fit")
}

// lmWallbox is a current controlled charger
type lmWallbox struct{ api.Charger }

// TestLmStatusSwitch: what the overview shows for a switch through an overload
// with the shed guard, and the events it logs
func TestLmStatusSwitch(t *testing.T) {
	lp, m, c := newSwitchLoadpoint(t, 3000, 0)
	lp.title = "Heizstab"
	clk := lp.clock.(*clock.Mock)
	guardFor(3*time.Minute, lp)

	state := func() string {
		st, _, _, _ := lmLoadpointState(lp.lmm(), lp, lp.chargePower, clk.Now())
		return st
	}

	// no room to start: waiting, with what it needs and what is free
	assert.False(t, switchCycle(t, lp, m, c, 8000))
	st, requested, allowed, _ := lmLoadpointState(lp.lmm(), lp, lp.chargePower, clk.Now())
	assert.Equal(t, lmStateWaiting, st)
	assert.Equal(t, 3000.0, requested)
	assert.Equal(t, 2000.0, allowed)

	// room: running
	assert.True(t, switchCycle(t, lp, m, c, 5000))
	lp.chargePower = 3000
	assert.Equal(t, lmStateRunning, state())

	// overload: shed and held off
	assert.False(t, switchCycle(t, lp, m, c, 12000))
	st, _, _, until := lmLoadpointState(lp.lmm(), lp, 0, clk.Now())
	assert.Equal(t, lmStateShed, st)
	if assert.NotNil(t, until) {
		assert.Equal(t, clk.Now().Add(3*time.Minute), *until)
	}

	ev := lp.lmm().Events()
	if assert.Len(t, ev, 1) {
		assert.Equal(t, lm.EventShed, ev[0].Type)
		assert.Equal(t, "Heizstab", ev[0].Load)
		assert.Equal(t, 3.0, ev[0].A, "guard minutes")
		assert.Equal(t, 15000.0, ev[0].B, "circuit power when shed")
	}

	// after the guard, with no demand: off
	clk.Add(3 * time.Minute)
	lp.chargePower = 0
	lp.lmLimit(0)
	assert.Equal(t, lmStateOff, state())
}

// TestLmStatusWallboxThrottled: a wallbox limited below its request but above
// its minimum is throttled, logged once
func TestLmStatusWallboxThrottled(t *testing.T) {
	lp, m, c := newSwitchLoadpoint(t, 0, 0)
	lp.title = "Wallbox"
	lp.charger = &lmWallbox{}
	lp.phases = 3
	lp.minCurrent = 6
	lp.enabled = true
	lp.chargePower = 11040

	for range 2 {
		m.power = 1000 + lp.chargePower
		require.NoError(t, c.Update(nil))
		lp.lmLimit(16)
	}

	st, requested, allowed, _ := lmLoadpointState(lp.lmm(), lp, lp.chargePower, lp.clock.Now())
	assert.Equal(t, lmStateThrottled, st)
	assert.Equal(t, 11040.0, requested)
	assert.Equal(t, 9000.0, allowed)

	ev := lp.lmm().Events()
	if assert.Len(t, ev, 1, "logged once, not every cycle") {
		assert.Equal(t, lm.EventThrottled, ev[0].Type)
		assert.Equal(t, 11040.0, ev[0].A)
		assert.Equal(t, "Wallbox", ev[0].Load)
		assert.Equal(t, 9000.0, ev[0].B)
	}
}

// lmLimit runs the circuit check of setLimit, without switching the charger.
// TestSetLimitUsesLmCircuit pins that setLimit gives the same result.
func (lp *Loadpoint) lmLimit(current float64) float64 {
	circuit := lp.lmCircuit()

	currentLimit := circuit.ValidateCurrent(lp.actualMaxChargeCurrent(), current)
	activePhases := lp.ActivePhases()
	powerLimit := circuit.ValidatePower(lp.chargePower, currentToPower(current, activePhases))
	limited := lp.roundedCurrent(min(currentLimit, powerToCurrent(powerLimit, activePhases)))

	circuit.done(current, limited)
	return limited
}

// fakeCharger switches without talking to a device
type fakeCharger struct {
	enabled  bool
	current  int64
	features []api.Feature
	rated    float64
}

func (c *fakeCharger) Status() (api.ChargeStatus, error) { return api.StatusC, nil }
func (c *fakeCharger) Enabled() (bool, error)            { return c.enabled, nil }
func (c *fakeCharger) Enable(v bool) error               { c.enabled = v; return nil }
func (c *fakeCharger) MaxCurrent(v int64) error          { c.current = v; return nil }
func (c *fakeCharger) Features() []api.Feature           { return c.features }
func (c *fakeCharger) RatedPower() float64               { return c.rated }

// TestSetLimitUsesLmCircuit is the contract with upstream's setLimit: its circuit
// check goes through lmCircuit, so priorities, switch devices and the shed guard
// apply to the real control path
func TestSetLimitUsesLmCircuit(t *testing.T) {
	for _, tc := range []struct {
		name     string
		charger  *fakeCharger
		phases   int
		grid     float64 // circuit power with the loadpoint off
		wantOn   bool
		wantAmps int64
	}{
		{"switch fits", &fakeCharger{features: []api.Feature{api.SwitchDevice}, rated: 3000}, 1, 6000, true, 16},
		// upstream alone would switch on with 1.6 kW to spare, above the minimum current
		{"switch does not fit", &fakeCharger{features: []api.Feature{api.SwitchDevice}, rated: 3000}, 1, 8410, false, 0},
		{"wallbox throttled to what fits", &fakeCharger{}, 3, 1000, true, 13},
	} {
		t.Run(tc.name, func(t *testing.T) {
			Voltage = 230

			m := &lmMeter{power: tc.grid}
			c, err := circuit.New(util.NewLogger("test"), "main", 0, 10000, m, 0)
			require.NoError(t, err)
			require.NoError(t, c.Update(nil))

			lp := NewLoadpoint(util.NewLogger("lp"), nil)
			lp.clock = clock.NewMock()
			lp.wakeUpTimer = NewTimer()
			lp.circuit = c
			lp.charger = tc.charger
			lp.phases = tc.phases
			lp.minCurrent, lp.maxCurrent = 6, 16

			require.NoError(t, lp.setLimit(16))
			assert.Equal(t, tc.wantOn, tc.charger.enabled)
			if tc.wantOn {
				assert.Equal(t, tc.wantAmps, tc.charger.current)
			}

			d, ok := lp.lmm().LastDecision(lp)
			require.True(t, ok, "the decision is recorded for the overview")
			assert.Positive(t, d.Requested)
		})
	}
}

// TestLoadpointUsesSiteLoadManagement: a loadpoint prepared by its site shares
// the site's load management, so the battery's demand, the priorities and the
// shed guard set there apply to it; one without a site keeps its own
func TestLoadpointUsesSiteLoadManagement(t *testing.T) {
	site := &Site{log: util.NewLogger("test")}

	lp := &Loadpoint{log: util.NewLogger("lp")}
	lp.site = site
	assert.Same(t, site.lmm(), lp.lmm())

	other := &Site{log: util.NewLogger("test")}
	assert.NotSame(t, site.lmm(), other.lmm(), "one per site")

	alone := &Loadpoint{log: util.NewLogger("lp")}
	assert.Same(t, alone.lmm(), alone.lmm())
	assert.NotSame(t, site.lmm(), alone.lmm())
}

// lmIdleSetup is two regulated loadpoints on a circuit metered at the grid:
// "high" is enabled but draws nothing, its car is full, and asks for its maximum
// every cycle; "low" charges. They are visited in turn, one per cycle.
type lmIdleSetup struct {
	t         *testing.T
	meter     *lmMeter
	circuit   api.Circuit
	base      float64
	high, low *Loadpoint
	highDraws bool // the high one takes what it is allowed instead of staying idle
}

func newIdleSetup(t *testing.T, limit, base float64) *lmIdleSetup {
	t.Helper()
	Voltage = 230

	m := &lmMeter{}
	c, err := circuit.New(util.NewLogger("test"), "main", 0, limit, m, 0)
	require.NoError(t, err)

	site := &Site{log: util.NewLogger("test")}

	mk := func(prio int, status api.ChargeStatus) *Loadpoint {
		return &Loadpoint{
			log:        util.NewLogger("lp"),
			clock:      clock.NewMock(),
			site:       site,
			circuit:    c,
			charger:    &fakeCharger{},
			priority:   prio,
			status:     status,
			enabled:    true,
			phases:     3,
			minCurrent: 6,
			maxCurrent: 16,
		}
	}

	s := &lmIdleSetup{t: t, meter: m, circuit: c, base: base, high: mk(5, api.StatusB), low: mk(1, api.StatusC)}

	// the low one charges with its maximum
	s.low.offeredCurrent = 16
	s.low.chargePower = currentToPower(16, 3)

	return s
}

// visit runs the limit check of one loadpoint. The charging one takes what it is
// allowed down to its minimum, the idle one only offers it.
func (s *lmIdleSetup) visit(lp *Loadpoint) float64 {
	s.t.Helper()

	s.meter.power = s.base + s.low.chargePower + s.high.chargePower
	require.NoError(s.t, s.circuit.Update(nil))

	limited := lp.lmLimit(16)
	if limited < 6 {
		limited = 0
	}

	lp.offeredCurrent = limited
	lp.enabled = limited > 0

	if lp == s.low || s.highDraws {
		lp.chargePower = currentToPower(limited, 3)
	}

	if lp == s.high && s.highDraws {
		lp.status = api.StatusB
		if limited > 0 {
			lp.status = api.StatusC
		}
	}

	return limited
}

// run visits high and low alternately and returns what the low one was allowed
func (s *lmIdleSetup) run(rounds int) []float64 {
	s.t.Helper()

	var res []float64
	for range rounds {
		s.visit(s.high)
		res = append(res, s.visit(s.low))
		if !s.highDraws {
			s.high.enabled = true // the car's own limit, not the circuit, keeps it idle
		}
	}
	return res
}

// TestIdleLoadpointDoesNotMakeLowerOneOscillate: a loadpoint of high priority is
// enabled without drawing anything and asks for its maximum every cycle. The
// circuit caps that request, the load below gives way, the request fits in the
// next cycle, the load below takes the power back, and so on. The idle
// loadpoint stands for its minimum instead, so the load below settles. The free
// power may be above or below that minimum.
func TestIdleLoadpointDoesNotMakeLowerOneOscillate(t *testing.T) {
	for _, tc := range []struct {
		name  string
		limit float64 // circuit power
		want  float64 // current of the low one once settled
	}{
		// 22 kW - 2 kW base - 11.04 kW low leave 8.96 kW, above the minimum of 4.14 kW
		{"free power above the minimum", 22000, 16},
		// 14 kW leave 0.96 kW, so the low one gives way until 4.14 kW are free
		{"free power below the minimum", 14000, 11},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newIdleSetup(t, tc.limit, 2000)

			res := s.run(16)
			for i, got := range res[4:] {
				assert.Equal(t, tc.want, got, "round %d: %v", i+4, res)
			}
		})
	}
}

// TestIdleLoadpointDemandEnds: the demand only stands while the loadpoint is on
// and idle, a charging one is served like any other
func TestIdleLoadpointDemandEnds(t *testing.T) {
	s := newIdleSetup(t, 14000, 2000)
	require.Equal(t, 11.0, s.run(16)[15], "settled below the idle loadpoint")

	// the car charges: the high one draws what it is offered and takes its share
	s.high.status = api.StatusC
	s.high.chargePower = currentToPower(s.high.offeredCurrent, 3)

	s.visit(s.high)
	assert.Equal(t, 0.0, s.visit(s.low), "the low one gives way to a charging load")
}

// TestHigherLoadpointStartsOnWhatCanBeFreed: the circuit has 11 kW, 2 kW are
// base load and the low loadpoint draws 8.97 kW. The one of higher priority
// wants 11.04 kW, which cannot be freed in full. It gets going on the minimum of
// 4.14 kW anyway, and the low one gives way for it.
func TestHigherLoadpointStartsOnWhatCanBeFreed(t *testing.T) {
	s := newIdleSetup(t, 11000, 2000)
	s.highDraws = true

	s.low.offeredCurrent = 13
	s.low.chargePower = currentToPower(13, 3)
	s.high.enabled = false

	s.run(8)

	assert.GreaterOrEqual(t, s.high.chargePower, currentToPower(6, 3), "the high one runs")
	assert.Zero(t, s.low.chargePower, "the low one gives way, its minimum does not fit next to it")
}

// TestLoadpointDemand: a loadpoint runs on any power from its minimum up, so
// load management only needs that much of its demand to let it start; one that
// is on and idle stands for it, one that charges does not. One that does not use
// what it is offered is not regulated, more headroom does not make it draw more.
func TestLoadpointDemand(t *testing.T) {
	s := newIdleSetup(t, 22000, 2000)
	min := currentToPower(6, 3)

	d := s.high.lmCircuit().demand(11040, 0, min, false)
	assert.Equal(t, lm.Demand{Regulated: true, Min: min, Standing: min}, d, "idle")

	// asking for less than the minimum is not asking for anything to stand
	d = s.high.lmCircuit().demand(min-1, 0, min, false)
	assert.Zero(t, d.Standing)

	d = s.low.lmCircuit().demand(11040, 2000, min, false)
	assert.Equal(t, lm.Demand{Regulated: true, Min: min - 2000}, d, "charging, the minimum less what it draws")

	d = s.low.lmCircuit().demand(11040, 5000, min, false)
	assert.Equal(t, lm.Demand{Regulated: true}, d, "charging above its minimum")

	d = s.low.lmCircuit().demand(11040, 2000, min, true)
	assert.Equal(t, lm.Demand{Min: min - 2000}, d, "charging, but not using what it is offered")
}

// TestUnusedOffer: only a charging loadpoint that draws more than the slack
// less than it is offered does not use its offer
func TestUnusedOffer(t *testing.T) {
	s := newIdleSetup(t, 22000, 2000)

	// offered 16 A
	assert.False(t, s.low.lmCircuit().unusedOffer(16), "draws its offer")
	assert.False(t, s.low.lmCircuit().unusedOffer(15), "within the slack")
	assert.True(t, s.low.lmCircuit().unusedOffer(14.9), "clearly below its offer")
	assert.True(t, s.low.lmCircuit().unusedOffer(0))

	s.low.offeredCurrent = 0
	assert.False(t, s.low.lmCircuit().unusedOffer(0), "nothing offered")

	s.high.offeredCurrent = 16
	assert.False(t, s.high.lmCircuit().unusedOffer(0), "not charging, in state B")
}

// TestLoadpointBelowItsOfferDoesNotShedLowerLoad: the circuit has 11 kW with
// 2 kW base load and a 3 kW heater of low priority. The loadpoint of higher
// priority asks for 16 A. If it takes what it is offered, the heater gives way
// for it. A car in its taper takes less than it is offered, 5 A of 16 A: freeing
// the heater would not make it draw more, and the heater would stay off until the
// car is full.
func TestLoadpointBelowItsOfferDoesNotShedLowerLoad(t *testing.T) {
	for _, tc := range []struct {
		name    string
		offered float64 // A
		drawn   float64 // A
		heater  bool    // runs on
	}{
		{"draws what it is offered", 6, 6, false},
		{"draws less than it is offered", 16, 5, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newIdleSetup(t, 11000, 2000)

			s.high.status = api.StatusC
			s.high.offeredCurrent = tc.offered
			s.high.chargePower = currentToPower(tc.drawn, 3)

			s.low.charger = &lmSwitch{rated: 3000}
			s.low.chargePower = 3000

			for i := range 4 {
				s.meter.power = s.base + s.low.chargePower + s.high.chargePower
				require.NoError(t, s.circuit.Update(nil))
				s.high.lmLimit(16)

				limited := s.low.lmLimit(16)
				assert.Equal(t, tc.heater, limited > 0, "round %d: heater limit %.3gA", i, limited)
			}
		})
	}
}
