package core

import (
	"testing"
	"time"

	"github.com/evcc-io/evcc/core/lm"
	"github.com/evcc-io/evcc/core/loadpoint"
	"github.com/evcc-io/evcc/util"
	"github.com/evcc-io/evcc/util/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLmShedGuardSettings(t *testing.T) {
	config.Reset()
	t.Cleanup(config.Reset)
	lm.Reset()
	t.Cleanup(lm.Reset)

	heater, wallbox := &Loadpoint{Title: "Heizstab"}, &Loadpoint{Title: "Wallbox"}
	require.NoError(t, config.Loadpoints().Add(config.NewStaticDevice(config.Named{Name: "db:5"}, loadpoint.API(heater))))
	require.NoError(t, config.Loadpoints().Add(config.NewStaticDevice(config.Named{Name: "lp-1"}, loadpoint.API(wallbox))))

	site := &Site{log: util.NewLogger("test")}
	site.restoreLmGuard()

	// nothing protected by default
	assert.Zero(t, lm.Shed(heater, time.Now()))

	require.NoError(t, site.SetLmShedGuard(5))
	require.NoError(t, site.SetLmShedProtected("db:5", true))
	assert.Equal(t, 5, site.GetLmShedGuard())

	assert.Equal(t, 5*time.Minute, lm.Shed(heater, time.Now()), "protected")
	assert.Zero(t, lm.Shed(wallbox, time.Now()), "not protected")

	// 0 minutes turns the guard off for all
	require.NoError(t, site.SetLmShedGuard(0))
	assert.Zero(t, lm.Guarded(heater, time.Now()))

	// removing the protection
	require.NoError(t, site.SetLmShedGuard(10))
	require.NoError(t, site.SetLmShedProtected("db:5", false))
	assert.Zero(t, lm.Shed(heater, time.Now()))

	// invalid input
	assert.Error(t, site.SetLmShedGuard(-1))
	assert.Error(t, site.SetLmShedGuard(121))
	assert.Error(t, site.SetLmShedProtected("db:99", true), "unknown loadpoint")
}
