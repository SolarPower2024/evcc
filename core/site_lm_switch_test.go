package core

import (
	"testing"
	"time"

	"github.com/benbjohnson/clock"
	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/circuit"
	"github.com/evcc-io/evcc/util"
	"github.com/evcc-io/evcc/util/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCircuitLimitWanted(t *testing.T) {
	assert.Equal(t, 10000.0, circuitLimitWanted(10000, false, false, 0), "as configured")
	assert.Equal(t, 0.0, circuitLimitWanted(10000, true, true, 11300), "off: lifted")
	assert.Equal(t, 11300.0, circuitLimitWanted(10000, false, true, 11300), "follows the peak")
	assert.Equal(t, 22000.0, circuitLimitWanted(22000, false, true, 11300), "never below the configured value")
}

// Switching load management off lifts the power limits and keeps the current
// limits; follow the peak raises the chosen circuit; both return to the
// configured values.
func TestLmSwitchAndFollowCircuit(t *testing.T) {
	config.Reset()
	t.Cleanup(config.Reset)

	log := util.NewLogger("test")
	main, err := circuit.New(log, "peak", 32, 10000, nil, 0)
	require.NoError(t, err)
	require.NoError(t, config.Circuits().Add(config.NewStaticDevice(config.Named{Name: "main"}, api.Circuit(main))))
	sub, err := circuit.New(log, "garage", 0, 7000, nil, 0)
	require.NoError(t, err)
	require.NoError(t, config.Circuits().Add(config.NewStaticDevice(config.Named{Name: "garage"}, api.Circuit(sub))))

	clk := clock.NewMock()
	clk.Set(time.Date(2026, 9, 20, 12, 0, 0, 0, time.Local))
	site := &Site{log: log}
	p := site.peak()
	p.clock = clk
	setPeakShaving(site, 10000, 20)
	p.followBuffer = defaultPeakFollowBuffer
	p.months = []peakMonth{{Month: "2026-09", Peak: 11800}}

	// off: power limits lifted, the fuse stays
	require.NoError(t, site.SetLmEnabled(false))
	assert.Zero(t, main.GetMaxPower())
	assert.Zero(t, sub.GetMaxPower())
	assert.Equal(t, 32.0, main.GetMaxCurrent())
	assert.Equal(t, 20000.0, sub.ValidatePower(0, 20000), "power no longer capped")
	assert.Less(t, main.ValidateCurrent(0, 40), 40.0, "fuse still caps the current")

	// on again
	require.NoError(t, site.SetLmEnabled(true))
	assert.Equal(t, 10000.0, main.GetMaxPower())
	assert.Equal(t, 7000.0, sub.GetMaxPower())

	// follow the peak with the main circuit: it rises along
	require.NoError(t, site.SetPeakFollowCircuit("main"))
	require.NoError(t, site.SetPeakFollow(true))
	assert.Equal(t, 11300.0, site.GetPeakShavingLimit())
	assert.Equal(t, 11300.0, main.GetMaxPower())
	assert.Equal(t, 7000.0, sub.GetMaxPower(), "not chosen")

	// off wins, on returns to the raised value
	require.NoError(t, site.SetLmEnabled(false))
	assert.Zero(t, main.GetMaxPower())
	require.NoError(t, site.SetLmEnabled(true))
	assert.Equal(t, 11300.0, main.GetMaxPower())

	// new month: back to the configured value
	clk.Set(time.Date(2026, 10, 1, 0, 5, 0, 0, time.Local))
	site.updatePeakFollow()
	site.applyCircuitLimits()
	assert.Equal(t, 10000.0, main.GetMaxPower())

	// follow off: stays configured
	p.months = append([]peakMonth{{Month: "2026-10", Peak: 12500}}, p.months...)
	site.updatePeakFollow()
	site.applyCircuitLimits()
	assert.Equal(t, 12000.0, main.GetMaxPower())
	require.NoError(t, site.SetPeakFollow(false))
	assert.Equal(t, 10000.0, main.GetMaxPower())

	require.Error(t, site.SetPeakFollowCircuit("unknown"))
}
