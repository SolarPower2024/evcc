package server

import (
	"context"
	"testing"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/api/globalconfig"
	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/core/loadpoint"
	"github.com/evcc-io/evcc/db"
	"github.com/evcc-io/evcc/db/settings"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stagesDevice struct{}

func (stagesDevice) StageStates() ([]bool, error) {
	return []bool{true, true, false}, nil
}

// the config page shows each stage's switch of a heater in stages
func TestTestInstanceStages(t *testing.T) {
	res := testInstance(context.Background(), stagesDevice{})
	assert.Equal(t, []bool{true, true, false}, res["stages"].Value)
}

// phaseSwitchLoadpoint is a loadpoint with 1p limits and phase delays, all other
// settings at their zero value
type phaseSwitchLoadpoint struct {
	loadpoint.API
}

func (phaseSwitchLoadpoint) GetCurrents1p() (float64, float64)                 { return 6, 20 }
func (phaseSwitchLoadpoint) SetCurrents1p(float64, float64) error              { return nil }
func (phaseSwitchLoadpoint) SetPhaseDelays(time.Duration, time.Duration) error { return nil }
func (phaseSwitchLoadpoint) GetPhaseDelays() (time.Duration, time.Duration) {
	return 5 * time.Minute, 4 * time.Minute
}

func (phaseSwitchLoadpoint) GetTitle() string                    { return "Wallbox" }
func (phaseSwitchLoadpoint) GetPriority() int                    { return 0 }
func (phaseSwitchLoadpoint) GetMinCurrent() float64              { return 8 }
func (phaseSwitchLoadpoint) GetMaxCurrent() float64              { return 16 }
func (phaseSwitchLoadpoint) GetAlwaysCharge() api.AlwaysCharge   { return api.AlwaysChargeOff }
func (phaseSwitchLoadpoint) GetDefaultMode() api.ChargeMode      { return api.ModePV }
func (phaseSwitchLoadpoint) GetPhasesConfigured() int            { return 0 }
func (phaseSwitchLoadpoint) GetLimitSoc() int                    { return 0 }
func (phaseSwitchLoadpoint) GetLimitEnergy() float64             { return 0 }
func (phaseSwitchLoadpoint) GetPlanEnergy() (time.Time, float64) { return time.Time{}, 0 }
func (phaseSwitchLoadpoint) GetPlanStrategy() api.PlanStrategy   { return api.PlanStrategy{} }
func (phaseSwitchLoadpoint) GetSocConfig() loadpoint.SocConfig   { return loadpoint.SocConfig{} }
func (phaseSwitchLoadpoint) GetUI() loadpoint.UIConfig           { return loadpoint.UIConfig{} }
func (phaseSwitchLoadpoint) GetMinSoc() int                      { return 0 }
func (phaseSwitchLoadpoint) GetDefaultVehicleRef() string        { return "" }
func (phaseSwitchLoadpoint) GetThresholds() loadpoint.ThresholdsConfig {
	return loadpoint.ThresholdsConfig{}
}
func (phaseSwitchLoadpoint) GetBatteryBoostLimit() int             { return 0 }
func (phaseSwitchLoadpoint) GetSmartCostLimit() *float64           { return nil }
func (phaseSwitchLoadpoint) GetSolarShare() float64                { return 0 }
func (phaseSwitchLoadpoint) GetSmartFeedInPriorityLimit() *float64 { return nil }

// the config of a loadpoint carries its 1p limits and phase delays to the form
func TestGetLoadpointDynamicConfigPhaseSwitch(t *testing.T) {
	res := getLoadpointDynamicConfig(phaseSwitchLoadpoint{})

	assert.Equal(t, loadpoint.PhaseSwitchConfig{
		MinCurrent1p:      6,
		MaxCurrent1p:      20,
		PhaseScale3pDelay: float64(5 * time.Minute),
		PhaseScale1pDelay: float64(4 * time.Minute),
	}, res.PhaseSwitchConfig)
}

// deleting the second feed-in tariff empties its reference and leaves the others
func TestCleanupTariffRefFeedInEeg(t *testing.T) {
	require.NoError(t, db.NewInstance("sqlite", ":memory:"))
	require.NoError(t, settings.SetJson(keys.TariffRefs, globalconfig.TariffRefs{
		Grid: "grid", FeedIn: "feedin", FeedInEeg: "eeg", Solar: []string{"eeg", "solar"},
	}))

	cleanupTariffRef("eeg")

	var refs globalconfig.TariffRefs
	require.NoError(t, settings.Json(keys.TariffRefs, &refs))
	assert.Equal(t, globalconfig.TariffRefs{Grid: "grid", FeedIn: "feedin", Solar: []string{"solar"}}, refs)
}
