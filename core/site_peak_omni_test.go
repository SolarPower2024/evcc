package core

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/benbjohnson/clock"
	"github.com/evcc-io/evcc/api"
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
	haProtSwitch   = "switch.prot"
	haProtLimit    = "number.prot_limit"
	haProtSoc      = "number.prot_soc"
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
	number := func(state string, lower, upper, step float64) *haEntity {
		return &haEntity{state, map[string]any{"min": lower, "max": upper, "step": step}}
	}

	return &fakeHA{
		fail: make(map[string]int),
		states: map[string]*haEntity{
			haSwitch:       {"off", map[string]any{}},
			haSelect:       {"None", map[string]any{"options": []string{"None", "Charge", "Discharge"}}},
			haDischargeNum: number("0", 0, 10000, 10),
			haChargeNum:    number("0", 0, 6250, 10),
			// as on the live system
			haProtSwitch: {"off", map[string]any{}},
			haProtLimit:  number("20000", 500, 20000, 100),
			haProtSoc:    number("20", 20, 100, 1),
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
// Home Assistant: the charge power is a number, the manual control a switch and
// the forced mode a select (grid charging), the peak shaving of Omnibattery a
// switch and two numbers (peak shaving). The discharge power is set as well, it
// must stay untouched. The clock stands still at the start of a window, so the
// allowed power is the limit until a test moves it.
func newOmni(t *testing.T) (*scenario, *fakeHA) {
	t.Helper()

	// the settings stay in memory, a database left open by another test would make
	// the cleanup of keepSettings fail, depending on the test order
	noSettingsDB(t)

	sc := newScenario(t)
	sc.site.lms().socChargeEnabled = false // discharge only, tests of charging turn it on

	ha := newFakeHA()

	conn, err := homeassistant.NewConnection(util.NewLogger("test"), homeassistant.SupervisorURI, "", false)
	require.NoError(t, err)
	conn.Client.Transport = ha

	s := sc.site.peak()
	s.clock = clock.NewMock()
	s.conn = conn
	s.batteryType = batteryTypeMarstek
	s.manualEntity = haSwitch
	s.modeEntity = haSelect
	s.entity = haDischargeNum
	s.chargeEntity = haChargeNum
	s.protSwitch = haProtSwitch
	s.protLimit = haProtLimit
	s.protSoc = haProtSoc
	require.NoError(t, sc.site.rebuildPeakSetter())
	require.NoError(t, sc.site.rebuildChargeSetter())
	require.NoError(t, sc.site.rebuildProtSetter())

	return sc, ha
}

// mockClock returns the clock of the scenario
func mockClock(sc *scenario) *clock.Mock {
	return sc.site.peak().clock.(*clock.Mock)
}

// protOn are the calls turning Omnibattery's peak shaving on
func protOn(soc, limit string) []string {
	return []string{
		"set_value " + haProtSoc + " " + soc,
		"set_value " + haProtLimit + " " + limit,
		"turn_on " + haProtSwitch,
	}
}

// withoutProt returns the calls not meant for Omnibattery's peak shaving
func withoutProt(calls []string) []string {
	var res []string
	for _, c := range calls {
		if !strings.Contains(c, "prot") {
			res = append(res, c)
		}
	}

	return res
}

// charging runs the first cycle of grid charging, both parts are on afterwards
func charging(t *testing.T) (*scenario, *fakeHA) {
	t.Helper()

	sc, ha := newOmni(t)
	sc.site.lms().socChargeEnabled = true

	require.True(t, sc.cycle(20, 1000, 0))
	require.Equal(t, append([]string{
		"turn_on " + haSwitch,
		"select_option " + haSelect + " Charge",
		"set_value " + haChargeNum + " 4000",
	}, protOn("30", "5000")...), ha.take())
	require.True(t, sc.site.peakOwned())
	require.True(t, sc.site.peak().protOwned)

	return sc, ha
}

// protecting runs the first cycle of a peak below the reserve, only Omnibattery's
// peak shaving is on afterwards
func protecting(t *testing.T) (*scenario, *fakeHA) {
	t.Helper()

	sc, ha := newOmni(t)

	sc.cycle(25, 8000, 0)
	require.Equal(t, protOn("30", "5000"), ha.take())
	require.True(t, sc.site.peak().protOwned)
	require.False(t, sc.site.peakOwned())

	return sc, ha
}

// TestOmniWant verifies the wish: grid charging with a setpoint, nothing else
func TestOmniWant(t *testing.T) {
	chargeFn := func(float64) error { return nil }

	tc := []struct {
		name   string
		setup  func(s *peakState)
		charge bool
		power  float64
	}{
		{"grid charging", func(s *peakState) { s.chargeSetpoint = 4000 }, true, 4000},
		{"grid charging wins over a peak", func(s *peakState) {
			s.chargeSetpoint = 4000
			s.enabled, s.shaving = true, true
		}, true, 4000},
		{"grid charging without a charge entity", func(s *peakState) {
			s.chargeSetpoint = 4000
			s.chargeSet = nil
		}, false, 0},
		{"below the reserve, a peak: Omnibattery's own", func(s *peakState) { s.enabled, s.shaving = true, true }, false, 0},
		{"meters lost", func(s *peakState) { s.enabled, s.shaving, s.metersLost = true, true, true }, false, 0},
		{"nothing", func(s *peakState) {}, false, 0},
	}

	for _, tc := range tc {
		t.Run(tc.name, func(t *testing.T) {
			site := &Site{log: util.NewLogger("test")}

			s := site.peak()
			s.chargeSet = chargeFn
			tc.setup(s)

			charge, power := site.omniWant()
			assert.Equal(t, tc.charge, charge)
			assert.Equal(t, tc.power, power)
		})
	}
}

// TestOmniProtectionOn verifies the table row "normal": peak shaving on turns
// Omnibattery's peak shaving on with the reserve as threshold and the allowed
// power of the window as limit, and nothing else
func TestOmniProtectionOn(t *testing.T) {
	sc, ha := protecting(t)

	assert.Equal(t, "on", ha.get(haProtSwitch))
	assert.Equal(t, "30", ha.get(haProtSoc))
	assert.Equal(t, "5000", ha.get(haProtLimit))
	assert.Equal(t, "off", ha.get(haSwitch), "no manual control")
	assert.Equal(t, "None", ha.get(haSelect))

	// stored, so a restart still knows evcc turned it on
	owned, err := settings.Bool(keys.PeakShavingProtOwned)
	require.NoError(t, err)
	assert.True(t, owned)

	// the next cycles write nothing while Home Assistant shows the values
	for range 3 {
		sc.cycle(25, 8000, 0)
	}
	assert.Empty(t, ha.take())

	// also above the reserve
	sc.cycle(50, 8000, 0)
	assert.Empty(t, ha.take())
	assert.Equal(t, "on", ha.get(haProtSwitch))
}

// TestOmniProtectionLimitFollowsWindow verifies that the limit is the allowed
// power of the window, rounded down to the step of the entity, written right away
// when it falls and only from minOmniLimitRise when it rises
func TestOmniProtectionLimitFollowsWindow(t *testing.T) {
	sc, ha := protecting(t)
	clk := mockClock(sc)

	// a draw above the limit lowers the allowed power
	for range 10 {
		clk.Add(30 * time.Second)
		sc.cycle(25, 8000, 0)
	}

	allowed := sc.site.peak().window.Allowed
	require.Less(t, allowed, 4900.0)

	want := math.Floor(allowed/100) * 100
	assert.Equal(t, strconv.FormatFloat(want, 'f', -1, 64), ha.get(haProtLimit))

	for _, c := range ha.take() {
		assert.Contains(t, c, "set_value "+haProtLimit)
	}

	// a limit in the entity above the allowed power is corrected right away, the
	// peak has to stay covered
	ha.set(haProtLimit, strconv.FormatFloat(want+100, 'f', -1, 64))
	sc.cycle(25, 8000, 0)
	assert.Equal(t, []string{"set_value " + haProtLimit + " " + strconv.FormatFloat(want, 'f', -1, 64)}, ha.take())

	// one below it only once the allowed power is minOmniLimitRise higher
	ha.set(haProtLimit, strconv.FormatFloat(want-400, 'f', -1, 64))
	sc.cycle(25, 8000, 0)
	assert.Empty(t, ha.take())

	ha.set(haProtLimit, strconv.FormatFloat(want-600, 'f', -1, 64))
	sc.cycle(25, 8000, 0)
	assert.Equal(t, []string{"set_value " + haProtLimit + " " + strconv.FormatFloat(want, 'f', -1, 64)}, ha.take())
}

// TestOmniProtectionLimitMinimum verifies that an allowed power below the
// entity's minimum writes the minimum: Omnibattery takes no lower limit
func TestOmniProtectionLimitMinimum(t *testing.T) {
	sc, ha := newOmni(t)
	conn := sc.site.peak().conn

	require.True(t, sc.site.omniWriteLimit(conn, haProtLimit, 120))
	assert.Equal(t, []string{"set_value " + haProtLimit + " 500"}, ha.take())
	assert.Equal(t, "500", ha.get(haProtLimit))
}

// TestOmniEffectiveReserve verifies that a reserve below Omnibattery's lowest
// threshold, e.g. from a battery profile, counts as that threshold everywhere:
// evcc's state of the reserve, the optimizer and Omnibattery agree
func TestOmniEffectiveReserve(t *testing.T) {
	sc, ha := newOmni(t)
	sc.site.peak().reserve = 15

	sc.cycle(18, 3000, 0)
	assert.True(t, sc.site.peakShavingActive(), "18 % is below the effective 20 %")
	assert.Equal(t, "20", ha.get(haProtSoc))

	on, _, reserve := sc.site.peakShavingConfigured()
	assert.True(t, on)
	assert.Equal(t, 20.0, reserve)

	// BYD keeps the reserve as set
	s := sc.site.peak()
	s.mu.Lock()
	s.batteryType = batteryTypeBYD
	assert.Equal(t, 15.0, s.effectiveReserve())
	s.mu.Unlock()
}

// TestOmniGridChargeWithoutManualWarns verifies that grid charging for the
// battery type Marstek without manual control switch writes nothing and says
// so in the log once
func TestOmniGridChargeWithoutManualWarns(t *testing.T) {
	sc, ha := newOmni(t)

	var buf strings.Builder
	sc.site.log = testLogger(&buf)

	s := sc.site.peak()
	s.manualEntity = ""

	for range 3 {
		sc.site.writeChargeValue(4000)
	}
	assert.Empty(t, ha.take())
	assert.Equal(t, 1, strings.Count(buf.String(), "needs the manual control switch"), buf.String())
}

// TestOmniProtectionThreshold verifies the soc threshold: the reserve, at least
// 20 %, rounded up to the step of the entity
func TestOmniProtectionThreshold(t *testing.T) {
	for _, tc := range []struct {
		reserve float64
		want    string
	}{
		{15, "20"},
		{11, "20"},
		{20, "20"},
		{45, "45"},
		{32.5, "33"},
	} {
		t.Run(fmt.Sprint(tc.reserve), func(t *testing.T) {
			sc, ha := newOmni(t)
			sc.site.peak().reserve = tc.reserve

			sc.cycle(50, 3000, 0)
			assert.Equal(t, tc.want, ha.get(haProtSoc))
		})
	}
}

// TestOmniProtectionOrder verifies that threshold and limit are written before
// the switch, each only after the one before landed
func TestOmniProtectionOrder(t *testing.T) {
	sc, ha := newOmni(t)

	// the threshold fails: nothing else
	ha.fail["number/set_value"] = http.StatusInternalServerError
	sc.cycle(25, 8000, 0)
	assert.Equal(t, []string{"set_value " + haProtSoc + " 30"}, ha.take())
	assert.False(t, sc.site.peak().protOwned)

	// the next cycle starts again
	delete(ha.fail, "number/set_value")
	sc.cycle(25, 8000, 0)
	assert.Equal(t, protOn("30", "5000"), ha.take())

	// the limit fails: no switch, an already running one stays
	ha.set(haProtSwitch, "off")
	ha.set(haProtLimit, "20000")
	ha.fail["number/set_value"] = http.StatusInternalServerError
	sc.cycle(25, 8000, 0)
	assert.Equal(t, []string{"set_value " + haProtLimit + " 5000"}, ha.take())
}

// TestOmniBelowReserveStaysAutomatic is the regression test for the failure of
// 10 October: below the reserve evcc never took Omnibattery into manual control
// with None, so the battery stood still and did not even charge from the pv
// surplus. It must not switch to manual, set a mode, or write a discharge power
// whatever the soc and the grid power are.
func TestOmniBelowReserveStaysAutomatic(t *testing.T) {
	sc, ha := newOmni(t)

	var all []string
	for _, soc := range []float64{29, 25, 10, 5} {
		for _, grid := range []float64{8000, 3000, 0, -2000} {
			sc.cycle(soc, grid, 0)
			all = append(all, ha.take()...)
			assert.False(t, sc.site.peakOwned())
		}
	}

	assert.NotEmpty(t, all)
	for _, c := range all {
		for _, entity := range []string{haSwitch, haSelect, haDischargeNum, haChargeNum} {
			assert.NotContains(t, c, entity, c)
		}
	}

	assert.Equal(t, "off", ha.get(haSwitch))
	assert.Equal(t, "None", ha.get(haSelect))
	assert.Equal(t, "0", ha.get(haDischargeNum))
	assert.Equal(t, "on", ha.get(haProtSwitch))
	assert.Equal(t, api.BatteryNormal, sc.mode())
	assert.Empty(t, sc.site.peak().omniShown)
}

// TestOmniOrder verifies that grid charging switches the manual control on first,
// then forces the mode, then writes the power, each only after the one before
// landed
func TestOmniOrder(t *testing.T) {
	sc, ha := newOmni(t)
	sc.site.lms().socChargeEnabled = true

	sc.cycle(20, 1000, 0)
	assert.Equal(t, []string{
		"turn_on " + haSwitch,
		"select_option " + haSelect + " Charge",
		"set_value " + haChargeNum + " 4000",
	}, withoutProt(ha.take()))
	assert.True(t, sc.site.peakOwned())
	assert.Equal(t, "on", ha.get(haSwitch))
	assert.Equal(t, "Charge", ha.get(haSelect))
	assert.Equal(t, "4000", ha.get(haChargeNum))

	// the switch fails: neither mode nor power
	ha.set(haSwitch, "off")
	ha.fail["switch/turn_on"] = http.StatusInternalServerError
	sc.cycle(20, 1000, 0)
	assert.Equal(t, []string{"turn_on " + haSwitch}, withoutProt(ha.take()))

	// the mode fails: no power
	delete(ha.fail, "switch/turn_on")
	ha.set(haSelect, "None")
	ha.set(haChargeNum, "0")
	ha.fail["select/select_option"] = http.StatusInternalServerError
	sc.cycle(20, 1000, 0)
	assert.Equal(t, []string{"turn_on " + haSwitch, "select_option " + haSelect + " Charge"}, withoutProt(ha.take()))

	// and the next cycle starts again
	delete(ha.fail, "select/select_option")
	sc.cycle(20, 1000, 0)
	assert.Equal(t, []string{"select_option " + haSelect + " Charge", "set_value " + haChargeNum + " 4000"}, withoutProt(ha.take()))
}

// TestOmniSkipsUnchanged verifies that nothing is written while Home Assistant
// shows what evcc wants
func TestOmniSkipsUnchanged(t *testing.T) {
	sc, ha := charging(t)

	for range 3 {
		sc.cycle(20, 1000, 0)
	}
	assert.Empty(t, ha.take())
}

// TestOmniGridCharge verifies the charge mode with the charge power, the end of
// it and that the discharge entity is not touched
func TestOmniGridCharge(t *testing.T) {
	sc, ha := charging(t)

	// charging goes on, the room below the peak limit changes
	assert.True(t, sc.cycle(20, 2000, 0))
	assert.Equal(t, []string{"set_value " + haChargeNum + " 3000"}, ha.take())

	// done: the charge power to zero first, then released; the peak shaving of
	// Omnibattery goes on
	assert.False(t, sc.cycle(90, 1000, 0))
	assert.Equal(t, []string{"set_value " + haChargeNum + " 0", "turn_off " + haSwitch}, ha.take())
	assert.Equal(t, "0", ha.get(haChargeNum))
	assert.False(t, sc.site.peakOwned())
	assert.True(t, sc.site.peak().protOwned)
	assert.Equal(t, "on", ha.get(haProtSwitch))

	// released once, and a charge power already at zero is not written again
	assert.False(t, sc.cycle(90, 1000, 0))
	assert.Empty(t, ha.take())
	assert.Equal(t, "0", ha.get(haDischargeNum))
}

// TestOmniGridChargeEndZeroFails verifies that the switch stays on while the
// charge power cannot be set to zero, and that the next cycle tries again
func TestOmniGridChargeEndZeroFails(t *testing.T) {
	sc, ha := charging(t)

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

// TestOmniCorrectsHandChange verifies that a switch, mode or value changed by hand
// while evcc controls is corrected in the next cycle
func TestOmniCorrectsHandChange(t *testing.T) {
	sc, ha := charging(t)

	ha.set(haSwitch, "off")
	sc.cycle(20, 1000, 0)
	assert.Equal(t, []string{"turn_on " + haSwitch}, ha.take())

	ha.set(haSelect, "None")
	sc.cycle(20, 1000, 0)
	assert.Equal(t, []string{"select_option " + haSelect + " Charge"}, ha.take())

	// Omnibattery's peak shaving
	ha.set(haProtSwitch, "off")
	ha.set(haProtSoc, "50")
	ha.set(haProtLimit, "9000")
	sc.cycle(20, 1000, 0)
	assert.Equal(t, protOn("30", "5000"), ha.take())
}

// TestOmniLeavesManualAloneWhenNotOwned verifies that a switch turned on by hand
// is left alone while evcc does not control. Once evcc forced mode and power into
// it while grid charging, evcc owns it and releases it afterwards, a forced charge
// would otherwise stay.
func TestOmniLeavesManualAloneWhenNotOwned(t *testing.T) {
	sc, ha := newOmni(t)
	s := sc.site.peak()

	ha.set(haSwitch, "on")

	// peak shaving off, then below and above the reserve
	s.enabled = false
	sc.cycle(25, 8000, 0)
	s.enabled = true
	sc.cycle(25, 8000, 0)
	sc.cycle(50, 8000, 0)
	assert.Equal(t, protOn("30", "5000"), ha.take())
	assert.Equal(t, "on", ha.get(haSwitch))
	assert.False(t, sc.site.peakOwned())

	// evcc controls without having to switch: mode and power, and owns it now
	sc.site.lms().socChargeEnabled = true
	sc.cycle(20, 1000, 0)
	assert.Equal(t, []string{
		"select_option " + haSelect + " Charge",
		"set_value " + haChargeNum + " 4000",
	}, ha.take())
	assert.True(t, sc.site.peakOwned())

	// so the forced charge does not stay once evcc gives up
	sc.cycle(90, 1000, 0)
	assert.Equal(t, []string{"set_value " + haChargeNum + " 0", "turn_off " + haSwitch}, ha.take())
	assert.False(t, sc.site.peakOwned())
}

// TestOmniProtectionLeavesSwitchWhenNotOwned verifies that Omnibattery's peak
// shaving switched on by hand is left alone while evcc's peak shaving is off. Once
// evcc drove it with its values, evcc owns it and turns it off afterwards, its
// last limit would otherwise stay in place.
func TestOmniProtectionLeavesSwitchWhenNotOwned(t *testing.T) {
	t.Run("peak shaving off", func(t *testing.T) {
		sc, ha := newOmni(t)
		ha.set(haProtSwitch, "on")
		sc.site.peak().enabled = false

		for range 3 {
			sc.cycle(25, 8000, 0)
		}
		require.NoError(t, sc.site.SetPeakShaving(false))
		assert.Empty(t, ha.take())
		assert.Equal(t, "on", ha.get(haProtSwitch))
	})

	t.Run("peak shaving on, then off", func(t *testing.T) {
		sc, ha := newOmni(t)
		ha.set(haProtSwitch, "on")

		sc.cycle(25, 8000, 0)
		assert.Equal(t, []string{"set_value " + haProtSoc + " 30", "set_value " + haProtLimit + " 5000"}, ha.take(), "no turn_on")
		assert.True(t, sc.site.peak().protOwned, "evcc drives it now")

		require.NoError(t, sc.site.SetPeakShaving(false))
		sc.cycle(25, 8000, 0)
		assert.Equal(t, []string{"turn_off " + haProtSwitch}, ha.take())
		assert.Equal(t, "off", ha.get(haProtSwitch))
	})
}

// TestOmniChangeRefusedWhenNotReleased verifies that the switches, the mode, the
// peak shaving entities and the battery type are kept while what evcc holds
// cannot be released: the battery would otherwise stay held with nothing left to
// release it
func TestOmniChangeRefusedWhenNotReleased(t *testing.T) {
	changes := []struct {
		name   string
		setup  func(t *testing.T) (*scenario, *fakeHA)
		fail   string // the service that fails
		change func(site *Site) error
		kept   func(site *Site) bool
		owned  func(site *Site) bool // what is still held
	}{
		{"peak shaving switch replaced", protecting, "switch/turn_off",
			func(site *Site) error { return site.SetPeakShavingProtSwitch("switch.other") },
			func(site *Site) bool { return site.GetPeakShavingProtSwitch() == haProtSwitch }, protOwned},
		{"peak shaving switch removed", protecting, "switch/turn_off",
			func(site *Site) error { return site.SetPeakShavingProtSwitch("") },
			func(site *Site) bool { return site.GetPeakShavingProtSwitch() == haProtSwitch }, protOwned},
		{"peak shaving limit removed", protecting, "switch/turn_off",
			func(site *Site) error { return site.SetPeakShavingProtLimit("") },
			func(site *Site) bool { return site.GetPeakShavingProtLimit() == haProtLimit }, protOwned},
		{"peak shaving soc removed", protecting, "switch/turn_off",
			func(site *Site) error { return site.SetPeakShavingProtSoc("") },
			func(site *Site) bool { return site.GetPeakShavingProtSoc() == haProtSoc }, protOwned},
		{"battery type changed", protecting, "switch/turn_off",
			func(site *Site) error { return site.SetPeakShavingBatteryType(batteryTypeBYD) },
			func(site *Site) bool { return site.GetPeakShavingBatteryType() == batteryTypeMarstek }, protOwned},
		{"manual switch replaced", charging, "switch/turn_off",
			func(site *Site) error { return site.SetPeakShavingManualEntity("switch.other") },
			func(site *Site) bool { return site.GetPeakShavingManualEntity() == haSwitch }, manualOwned},
		{"manual switch removed", charging, "switch/turn_off",
			func(site *Site) error { return site.SetPeakShavingManualEntity("") },
			func(site *Site) bool { return site.GetPeakShavingManualEntity() == haSwitch }, manualOwned},
		{"manual switch removed, zero fails", charging, "number/set_value",
			func(site *Site) error { return site.SetPeakShavingManualEntity("") },
			func(site *Site) bool { return site.GetPeakShavingManualEntity() == haSwitch }, manualOwned},
		{"mode removed", charging, "switch/turn_off",
			func(site *Site) error { return site.SetPeakShavingModeEntity("") },
			func(site *Site) bool { return site.GetPeakShavingModeEntity() == haSelect }, manualOwned},
		{"battery type changed while charging", charging, "switch/turn_off",
			func(site *Site) error { return site.SetPeakShavingBatteryType(batteryTypeBYD) },
			func(site *Site) bool { return site.GetPeakShavingBatteryType() == batteryTypeMarstek }, manualOwned},
	}

	for _, tc := range changes {
		t.Run(tc.name, func(t *testing.T) {
			sc, ha := tc.setup(t)

			ha.fail[tc.fail] = http.StatusInternalServerError
			require.ErrorIs(t, tc.change(sc.site), errHandBack)
			assert.True(t, tc.kept(sc.site))
			assert.True(t, tc.owned(sc.site))
			assert.True(t, sc.site.GetPeakShaving())

			// once Home Assistant takes it, the change goes through and releases
			delete(ha.fail, tc.fail)
			ha.take()
			require.NoError(t, tc.change(sc.site))
			assert.False(t, tc.owned(sc.site))
		})
	}
}

func protOwned(site *Site) bool {
	return site.peak().protOwned
}

func manualOwned(site *Site) bool {
	return site.peakOwned()
}

// TestOmniTypeNeedsEntitiesWhileOn verifies that running peak shaving cannot be
// moved to a battery type lacking what it needs: it would stay on writing nothing
func TestOmniTypeNeedsEntitiesWhileOn(t *testing.T) {
	t.Run("marstek without the peak shaving entities", func(t *testing.T) {
		sc, ha := newOmni(t)
		s := sc.site.peak()
		s.batteryType = batteryTypeBYD
		s.protLimit = ""

		require.ErrorIs(t, sc.site.SetPeakShavingBatteryType(batteryTypeMarstek), errProtMissing)
		assert.Equal(t, batteryTypeBYD, sc.site.GetPeakShavingBatteryType())
		assert.Empty(t, ha.take())

		// with peak shaving off the type can be chosen first
		require.NoError(t, sc.site.SetPeakShaving(false))
		require.NoError(t, sc.site.SetPeakShavingBatteryType(batteryTypeMarstek))
	})

	t.Run("byd without the discharge entity", func(t *testing.T) {
		sc, _ := newOmni(t)
		s := sc.site.peak()
		s.entity, s.set = "", nil

		require.ErrorIs(t, sc.site.SetPeakShavingBatteryType(batteryTypeBYD), errTargetMissing)
		assert.Equal(t, batteryTypeMarstek, sc.site.GetPeakShavingBatteryType())

		require.NoError(t, sc.site.SetPeakShaving(false))
		require.NoError(t, sc.site.SetPeakShavingBatteryType(batteryTypeBYD))
	})
}

// assertProtReleased checks that only Omnibattery's peak shaving was switched off
func assertProtReleased(t *testing.T, sc *scenario, ha *fakeHA) {
	t.Helper()

	assert.Equal(t, []string{"turn_off " + haProtSwitch}, ha.take())
	assert.False(t, sc.site.peak().protOwned)
	assert.Equal(t, "off", ha.get(haProtSwitch))
	assert.Equal(t, "30", ha.get(haProtSoc), "threshold and limit stay")
	assert.Equal(t, "5000", ha.get(haProtLimit))
}

// TestOmniRelease verifies the release of Omnibattery's peak shaving when peak
// shaving is switched off, the battery type is changed or one of its entities
// goes, and that of the manual control when a grid charge ends otherwise
func TestOmniRelease(t *testing.T) {
	t.Run("peak shaving off", func(t *testing.T) {
		sc, ha := protecting(t)
		require.NoError(t, sc.site.SetPeakShaving(false))
		assertProtReleased(t, sc, ha)

		// released once
		sc.cycle(25, 8000, 0)
		assert.Empty(t, ha.take())

		// and on again with it
		require.NoError(t, sc.site.SetPeakShaving(true))
		sc.cycle(25, 8000, 0)
		assert.Equal(t, []string{"turn_on " + haProtSwitch}, ha.take())
	})

	t.Run("peak shaving not configured any more", func(t *testing.T) {
		sc, ha := protecting(t)
		sc.site.batteryMeters = nil
		sc.cycle(25, 8000, 0)
		assertProtReleased(t, sc, ha)
	})

	t.Run("battery type changed", func(t *testing.T) {
		sc, ha := protecting(t)
		require.NoError(t, sc.site.SetPeakShavingBatteryType(batteryTypeBYD))
		assertProtReleased(t, sc, ha)

		// BYD writes the power alone
		sc.cycle(25, 9000, 0)
		assert.Equal(t, []string{"set_value " + haDischargeNum + " 4000"}, ha.take())
		assert.True(t, sc.site.peakOwned())

		// and back: the entity held at the setpoint gets the free value
		require.NoError(t, sc.site.SetPeakShavingBatteryType(batteryTypeMarstek))
		assert.Equal(t, []string{"set_value " + haDischargeNum + " " + fmt.Sprint(peak.DefaultFreeValue)}, ha.take())
		assert.False(t, sc.site.peakOwned())

		sc.cycle(25, 9000, 0)
		assert.Equal(t, []string{"turn_on " + haProtSwitch}, ha.take())
	})

	t.Run("battery type changed while charging", func(t *testing.T) {
		sc, ha := charging(t)
		require.NoError(t, sc.site.SetPeakShavingBatteryType(batteryTypeBYD))
		assert.Equal(t, []string{"set_value " + haChargeNum + " 0", "turn_off " + haSwitch, "turn_off " + haProtSwitch}, ha.take())
		assert.False(t, sc.site.peakOwned())
		assert.False(t, sc.site.peak().protOwned)
	})

	for _, tc := range []struct {
		name   string
		remove func(site *Site) error
	}{
		{"switch removed", func(site *Site) error { return site.SetPeakShavingProtSwitch("") }},
		{"limit removed", func(site *Site) error { return site.SetPeakShavingProtLimit("") }},
		{"soc removed", func(site *Site) error { return site.SetPeakShavingProtSoc("") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sc, ha := protecting(t)
			require.NoError(t, tc.remove(sc.site))
			assertProtReleased(t, sc, ha)
			assert.False(t, sc.site.GetPeakShaving(), "cannot run without it")
		})
	}

	t.Run("peak shaving entity swapped", func(t *testing.T) {
		sc, ha := protecting(t)
		require.NoError(t, sc.site.SetPeakShavingProtLimit("number.other_limit"))
		assert.Empty(t, ha.take(), "limit and threshold stay, the switch is not touched")
		assert.True(t, sc.site.GetPeakShaving())
	})

	t.Run("manual switch removed", func(t *testing.T) {
		sc, ha := charging(t)
		require.NoError(t, sc.site.SetPeakShavingManualEntity(""))
		assert.Equal(t, []string{"set_value " + haChargeNum + " 0", "turn_off " + haSwitch}, ha.take())
		assert.False(t, sc.site.peakOwned())
		assert.True(t, sc.site.GetPeakShaving(), "peak shaving does not need it")
		assert.True(t, sc.site.peak().protOwned)
	})

	t.Run("mode removed", func(t *testing.T) {
		sc, ha := charging(t)
		require.NoError(t, sc.site.SetPeakShavingModeEntity(""))
		assert.Equal(t, []string{"set_value " + haChargeNum + " 0", "turn_off " + haSwitch}, ha.take())
		assert.True(t, sc.site.GetPeakShaving())
	})

	t.Run("discharge entity removed", func(t *testing.T) {
		sc, ha := protecting(t)
		require.NoError(t, sc.site.SetPeakShavingEntity(""))
		assert.Empty(t, ha.take())
		assert.True(t, sc.site.GetPeakShaving(), "Marstek does not need it")
		assert.True(t, sc.site.peak().protOwned)
	})
}

// TestOmniMetersLost verifies that without meter values Omnibattery goes on with
// the peak limit (the one following the peak included) instead of the allowed
// power of the window, and that grid charging gives way as before
func TestOmniMetersLost(t *testing.T) {
	sc, ha := newOmni(t)
	sc.site.lms().socChargeEnabled = true
	clk := mockClock(sc)

	// charging, and a window that allows less than the limit
	sc.cycle(20, 1000, 0)
	for range 3 {
		clk.Add(30 * time.Second)
		sc.cycle(20, 1000, 0)
	}

	// a limit far below the allowed power is raised to it
	ha.set(haProtLimit, "4000")
	clk.Add(30 * time.Second)
	sc.cycle(20, 1000, 0)
	require.Greater(t, sc.site.peak().window.Allowed, 5100.0)
	require.True(t, sc.site.peakOwned())
	require.NotEqual(t, "5000", ha.get(haProtLimit))
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
	assert.Equal(t, []string{
		"set_value " + haChargeNum + " 0",
		"turn_off " + haSwitch,
		"set_value " + haProtLimit + " 5000",
	}, ha.take())
	assert.Equal(t, "on", ha.get(haProtSwitch), "Omnibattery goes on")
	assert.False(t, sc.site.peakOwned())

	failed()
	assert.Empty(t, ha.take())

	// the meters are back
	clk.Add(30 * time.Second)
	sc.cycle(20, 1000, 0)
	calls := ha.take()
	require.NotEmpty(t, calls)
	assert.Equal(t, "turn_on "+haSwitch, calls[0])

	// the window's limit again; a rise is written once it is minOmniLimitRise above
	want := math.Floor(sc.site.peak().window.Allowed/100) * 100
	if want < 5000+minOmniLimitRise {
		want = 5000
	}
	assert.Equal(t, strconv.FormatFloat(want, 'f', -1, 64), ha.get(haProtLimit))
}

// TestOmniReleaseAfterRestart verifies that a restart in the middle of a control
// still releases: the type, the entities and what evcc turned on are stored
func TestOmniReleaseAfterRestart(t *testing.T) {
	sc, ha := newOmni(t)

	sc.cycle(25, 8000, 0)
	ha.take()
	ha.set(haSwitch, "on")

	// what the api stored; a new site reads it back
	settings.SetString(keys.PeakShavingBatteryType, batteryTypeMarstek)
	settings.SetString(keys.PeakShavingManualEntity, haSwitch)
	settings.SetString(keys.PeakShavingModeEntity, haSelect)
	settings.SetString(keys.PeakShavingEntity, haDischargeNum)
	settings.SetString(keys.PeakShavingChargeEntity, haChargeNum)
	settings.SetString(keys.PeakShavingProtSwitch, haProtSwitch)
	settings.SetString(keys.PeakShavingProtLimit, haProtLimit)
	settings.SetString(keys.PeakShavingProtSoc, haProtSoc)
	settings.SetBool(keys.PeakShaving, false)
	settings.SetBool(keys.PeakShavingOwned, true)
	settings.SetBool(keys.PeakShavingProtOwned, true)

	again := newScenario(t)
	again.site.lms().socChargeEnabled = false
	again.site.custom.peak.conn = sc.site.peak().conn
	again.site.restorePeakSettings()

	assert.True(t, again.site.omniEnabled())
	assert.True(t, again.site.peakConfigured())
	assert.True(t, again.site.peakOwned())
	assert.True(t, again.site.peak().protOwned)
	assert.Equal(t, haProtSwitch, again.site.GetPeakShavingProtSwitch())
	assert.Equal(t, haProtLimit, again.site.GetPeakShavingProtLimit())
	assert.Equal(t, haProtSoc, again.site.GetPeakShavingProtSoc())

	for range 3 {
		again.cycle(25, 8000, 0)
	}
	assert.Equal(t, []string{"turn_off " + haSwitch, "turn_off " + haProtSwitch}, ha.take())
	assert.False(t, again.site.peakOwned())
	assert.False(t, again.site.peak().protOwned)
}

// TestOmniNeverTouchesDischarge verifies that neither the discharge power nor the
// free value ever reaches a Marstek
func TestOmniNeverTouchesDischarge(t *testing.T) {
	sc, ha := newOmni(t)

	var all []string
	run := func(f func()) {
		f()
		all = append(all, ha.take()...)
	}

	run(func() { sc.cycle(50, 8000, 0) }) // above the reserve
	run(func() { sc.cycle(25, 8000, 0) })
	run(func() { sc.cycle(50, 8000, 0) })
	run(func() { sc.cycle(25, 8000, 0) })
	run(func() { require.NoError(t, sc.site.SetPeakShaving(false)) })
	run(func() { sc.cycle(25, 8000, 0) })
	run(func() { require.NoError(t, sc.site.SetPeakShavingEntity("")) })

	assert.NotEmpty(t, all)
	for _, call := range all {
		assert.NotContains(t, call, haDischargeNum, call)
	}
	assert.Equal(t, "0", ha.get(haDischargeNum))
}

// TestOmniEntityChecks verifies that the entities are checked when set: the
// domain, and for the select the options as written
func TestOmniEntityChecks(t *testing.T) {
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

	// the switches
	require.ErrorContains(t, sc.site.SetPeakShavingManualEntity("sensor.manual"), "switch or input_boolean")
	require.NoError(t, sc.site.SetPeakShavingManualEntity("input_boolean.manual"))
	require.ErrorContains(t, sc.site.SetPeakShavingProtSwitch("sensor.prot"), "switch or input_boolean")
	require.NoError(t, sc.site.SetPeakShavingProtSwitch("input_boolean.prot"))
	assert.Equal(t, "input_boolean.prot", sc.site.GetPeakShavingProtSwitch())

	// the numbers
	require.ErrorContains(t, sc.site.SetPeakShavingProtLimit("sensor.limit"), "number or input_number")
	require.ErrorContains(t, sc.site.SetPeakShavingProtSoc("switch.soc"), "number or input_number")
	require.NoError(t, sc.site.SetPeakShavingProtLimit("input_number.limit"))
	require.NoError(t, sc.site.SetPeakShavingProtSoc("input_number.soc"))
	assert.Equal(t, "input_number.limit", sc.site.GetPeakShavingProtLimit())
	assert.Equal(t, "input_number.soc", sc.site.GetPeakShavingProtSoc())
	assert.True(t, sc.site.peakConfigured(), "the swapped setter is there")

	// the battery type
	require.Error(t, sc.site.SetPeakShavingBatteryType("zendure"))
}

// TestOmniPeakShavingNeedsEntities verifies that peak shaving cannot be turned on
// for the battery type Marstek without the three entities of Omnibattery's peak
// shaving, and that the discharge entity is not needed
func TestOmniPeakShavingNeedsEntities(t *testing.T) {
	sc, _ := newOmni(t)
	s := sc.site.peak()
	s.enabled = false
	s.entity, s.set = "", nil
	s.manualEntity, s.modeEntity = "", ""

	for _, missing := range []func(){
		func() { s.protSwitch = "" },
		func() { s.protLimit = "" },
		func() { s.protSoc, s.protSocSet = "", nil },
	} {
		missing()
		require.ErrorIs(t, sc.site.SetPeakShaving(true), errProtMissing)
		s.protSwitch, s.protLimit, s.protSoc = haProtSwitch, haProtLimit, haProtSoc
		require.NoError(t, sc.site.rebuildProtSetter())
	}

	require.NoError(t, sc.site.SetPeakShaving(true), "neither discharge, switch nor mode")

	// BYD needs the discharge entity
	require.NoError(t, sc.site.SetPeakShaving(false))
	s.batteryType = batteryTypeBYD
	require.ErrorIs(t, sc.site.SetPeakShaving(true), errTargetMissing)
}

// TestOmniInertForBYD verifies that the BYD type writes the discharge power
// alone, with the Marstek entities set or not
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

			assert.Equal(t, "off", ha.get(haProtSwitch))
			assert.Equal(t, "20000", ha.get(haProtLimit))
			assert.False(t, sc.site.peak().protOwned)
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

// TestOmniNoHysteresis verifies that the reserve has no soc band for the battery
// type Marstek: evcc's state of the reserve changes exactly at the reserve, as
// Omnibattery's threshold does. BYD keeps the 2 %.
func TestOmniNoHysteresis(t *testing.T) {
	t.Run("marstek", func(t *testing.T) {
		sc, _ := newOmni(t)
		sc.site.peak().reserve = 20

		sc.cycle(21, 3000, 0)
		assert.False(t, sc.site.peakShavingActive())

		sc.cycle(19, 3000, 0)
		assert.True(t, sc.site.peakShavingActive())

		sc.cycle(20.5, 3000, 0)
		assert.False(t, sc.site.peakShavingActive(), "no band above the reserve")

		// the threshold is the reserve without a margin
		assert.Equal(t, 0.0, sc.site.peakHysteresis())
	})

	t.Run("byd", func(t *testing.T) {
		sc, _ := newOmni(t)
		s := sc.site.peak()
		s.batteryType = batteryTypeBYD
		s.reserve = 20

		sc.cycle(21, 3000, 0)
		assert.False(t, sc.site.peakShavingActive())

		sc.cycle(19, 3000, 0)
		assert.True(t, sc.site.peakShavingActive())

		sc.cycle(21, 3000, 0)
		assert.True(t, sc.site.peakShavingActive(), "in the band")

		sc.cycle(22, 3000, 0)
		assert.False(t, sc.site.peakShavingActive())
		assert.Equal(t, peak.DefaultHysteresis, sc.site.peakHysteresis())
	})
}
