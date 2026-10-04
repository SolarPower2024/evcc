// Package peak holds the core of battery peak shaving for a demand charge
// (Leistungspreis): the metering of the clock-aligned 15 minute window the
// demand charge is billed on, the grid power the rest of the window allows,
// the battery setpoint keeping the grid below it and the reserve hysteresis.
//
// It knows nothing about the site, Home Assistant or settings. The site feeds
// it one sample per cycle and writes the setpoint, see core/site_peakshaving.go.
package peak

import (
	"math"
	"time"
)

const (
	// Window is the metering interval a demand charge is billed on
	Window = 15 * time.Minute

	// Cycle is how long a setpoint stays in place; at the end of a window it
	// reaches into the next one
	Cycle = 30 * time.Second

	// MaxGap is how old a sample may be to carry over into a new window
	MaxGap = 2 * time.Minute

	// DefaultFreeze is the minute of the window from which the allowed power no
	// longer grows: close to the end, a clock off by a few seconds could move a
	// large draw into the next window
	DefaultFreeze = 12 * time.Minute

	// DefaultCap is the allowed power at most as a multiple of the limit
	DefaultCap = 2.0

	// DefaultHysteresis is the soc band in % keeping a fluctuating soc from
	// flapping across the reserve
	DefaultHysteresis = 2.0

	// DefaultFreeValue signals the discharge controller that the battery may be
	// used without restriction, i.e. the soc is above the reserve
	DefaultFreeValue = 10000.0

	// an energy counter standing still while the grid power says this much was
	// drawn over MaxGap has stopped updating
	staleWs = 20 * 3600.0 // 20Wh
)

// Where the energy drawn in the window comes from
const (
	SourceMeter  = "meter"  // grid meter's import counter
	SourceEntity = "entity" // Home Assistant energy sensor
	SourcePower  = "power"  // grid power of each cycle
)

// Setpoint returns the battery power needed to keep the grid draw at or below
// the allowed power, see Allowed.
//
// It deliberately does not use the grid power on its own. The grid meter
// already reflects whatever the battery is doing, so feeding that back would
// make the controller chase its own output: it would shave, see a compliant
// grid value, stop shaving, see the peak return, and oscillate every cycle.
// Adding the battery power back recovers the demand as it would be without the
// battery, which is a fixed quantity the setpoint can be derived from. evcc
// counts discharging as positive and charging as negative, so both directions
// are handled by the same sum. Whole watts are plenty, and some number entities
// reject fractions.
func Setpoint(gridPower, batteryPower, allowed float64) float64 {
	return math.Max(0, math.Round(gridPower+batteryPower-allowed))
}

// Allowed returns the grid power that may be drawn for the rest of the window
// with the window average still ending at the limit. Energy left unused earlier
// allows more, energy drawn above the limit allows less, down to nothing once
// the window's budget is spent.
func Allowed(limit, usedWs float64, elapsed time.Duration) float64 {
	budget := limit*Window.Seconds() - usedWs
	remaining := Window - elapsed

	// the part of the cycle reaching into the next window gets that window's
	// budget, rather than squeezing this window's rest into a few seconds
	if remaining < Cycle {
		budget += limit * (Cycle - remaining).Seconds()
		remaining = Cycle
	}

	return max(0, budget/remaining.Seconds())
}

// Reserved reports whether the battery is held back for peaks: at or below the
// reserve soc it is, from reserve + hysteresis on it is not, in between it
// keeps its previous state
func Reserved(prev bool, soc, reserve, hysteresis float64) bool {
	switch {
	case soc <= reserve:
		return true
	case soc >= reserve+hysteresis:
		return false
	}
	return prev
}

// Range is what a number entity accepts: values from Min to Max in steps of Step
// counted from Min. A zero Step or a Max not above Min leaves that part out.
type Range struct {
	Min, Max, Step float64
}

// Fit moves a value onto the entity's range. Home Assistant rejects values
// outside min and max, and some devices misbehave on values off the step (a
// Fronius battery charges from the grid with 500W unless the power is a multiple
// of 10W). Up rounds to the next step above, for a discharge setpoint that has
// to cover a peak; otherwise to the step below, for a charge power that has to
// stay within the limit.
func (r Range) Fit(value float64, up bool) float64 {
	if r.Step > 0 {
		// the tolerance keeps float noise from moving a value already on a step
		steps := (value - r.Min) / r.Step
		if up {
			steps = math.Ceil(steps - 1e-9)
		} else {
			steps = math.Floor(steps + 1e-9)
		}
		value = r.Min + steps*r.Step
	}

	if r.Max > r.Min {
		value = min(max(value, r.Min), r.Max)
	}

	return value
}
