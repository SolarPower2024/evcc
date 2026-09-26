package core

// Custom extension: the gate the optimizer's automatic mode passes.
//
// In automatic mode the optimizer sets the battery mode. Its charge request
// passes the same gate as the fork's own grid charging (peak, circuit, charge
// power setpoint) and becomes hold when refused. Hold gives way to normal
// while a peak has to be covered, and peak shaving then only covers peaks.
// When the optimizer result is missing or stale, the fork's grid charging
// applies as without the optimizer. Without automatic mode the battery follows
// upstream.

import "github.com/evcc-io/evcc/api"

// optimizerInControl reports whether the optimizer in automatic mode decides
// the battery mode right now: automatic mode with a current result
func (site *Site) optimizerInControl() bool {
	if !site.Automatic() || site.GetBatteryModeExternal() != api.BatteryUnknown {
		return false
	}
	_, ok := site.batterySuggestionMode()
	return ok
}

// optimizerCharges reports whether the optimizer in control wants grid charging
func (site *Site) optimizerCharges() bool {
	if !site.optimizerInControl() {
		return false
	}
	mode, _ := site.batterySuggestionMode()
	return mode == api.BatteryCharge
}

// optimizerHolds reports whether the optimizer in control withholds discharging
func (site *Site) optimizerHolds() bool {
	if !site.optimizerInControl() {
		return false
	}
	mode, _ := site.batterySuggestionMode()
	return mode == api.BatteryHold || mode == api.BatteryHoldCharge
}

// peakNeedsBattery reports whether the demand exceeds what may be drawn from
// the grid right now, so the battery has to discharge
func (site *Site) peakNeedsBattery() bool {
	s := site.peak()

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.enabled && s.set != nil && s.demand > s.allowed
}

// lmGateBatteryMode passes the battery mode upstream decided in automatic mode:
// a charge request the gate refuses becomes hold, hold gives way to normal
// while a peak has to be covered, and without a current optimizer result the
// fork's grid charging applies. Unknown means no change.
func (site *Site) lmGateBatteryMode(mode api.BatteryMode, gridCharge bool) api.BatteryMode {
	if !site.Automatic() || site.GetBatteryModeExternal() != api.BatteryUnknown {
		return mode
	}

	current := site.GetBatteryMode()
	change := func(m api.BatteryMode) api.BatteryMode {
		if m == current {
			return api.BatteryUnknown
		}
		return m
	}

	// missing or stale result: the fork's own grid charging, as without the optimizer
	if !site.optimizerInControl() {
		if gridCharge {
			return change(api.BatteryCharge)
		}
		return mode
	}

	target := mode
	if target == api.BatteryUnknown {
		target = current
	}

	switch target {
	case api.BatteryCharge:
		if !site.gridChargeGate() {
			site.log.DEBUG.Println("battery mode: optimizer charge refused by load management or peak shaving, holding")
			return change(api.BatteryHold)
		}

	case api.BatteryHold, api.BatteryHoldCharge:
		site.releaseGridCharge()
		if site.peakNeedsBattery() {
			site.log.DEBUG.Println("battery mode: optimizer hold, covering a peak")
			return change(api.BatteryNormal)
		}

	default:
		site.releaseGridCharge()
	}

	return mode
}
