package core

import "github.com/evcc-io/evcc/core/keys"

// Custom extension: tells the ui that the charger switches a heater in stages,
// see charger/switchstages.go. Its mode buttons then read Off/Smart/On as for
// upstream's switch devices, without the switch device feature, which would
// drop the current settings and change load management.
//
// Without a power sensor the heater's power is an assumption (stages switched
// on times the stage power), while its own thermostat may have cut out. Control,
// circuits and load management keep using it. The home consumption, the energy
// flow, the energy history and the sessions count only measured power, so the
// heater's real draw shows up in the home consumption instead.

// stagedCharger is implemented by charger.SwitchStages
type stagedCharger interface {
	Stages() int
}

// powerEstimator is implemented by charger.SwitchStages
type powerEstimator interface {
	PowerEstimated() bool
}

// publishStages publishes if the charger switches in stages
func (lp *Loadpoint) publishStages() {
	_, ok := lp.charger.(stagedCharger)
	lp.publish(keys.ChargerStages, ok)
}

// powerEstimated reports if the charge power is assumed rather than measured:
// the charger is its own meter and estimates. A loadpoint meter measures.
func (lp *Loadpoint) powerEstimated() bool {
	if lp.chargeMeter == nil {
		return false
	}
	pe, ok := lp.chargeMeter.source.(powerEstimator)
	return ok && pe.PowerEstimated()
}

// meteredPower returns the power for home consumption, energy and sessions: 0
// for an assumed power
func (lp *Loadpoint) meteredPower(power float64) float64 {
	if lp.powerEstimated() {
		return 0
	}
	return power
}

// publishChargePower publishes the measured charge power, an assumed one apart
func (lp *Loadpoint) publishChargePower(power float64) {
	var estimate float64
	if lp.powerEstimated() {
		power, estimate = 0, power
	}
	lp.publish(keys.ChargePower, power)
	lp.publish(keys.ChargePowerEstimate, estimate)
}
