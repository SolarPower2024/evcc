package core

import (
	"testing"

	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/db/settings"
	"github.com/evcc-io/evcc/util"
	"github.com/stretchr/testify/assert"
)

// TestSocChargeRunningSurvivesRestart verifies that soc-based grid charging
// interrupted by a restart carries on instead of waiting for the start soc again
func TestSocChargeRunningSurvivesRestart(t *testing.T) {
	defer settings.SetBool(keys.BatterySocGridChargeRunning, false)

	before := &Site{log: util.NewLogger("test")}
	before.setSocChargeRunning(true)

	after := &Site{log: util.NewLogger("test")}
	after.restoreLmSettings()

	assert.True(t, after.lms().socChargeRunning)

	// charging that ended stays ended
	after.setSocChargeRunning(false)

	again := &Site{log: util.NewLogger("test")}
	again.restoreLmSettings()

	assert.False(t, again.lms().socChargeRunning)
}
