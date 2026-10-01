package core

import (
	"testing"

	evbus "github.com/asaskevich/EventBus"
	"github.com/benbjohnson/clock"
	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/loadpoint"
	"github.com/evcc-io/evcc/core/settings"
	"github.com/evcc-io/evcc/util"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

// phaseCurrentsLoadpoint returns a charging 1p3p loadpoint with 8-16A on 3p
// and the given 1p limits
func phaseCurrentsLoadpoint(t *testing.T, phases int, min1p, max1p float64) *Loadpoint {
	t.Helper()
	Voltage = 230
	ctrl := gomock.NewController(t)

	plainCharger := api.NewMockCharger(ctrl)
	plainCharger.EXPECT().Enabled().Return(true, nil).AnyTimes()
	plainCharger.EXPECT().Enable(gomock.Any()).Return(nil).AnyTimes()
	plainCharger.EXPECT().MaxCurrent(gomock.Any()).Return(nil).AnyTimes()

	phaseCharger := api.NewMockPhaseSwitcher(ctrl)
	phaseCharger.EXPECT().Phases1p3p(gomock.Any()).Return(nil).AnyTimes()

	return &Loadpoint{
		log:             util.NewLogger("foo"),
		bus:             evbus.New(),
		clock:           clock.NewMock(),
		chargeMeter:     newChargeMeter(&Null{}),
		chargeRater:     &Null{},
		chargeTimer:     &Null{},
		progress:        NewProgress(0, 10),
		wakeUpTimer:     NewTimer(),
		mode:            api.ModePV,
		minCurrent:      8,
		maxCurrent:      16,
		phaseCurrents1p: phaseCurrents1p{minCurrent1p: min1p, maxCurrent1p: max1p},
		phases:          phases,
		measuredPhases:  phases,
		enabled:         true,
		status:          api.StatusC,
		charger: struct {
			*api.MockCharger
			*api.MockPhaseSwitcher
		}{plainCharger, phaseCharger},
	}
}

func TestEffectiveCurrent1p(t *testing.T) {
	lp := phaseCurrentsLoadpoint(t, 1, 6, 20)

	assert.Equal(t, 6.0, lp.effectiveMinCurrentFor(1), "1p min")
	assert.Equal(t, 20.0, lp.effectiveMaxCurrentFor(1), "1p max")
	assert.Equal(t, 8.0, lp.effectiveMinCurrentFor(3), "3p min")
	assert.Equal(t, 16.0, lp.effectiveMaxCurrentFor(3), "3p max")

	// the active phases decide
	assert.Equal(t, 6.0, lp.effectiveMinCurrent())
	assert.Equal(t, 20.0, lp.effectiveMaxCurrent())
	lp.phases, lp.measuredPhases = 3, 3
	assert.Equal(t, 8.0, lp.effectiveMinCurrent())
	assert.Equal(t, 16.0, lp.effectiveMaxCurrent())

	// min power from the 1p limit, max power from the 3p limit
	lp.phases, lp.measuredPhases = 0, 0
	assert.Equal(t, 230*6.0, lp.EffectiveMinPower(), "min power")
	assert.Equal(t, 3*230*16.0, lp.EffectiveMaxPower(), "max power")

	// unset 1p values fall back to the regular range
	lp.minCurrent1p, lp.maxCurrent1p = 0, 0
	assert.Equal(t, 8.0, lp.effectiveMinCurrentFor(1))
	assert.Equal(t, 16.0, lp.effectiveMaxCurrentFor(1))
}

func TestCurrents1pIgnoredWithoutPhaseSwitching(t *testing.T) {
	// a fixed phase charger: the regular limits already are its 1p limits
	lp := phaseCurrentsLoadpoint(t, 1, 6, 20)
	lp.charger = api.NewMockCharger(gomock.NewController(t))

	assert.Equal(t, 8.0, lp.effectiveMinCurrentFor(1))
	assert.Equal(t, 16.0, lp.effectiveMaxCurrentFor(1))
}

func TestPvScalePhasesCurrents1p(t *testing.T) {
	// 3p: 8-16A, 1p: 6-20A unless set otherwise
	for _, tc := range []struct {
		desc         string
		phases       int
		min1p, max1p float64
		available    float64 // W
		want         int
	}{
		// scaling up needs the 1p maximum exhausted and the 3p minimum reached
		{"1p: 20A on 1p, 1p max not exceeded", 1, 6, 20, 4600, 0},
		{"1p: 22A on 1p, but only 7.3A on 3p", 1, 6, 20, 5060, 0},
		{"1p: 8.1A on 3p reaches the 3p min", 1, 6, 20, 5600, 3},
		{"1p: no 1p values, 16A max exceeded, 7.3A on 3p", 1, 0, 0, 5060, 0},
		{"1p: no 1p values, 8.1A on 3p", 1, 0, 0, 5600, 3},
		{"1p: 1p max 25A not exceeded at 24.3A", 1, 6, 25, 5600, 0},

		// scaling down below the 3p minimum, if 1p is sustainable
		{"3p: 8.1A stays", 3, 6, 20, 5600, 0},
		{"3p: 7.8A below the 3p min, 23.5A on 1p", 3, 6, 20, 5400, 1},
		{"3p: 2kW, 8.7A on 1p reaches the 1p min of 6A", 3, 6, 20, 2000, 1},
		{"3p: 2kW, 8.7A on 1p below a 1p min of 10A: disable instead", 3, 10, 20, 2000, 0},
		{"3p: 1.5kW, no 1p values: 6.5A below the regular 8A", 3, 0, 0, 1500, 0},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			lp := phaseCurrentsLoadpoint(t, tc.phases, tc.min1p, tc.max1p)

			// charging at the minimum of the active phases, the rest is grid
			// export (negative) or import (positive)
			lp.chargePower = currentToPower(lp.effectiveMinCurrent(), tc.phases)
			sitePower := lp.chargePower - tc.available

			// as pvMaxCurrent passes them: the limits of the active phases
			got := lp.pvScalePhases(sitePower, lp.effectiveMinCurrent(), lp.effectiveMaxCurrent(), true)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestSetCurrents1p(t *testing.T) {
	store := settings.NewMemorySettings()
	lp := NewLoadpoint(util.NewLogger("foo"), store)
	lp.minCurrent, lp.maxCurrent = 8, 16

	require.NoError(t, lp.SetCurrents1p(6, 20))
	minCurrent, maxCurrent := lp.GetCurrents1p()
	assert.Equal(t, 6.0, minCurrent)
	assert.Equal(t, 20.0, maxCurrent)

	assert.Error(t, lp.SetCurrents1p(-1, 20), "negative")
	assert.Error(t, lp.SetCurrents1p(21, 20), "min above max")
	assert.Error(t, lp.SetCurrents1p(18, 0), "min above the regular max it falls back to")
	assert.Error(t, lp.SetCurrents1p(0, 7), "max below the regular min it falls back to")

	// restored from the settings into a fresh loadpoint
	restored := NewLoadpoint(util.NewLogger("bar"), store)
	restored.restoreCurrents1p()
	minCurrent, maxCurrent = restored.GetCurrents1p()
	assert.Equal(t, 6.0, minCurrent, "restored")
	assert.Equal(t, 20.0, maxCurrent, "restored")

	require.NoError(t, lp.SetCurrents1p(0, 0), "unset")
	minCurrent, maxCurrent = lp.GetCurrents1p()
	assert.Zero(t, minCurrent)
	assert.Zero(t, maxCurrent)
}

// TestCurrents1pConfig pins the config round trip: the ui sends and reads the
// 1p values with the regular dynamic config, applied after min and max current
func TestCurrents1pConfig(t *testing.T) {
	lp := phaseCurrentsLoadpoint(t, 3, 0, 0)
	lp.settings = settings.NewMemorySettings()
	lp.minCurrent = 6

	dynamic, _, err := loadpoint.SplitConfig(map[string]any{
		"title": "Wallbox", "defaultMode": "pv", "minCurrent": 8, "maxCurrent": 16,
		"minCurrent1p": 6, "maxCurrent1p": 20,
	})
	require.NoError(t, err)
	require.NoError(t, dynamic.Apply(lp))

	assert.Equal(t, 8.0, lp.GetMinCurrent())
	assert.Equal(t, loadpoint.Currents1pConfig{MinCurrent1p: 6, MaxCurrent1p: 20}, loadpoint.Currents1pConfigOf(lp))
}

// TestCurrents1pInertWhenUnused pins that without 1p values the regular limits
// apply exactly as upstream, without looking at the phases
func TestCurrents1pInertWhenUnused(t *testing.T) {
	lp := phaseCurrentsLoadpoint(t, 1, 0, 0)

	// a vehicle without expectations: any phase lookup would fail the test
	lp.vehicle = api.NewMockVehicle(gomock.NewController(t))
	lp.vehicle.(*api.MockVehicle).EXPECT().OnIdentified().Return(api.ActionConfig{}).AnyTimes()

	assert.Equal(t, 8.0, lp.effectiveMinCurrent())
	assert.Equal(t, 16.0, lp.effectiveMaxCurrent())
}
