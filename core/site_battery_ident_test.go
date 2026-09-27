package core

import (
	"math"
	"testing"
	"time"

	"github.com/evcc-io/evcc/core/metrics"
	"github.com/evcc-io/evcc/util"
	optimizer "github.com/evcc-io/optimizer/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// simBattery produces slots of a battery with the given capacity and one way
// efficiency: each day charging from 20 to 90 % and discharging back, soc
// rounded to whole percent like most batteries report it
type simBattery struct {
	capacity, eta float64 // kWh, one way
	soc           float64
	at            time.Time
	slots         []metrics.MeterSlot
}

func (b *simBattery) slot(charge, discharge float64) {
	soc := math.Round(b.soc)
	b.slots = append(b.slots, metrics.MeterSlot{Start: b.at, Energy: charge, ReturnEnergy: discharge, Soc: &soc})
	b.soc += (charge*b.eta - discharge/b.eta) / b.capacity * 100
	b.at = b.at.Add(15 * time.Minute)
}

func (b *simBattery) days(n int) {
	for range n {
		for b.soc < 90 {
			b.slot(0.5, 0)
		}
		for range 8 {
			b.slot(0, 0)
		}
		for b.soc > 20 {
			b.slot(0, 0.4)
		}
		for range 8 {
			b.slot(0, 0)
		}
	}
}

func newSimBattery() *simBattery {
	return &simBattery{capacity: 10, eta: 0.95, soc: 20, at: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}
}

func TestIdentifyBattery(t *testing.T) {
	b := newSimBattery()
	b.days(5)

	res := identifyBattery(b.slots, 11)
	assert.Equal(t, 5, res.Charges)
	assert.Equal(t, 5, res.Discharges)
	assert.True(t, res.Valid)
	assert.InDelta(t, 10, res.Capacity, 0.3, "capacity")
	assert.InDelta(t, 0.95*0.95, res.Efficiency, 0.03, "round trip")
}

func TestIdentifyBatteryNeedsRuns(t *testing.T) {
	b := newSimBattery()
	b.days(2)

	res := identifyBattery(b.slots, 10)
	assert.False(t, res.Valid, "two runs each are not enough")
	assert.Zero(t, res.Capacity)
}

func TestIdentifyBatteryImplausible(t *testing.T) {
	b := newSimBattery()
	b.days(4)

	res := identifyBattery(b.slots, 5) // measured twice the datasheet value
	assert.False(t, res.Valid)
	assert.NotZero(t, res.Capacity, "still shown")
}

func TestBatteryRunsSkipsDisturbed(t *testing.T) {
	// a recalibration jump in the middle of the charging run
	b := newSimBattery()
	b.slot(0.5, 0)
	b.slot(0.5, 0)
	b.soc += 20
	for b.soc < 90 {
		b.slot(0.5, 0)
	}
	b.slot(0, 0.4)
	charge, _ := batteryRuns(b.slots)
	require.Len(t, charge, 1, "jump: only the run after it")
	assert.InDelta(t, 10/0.95, charge[0], 0.4)

	// a gap of an hour
	b = newSimBattery()
	for i := 0; b.soc < 90; i++ {
		if i == 5 {
			b.at = b.at.Add(time.Hour)
		}
		b.slot(0.5, 0)
	}
	b.slot(0, 0.4)
	charge, _ = batteryRuns(b.slots)
	require.Len(t, charge, 1, "gap: only the run after it")
	assert.InDelta(t, 10/0.95, charge[0], 0.4)

	// noticeable flow the other way within the run
	b = newSimBattery()
	for b.soc < 90 {
		b.slot(0.5, 0.05)
	}
	b.slot(0, 0.4)
	charge, _ = batteryRuns(b.slots)
	assert.Empty(t, charge, "flow both ways")

	// too small a run
	b = newSimBattery()
	for b.soc < 35 {
		b.slot(0.5, 0)
	}
	b.slot(0, 0.4)
	charge, _ = batteryRuns(b.slots)
	assert.Empty(t, charge, "15 % soc")
}

// A run followed by idle slots ends there, e.g. a full battery.
func TestBatteryRunsEndIdle(t *testing.T) {
	b := newSimBattery()
	for b.soc < 90 {
		b.slot(0.5, 0)
	}
	for range 4 {
		b.slot(0, 0)
	}
	charge, discharge := batteryRuns(b.slots)
	assert.Len(t, charge, 1)
	assert.Empty(t, discharge)
}

func TestApplyBatteryIdent(t *testing.T) {
	site := &Site{log: util.NewLogger("test")}
	s := site.lms()
	s.ident = []batteryIdentResult{{Name: "batt", Capacity: 9, Efficiency: 0.9, Valid: true}}

	bat := optimizer.BatteryConfig{SCapacity: 10000, SInitial: 5000, SMin: 1000, SMax: 10000}
	detail := batteryDetail{Name: "batt", Capacity: 10}

	site.applyBatteryIdent(&bat, &detail)
	assert.Equal(t, float32(10000), bat.SCapacity, "not used unless switched on")

	require.NoError(t, site.SetBatteryIdentUse(true))
	site.applyBatteryIdent(&bat, &detail)
	assert.InDelta(t, 9000, bat.SCapacity, 0.1)
	assert.InDelta(t, 4500, bat.SInitial, 0.1, "soc stays 50 %")
	assert.InDelta(t, 900, bat.SMin, 0.1)
	assert.InDelta(t, 9000, bat.SMax, 0.1)
	assert.Equal(t, 9.0, detail.Capacity)

	other := optimizer.BatteryConfig{SCapacity: 5000}
	site.applyBatteryIdent(&other, &batteryDetail{Name: "other"})
	assert.Equal(t, float32(5000), other.SCapacity, "not identified")

	s.ident[0].Valid = false
	bat = optimizer.BatteryConfig{SCapacity: 10000}
	site.applyBatteryIdent(&bat, &detail)
	assert.Equal(t, float32(10000), bat.SCapacity, "implausible values are not used")

	require.NoError(t, site.SetBatteryIdentUse(false))
}
