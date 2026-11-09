package core

// Custom extension: peak shaving with a BYD battery behind a Fronius inverter,
// regulated by the Home Assistant integration evcc Peak Fronius.
//
// Like Omnibattery for the Marstek, the integration regulates the battery itself
// within seconds (it sets the discharge limit, the storage mode and the grid
// charge power through fronius_modbus); evcc is its input, see site_peak_ext.go:
// the soc threshold, the limit, the switch. The battery mode stays evcc's
// standard: evcc calls the Home Assistant scripts of the battery modes, and these
// set the request of the integration (normal, grid_charge, hold) themselves; evcc
// does not write it. The grid charge power is the number entity of the battery
// grid charge power, as before.
//
// The limit is also the sign of life of evcc: the integration sets it to 0 at the
// start of every quarter hour and gives the battery free when evcc does not write
// it again within 2 minutes. So evcc never writes 0 (minBydLimit) and writes the
// limit again when the entity shows 0. It does so in every cycle, with peak shaving
// on or off and whatever the battery mode is, as long as the limit entity is set
// and a battery is configured: evcc counts as gone for the integration otherwise.
//
// What evcc held is handed back: the switch off, the grid charge power to 0, see
// releasePeak.

import "github.com/evcc-io/evcc/core/peak"

// bydHysteresis is the soc band in % above the threshold in which the integration
// goes on holding the battery back. It is fixed there, so evcc's state of the
// reserve (battery mode, display) has to use the same band to change where the
// integration does; it was a setting until the integration took over.
const bydHysteresis = peak.DefaultHysteresis

// bydLimitRule returns what the limit written to the integration has to fulfil:
// at least minBydLimit, as 0 means expired there, and written again when the
// entity shows 0. The other battery types have no such rule.
func (site *Site) bydLimitRule() (floor float64, zero bool) {
	if site.omniType() {
		return 0, false
	}

	return minBydLimit, true
}

// peakRuns reports whether peak shaving runs: on, set up and with a battery
func (site *Site) peakRuns() bool {
	s := site.peak()

	s.mu.Lock()
	run := s.enabled && s.configured()
	s.mu.Unlock()

	return run && site.batteryConfigured()
}
