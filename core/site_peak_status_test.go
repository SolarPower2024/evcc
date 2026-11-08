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
				assert.NotNil(t, st.Window, "the window of the running quarter hour")

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
	for _, e := range site.lmm().AllEvents(nil) {
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
	for _, e := range site.lmm().AllEvents(nil) {
		if e.Type == lm.EventPeakEnd {
			end = e
		}
	}
	assert.Equal(t, 4000.0, end.A, "the most the battery gave")

	// above the reserve again
	sc.cycle(33, 3000, 0)
	assert.Equal(t, 1, eventTypes(site)[lm.EventReserveAbove])

	// the battery mode
	site.batteryMode = api.BatteryHold
	site.publishPeakStatus()
	site.publishPeakStatus()
	assert.Equal(t, 1, eventTypes(site)[lm.EventBatteryMode])

	// a meter outage and its end
	s.metersLost = true
	site.publishPeakStatus()
	s.metersLost = false
	site.publishPeakStatus()
	assert.Equal(t, 2, eventTypes(site)[lm.EventMeter])

	// the storage
	ha.fail["number/set_value"] = http.StatusInternalServerError
	s.reserve = 35
	sc.cycle(33, 3000, 0)
	delete(ha.fail, "number/set_value")
	sc.cycle(33, 3000, 0)
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

// TestPeakStatusNotSetUp: without the entities of the controller there is nothing
// to show and nothing to log
func TestPeakStatusNotSetUp(t *testing.T) {
	site, _ := peakWindowSite(t, 0)

	assert.Equal(t, peakStatus{}, site.peakStatusNow())

	site.publishPeakStatus()
	assert.Empty(t, eventTypes(site))
}
