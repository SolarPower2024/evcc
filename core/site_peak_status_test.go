package core

import (
	"net/http"
	"testing"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/lm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	haBydSwitch = "switch.byd_peak_shaving"
	haBydStatus = "sensor.byd_status"
)

// newBydStatus returns a scenario of the battery type BYD: the switch is named as
// the integration names it, so the status sensor is found
func newBydStatus(t *testing.T) (*scenario, *fakeHA) {
	t.Helper()

	sc := newScenario(t)
	sc.site.lms().socChargeEnabled = false

	sc.ha.states[haBydSwitch] = &haEntity{"off", map[string]any{}}
	sc.ha.states[haBydStatus] = &haEntity{"free", map[string]any{}}
	sc.site.peak().protSwitch = haBydSwitch

	return sc, sc.ha
}

// statusCases are the rows of the table of the concept: how to get into each
// state from a site that runs peak shaving
var statusCases = []struct {
	name  string
	state string
	set   func(sc *scenario, ha *fakeHA)
}{
	{"off", peakStateOff, func(sc *scenario, ha *fakeHA) {
		sc.site.peak().enabled = false
		sc.cycle(25, 8000, 0)
	}},
	{"storage", peakStateStorage, func(sc *scenario, ha *fakeHA) {
		ha.fail["number/set_value"] = http.StatusInternalServerError
		sc.cycle(25, 8000, 0)
	}},
	{"meter", peakStateMeter, func(sc *scenario, ha *fakeHA) {
		sc.cycle(25, 8000, 0)
		sc.site.peak().metersLost = true
	}},
	{"gridCharge", peakStateGridCharge, func(sc *scenario, ha *fakeHA) {
		sc.cycle(25, 3000, 0)
		sc.site.batteryMode = api.BatteryCharge
	}},
	{"holdShaving", peakStateHoldShaving, func(sc *scenario, ha *fakeHA) {
		sc.cycle(25, 8000, 0)
		sc.site.batteryMode = api.BatteryHold
	}},
	{"gridDischarge", peakStateGridDischarge, func(sc *scenario, ha *fakeHA) {
		sc.cycle(50, 3000, 0)
		sc.site.batteryMode = api.BatteryDischarge
	}},
	{"hold", peakStateHold, func(sc *scenario, ha *fakeHA) {
		sc.cycle(50, 3000, 0)
		sc.site.batteryMode = api.BatteryHold
	}},
	{"shaving", peakStateShaving, func(sc *scenario, ha *fakeHA) {
		sc.cycle(25, 8000, 0)
	}},
	{"saving", peakStateSaving, func(sc *scenario, ha *fakeHA) {
		sc.cycle(25, 3000, 0)
	}},
	{"free", peakStateFree, func(sc *scenario, ha *fakeHA) {
		sc.cycle(50, 3000, 0)
	}},
}

// TestPeakStatusStates: every state of the table, for both battery types
func TestPeakStatusStates(t *testing.T) {
	for _, typ := range []string{batteryTypeMarstek, batteryTypeBYD} {
		for _, tc := range statusCases {
			t.Run(typ+"/"+tc.name, func(t *testing.T) {
				var (
					sc *scenario
					ha *fakeHA
				)

				if typ == batteryTypeMarstek {
					sc, ha = newOmni(t)
				} else {
					sc, ha = newBydStatus(t)
				}

				tc.set(sc, ha)

				st := sc.site.peakStatusNow()
				assert.Equal(t, tc.state, st.State)
				assert.Equal(t, typ, st.Type)
				assert.Equal(t, 5000.0, st.Limit)
				assert.Equal(t, 30.0, st.Reserve)
				if tc.state != peakStateMeter {
					assert.NotNil(t, st.Window, "the window of the running quarter hour")
				}

				// the hysteresis of the integration only
				if typ == batteryTypeBYD {
					if assert.NotNil(t, st.FreeAt) {
						assert.Equal(t, 32.0, *st.FreeAt)
					}
				} else {
					assert.Nil(t, st.FreeAt, "Omnibattery switches exactly at the reserve")
				}
			})
		}
	}
}

// TestPeakStatusValues: the numbers of a peak below the reserve
func TestPeakStatusValues(t *testing.T) {
	sc, _ := newBydStatus(t)

	sc.cycle(25, 8000, 0)

	st := sc.site.peakStatusNow()
	assert.Equal(t, peakStateShaving, st.State)
	assert.Equal(t, 25.0, st.Soc)
	assert.Equal(t, 8000.0, st.Grid)
	assert.Equal(t, 8000.0, st.House)
	assert.Equal(t, 3000.0, st.Setpoint, "the battery gives what exceeds the limit")
	assert.True(t, st.Limited)
	assert.True(t, st.StorageOk)
	assert.Equal(t, "normal", st.BatteryMode)

	// above the reserve the battery is not limited
	sc.cycle(50, 8000, 0)
	assert.False(t, sc.site.peakStatusNow().Limited)
}

// TestPeakStatusOrder: the checks run in the order of the table
func TestPeakStatusOrder(t *testing.T) {
	sc, ha := newBydStatus(t)
	s := sc.site.peak()

	// a meter outage comes before the reserve
	sc.cycle(25, 8000, 0)
	s.metersLost = true
	assert.Equal(t, peakStateMeter, sc.site.peakStatusNow().State)

	// the storage comes before the meter outage
	ha.set(haBydStatus, bydStatusNoEvcc)
	sc.site.publishPeakStatus()
	assert.Equal(t, peakStateStorage, sc.site.peakStatusNow().State)

	// off comes before everything
	s.enabled = false
	assert.Equal(t, peakStateOff, sc.site.peakStatusNow().State)

	// grid charging comes before the hold, the hold with a peak before the reserve
	s.enabled, s.metersLost = true, false
	ha.set(haBydStatus, "shaving")
	sc.site.publishPeakStatus()
	sc.site.batteryMode = api.BatteryCharge
	assert.Equal(t, peakStateGridCharge, sc.site.peakStatusNow().State)
	sc.site.batteryMode = api.BatteryHold
	assert.Equal(t, peakStateHoldShaving, sc.site.peakStatusNow().State)
}

// TestPeakStatusStorage: the storage is not reachable when a write fails, the
// battery has no values or the integration says evcc is gone
func TestPeakStatusStorage(t *testing.T) {
	t.Run("write", func(t *testing.T) {
		sc, ha := newOmni(t)
		sc.cycle(25, 8000, 0)
		require.True(t, sc.site.peakStatusNow().StorageOk)

		ha.fail["number/set_value"] = http.StatusInternalServerError
		sc.site.peak().reserve = 40 // a new threshold is written
		sc.cycle(25, 8000, 0)
		assert.False(t, sc.site.peakStatusNow().StorageOk)
		assert.Equal(t, peakStateStorage, sc.site.peakStatusNow().State)

		delete(ha.fail, "number/set_value")
		sc.cycle(25, 8000, 0)
		assert.True(t, sc.site.peakStatusNow().StorageOk, "connected again")
	})

	t.Run("battery values", func(t *testing.T) {
		sc, _ := newOmni(t)
		sc.cycle(25, 8000, 0)
		require.True(t, sc.site.peakStatusNow().StorageOk)

		sc.site.battery.Devices = nil // no battery returned a soc
		assert.False(t, sc.site.peakStatusNow().StorageOk)

		setBatterySoc(sc.site, 25)
		sc.site.setPeakMeterRead("battery", 0, assert.AnError)
		assert.False(t, sc.site.peakStatusNow().StorageOk, "the battery power cannot be read")
	})

	t.Run("integration lost evcc", func(t *testing.T) {
		sc, ha := newBydStatus(t)
		ha.set(haBydStatus, bydStatusNoEvcc)
		sc.cycle(25, 8000, 0)
		assert.False(t, sc.site.peakStatusNow().StorageOk)
	})
}

// TestPeakStatusBydSensor: the status sensor is read when there is one; without
// it only the extra hints are missing
func TestPeakStatusBydSensor(t *testing.T) {
	assert.Equal(t, "sensor.byd_status", bydStatusEntity("switch.byd_peak_shaving"))
	assert.Empty(t, bydStatusEntity("switch.prot"), "named otherwise: no sensor derived")
	assert.Empty(t, bydStatusEntity("input_boolean.byd_peak_shaving"))
	assert.Empty(t, bydStatusEntity("switch._peak_shaving"))

	t.Run("missing", func(t *testing.T) {
		sc, ha := newBydStatus(t)
		delete(ha.states, haBydStatus)

		sc.cycle(25, 8000, 0)

		st := sc.site.peakStatusNow()
		assert.True(t, st.StorageOk, "no error without the sensor")
		assert.Empty(t, st.StorageHint)
		assert.Equal(t, peakStateShaving, st.State)
	})

	t.Run("unavailable", func(t *testing.T) {
		sc, ha := newBydStatus(t)
		ha.set(haBydStatus, "unavailable")

		sc.cycle(25, 8000, 0)
		assert.True(t, sc.site.peakStatusNow().StorageOk)
	})

	t.Run("not derivable", func(t *testing.T) {
		sc := newScenario(t) // the switch is named switch.prot

		sc.cycle(25, 8000, 0)
		assert.True(t, sc.site.peakStatusNow().StorageOk)
	})

	t.Run("no soc", func(t *testing.T) {
		sc, ha := newBydStatus(t)
		ha.set(haBydStatus, bydStatusNoSoc)
		sc.cycle(25, 8000, 0)

		st := sc.site.peakStatusNow()
		assert.Equal(t, peakHintNoSoc, st.StorageHint)
		assert.True(t, st.StorageOk)
	})

	t.Run("mode not taken", func(t *testing.T) {
		sc, ha := newBydStatus(t)
		clk := mockClock(sc)

		sc.cycle(50, 3000, 0)
		sc.site.batteryMode = api.BatteryHold

		// the integration still says free
		sc.site.publishPeakStatus()
		assert.Empty(t, sc.site.peakStatusNow().StorageHint, "not yet")

		clk.Add(2 * time.Minute)
		sc.site.publishPeakStatus()
		assert.Equal(t, peakHintModeStuck, sc.site.peakStatusNow().StorageHint)

		// it follows
		ha.set(haBydStatus, bydStatusHold)
		sc.site.publishPeakStatus()
		assert.Empty(t, sc.site.peakStatusNow().StorageHint)
	})
}

// eventTypes counts the logged events by type
func eventTypes(site *Site) map[string]int {
	res := make(map[string]int)
	for _, e := range site.lmm().AllEvents(time.Time{}, nil) {
		res[e.Type]++
	}

	return res
}

// TestPeakStatusEvents: a change of the state is logged once, the start only sets
// the reference
func TestPeakStatusEvents(t *testing.T) {
	sc, ha := newBydStatus(t)
	site := sc.site
	s := site.peak()

	// the first cycles log nothing
	sc.cycle(50, 3000, 0)
	sc.cycle(50, 3000, 0)
	assert.Empty(t, eventTypes(site))

	// below the reserve: once
	sc.cycle(25, 3000, 0)
	sc.cycle(25, 3000, 0)
	assert.Equal(t, map[string]int{lm.EventReserveBelow: 1}, eventTypes(site))

	// a peak of 8 kW with the battery giving 3 kW and later 4 kW
	sc.cycle(25, 8000, 0)
	sc.cycle(25, 5000, 3000)
	sc.cycle(25, 4000, 4000)
	sc.cycle(25, 3000, 0)

	types := eventTypes(site)
	assert.Equal(t, 1, types[lm.EventPeak])
	assert.Equal(t, 1, types[lm.EventPeakEnd], "once at the end")

	var end lm.Event
	for _, e := range site.lmm().AllEvents(time.Time{}, nil) {
		if e.Type == lm.EventPeakEnd {
			end = e
		}
	}
	assert.Equal(t, 4000.0, end.A, "the most the battery gave")

	// above the reserve again
	sc.cycle(33, 3000, 0)
	assert.Equal(t, 1, eventTypes(site)[lm.EventReserveAbove])

	// what stays for a minute is logged, once
	clk := mockClock(sc)
	settle := func() {
		site.publishPeakStatus()
		clk.Add(61 * time.Second)
		site.publishPeakStatus()
		site.publishPeakStatus()
	}

	// the battery mode
	site.batteryMode = api.BatteryHold
	settle()
	assert.Equal(t, 1, eventTypes(site)[lm.EventBatteryMode])

	// a meter outage and its end
	s.metersLost = true
	settle()
	s.metersLost = false
	settle()
	assert.Equal(t, 2, eventTypes(site)[lm.EventMeter])

	// the storage
	ha.fail["number/set_value"] = http.StatusInternalServerError
	s.reserve = 35
	sc.cycle(33, 3000, 0)
	settle()
	delete(ha.fail, "number/set_value")
	sc.cycle(33, 3000, 0)
	settle()
	assert.Equal(t, 2, eventTypes(site)[lm.EventStorage], "lost and connected again")

	// the switch: switching off is no rise above the reserve
	before := eventTypes(site)[lm.EventReserveAbove]
	s.enabled = false
	site.publishPeakStatus()
	s.enabled = true
	site.publishPeakStatus()
	types = eventTypes(site)
	assert.Equal(t, 2, types[lm.EventPeakSwitch])
	assert.Equal(t, before, types[lm.EventReserveAbove])
}

// TestPeakStatusEventsDebounced: a change that returns within a minute is no pair
func TestPeakStatusEventsDebounced(t *testing.T) {
	sc, _ := newBydStatus(t)
	site := sc.site
	s := site.peak()
	clk := mockClock(sc)

	sc.cycle(50, 3000, 0)
	site.batteryMode = api.BatteryNormal
	site.publishPeakStatus()

	// the mode flips and flips back after 20 s, the meter after 30 s
	site.batteryMode = api.BatteryHold
	s.metersLost = true
	site.publishPeakStatus()
	clk.Add(20 * time.Second)
	site.batteryMode = api.BatteryNormal
	site.publishPeakStatus()
	clk.Add(10 * time.Second)
	s.metersLost = false
	site.publishPeakStatus()
	clk.Add(2 * time.Minute)
	site.publishPeakStatus()

	assert.Empty(t, eventTypes(site), "no pair")

	// stays: logged with the time of the change
	changed := clk.Now()
	site.batteryMode = api.BatteryHold
	site.publishPeakStatus()
	clk.Add(30 * time.Second)
	site.publishPeakStatus()
	assert.Empty(t, eventTypes(site), "not yet")

	clk.Add(40 * time.Second)
	site.publishPeakStatus()

	ev := site.lmm().AllEvents(time.Time{}, nil)
	if assert.Len(t, ev, 1) {
		assert.Equal(t, lm.EventBatteryMode, ev[0].Type)
		assert.Equal(t, changed, ev[0].At)
	}
}

// TestPeakStatusStorageBack: a failed write is over when the entity shows what was
// to be written, though nothing is written then
func TestPeakStatusStorageBack(t *testing.T) {
	sc, ha := newBydStatus(t)
	site := sc.site
	clk := mockClock(sc)

	sc.cycle(25, 3000, 0)
	require.True(t, site.peakStatusNow().StorageOk)

	// switching on fails (a timeout in Home Assistant), the switch stays off
	ha.set(haBydSwitch, "off")
	delete(site.peak().writeErr, "byd peak shaving switch")
	ha.fail["switch/turn_on"] = http.StatusInternalServerError
	site.peak().protOwned = false
	sc.cycle(25, 3000, 0)
	require.False(t, site.peakStatusNow().StorageOk)

	clk.Add(61 * time.Second)
	site.publishPeakStatus()
	require.Equal(t, 1, eventTypes(site)[lm.EventStorage])

	// the next cycle reads it on (the call got through after all): nothing to write
	delete(ha.fail, "switch/turn_on")
	ha.set(haBydSwitch, "on")
	ha.take()
	sc.cycle(25, 3000, 0)
	assert.NotContains(t, ha.take(), "turn_on "+haBydSwitch, "nothing written")
	assert.True(t, site.peakStatusNow().StorageOk)

	clk.Add(61 * time.Second)
	site.publishPeakStatus()
	assert.Equal(t, 2, eventTypes(site)[lm.EventStorage], "storage back")
}

// TestPeakStatusWindowCurrent: the quarter hour shows only while it is the running
// one, as the control takes it
func TestPeakStatusWindowCurrent(t *testing.T) {
	sc, _ := newBydStatus(t)
	clk := mockClock(sc)

	sc.cycle(25, 3000, 0)
	assert.NotNil(t, sc.site.peakStatusNow().Window)

	// the counter fails: no cycle renews the window, the next quarter hour begins
	clk.Add(16 * time.Minute)
	assert.Nil(t, sc.site.peakStatusNow().Window, "the old window is no longer shown")

	sc.cycle(25, 3000, 0)
	assert.NotNil(t, sc.site.peakStatusNow().Window)
}

// TestPeakStatusApiPath: switching from the api asks Home Assistant nothing and a
// status derived there does not stay under an older one of the cycle
func TestPeakStatusApiPath(t *testing.T) {
	sc, ha := newBydStatus(t)
	site := sc.site

	sc.cycle(25, 3000, 0)
	ha.take()

	// the sensor says no soc; the cycle read it
	ha.set(haBydStatus, bydStatusNoSoc)
	site.publishPeakStatus()
	assert.Equal(t, peakHintNoSoc, site.peakStatusNow().StorageHint)

	// the api path: the sensor changed meanwhile, it is not read, the last value stays
	ha.set(haBydStatus, bydStatusNoEvcc)
	site.publishPeakStatusCached()
	assert.Equal(t, peakStateSaving, site.peakStatusNow().State, "no_evcc not read")
	assert.True(t, site.peakStatusNow().StorageOk)

	// the last publication is the newest state
	site.peak().enabled = false
	site.publishPeakStatusCached()
	assert.Equal(t, peakStateOff, site.peak().status.last.State)
}

// TestPeakStatusHoldReason: only a hold the peak path makes limits the discharge;
// grid discharge is no free battery
func TestPeakStatusHoldReason(t *testing.T) {
	sc, _ := newBydStatus(t)
	site := sc.site

	// above the reserve, held for another reason (vehicle, api)
	sc.cycle(50, 3000, 0)
	site.batteryMode = api.BatteryHold
	st := site.peakStatusNow()
	assert.Equal(t, peakStateHold, st.State)
	assert.True(t, st.Held)
	assert.False(t, st.Limited, "no discharge limit from a hold of another kind")

	// below the reserve the reserve limits
	sc.cycle(25, 3000, 0)
	site.batteryMode = api.BatteryHold
	assert.True(t, site.peakStatusNow().Limited)

	site.batteryMode = api.BatteryDischarge
	st = site.peakStatusNow()
	assert.Equal(t, peakStateGridDischarge, st.State)
	assert.False(t, st.Held)
}

// TestPeakStatusBydSensorOnlyWhileOn: with peak shaving off the integration is asked nothing
func TestPeakStatusBydSensorOnlyWhileOn(t *testing.T) {
	sc, ha := newBydStatus(t)
	site := sc.site

	ha.set(haBydStatus, bydStatusNoSoc)
	site.peak().enabled = false
	ha.take()
	assert.Empty(t, site.readBydStatus())

	site.peak().enabled = true
	assert.Equal(t, bydStatusNoSoc, site.readBydStatus())
}

// TestPeakStatusNotSetUp: without the entities of the controller there is nothing
// to show and nothing to log
func TestPeakStatusNotSetUp(t *testing.T) {
	site, _ := peakWindowSite(t, 0)

	assert.Equal(t, peakStatus{}, site.peakStatusNow())

	site.publishPeakStatus()
	assert.Empty(t, eventTypes(site))
}
