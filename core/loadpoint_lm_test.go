package core

import (
	"testing"

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
	lm.Reset()
	Voltage = 230

	m := &lmMeter{power: grid}
	c, err := circuit.New(util.NewLogger("test"), "main", 0, 10000, m, 0)
	require.NoError(t, err)
	require.NoError(t, c.Update(nil))

	lp := &Loadpoint{
		log:        util.NewLogger("lp"),
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
