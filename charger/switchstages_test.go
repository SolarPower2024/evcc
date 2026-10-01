package charger

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/benbjohnson/clock"
	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/loadpoint"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

// fakeSwitches stands in for the switch entities and records every write
type fakeSwitches struct {
	on     []bool
	writes []string
	fail   int // stage that fails to switch, 0 = none
}

func (f *fakeSwitches) stages() []switchStage {
	res := make([]switchStage, len(f.on))
	for i := range f.on {
		res[i] = switchStage{
			enabled: func() (bool, error) { return f.on[i], nil },
			enable: func(on bool) error {
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
	return countOn(f.on)
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
	// switches found on, e.g. after a restart, count as enabled: the current
	// evcc sends applies without a new Enable
	c, f, _ := stagesCharger(t, 0)
	f.on = []bool{true, true, true}

	_, err := c.GetMaxCurrent()
	assert.ErrorIs(t, err, api.ErrNotAvailable, "unknown before evcc sent one")

	ok, err := c.Enabled()
	require.NoError(t, err)
	assert.True(t, ok)

	require.NoError(t, c.MaxCurrentMillis(amps(3000)))
	assert.Equal(t, []bool{true, false, false}, f.on)
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

func TestSwitchStagesFailure(t *testing.T) {
	c, f, _ := stagesCharger(t, 0)

	require.NoError(t, c.MaxCurrentMillis(amps(9000)))
	require.NoError(t, c.Enable(true))

	// switching down stops at the failing stage without switching anything on
	f.fail = 2
	require.Error(t, c.MaxCurrentMillis(amps(3000)))
	assert.Equal(t, []bool{true, true, false}, f.on)

	f.fail = 0
	require.NoError(t, c.MaxCurrentMillis(amps(3000)))
	assert.Equal(t, []bool{true, false, false}, f.on)
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
