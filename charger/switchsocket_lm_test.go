package charger

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func switchConfig(on bool, rated float64, power bool) map[string]any {
	cc := map[string]any{
		"enabled":      map[string]any{"source": "const", "value": on},
		"enable":       map[string]any{"source": "js", "script": "enable"},
		"standbypower": 15,
	}
	if rated > 0 {
		cc["ratedpower"] = rated
	}
	if power {
		cc["power"] = map[string]any{"source": "const", "value": 2800}
	}
	return cc
}

func TestSwitchSocketRatedPower(t *testing.T) {
	// without a power sensor the configured power is what the switch draws when on
	c, err := NewSwitchSocketFromConfig(t.Context(), switchConfig(true, 3000, false))
	require.NoError(t, err)

	s := c.(*SwitchSocket)
	assert.Equal(t, 3000.0, s.RatedPower())

	p, err := s.CurrentPower()
	require.NoError(t, err)
	assert.Equal(t, 3000.0, p, "on")

	c, err = NewSwitchSocketFromConfig(t.Context(), switchConfig(false, 3000, false))
	require.NoError(t, err)
	p, err = c.(*SwitchSocket).CurrentPower()
	require.NoError(t, err)
	assert.Equal(t, 0.0, p, "off")

	// with a sensor the measurement is reported, the configured power only kept
	c, err = NewSwitchSocketFromConfig(t.Context(), switchConfig(true, 3000, true))
	require.NoError(t, err)
	p, err = c.(*SwitchSocket).CurrentPower()
	require.NoError(t, err)
	assert.Equal(t, 2800.0, p)
	assert.Equal(t, 3000.0, c.(*SwitchSocket).RatedPower())

	// neither sensor nor configured power: still refused as before
	_, err = NewSwitchSocketFromConfig(t.Context(), switchConfig(true, 0, false))
	assert.Error(t, err)
}

func TestSwitchSocketRatedPowerFromStandbypower(t *testing.T) {
	cfg := func(standby float64, rated float64) map[string]any {
		cc := switchConfig(true, rated, true)
		cc["standbypower"] = standby
		return cc
	}

	// a negative standbypower is the power drawn when on
	c, err := NewSwitchSocketFromConfig(t.Context(), cfg(-3000, 0))
	require.NoError(t, err)
	assert.Equal(t, 3000.0, c.(*SwitchSocket).RatedPower())

	// a positive one is the standby draw, not a rating
	c, err = NewSwitchSocketFromConfig(t.Context(), cfg(15, 0))
	require.NoError(t, err)
	assert.Equal(t, 0.0, c.(*SwitchSocket).RatedPower())

	// ratedpower comes first
	c, err = NewSwitchSocketFromConfig(t.Context(), cfg(-3000, 2500))
	require.NoError(t, err)
	assert.Equal(t, 2500.0, c.(*SwitchSocket).RatedPower())
}
