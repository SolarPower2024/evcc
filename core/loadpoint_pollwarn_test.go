package core

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/coordinator"
	"github.com/evcc-io/evcc/core/loadpoint"
	"github.com/evcc-io/evcc/core/settings"
	"github.com/evcc-io/evcc/util"
	"github.com/evcc-io/evcc/util/config"
	"github.com/evcc-io/evcc/vehicle"
	jww "github.com/spf13/jwalterweatherman"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

const pollWarning = "may deplete your battery"

func pollLogger() (*util.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return &util.Logger{Notepad: jww.NewNotepad(jww.LevelTrace, jww.LevelTrace, io.Discard, &buf, "", 0)}, &buf
}

// pollLoadpoint is a loadpoint polling its vehicles always, with the vehicles of its site
func pollLoadpoint(vehicles ...api.Vehicle) (*Loadpoint, *bytes.Buffer) {
	log, buf := pollLogger()
	lp := &Loadpoint{log: log}
	lp.Soc.Poll.Mode = loadpoint.PollAlways
	lp.coordinator = coordinator.NewAdapter(lp, coordinator.New(log, vehicles))
	return lp, buf
}

// TestWarnPollMode: the warning stays for polling vehicles of the manufacturer
// and goes when there is no vehicle or all of them come from Home Assistant
func TestWarnPollMode(t *testing.T) {
	ctrl := gomock.NewController(t)
	ha := func() api.Vehicle { return &vehicle.HomeAssistant{} }
	other := func() api.Vehicle { return api.NewMockVehicle(ctrl) }

	for _, tc := range []struct {
		name     string
		vehicles []api.Vehicle
		warned   bool
	}{
		{"none", nil, false},
		{"only home assistant", []api.Vehicle{ha(), ha()}, false},
		{"mixed", []api.Vehicle{ha(), other()}, true},
		{"other", []api.Vehicle{other()}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lp, buf := pollLoadpoint(tc.vehicles...)
			lp.warnPollMode()
			assert.Equal(t, tc.warned, strings.Count(buf.String(), pollWarning) == 1, buf.String())
			assert.LessOrEqual(t, strings.Count(buf.String(), pollWarning), 1)
		})
	}

	t.Run("charging mode", func(t *testing.T) {
		lp, buf := pollLoadpoint(other())
		lp.Soc.Poll.Mode = loadpoint.PollCharging
		lp.warnPollMode()
		assert.Empty(t, buf.String())
	})
}

// TestWarnPollModeHomeAssistantTypes: the vehicle type of yaml and of the
// template "Home Assistant" is the one the check looks for
func TestWarnPollModeHomeAssistantTypes(t *testing.T) {
	v, err := vehicle.NewFromConfig(context.Background(), "homeassistant", map[string]any{
		"uri":     "http://127.0.0.1:1",
		"sensors": map[string]any{"soc": "sensor.soc"},
	})
	require.NoError(t, err)
	assert.IsType(t, &vehicle.HomeAssistant{}, v)

	lp, buf := pollLoadpoint(v)
	lp.warnPollMode()
	assert.Empty(t, buf.String())

	// the template renders that type
	b, err := os.ReadFile("../templates/definition/vehicle/homeassistant.yaml")
	require.NoError(t, err)
	assert.Contains(t, string(b), "type: homeassistant")
}

// TestWarnPollModeBehindWrapper: a vehicle that could not be created is not
// known to be from Home Assistant, so the warning stays
func TestWarnPollModeBehindWrapper(t *testing.T) {
	w := vehicle.NewWrapper(context.Background(), "v", "homeassistant", map[string]any{}, assert.AnError)
	lp, buf := pollLoadpoint(w)
	lp.warnPollMode()
	assert.Contains(t, buf.String(), pollWarning)
}

// TestLoadConfigDoesNotWarnPollMode: loading the configuration knows no
// vehicles yet, the warning comes with Prepare (warnPollMode)
func TestLoadConfigDoesNotWarnPollMode(t *testing.T) {
	ctrl := gomock.NewController(t)
	require.NoError(t, config.Chargers().Add(config.NewStaticDevice(config.Named{Name: "poll-charger"}, api.Charger(api.NewMockCharger(ctrl)))))

	log, buf := pollLogger()
	lp, err := NewLoadpointFromConfig(log, settings.NewDatabaseSettingsAdapter("poll."), nil, map[string]any{
		"charger": "poll-charger",
		"soc":     map[string]any{"poll": map[string]any{"mode": "always"}},
	})
	require.NoError(t, err)
	assert.Equal(t, loadpoint.PollAlways, lp.Soc.Poll.Mode, "the mode is kept")
	assert.NotContains(t, buf.String(), pollWarning)
}
