package core

import (
	"context"
	"testing"
	"time"

	"github.com/benbjohnson/clock"
	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/circuit"
	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/db/settings"
	"github.com/evcc-io/evcc/util"
	"github.com/evcc-io/evcc/util/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCircuitLimitWanted(t *testing.T) {
	noSettingsDB(t)
	assert.Equal(t, 10000.0, circuitLimitWanted(10000, false, 0), "as configured")
	assert.Equal(t, 3500.0, circuitLimitWanted(10000, true, 3500), "the peak limit, below the configured value")
}

// The circuits keep their configured power limits, also with a load
// management circuit chosen.
func TestLmCircuitConfiguredLimits(t *testing.T) {
	site, main, sub := lmSwitchCircuits(t, 10000)

	require.NoError(t, site.SetLmCircuit("main"))
	assert.Equal(t, 10000.0, main.GetMaxPower())
	assert.Equal(t, 7000.0, sub.GetMaxPower())
	assert.Equal(t, 32.0, main.GetMaxCurrent())
	assert.Empty(t, site.lms().offLimits)

	require.Error(t, site.SetLmCircuit("unknown"))
	assert.Equal(t, "main", site.GetLmCircuit())
}

// A stored switch "load management off" of an earlier version is removed at the
// start: the circuits keep their configured limits.
func TestLmOffDropped(t *testing.T) {
	site, main, sub := lmSwitchCircuits(t, 3500)
	settings.SetBool(keys.LmOff, true)

	site = &Site{log: site.log}
	site.restoreLmSwitch()
	assert.Equal(t, 10000.0, main.GetMaxPower())
	assert.Equal(t, 7000.0, sub.GetMaxPower())

	_, err := settings.Bool(keys.LmOff)
	assert.Error(t, err, "setting removed")

	// off or not stored: nothing to do
	settings.SetBool(keys.LmOff, false)
	site.restoreLmSwitch()
	_, err = settings.Bool(keys.LmOff)
	assert.Error(t, err)
	site.restoreLmSwitch()
}

// The circuit chosen for follow the peak in lm3/lm4 becomes the load
// management circuit.
func TestLmCircuitTakenOver(t *testing.T) {
	noSettingsDB(t)
	config.Reset()
	t.Cleanup(config.Reset)
	clean := func() {
		_ = settings.Delete(keys.LmCircuit)
		_ = settings.Delete(keys.PeakFollowCircuit)
	}
	clean() // the takeover only runs without a load management circuit stored
	t.Cleanup(clean)

	log := util.NewLogger("test")
	main, err := circuit.New(log, "peak", 0, 10000, nil, 0)
	require.NoError(t, err)
	require.NoError(t, config.Circuits().Add(config.NewStaticDevice(config.Named{Name: "main"}, api.Circuit(main))))

	settings.SetString(keys.PeakFollowCircuit, "main")
	site := &Site{log: log}
	site.restoreLmSwitch()
	assert.Equal(t, "main", site.GetLmCircuit())
	v, err := settings.String(keys.LmCircuit)
	require.NoError(t, err)
	assert.Equal(t, "main", v)
	_, err = settings.String(keys.PeakFollowCircuit)
	assert.Error(t, err, "old key removed")

	// set to none: stays none after a restart
	require.NoError(t, site.SetLmCircuit(""))
	site = &Site{log: log}
	site.restoreLmSwitch()
	assert.Empty(t, site.GetLmCircuit())
}

// lmSwitchCircuits adds the circuits "main" (10 kW) and "garage" (7 kW) and
// returns a site with a battery and peak shaving on at limit
func lmSwitchCircuits(t *testing.T, limit float64) (*Site, *circuit.Circuit, *circuit.Circuit) {
	t.Helper()

	noSettingsDB(t)
	keepSettings(t)
	config.Reset()
	t.Cleanup(config.Reset)

	log := util.NewLogger("test")
	main, err := circuit.New(log, "peak", 32, 10000, nil, 0)
	require.NoError(t, err)
	require.NoError(t, config.Circuits().Add(config.NewStaticDevice(config.Named{Name: "main"}, api.Circuit(main))))
	sub, err := circuit.New(log, "garage", 0, 7000, nil, 0)
	require.NoError(t, err)
	require.NoError(t, config.Circuits().Add(config.NewStaticDevice(config.Named{Name: "garage"}, api.Circuit(sub))))

	site := &Site{
		log:           log,
		batteryMeters: []config.Device[api.Meter]{config.NewStaticDevice(config.Named{Name: "bat"}, api.Meter(&scenarioBattery{}))},
	}
	site.peak().clock = clock.NewMock()
	setPeakShaving(site, limit, 20)

	return site, main, sub
}

// The peak limit as the load management (peak) circuit's limit: off by default,
// on it replaces the configured value, also below it, follows every change of
// the peak limit. Peak shaving off returns
// the configured value.
func TestLmCircuitFromPeak(t *testing.T) {
	site, main, sub := lmSwitchCircuits(t, 3500)
	p := site.peak()
	clk := p.clock.(*clock.Mock)
	clk.Set(time.Date(2026, 9, 20, 12, 0, 0, 0, time.Local))

	// default off: nothing changes
	require.NoError(t, site.SetLmCircuit("main"))
	assert.Equal(t, 10000.0, main.GetMaxPower())
	assert.False(t, site.advanced().CircuitFromPeak)

	// on without a circuit chosen: no effect, not on all circuits
	require.NoError(t, site.SetLmCircuit(""))
	require.NoError(t, site.SetLmAdvanced("circuitFromPeak", 1))
	assert.Equal(t, 10000.0, main.GetMaxPower())
	assert.Equal(t, 7000.0, sub.GetMaxPower())

	// on with the circuit chosen: the peak limit, the other circuit stays
	require.NoError(t, site.SetLmCircuit("main"))
	assert.Equal(t, 3500.0, main.GetMaxPower())
	assert.Equal(t, 7000.0, sub.GetMaxPower(), "not the load management circuit")
	assert.Equal(t, 32.0, main.GetMaxCurrent(), "the fuse stays")

	// the peak limit set by hand is taken at once, also above the configured value
	require.NoError(t, site.SetPeakShavingLimit(5000))
	assert.Equal(t, 5000.0, main.GetMaxPower())
	require.NoError(t, site.SetPeakShavingLimit(12000))
	assert.Equal(t, 12000.0, main.GetMaxPower())

	// a change of the limit without the setter is taken with the next call
	p.mu.Lock()
	p.limit = 4000
	p.mu.Unlock()
	assert.Equal(t, 12000.0, main.GetMaxPower())
	site.applyCircuitLimits()
	assert.Equal(t, 4000.0, main.GetMaxPower())

	// the month's peak raises the limit and with it the circuit, a new month returns to the limit
	require.NoError(t, site.SetPeakShavingLimit(5000))
	p.months = []peakMonth{{Month: "2026-09", Peak: 8800}}
	site.updatePeakLimit()
	site.applyCircuitLimits()
	assert.Equal(t, 8800.0, site.GetPeakShavingLimit())
	assert.Equal(t, 8800.0, main.GetMaxPower())
	clk.Set(time.Date(2026, 10, 1, 0, 5, 0, 0, time.Local))
	site.updatePeakLimit()
	site.applyCircuitLimits()
	assert.Equal(t, 5000.0, main.GetMaxPower(), "below the configured value as well")

	// peak shaving off: the configured value returns, on again the peak limit
	require.NoError(t, site.SetPeakShaving(false))
	assert.Equal(t, 10000.0, main.GetMaxPower())
	require.NoError(t, site.SetPeakShaving(true))
	assert.Equal(t, 5000.0, main.GetMaxPower())

	// the switch off returns the configured value
	require.NoError(t, site.SetLmAdvanced("circuitFromPeak", 0))
	assert.Equal(t, 10000.0, main.GetMaxPower())
	assert.False(t, site.advanced().CircuitFromPeak)
	assert.Empty(t, site.lms().offLimits)

	require.Error(t, site.SetLmAdvanced("circuitFromPeak", 2))
}

// The ui shows fromPeak only while the circuit's limit is the peak limit.
func TestLmCircuitFromPeakState(t *testing.T) {
	site, main, _ := lmSwitchCircuits(t, 3500)
	ch := make(chan util.Param, 100)
	site.valueChan = ch

	state := func() lmSwitchState {
		var res lmSwitchState
		for {
			select {
			case p := <-ch:
				if v, ok := p.Val.(lmSwitchState); ok && p.Key == keys.LmOff {
					res = v
				}
			default:
				return res
			}
		}
	}

	require.NoError(t, site.SetLmCircuit("main"))
	assert.False(t, state().FromPeak)

	require.NoError(t, site.SetLmAdvanced("circuitFromPeak", 1))
	st := state()
	assert.True(t, st.FromPeak)
	assert.Equal(t, 10000.0, st.Limits["main"], "the configured value is kept")
	assert.Equal(t, 3500.0, main.GetMaxPower())

	// the peak limit equals the configured value: still from the peak
	require.NoError(t, site.SetPeakShavingLimit(10000))
	assert.True(t, site.lms().offFromPeak)
	assert.Empty(t, site.lms().offLimits)
	assert.True(t, state().FromPeak)

	require.NoError(t, site.SetPeakShaving(false))
	assert.False(t, state().FromPeak, "peak shaving off")
}

// A circuit whose limit comes from a plugin stays, and so does one without a
// power limit.
func TestLmCircuitFromPeakPlugin(t *testing.T) {
	site, main, _ := lmSwitchCircuits(t, 3500)

	dyn, err := circuit.NewConfigurableFromConfig(context.Background(), map[string]any{
		"title":       "plugin",
		"maxpower":    10000,
		"getmaxpower": map[string]any{"source": "const", "value": "8000"},
	})
	require.NoError(t, err)
	require.NoError(t, config.Circuits().Add(config.NewStaticDevice(config.Named{Name: "dyn"}, dyn)))
	require.Equal(t, 8000.0, dyn.GetMaxPower())

	require.NoError(t, site.SetLmCircuit("dyn"))
	require.NoError(t, site.SetLmAdvanced("circuitFromPeak", 1))
	assert.Equal(t, 8000.0, dyn.GetMaxPower(), "plugin limit untouched")
	assert.True(t, site.lms().offDynamic["dyn"])
	assert.False(t, site.lms().offFromPeak)
	assert.Equal(t, 10000.0, main.GetMaxPower(), "the others untouched")

	// no power limit set: nothing to replace
	none, err := circuit.New(util.NewLogger("test"), "none", 16, 0, nil, 0)
	require.NoError(t, err)
	require.NoError(t, config.Circuits().Add(config.NewStaticDevice(config.Named{Name: "none"}, api.Circuit(none))))
	require.NoError(t, site.SetLmCircuit("none"))
	assert.Zero(t, none.GetMaxPower())
	assert.False(t, site.lms().offFromPeak)
}

// The switch survives a restart and takes effect at once: restoreCustom brings
// the load management settings back before the peak settings, so the circuit
// takes the peak limit only after both, not with the first cycle.
func TestLmCircuitFromPeakRestart(t *testing.T) {
	noSettingsDB(t)
	freshSettings(t)
	a, main, _ := lmSwitchCircuits(t, 3500)
	require.NoError(t, a.SetLmCircuit("main"))
	require.NoError(t, a.SetLmAdvanced("circuitFromPeak", 1))
	require.Equal(t, 3500.0, main.GetMaxPower())

	// peak shaving on with 4000 W as stored by the api
	settings.SetBool(keys.PeakShaving, true)
	settings.SetFloat(keys.PeakShavingLimit, 4000)

	// a restart: the circuit starts with its configured limit again
	main.SetMaxPower(10000)
	useLogFileDir(t)

	b := &Site{log: a.log, batteryMeters: a.batteryMeters}
	b.restoreCustom()
	assert.True(t, b.advanced().CircuitFromPeak)
	assert.Equal(t, "main", b.GetLmCircuit())

	assert.Equal(t, 4000.0, main.GetMaxPower(), "the peak limit applies after the restore, not with the first cycle")
	assert.True(t, b.lms().offFromPeak)
	assert.Equal(t, 10000.0, b.lms().offLimits["main"], "the configured value is kept")

	// not stored when off, so a default changed later applies
	require.NoError(t, a.SetLmAdvanced("circuitFromPeak", 0))
	var adv lmAdvanced
	require.NoError(t, settings.Json(keys.LmAdvanced, &adv))
	assert.False(t, adv.CircuitFromPeak)
}

// A restart between taking over the circuit and removing the old key leaves both:
// the old key goes, the circuit stays
func TestLmCircuitOldKeyLeftOver(t *testing.T) {
	noSettingsDB(t)
	config.Reset()
	t.Cleanup(config.Reset)
	keepSettings(t)

	log := util.NewLogger("test")
	main, err := circuit.New(log, "peak", 0, 10000, nil, 0)
	require.NoError(t, err)
	require.NoError(t, config.Circuits().Add(config.NewStaticDevice(config.Named{Name: "main"}, api.Circuit(main))))

	settings.SetString(keys.LmCircuit, "main")
	settings.SetString(keys.PeakFollowCircuit, "main")

	site := &Site{log: log}
	site.restoreLmSwitch()

	assert.Equal(t, "main", site.GetLmCircuit())
	_, err = settings.String(keys.PeakFollowCircuit)
	assert.Error(t, err, "old key removed")
	v, _ := settings.String(keys.LmCircuit)
	assert.Equal(t, "main", v)
}

// A deleted load management circuit takes no peak limit: the cycle removes the
// name, the circuit that is gone does not widen anything.
func TestLmCircuitDeleted(t *testing.T) {
	site, main, sub := lmSwitchCircuits(t, 3500)

	require.NoError(t, site.SetLmCircuit("garage"))
	require.NoError(t, site.SetLmAdvanced("circuitFromPeak", 1))
	assert.Equal(t, 3500.0, sub.GetMaxPower())
	assert.Equal(t, 10000.0, main.GetMaxPower())

	sub.SetMaxPower(7000)
	require.NoError(t, config.Circuits().Delete("garage"))
	site.lms().offDynamic = map[string]bool{"garage": true}
	require.Contains(t, site.lms().offLimits, "garage")
	site.applyCircuitLimits()
	assert.Empty(t, site.lms().offLimits, "the entry of the deleted circuit is gone")
	assert.Empty(t, site.lms().offDynamic, "so is its plugin mark")
	assert.Equal(t, 10000.0, main.GetMaxPower(), "nothing changed")
	assert.Equal(t, "garage", site.GetLmCircuit(), "the stored name stays until the cycle")

	site.updateCustom(siteState{})
	assert.Empty(t, site.GetLmCircuit())
	assert.Equal(t, 10000.0, main.GetMaxPower())
	_, err := settings.String(keys.LmCircuit)
	assert.Error(t, err, "key removed")
}

// A stored name of a circuit that is gone is cleaned up at the start.
func TestLmCircuitDeletedAtStart(t *testing.T) {
	site, main, _ := lmSwitchCircuits(t, 3500)
	settings.SetString(keys.LmCircuit, "gone")

	site = &Site{log: site.log}
	site.restoreLmSwitch()
	assert.Equal(t, 10000.0, main.GetMaxPower())
	assert.Equal(t, "gone", site.GetLmCircuit())

	site.dropOrphanCircuits()
	assert.Empty(t, site.GetLmCircuit())
	assert.Equal(t, 10000.0, main.GetMaxPower())
}

// The setters run on API goroutines and must not publish the circuits: that
// reads the values updateCircuits writes without a lock. The cycle publishes
// them, up to one cycle later. The race part only tells under -race (CI).
func TestCircuitLimitSettersDoNotPublish(t *testing.T) {
	site, main, _ := lmSwitchCircuits(t, 6000)
	site.circuit = main

	ch := make(chan util.Param, 1000)
	site.valueChan = ch

	require.NoError(t, site.SetLmCircuit("main"))
	require.NoError(t, site.SetLmAdvanced("circuitFromPeak", 1))

	close(ch)
	var lmOff bool
	for p := range ch {
		assert.NotEqual(t, keys.Circuits, p.Key, "a setter published the circuits")
		lmOff = lmOff || p.Key == keys.LmOff
	}
	assert.True(t, lmOff, "the circuit state itself is still published")

	// cycle against setters
	site.valueChan = nil
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 200 {
			site.updateCircuits()
		}
	}()
	for i := range 200 {
		_ = site.SetLmCircuit([]string{"", "main"}[i%2])
		_ = site.SetPeakShavingLimit(float64(5000 + i))
	}
	<-done
}
