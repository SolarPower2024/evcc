package core

import (
	"testing"

	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/core/lm/profile"
	"github.com/evcc-io/evcc/db/settings"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fp(v float64) *float64 { return &v }
func bp(v bool) *bool       { return &v }

// TestApplyLmProfile: switching from a summer to a winter profile raises all
// three battery usage values, which evcc's setters would refuse one by one
func TestApplyLmProfile(t *testing.T) {
	sc := newScenario(t)
	site := sc.site
	settings.SetJson(keys.LmProfiles, []profile.Profile{})

	site.peak().set = func(float64) error { return nil }

	summer, err := site.SaveLmProfile(profile.Profile{
		Name: "Sommer", Icon: "sun",
		GridCharge:  bp(false),
		PrioritySoc: fp(20), BufferSoc: fp(30), BufferStartSoc: fp(40),
		PeakShaving: bp(false),
	})
	require.NoError(t, err)
	assert.NotEmpty(t, summer.ID)

	winter, err := site.SaveLmProfile(profile.Profile{
		Name: "Winter", Icon: "snow",
		GridCharge: bp(true), GridChargeStart: fp(85), GridChargeStop: fp(95),
		PrioritySoc: fp(80), BufferSoc: fp(90), BufferStartSoc: fp(95),
		PeakShaving: bp(true), PeakReserve: fp(40), PeakLimit: fp(8500),
	})
	require.NoError(t, err)

	require.NoError(t, site.ApplyLmProfile(summer.ID))
	assert.Equal(t, 20.0, site.GetPrioritySoc())
	assert.Equal(t, 40.0, site.GetBufferStartSoc())
	assert.False(t, site.GetBatterySocGridCharge())
	assert.False(t, site.GetPeakShaving())

	// start 85 is above the current stop of 80: stop has to go first
	require.NoError(t, site.ApplyLmProfile(winter.ID))
	assert.Equal(t, 80.0, site.GetPrioritySoc())
	assert.Equal(t, 90.0, site.GetBufferSoc())
	assert.Equal(t, 95.0, site.GetBufferStartSoc())
	assert.True(t, site.GetBatterySocGridCharge())
	assert.Equal(t, 85.0, site.GetBatterySocGridChargeStart())
	assert.Equal(t, 95.0, site.GetBatterySocGridChargeStop())
	assert.True(t, site.GetPeakShaving())
	assert.Equal(t, 40.0, site.GetPeakShavingReserve())
	assert.Equal(t, 8500.0, site.GetPeakShavingLimit())

	active, _ := settings.String(keys.LmProfileActive)
	assert.Equal(t, winter.ID, active)

	// and back down again
	require.NoError(t, site.ApplyLmProfile(summer.ID))
	assert.Equal(t, 20.0, site.GetPrioritySoc())
	assert.Equal(t, 30.0, site.GetBufferSoc())
	assert.Equal(t, 40.0, site.GetBufferStartSoc())
	assert.Equal(t, 85.0, site.GetBatterySocGridChargeStart(), "left out, unchanged")

	// a profile whose battery usage does not fit the values it leaves out
	partial, err := site.SaveLmProfile(profile.Profile{Name: "Kaputt", PrioritySoc: fp(50)})
	require.NoError(t, err)
	assert.Error(t, site.ApplyLmProfile(partial.ID), "50 is above the buffer soc of 30")
	assert.Equal(t, 20.0, site.GetPrioritySoc(), "nothing changed")
	active, _ = settings.String(keys.LmProfileActive)
	assert.Equal(t, summer.ID, active, "the failed profile does not become active")

	// deleting the active profile clears it
	require.NoError(t, site.DeleteLmProfile(summer.ID))
	active, _ = settings.String(keys.LmProfileActive)
	assert.Empty(t, active)
	assert.Len(t, lmProfiles(), 2)

	assert.Error(t, site.ApplyLmProfile("unknown"))
	_, err = site.SaveLmProfile(profile.Profile{Name: "Wallbox", SolarShare: map[string]float64{"db:99": 50}})
	assert.Error(t, err, "unknown loadpoint")
}
