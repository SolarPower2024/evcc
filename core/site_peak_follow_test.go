package core

import (
	"testing"
	"time"

	"github.com/benbjohnson/clock"
	"github.com/evcc-io/evcc/util"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPeakFollowLimit(t *testing.T) {
	assert.Equal(t, 9500.0, peakFollowLimit(5000, 10000, 500), "10 kW peak, 0.5 kW buffer")
	assert.Equal(t, 11800.0, peakFollowLimit(10000, 12345, 500), "rounded down to 100 W")
	assert.Equal(t, 10000.0, peakFollowLimit(10000, 10200, 500), "never below the base")
	assert.Equal(t, 10000.0, peakFollowLimit(10000, 0, 500), "no peak yet this month")
	assert.Equal(t, maxPeakLimit, peakFollowLimit(10000, 30000, 0), "capped")
}

// The limit rises with the month's peak, a limit set by hand is the base, a new
// month and switching off return to it.
func TestPeakFollow(t *testing.T) {
	clk := clock.NewMock()
	clk.Set(time.Date(2026, 9, 20, 12, 0, 0, 0, time.Local))

	site := &Site{log: util.NewLogger("test")}
	s := site.peak()
	s.clock = clk
	setPeakShaving(site, 10000, 20)
	s.followBuffer = defaultPeakFollowBuffer

	s.months = []peakMonth{{Month: "2026-09", Peak: 12345}}

	// off: the limit stays
	site.updatePeakFollow()
	assert.Equal(t, 10000.0, site.GetPeakShavingLimit())

	require.NoError(t, site.SetPeakFollow(true))
	assert.Equal(t, 11800.0, site.GetPeakShavingLimit(), "month's peak minus buffer")
	assert.Equal(t, 10000.0, s.followBase)

	// a higher peak later in the month
	s.months[0].Peak = 14000
	site.updatePeakFollow()
	assert.Equal(t, 13500.0, site.GetPeakShavingLimit())

	// set by hand while following: the base, the month's peak still counts
	require.NoError(t, site.SetPeakShavingLimit(15000))
	assert.Equal(t, 15000.0, site.GetPeakShavingLimit())
	require.NoError(t, site.SetPeakShavingLimit(8000))
	assert.Equal(t, 8000.0, s.followBase)
	assert.Equal(t, 13500.0, site.GetPeakShavingLimit())

	// buffer
	require.Error(t, site.SetPeakFollowBuffer(-100))
	require.Error(t, site.SetPeakFollowBuffer(250))
	require.NoError(t, site.SetPeakFollowBuffer(1000))
	assert.Equal(t, 13000.0, site.GetPeakShavingLimit())

	// new month without a peak yet: back to the base
	clk.Set(time.Date(2026, 10, 1, 0, 5, 0, 0, time.Local))
	site.updatePeakFollow()
	assert.Equal(t, 8000.0, site.GetPeakShavingLimit())

	// off: back to the base
	s.months = append([]peakMonth{{Month: "2026-10", Peak: 11000}}, s.months...)
	site.updatePeakFollow()
	assert.Equal(t, 10000.0, site.GetPeakShavingLimit())
	require.NoError(t, site.SetPeakFollow(false))
	assert.Equal(t, 8000.0, site.GetPeakShavingLimit())
	site.updatePeakFollow()
	assert.Equal(t, 8000.0, site.GetPeakShavingLimit(), "off: no longer follows")
}
