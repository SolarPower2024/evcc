package charger

import (
	"bytes"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/benbjohnson/clock"
	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/loadpoint"
	"github.com/evcc-io/evcc/util"
	jww "github.com/spf13/jwalterweatherman"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

// fakeSwitches stands in for the switch entities and records every write
type fakeSwitches struct {
	on     []bool
	writes []string
	fail   int // stage that fails to switch, 0 = none
	tries  int // switch attempts, failed ones too
	lost   int // stage whose switch cannot be read, 0 = none
}

func (f *fakeSwitches) stages() []switchStage {
	res := make([]switchStage, len(f.on))
	for i := range f.on {
		res[i] = switchStage{
			enabled: func() (bool, error) {
				if f.lost == i+1 {
					return false, api.ErrNotAvailable
				}
				return f.on[i], nil
			},
			enable: func(on bool) error {
				f.tries++
				if f.fail == i+1 {
					return errors.New("unavailable")
				}
				f.on[i] = on
				f.writes = append(f.writes, map[bool]string{true: "on", false: "off"}[on]+string(rune('1'+i)))
				return nil
			},
		}
	}
	return res
}

func (f *fakeSwitches) count() int {
	var n int
	for _, on := range f.on {
		if on {
			n++
		}
	}
	return n
}

// stagesCharger returns a three stage 3kW heater on a 3-phase loadpoint
func stagesCharger(t *testing.T, delay time.Duration) (*SwitchStages, *fakeSwitches, *clock.Mock) {
	f := &fakeSwitches{on: make([]bool, 3)}
	c := NewSwitchStages(&embed{}, f.stages(), 3000, delay, nil)

	clk := clock.NewMock()
	c.clock = clk

	lp := loadpoint.NewMockAPI(gomock.NewController(t))
	lp.EXPECT().ActivePhases().Return(3).AnyTimes()
	lp.EXPECT().GetMode().Return(api.ModePV).AnyTimes()
	c.LoadpointControl(lp)

	return c, f, clk
}

// amps returns the per-phase current evcc offers for a power on 3 phases
func amps(power float64) float64 {
	return power / (voltage * 3)
}

func TestSwitchStagesLimits(t *testing.T) {
	c, _, _ := stagesCharger(t, 0)

	minP, maxP, err := c.GetMinMaxPower()
	require.NoError(t, err)
	assert.Equal(t, 3000.0, minP)
	assert.Equal(t, 9000.0, maxP)
}

func TestSwitchStagesFloor(t *testing.T) {
	c, f, _ := stagesCharger(t, 0)

	// stored while off, switched by Enable
	require.NoError(t, c.MaxCurrentMillis(amps(9000)))
	assert.Equal(t, 0, f.count(), "off until enabled")

	require.NoError(t, c.Enable(true))
	assert.Equal(t, []bool{true, true, true}, f.on)

	for _, tc := range []struct {
		power  float64
		stages int
	}{
		{8999, 2}, // never rounds up a partial stage
		{6000, 2},
		{5999, 1},
		{3000, 1},
		{2999.9, 1}, // the minimum keeps one stage while enabled
		{7500, 2},
		{12000, 3}, // capped at all stages
	} {
		require.NoError(t, c.MaxCurrentMillis(amps(tc.power)))
		assert.Equal(t, tc.stages, f.count(), "%.0fW", tc.power)
	}

	// the requested current is reported back, not the stages' current
	cur, err := c.GetMaxCurrent()
	require.NoError(t, err)
	assert.InDelta(t, amps(12000), cur, 1e-9)

	require.NoError(t, c.Enable(false))
	assert.Equal(t, 0, f.count())
}

func TestSwitchStagesOrder(t *testing.T) {
	c, f, _ := stagesCharger(t, 0)

	require.NoError(t, c.MaxCurrentMillis(amps(6000)))
	require.NoError(t, c.Enable(true))
	assert.Equal(t, []string{"on1", "on2"}, f.writes, "lowest first on")

	f.writes = nil
	require.NoError(t, c.Enable(false))
	assert.Equal(t, []string{"off2", "off1"}, f.writes, "highest first off")

	// only switches that change are written
	require.NoError(t, c.MaxCurrentMillis(amps(9000)))
	require.NoError(t, c.Enable(true))
	f.writes = nil
	require.NoError(t, c.MaxCurrentMillis(amps(9000)))
	assert.Empty(t, f.writes)
}

func TestSwitchStagesDelay(t *testing.T) {
	c, f, clk := stagesCharger(t, time.Minute)

	// switching on from off is not delayed
	require.NoError(t, c.MaxCurrentMillis(amps(6000)))
	require.NoError(t, c.Enable(true))
	assert.Equal(t, 2, f.count())

	// down is immediate
	clk.Add(10 * time.Second)
	require.NoError(t, c.MaxCurrentMillis(amps(3000)))
	assert.Equal(t, 1, f.count())

	// up waits for the delay since the last change
	clk.Add(30 * time.Second)
	require.NoError(t, c.MaxCurrentMillis(amps(9000)))
	assert.Equal(t, 1, f.count(), "held back")

	require.NoError(t, c.catchUp())
	assert.Equal(t, 1, f.count(), "still held back")

	// caught up once the delay has passed, without a new current from evcc
	clk.Add(31 * time.Second)
	require.NoError(t, c.catchUp())
	assert.Equal(t, 3, f.count())

	// a lower request in the meantime wins over a held back step up
	require.NoError(t, c.MaxCurrentMillis(amps(3000)))
	require.NoError(t, c.MaxCurrentMillis(amps(9000)))
	require.NoError(t, c.MaxCurrentMillis(amps(6000)))
	clk.Add(time.Minute)
	require.NoError(t, c.catchUp())
	assert.Equal(t, 2, f.count())

	// nothing to catch up while off
	require.NoError(t, c.Enable(false))
	clk.Add(time.Minute)
	require.NoError(t, c.catchUp())
	assert.Equal(t, 0, f.count())
}

func TestSwitchStagesRunningAtStart(t *testing.T) {
	// switches found on, e.g. after a restart, count as enabled and as running at
	// the current of their stages: the first request, the minimum, keeps them,
	// the current evcc sends next applies without a new Enable
	c, f, _ := stagesCharger(t, 0)
	f.on = []bool{true, true, true}

	_, err := c.GetMaxCurrent()
	assert.ErrorIs(t, err, api.ErrNotAvailable, "unknown before evcc sent one")

	ok, err := c.Enabled()
	require.NoError(t, err)
	assert.True(t, ok)

	require.NoError(t, c.MaxCurrentMillis(amps(3000)))
	assert.Equal(t, []bool{true, true, true}, f.on, "the first request is taken as the running current")
	assert.Empty(t, f.writes)

	cur, err := c.GetMaxCurrent()
	require.NoError(t, err)
	assert.InDelta(t, amps(9000), cur, 1e-9)

	// so evcc writes the minimum it wants instead of finding it already set
	require.NoError(t, c.MaxCurrentMillis(amps(3000)))
	assert.Equal(t, []bool{true, false, false}, f.on)
}

func TestSwitchStagesRunningAtStartPartly(t *testing.T) {
	c, f, _ := stagesCharger(t, 0)
	f.on = []bool{true, true, false}

	require.NoError(t, c.MaxCurrentMillis(amps(3000)))
	assert.Equal(t, []bool{true, true, false}, f.on)

	cur, err := c.GetMaxCurrent()
	require.NoError(t, err)
	assert.InDelta(t, amps(6000), cur, 1e-9)

	// nothing running: the request is stored as before
	c, f, _ = stagesCharger(t, 0)
	require.NoError(t, c.MaxCurrentMillis(amps(3000)))
	assert.Equal(t, 0, f.count())

	cur, err = c.GetMaxCurrent()
	require.NoError(t, err)
	assert.InDelta(t, amps(3000), cur, 1e-9)
}

func TestSwitchStagesStatusAndPower(t *testing.T) {
	c, f, _ := stagesCharger(t, 0)

	s, err := c.Status()
	require.NoError(t, err)
	assert.Equal(t, api.StatusB, s)

	p, err := c.CurrentPower()
	require.NoError(t, err)
	assert.Equal(t, 0.0, p)

	require.NoError(t, c.MaxCurrentMillis(amps(6000)))
	require.NoError(t, c.Enable(true))

	s, err = c.Status()
	require.NoError(t, err)
	assert.Equal(t, api.StatusC, s)

	// without a sensor the stages switched on
	p, err = c.CurrentPower()
	require.NoError(t, err)
	assert.Equal(t, 6000.0, p)

	// a sensor wins
	c.power = func() (float64, error) { return 5800, nil }
	p, err = c.CurrentPower()
	require.NoError(t, err)
	assert.Equal(t, 5800.0, p)

	_ = f
}

func TestSwitchStagesThermostat(t *testing.T) {
	// switches on, the heater's own thermostat cut out: ready, not heating
	c, _, _ := stagesCharger(t, 0)
	c.standby = 15

	var measured float64
	c.power = func() (float64, error) { return measured, nil }

	require.NoError(t, c.MaxCurrentMillis(amps(9000)))
	require.NoError(t, c.Enable(true))

	for _, tc := range []struct {
		measured float64
		status   api.ChargeStatus
		power    float64
	}{
		{0, api.StatusB, 0},
		{12, api.StatusB, 0}, // standby draw of the electronics
		{15, api.StatusB, 0},
		{2950, api.StatusC, 2950},
		{8900, api.StatusC, 8900},
	} {
		measured = tc.measured
		s, err := c.Status()
		require.NoError(t, err)
		assert.Equal(t, tc.status, s, "%.0fW", tc.measured)

		p, err := c.CurrentPower()
		require.NoError(t, err)
		assert.Equal(t, tc.power, p, "%.0fW", tc.measured)
	}

	// still enabled for evcc: the switches decide, not the draw
	measured = 0
	ok, err := c.Enabled()
	require.NoError(t, err)
	assert.True(t, ok)

	// without a sensor nothing is known about the thermostat
	c.power = nil
	s, err := c.Status()
	require.NoError(t, err)
	assert.Equal(t, api.StatusC, s)
}

func TestSwitchStagesFailure(t *testing.T) {
	c, f, _ := stagesCharger(t, 0)

	require.NoError(t, c.MaxCurrentMillis(amps(9000)))
	require.NoError(t, c.Enable(true))

	// switching down stops at the failing stage without switching anything on
	f.fail = 2
	require.Error(t, c.MaxCurrentMillis(amps(3000)))
	assert.Equal(t, []bool{true, true, false}, f.on)

	// the failed request is not taken over, evcc reads back the old current and
	// so sets the new one again; the level is what is really on
	cur, err := c.GetMaxCurrent()
	require.NoError(t, err)
	assert.InDelta(t, amps(9000), cur, 1e-9)
	assert.Equal(t, 2, c.level)

	f.fail = 0
	require.NoError(t, c.MaxCurrentMillis(amps(3000)))
	assert.Equal(t, []bool{true, false, false}, f.on)
}

func TestSwitchStagesFailureStepUp(t *testing.T) {
	c, f, _ := stagesCharger(t, 0)

	require.NoError(t, c.MaxCurrentMillis(amps(3000)))
	require.NoError(t, c.Enable(true))

	// the second stage comes on, the third fails
	f.fail = 3
	require.Error(t, c.MaxCurrentMillis(amps(9000)))
	assert.Equal(t, []bool{true, true, false}, f.on)
	assert.Equal(t, 2, c.level)

	cur, err := c.GetMaxCurrent()
	require.NoError(t, err)
	assert.InDelta(t, amps(3000), cur, 1e-9, "the request is still the old one")

	f.fail = 0
	require.NoError(t, c.MaxCurrentMillis(amps(9000)))
	assert.Equal(t, []bool{true, true, true}, f.on)
}

func TestSwitchStagesCatchUpAfterFailedEnable(t *testing.T) {
	c, f, clk := stagesCharger(t, time.Minute)

	require.NoError(t, c.MaxCurrentMillis(amps(9000)))

	// the first switch fails: evcc sees the error and the heater stays disabled
	f.fail = 1
	require.Error(t, c.Enable(true))

	// the catch-up does not carry the failed Enable out once the switch is back
	f.fail = 0
	clk.Add(time.Minute)
	require.NoError(t, c.catchUp())
	assert.Equal(t, 0, f.count())

	// partly switched on: nothing is caught up either, evcc retries the Enable
	f.fail = 2
	require.Error(t, c.Enable(true))
	assert.Equal(t, []bool{true, false, false}, f.on)

	f.fail = 0
	clk.Add(time.Minute)
	require.NoError(t, c.catchUp())
	assert.Equal(t, 1, f.count())

	require.NoError(t, c.Enable(true))
	assert.Equal(t, 3, f.count())
}

func TestSwitchStagesCatchUpOnlyRunning(t *testing.T) {
	// from no stage apply never holds a step back, so the catch-up leaves it to evcc
	c, f, clk := stagesCharger(t, time.Minute)

	c.enabled, c.current = true, amps(9000)
	clk.Add(time.Minute)
	require.NoError(t, c.catchUp())
	assert.Equal(t, 0, f.count())
}

func TestSwitchStagesCatchUpRetry(t *testing.T) {
	c, f, clk := stagesCharger(t, time.Minute)

	require.NoError(t, c.MaxCurrentMillis(amps(3000)))
	require.NoError(t, c.Enable(true))
	require.NoError(t, c.MaxCurrentMillis(amps(9000)))
	assert.Equal(t, 1, f.count(), "held back")

	// a stage keeps failing: one attempt per delay, not one per tick
	f.fail = 2
	f.tries = 0
	clk.Add(time.Minute)

	for i := range 12 {
		if i > 0 {
			clk.Add(5 * time.Second)
		}
		_ = c.catchUp()
		assert.Equal(t, 1, f.tries, "tick %d", i)
	}

	clk.Add(5 * time.Second)
	require.Error(t, c.catchUp())
	assert.Equal(t, 2, f.tries)

	// back again: caught up after the next delay
	f.fail = 0
	clk.Add(time.Minute)
	require.NoError(t, c.catchUp())
	assert.Equal(t, 3, f.count())
}

func TestSwitchStagesCatchUpLogOnce(t *testing.T) {
	c, _, _ := stagesCharger(t, time.Minute)

	var buf bytes.Buffer
	c.log = &util.Logger{Notepad: jww.NewNotepad(jww.LevelTrace, jww.LevelTrace, &buf, io.Discard, "", 0)}

	c.logCatchUp(errors.New("stage 2 on: unavailable"))
	c.logCatchUp(errors.New("stage 2 on: unavailable"))
	assert.Equal(t, 1, strings.Count(buf.String(), "ERROR"), "same cause once")
	assert.Equal(t, 1, strings.Count(buf.String(), "DEBUG"))

	c.logCatchUp(errors.New("stage 3 on: unavailable"))
	assert.Equal(t, 2, strings.Count(buf.String(), "ERROR"), "new cause")
}

func TestSwitchStagesUnreadableStage(t *testing.T) {
	c, f, _ := stagesCharger(t, 0)

	var buf bytes.Buffer
	c.log = &util.Logger{Notepad: jww.NewNotepad(jww.LevelTrace, jww.LevelTrace, &buf, io.Discard, "", 0)}

	f.on = []bool{true, true, true}
	f.lost = 3

	// the readable stages decide, no error
	s, err := c.Status()
	require.NoError(t, err)
	assert.Equal(t, api.StatusC, s)

	ok, err := c.Enabled()
	require.NoError(t, err)
	assert.True(t, ok)

	// the unreadable stage counts as on
	p, err := c.CurrentPower()
	require.NoError(t, err)
	assert.Equal(t, 9000.0, p)

	// switching off reaches the readable stages and writes nothing blind to the other
	require.NoError(t, c.Enable(false))
	assert.Equal(t, []string{"off2", "off1"}, f.writes)

	s, err = c.Status()
	require.NoError(t, err)
	assert.Equal(t, api.StatusB, s)

	ok, err = c.Enabled()
	require.NoError(t, err)
	assert.False(t, ok)

	p, err = c.CurrentPower()
	require.NoError(t, err)
	assert.Equal(t, 3000.0, p, "the unreadable stage may still run")

	assert.Equal(t, 1, strings.Count(buf.String(), "WARN"), "once per outage, not per read")

	// the config page reports it
	_, err = c.StageStates()
	assert.ErrorIs(t, err, api.ErrNotAvailable)

	// readable again, reported once
	f.lost = 0
	_, err = c.Status()
	require.NoError(t, err)
	_, err = c.Status()
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(buf.String(), "readable again"))

	states, err := c.StageStates()
	require.NoError(t, err)
	assert.Equal(t, []bool{false, false, true}, states)
}

func TestSwitchStagesUnreadableStageStepUp(t *testing.T) {
	// an unreadable stage in the way keeps the target out of reach: the readable
	// ones are switched, nothing is written to it and the attempts keep the delay
	c, f, clk := stagesCharger(t, time.Minute)
	f.lost = 2

	require.NoError(t, c.MaxCurrentMillis(amps(9000)))
	require.NoError(t, c.Enable(true))
	assert.Equal(t, []bool{true, false, true}, f.on)
	assert.Equal(t, []string{"on1", "on3"}, f.writes)

	f.writes = nil
	for range 12 {
		clk.Add(5 * time.Second)
		require.NoError(t, c.catchUp())
	}
	assert.Empty(t, f.writes)
}

func TestSwitchStagesNoStageReadable(t *testing.T) {
	c, f, _ := stagesCharger(t, 0)
	f.on = []bool{true, true, true}

	for i := range c.stages {
		c.stages[i].enabled = func() (bool, error) { return false, api.ErrNotAvailable }
	}

	_, err := c.Status()
	assert.ErrorIs(t, err, api.ErrNotAvailable)

	_, err = c.Enabled()
	assert.ErrorIs(t, err, api.ErrNotAvailable)

	_, err = c.CurrentPower()
	assert.ErrorIs(t, err, api.ErrNotAvailable)

	assert.ErrorIs(t, c.Enable(false), api.ErrNotAvailable)
	assert.Empty(t, f.writes, "nothing written blind")
}

func TestSwitchStagesSinglePhaseLoadpoint(t *testing.T) {
	// a loadpoint configured 1-phase converts with one phase: the conversion
	// stays consistent with the loadpoint, it never draws more than offered
	f := &fakeSwitches{on: make([]bool, 3)}
	c := NewSwitchStages(&embed{}, f.stages(), 3000, 0, nil)
	lp := loadpoint.NewMockAPI(gomock.NewController(t))
	lp.EXPECT().ActivePhases().Return(1).AnyTimes()
	c.LoadpointControl(lp)

	require.NoError(t, c.MaxCurrentMillis(16))
	require.NoError(t, c.Enable(true))
	assert.Equal(t, 1, f.count(), "16A at 1p is 3.7kW")
}

func featured(c api.Charger, f api.Feature) bool {
	d, ok := c.(api.FeatureDescriber)
	return ok && slices.Contains(d.Features(), f)
}

func TestSwitchStagesConfig(t *testing.T) {
	stage := map[string]any{
		"enabled": map[string]any{"source": "const", "value": false},
		"enable":  map[string]any{"source": "js", "script": "enable"},
	}

	c, err := NewSwitchStagesFromConfig(t.Context(), map[string]any{
		"stages":     []any{stage, stage, stage},
		"stagepower": 3000,
		"features":   []any{"heating", "integrateddevice"},
	})
	require.NoError(t, err)

	assert.True(t, featured(c, api.Heating))
	assert.True(t, featured(c, api.IntegratedDevice))
	assert.False(t, featured(c, api.SwitchDevice), "regulated in stages, not all or nothing")

	_, err = NewSwitchStagesFromConfig(t.Context(), map[string]any{"stages": []any{stage}})
	assert.Error(t, err, "stage power required")

	_, err = NewSwitchStagesFromConfig(t.Context(), map[string]any{"stagepower": 3000})
	assert.Error(t, err, "stages required")
}

func TestSwitchStagesRunningCurrent(t *testing.T) {
	// the current of the stages switched on, for core's pv hold
	c, _, _ := stagesCharger(t, 0)
	assert.Equal(t, 0.0, c.RunningCurrent(3))

	require.NoError(t, c.MaxCurrentMillis(amps(6000)))
	require.NoError(t, c.Enable(true))
	assert.InDelta(t, amps(6000), c.RunningCurrent(3), 1e-9)
	assert.InDelta(t, 6000/voltage, c.RunningCurrent(1), 1e-9)
}
