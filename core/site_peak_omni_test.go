package core

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/benbjohnson/clock"
	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/core/peak"
	"github.com/evcc-io/evcc/db/settings"
	"github.com/evcc-io/evcc/util"
	"github.com/evcc-io/evcc/util/homeassistant"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	haSwitch       = "switch.manual"
	haSelect       = "select.mode"
	haDischargeNum = "number.discharge"
	haChargeNum    = "number.charge"
)

// fakeHA is a Home Assistant answering state reads and service calls, and
// recording the calls as "service entity [value]". A failing service answers
// with the status in fail, e.g. "switch/turn_on".
type fakeHA struct {
	mu     sync.Mutex
	states map[string]*haEntity
	calls  []string
	fail   map[string]int
}

type haEntity struct {
	state string
	attrs map[string]any
}

func newFakeHA() *fakeHA {
	number := func(state string, upper float64) *haEntity {
		return &haEntity{state, map[string]any{"min": 0, "max": upper, "step": 10}}
	}

	return &fakeHA{
		fail: make(map[string]int),
		states: map[string]*haEntity{
			haSwitch:       {"off", map[string]any{}},
			haSelect:       {"None", map[string]any{"options": []string{"None", "Charge", "Discharge"}}},
			haDischargeNum: number("0", 10000),
			haChargeNum:    number("0", 6250),
		},
	}
}

func (f *fakeHA) set(entity, state string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.states[entity].state = state
}

func (f *fakeHA) get(entity string) string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.states[entity].state
}

// take returns the calls since the last take
func (f *fakeHA) take() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	calls := f.calls
	f.calls = nil

	return calls
}

func (f *fakeHA) RoundTrip(req *http.Request) (*http.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	reply := func(code int, body string) (*http.Response, error) {
		return &http.Response{
			StatusCode: code,
			Status:     fmt.Sprintf("%d %s", code, http.StatusText(code)),
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	}

	path := strings.TrimPrefix(req.URL.Path, "/core")

	if id, ok := strings.CutPrefix(path, "/api/states/"); ok && req.Method == http.MethodGet {
		e, ok := f.states[id]
		if !ok {
			return reply(http.StatusNotFound, `{"message":"Entity not found."}`)
		}

		b, _ := json.Marshal(map[string]any{"entity_id": id, "state": e.state, "attributes": e.attrs})

		return reply(http.StatusOK, string(b))
	}

	service, ok := strings.CutPrefix(path, "/api/services/")
	if !ok || req.Method != http.MethodPost {
		return reply(http.StatusNotFound, "")
	}

	domain, name, _ := strings.Cut(service, "/")

	var data map[string]any
	if err := json.NewDecoder(req.Body).Decode(&data); err != nil {
		return reply(http.StatusBadRequest, err.Error())
	}

	id, _ := data["entity_id"].(string)
	call := name + " " + id
	for _, k := range []string{"option", "value"} {
		if v, ok := data[k]; ok {
			call += " " + fmt.Sprint(v)
		}
	}
	f.calls = append(f.calls, call)

	if code := f.fail[domain+"/"+name]; code != 0 {
		return reply(code, "500 Internal Server Error\n\nServer got itself in trouble")
	}

	e := f.states[id]

	switch name {
	case "turn_on":
		e.state = "on"
	case "turn_off":
		e.state = "off"
	case "select_option":
		e.state = fmt.Sprint(data["option"])
	case "set_value":
		e.state = strconv.FormatFloat(data["value"].(float64), 'f', -1, 64)
	}

	return reply(http.StatusOK, "[]")
}

// newOmni returns a scenario with the battery type Marstek set up against a fake
// Home Assistant: discharge power and charge power are numbers, the manual control
// a switch and the forced mode a select
func newOmni(t *testing.T) (*scenario, *fakeHA) {
	t.Helper()

	sc := newScenario(t)
	sc.site.lms().socChargeEnabled = false // discharge only, tests of charging turn it on

	ha := newFakeHA()

	conn, err := homeassistant.NewConnection(util.NewLogger("test"), homeassistant.SupervisorURI, "", false)
	require.NoError(t, err)
	conn.Client.Transport = ha

	s := sc.site.peak()
	s.conn = conn
	s.batteryType = batteryTypeMarstek
	s.manualEntity = haSwitch
	s.modeEntity = haSelect
	s.entity = haDischargeNum
	s.chargeEntity = haChargeNum
	require.NoError(t, sc.site.rebuildPeakSetter())
	require.NoError(t, sc.site.rebuildChargeSetter())

	return sc, ha
}

// TestOmniWant verifies the wish for every line of the table, from the top
func TestOmniWant(t *testing.T) {
	charge := func(float64) error { return nil }
	discharge := func(float64) error { return nil }

	tc := []struct {
		name    string
		setup   func(s *peakState)
		on      bool
		mode    omniMode
		power   float64
		wantOut bool // the power is written
	}{
		{"grid charging", func(s *peakState) { s.chargeSetpoint = 4000 }, true, omniCharge, 4000, true},
		{"grid charging wins over a peak", func(s *peakState) {
			s.chargeSetpoint = 4000
			s.enabled, s.shaving, s.omniPeakValue = true, true, 3000
		}, true, omniCharge, 4000, true},
		{"grid charging without a charge entity", func(s *peakState) {
			s.chargeSetpoint = 4000
			s.chargeSet = nil
		}, false, "", 0, false},
		{"peak", func(s *peakState) { s.enabled, s.shaving, s.omniPeakValue = true, true, 3000 }, true, omniDischarge, 3000, true},
		{"below the reserve, no peak", func(s *peakState) { s.enabled, s.shaving = true, true }, true, omniIdle, 0, false},
		{"above the reserve", func(s *peakState) { s.enabled, s.omniPeakValue = true, 3000 }, false, "", 0, false},
		{"peak shaving off", func(s *peakState) { s.shaving, s.omniPeakValue = true, 3000 }, false, "", 0, false},
		{"meters lost", func(s *peakState) {
			s.enabled, s.shaving, s.omniPeakValue, s.metersLost = true, true, 3000, true
		}, false, "", 0, false},
		{"nothing", func(s *peakState) {}, false, "", 0, false},
	}

	for _, tc := range tc {
		t.Run(tc.name, func(t *testing.T) {
			site := &Site{log: util.NewLogger("test")}

			s := site.peak()
			s.set = discharge
			s.chargeSet = charge
			tc.setup(s)

			on, mode, power, out := site.omniWant()
			assert.Equal(t, tc.on, on)
			assert.Equal(t, tc.mode, mode)
			assert.Equal(t, tc.power, power)
			assert.Equal(t, tc.wantOut, out != nil)
		})
	}
}

// TestOmniOrder verifies that the manual control is switched on first, then the
// mode forced, then the power written, each only after the one before landed
func TestOmniOrder(t *testing.T) {
	sc, ha := newOmni(t)

	// a peak under the reserve
	sc.cycle(25, 8000, 0)
	assert.Equal(t, []string{
		"turn_on " + haSwitch,
		"select_option " + haSelect + " Discharge",
		"set_value " + haDischargeNum + " 3000",
	}, ha.take())
	assert.True(t, sc.site.peakOwned())
	assert.Equal(t, "on", ha.get(haSwitch))
	assert.Equal(t, "Discharge", ha.get(haSelect))
	assert.Equal(t, "3000", ha.get(haDischargeNum))

	// the switch fails: neither mode nor power
	ha.set(haSwitch, "off")
	ha.fail["switch/turn_on"] = http.StatusInternalServerError
	sc.cycle(25, 8000, 0)
	assert.Equal(t, []string{"turn_on " + haSwitch}, ha.take())

	// the mode fails: no power
	delete(ha.fail, "switch/turn_on")
	ha.set(haSelect, "None")
	ha.set(haDischargeNum, "0")
	ha.fail["select/select_option"] = http.StatusInternalServerError
	sc.cycle(25, 8000, 0)
	assert.Equal(t, []string{"turn_on " + haSwitch, "select_option " + haSelect + " Discharge"}, ha.take())

	// and the next cycle starts again
	delete(ha.fail, "select/select_option")
	sc.cycle(25, 8000, 0)
	assert.Equal(t, []string{"select_option " + haSelect + " Discharge", "set_value " + haDischargeNum + " 3000"}, ha.take())
}

// TestOmniSkipsUnchanged verifies that nothing is written while Home Assistant
// shows what evcc wants
func TestOmniSkipsUnchanged(t *testing.T) {
	sc, ha := newOmni(t)

	sc.cycle(25, 8000, 0)
	ha.take()

	for range 3 {
		sc.cycle(25, 8000, 0)
	}
	assert.Empty(t, ha.take())

	// below the reserve without a peak: the mode None, the power stays
	sc.cycle(25, 3000, 0)
	assert.Equal(t, []string{"select_option " + haSelect + " None"}, ha.take())
	sc.cycle(25, 3000, 0)
	assert.Empty(t, ha.take())
	assert.Equal(t, "3000", ha.get(haDischargeNum))
}

// TestOmniGridCharge verifies the charge mode with the charge power, and that the
// discharge entity is not touched
func TestOmniGridCharge(t *testing.T) {
	sc, ha := newOmni(t)
	sc.site.lms().socChargeEnabled = true

	assert.True(t, sc.cycle(20, 1000, 0))
	assert.Equal(t, []string{
		"turn_on " + haSwitch,
		"select_option " + haSelect + " Charge",
		"set_value " + haChargeNum + " 4000",
	}, ha.take())
	assert.Equal(t, 4000.0, sc.site.peak().chargeSetpoint)

	// charging goes on, the room below the peak limit changes
	assert.True(t, sc.cycle(20, 2000, 0))
	assert.Equal(t, []string{"set_value " + haChargeNum + " 3000"}, ha.take())

	// done: the charge power to zero first, then released
	assert.False(t, sc.cycle(90, 1000, 0))
	assert.Equal(t, []string{"set_value " + haChargeNum + " 0", "turn_off " + haSwitch}, ha.take())
	assert.Equal(t, "0", ha.get(haChargeNum))
	assert.False(t, sc.site.peakOwned())

	// released once, and a charge power already at zero is not written again
	assert.False(t, sc.cycle(90, 1000, 0))
	assert.Empty(t, ha.take())
}

// TestOmniGridChargeEndZeroFails verifies that the switch stays on while the
// charge power cannot be set to zero, and that the next cycle tries again
func TestOmniGridChargeEndZeroFails(t *testing.T) {
	sc, ha := newOmni(t)
	sc.site.lms().socChargeEnabled = true

	assert.True(t, sc.cycle(20, 1000, 0))
	ha.take()

	ha.fail["number/set_value"] = http.StatusInternalServerError
	for range 2 {
		assert.False(t, sc.cycle(90, 1000, 0))
		assert.Equal(t, []string{"set_value " + haChargeNum + " 0"}, ha.take(), "no turn_off")
		assert.Equal(t, "on", ha.get(haSwitch))
		assert.True(t, sc.site.peakOwned())
	}

	delete(ha.fail, "number/set_value")
	assert.False(t, sc.cycle(90, 1000, 0))
	assert.Equal(t, []string{"set_value " + haChargeNum + " 0", "turn_off " + haSwitch}, ha.take())
	assert.False(t, sc.site.peakOwned())
}

// TestOmniGridChargeWithoutChargeEntity verifies that grid charging without the
// charge power entity stays as it is: on and off by the battery mode, nothing
// switched
func TestOmniGridChargeWithoutChargeEntity(t *testing.T) {
	sc, ha := newOmni(t)
	sc.site.lms().socChargeEnabled = true

	s := sc.site.peak()
	s.chargeEntity = ""
	require.NoError(t, sc.site.rebuildChargeSetter())
	s.enabled = false

	assert.True(t, sc.cycle(20, 1000, 0))
	assert.True(t, sc.cycle(20, 1000, 0))
	assert.Empty(t, ha.take())
	assert.False(t, sc.site.peakOwned())
}

// TestOmniCorrectsHandChange verifies that a switch or mode changed by hand while
// evcc controls is corrected in the next cycle
func TestOmniCorrectsHandChange(t *testing.T) {
	sc, ha := newOmni(t)

	sc.cycle(25, 8000, 0)
	ha.take()

	ha.set(haSwitch, "off")
	sc.cycle(25, 8000, 0)
	assert.Equal(t, []string{"turn_on " + haSwitch}, ha.take())

	ha.set(haSelect, "Charge")
	sc.cycle(25, 8000, 0)
	assert.Equal(t, []string{"select_option " + haSelect + " Discharge"}, ha.take())
}

// TestOmniLeavesManualAloneWhenNotOwned verifies that a switch turned on by hand
// is left alone while evcc does not control. Once evcc forced mode and power into
// it, evcc owns it and releases it afterwards, a forced discharge would otherwise
// stay.
func TestOmniLeavesManualAloneWhenNotOwned(t *testing.T) {
	sc, ha := newOmni(t)
	s := sc.site.peak()

	ha.set(haSwitch, "on")

	// peak shaving off, then above the reserve
	s.enabled = false
	sc.cycle(25, 8000, 0)
	s.enabled = true
	sc.cycle(50, 8000, 0)
	sc.cycle(50, 8000, 0)
	assert.Empty(t, ha.take())
	assert.Equal(t, "on", ha.get(haSwitch))

	// evcc controls without having to switch: mode and power, and owns it now
	sc.cycle(25, 8000, 0)
	assert.Equal(t, []string{
		"select_option " + haSelect + " Discharge",
		"set_value " + haDischargeNum + " 3000",
	}, ha.take())
	assert.True(t, sc.site.peakOwned())

	// so the forced discharge does not stay once evcc gives up
	sc.cycle(50, 8000, 0)
	assertReleased(t, sc, ha)
}

// TestOmniChangeRefusedWhenNotReleased verifies that the switch, the mode, the
// discharge entity and the battery type are kept while what evcc holds cannot be
// released: the battery would otherwise stay in manual control with nothing left
// to release it
func TestOmniChangeRefusedWhenNotReleased(t *testing.T) {
	changes := []struct {
		name   string
		change func(site *Site) error
		kept   func(site *Site) bool
	}{
		{"switch replaced", func(site *Site) error { return site.SetPeakShavingManualEntity("switch.other") },
			func(site *Site) bool { return site.GetPeakShavingManualEntity() == haSwitch }},
		{"switch removed", func(site *Site) error { return site.SetPeakShavingManualEntity("") },
			func(site *Site) bool { return site.GetPeakShavingManualEntity() == haSwitch }},
		{"mode removed", func(site *Site) error { return site.SetPeakShavingModeEntity("") },
			func(site *Site) bool { return site.GetPeakShavingModeEntity() == haSelect }},
		{"discharge entity removed", func(site *Site) error { return site.SetPeakShavingEntity("") },
			func(site *Site) bool { return site.GetPeakShavingEntity() == haDischargeNum && site.omniEnabled() }},
		{"battery type changed", func(site *Site) error { return site.SetPeakShavingBatteryType(batteryTypeBYD) },
			func(site *Site) bool { return site.GetPeakShavingBatteryType() == batteryTypeMarstek }},
	}

	for _, tc := range changes {
		t.Run(tc.name, func(t *testing.T) {
			sc, ha := newOmni(t)
			sc.cycle(25, 8000, 0)
			ha.take()

			ha.fail["switch/turn_off"] = http.StatusInternalServerError
			require.ErrorIs(t, tc.change(sc.site), errHandBack)
			assert.True(t, tc.kept(sc.site))
			assert.True(t, sc.site.peakOwned())
			assert.True(t, sc.site.GetPeakShaving())

			// once Home Assistant takes it, the change goes through and releases
			delete(ha.fail, "switch/turn_off")
			ha.take()
			require.NoError(t, tc.change(sc.site))
			assert.Equal(t, "off", ha.get(haSwitch))
			assert.False(t, sc.site.peakOwned())
		})
	}
}

// TestOmniTypeNeedsEntitiesWhileOn verifies that running peak shaving cannot be
// moved to the battery type Marstek without its switch and mode: it would stay on
// writing nothing
func TestOmniTypeNeedsEntitiesWhileOn(t *testing.T) {
	sc, ha := newOmni(t)
	s := sc.site.peak()
	s.batteryType = batteryTypeBYD
	s.manualEntity = ""

	require.ErrorContains(t, sc.site.SetPeakShavingBatteryType(batteryTypeMarstek), "no manual switch or mode entity configured")
	assert.Equal(t, batteryTypeBYD, sc.site.GetPeakShavingBatteryType())
	assert.Empty(t, ha.take())

	// with peak shaving off the type can be chosen first
	require.NoError(t, sc.site.SetPeakShaving(false))
	require.NoError(t, sc.site.SetPeakShavingBatteryType(batteryTypeMarstek))
}

// assertReleased checks that only the manual control was switched off
func assertReleased(t *testing.T, sc *scenario, ha *fakeHA) {
	t.Helper()

	assert.Equal(t, []string{"turn_off " + haSwitch}, ha.take())
	assert.False(t, sc.site.peakOwned())
	assert.Equal(t, "off", ha.get(haSwitch))
	assert.Equal(t, "Discharge", ha.get(haSelect), "mode and power stay")
	assert.Equal(t, "3000", ha.get(haDischargeNum))
}

// TestOmniRelease verifies the release by the manual control alone when peak
// shaving is switched off, the reserve is left, the meters fail and the battery
// type is changed
func TestOmniRelease(t *testing.T) {
	control := func(t *testing.T) (*scenario, *fakeHA) {
		sc, ha := newOmni(t)
		sc.cycle(25, 8000, 0)
		require.Len(t, ha.take(), 3)
		require.True(t, sc.site.peakOwned())

		return sc, ha
	}

	t.Run("peak shaving off", func(t *testing.T) {
		sc, ha := control(t)
		require.NoError(t, sc.site.SetPeakShaving(false))
		assertReleased(t, sc, ha)

		// released once
		sc.cycle(25, 8000, 0)
		assert.Empty(t, ha.take())
	})

	t.Run("above the reserve", func(t *testing.T) {
		sc, ha := control(t)
		sc.cycle(50, 8000, 0)
		assertReleased(t, sc, ha)

		sc.cycle(50, 8000, 0)
		assert.Empty(t, ha.take())
	})

	t.Run("meters lost", func(t *testing.T) {
		sc, ha := newOmni(t)
		clk := clock.NewMock()
		sc.site.peak().clock = clk

		sc.cycle(25, 8000, 0)
		ha.take()

		failed := func() {
			clk.Add(30 * time.Second)
			charge := sc.site.batteryGridChargeRequested(sc.rate)
			sc.site.updateBatteryModePeakAware(charge, false, sc.rate)
		}

		for range 4 {
			failed()
		}
		assert.Empty(t, ha.take())

		failed()
		assertReleased(t, sc, ha)

		failed()
		assert.Empty(t, ha.take())

		// the meters are back
		clk.Add(30 * time.Second)
		sc.cycle(25, 8000, 0)
		calls := ha.take()
		require.NotEmpty(t, calls)
		assert.Equal(t, "turn_on "+haSwitch, calls[0])
	})

	t.Run("battery type changed", func(t *testing.T) {
		sc, ha := control(t)
		require.NoError(t, sc.site.SetPeakShavingBatteryType(batteryTypeBYD))
		assertReleased(t, sc, ha)

		// BYD writes the power alone
		sc.cycle(25, 9000, 0)
		assert.Equal(t, []string{"set_value " + haDischargeNum + " 4000"}, ha.take())
		assert.True(t, sc.site.peakOwned())

		// and back: the entity held at the setpoint gets the free value
		require.NoError(t, sc.site.SetPeakShavingBatteryType(batteryTypeMarstek))
		assert.Equal(t, []string{"set_value " + haDischargeNum + " " + fmt.Sprint(peak.DefaultFreeValue)}, ha.take())
		assert.False(t, sc.site.peakOwned())
	})

	t.Run("switch removed", func(t *testing.T) {
		sc, ha := control(t)
		require.NoError(t, sc.site.SetPeakShavingManualEntity(""))
		assert.Equal(t, []string{"turn_off " + haSwitch}, ha.take())
		assert.False(t, sc.site.peakOwned())
		assert.False(t, sc.site.GetPeakShaving(), "cannot run without the switch")
	})

	t.Run("mode removed", func(t *testing.T) {
		sc, ha := control(t)
		require.NoError(t, sc.site.SetPeakShavingModeEntity(""))
		assert.Equal(t, []string{"turn_off " + haSwitch}, ha.take())
		assert.False(t, sc.site.GetPeakShaving())
	})

	t.Run("discharge entity removed", func(t *testing.T) {
		sc, ha := control(t)
		require.NoError(t, sc.site.SetPeakShavingEntity(""))
		assert.Equal(t, []string{"turn_off " + haSwitch}, ha.take())
		assert.False(t, sc.site.peakOwned())
		assert.False(t, sc.site.GetPeakShaving())
	})
}

// TestOmniReleaseAfterRestart verifies that a restart in the middle of a control
// still releases: the type, the entities and owned are stored
func TestOmniReleaseAfterRestart(t *testing.T) {
	sc, ha := newOmni(t)

	sc.cycle(25, 8000, 0)
	ha.take()

	// what the api stored; a new site reads it back
	settings.SetString(keys.PeakShavingBatteryType, batteryTypeMarstek)
	settings.SetString(keys.PeakShavingManualEntity, haSwitch)
	settings.SetString(keys.PeakShavingModeEntity, haSelect)
	settings.SetString(keys.PeakShavingEntity, haDischargeNum)
	settings.SetString(keys.PeakShavingChargeEntity, haChargeNum)
	settings.SetBool(keys.PeakShaving, false)
	settings.SetBool(keys.PeakShavingOwned, true)

	again := newScenario(t)
	again.site.lms().socChargeEnabled = false
	again.site.custom.peak.conn = sc.site.peak().conn
	again.site.restorePeakSettings()

	assert.True(t, again.site.omniEnabled())
	assert.True(t, again.site.peakOwned())

	for range 3 {
		again.cycle(25, 8000, 0)
	}
	assert.Equal(t, []string{"turn_off " + haSwitch}, ha.take())
	assert.False(t, again.site.peakOwned())
}

// TestOmniNoFreeValue verifies that the free value never reaches a Marstek
func TestOmniNoFreeValue(t *testing.T) {
	sc, ha := newOmni(t)

	var all []string
	run := func(f func()) {
		f()
		all = append(all, ha.take()...)
	}

	run(func() { sc.cycle(50, 8000, 0) }) // above the reserve
	run(func() { sc.cycle(25, 8000, 0) })
	run(func() { sc.cycle(50, 8000, 0) }) // released
	run(func() { sc.cycle(25, 8000, 0) })
	run(func() { require.NoError(t, sc.site.SetPeakShaving(false)) })
	run(func() { sc.cycle(25, 8000, 0) })
	run(func() { require.NoError(t, sc.site.SetPeakShavingEntity("")) })

	assert.NotEmpty(t, all)
	for _, call := range all {
		assert.NotContains(t, call, fmt.Sprint(peak.DefaultFreeValue), call)
	}
}

// TestOmniModeEntityOptions verifies that the entities are checked when set: the
// domain, and for the select the options as written
func TestOmniModeEntityOptions(t *testing.T) {
	sc, ha := newOmni(t)
	s := sc.site.peak()
	s.modeEntity = ""

	err := sc.site.SetPeakShavingModeEntity("sensor.mode")
	require.ErrorContains(t, err, "select or input_select")

	// Discharge missing
	ha.mu.Lock()
	ha.states["select.bad"] = &haEntity{"None", map[string]any{"options": []string{"None", "Charge"}}}
	ha.states["select.lower"] = &haEntity{"none", map[string]any{"options": []string{"none", "charge", "discharge"}}}
	ha.mu.Unlock()

	err = sc.site.SetPeakShavingModeEntity("select.bad")
	require.ErrorContains(t, err, "Discharge")
	require.Error(t, sc.site.SetPeakShavingModeEntity("select.lower"), "the exact spelling")
	require.Error(t, sc.site.SetPeakShavingModeEntity("select.missing"))
	assert.Empty(t, sc.site.GetPeakShavingModeEntity())

	require.NoError(t, sc.site.SetPeakShavingModeEntity(haSelect))
	assert.Equal(t, haSelect, sc.site.GetPeakShavingModeEntity())

	// the switch
	require.ErrorContains(t, sc.site.SetPeakShavingManualEntity("sensor.manual"), "switch or input_boolean")
	require.NoError(t, sc.site.SetPeakShavingManualEntity("input_boolean.manual"))

	// the battery type
	require.Error(t, sc.site.SetPeakShavingBatteryType("zendure"))
}

// TestOmniPeakShavingNeedsEntities verifies that peak shaving cannot be turned on
// for the battery type Marstek without its switch and mode
func TestOmniPeakShavingNeedsEntities(t *testing.T) {
	sc, _ := newOmni(t)
	s := sc.site.peak()
	s.enabled = false
	s.manualEntity = ""

	require.ErrorContains(t, sc.site.SetPeakShaving(true), "no manual switch or mode entity configured")

	s.batteryType = batteryTypeBYD
	require.NoError(t, sc.site.SetPeakShaving(true))
}

// TestOmniInertForBYD verifies that the BYD type writes the power alone, with the
// Marstek entities set or not
func TestOmniInertForBYD(t *testing.T) {
	for _, typ := range []string{"", batteryTypeBYD} {
		t.Run("type "+typ, func(t *testing.T) {
			sc, ha := newOmni(t)
			sc.site.peak().batteryType = typ

			sc.cycle(25, 8000, 0)
			assert.Equal(t, []string{"set_value " + haDischargeNum + " 3000"}, ha.take())
			assert.Equal(t, batteryTypeBYD, sc.site.GetPeakShavingBatteryType())

			// the free value above the reserve
			sc.cycle(50, 8000, 0)
			assert.Equal(t, []string{"set_value " + haDischargeNum + " " + fmt.Sprint(peak.DefaultFreeValue)}, ha.take())
		})
	}
}

// TestOmniErrorLogged verifies that a rejected write is logged once with the
// answer of Home Assistant and its hint
func TestOmniErrorLogged(t *testing.T) {
	sc, ha := newOmni(t)

	var buf strings.Builder
	sc.site.log = testLogger(&buf)

	ha.fail["switch/turn_on"] = http.StatusInternalServerError
	for range 5 {
		sc.cycle(25, 8000, 0)
	}

	assert.Equal(t, 1, strings.Count(buf.String(), "ERROR"), buf.String())
	assert.Contains(t, buf.String(), "Server got itself in trouble")
	assert.Contains(t, buf.String(), "(details in the Home Assistant log)")
}
