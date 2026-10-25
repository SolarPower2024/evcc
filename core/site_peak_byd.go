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
//
// Until this version evcc wrote the discharge power itself into a number entity
// (peakShavingEntity). That way is gone; a stored entity is released and removed
// at the start, see migratePeakEntity.

import (
	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/core/peak"
	"github.com/evcc-io/evcc/db/settings"
)

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

// migratePeakEntity ends the old way of the battery type BYD, where evcc wrote the
// discharge power into a number entity (peakShavingEntity). If evcc still held the
// battery back through it (peakShavingOwned), the free value goes there once, a
// failure is logged and not repeated. Then the entity and the mark are deleted. For
// the battery type Marstek the mark means the manual control and stays. The stored
// free value goes with it, see dropFreeValue. Peak shaving that was on is turned
// off while one of the three entities of the integration is missing. Called at the
// start, after the settings are back.
func (site *Site) migratePeakEntity() {
	defer site.dropFreeValue()

	entity, err := settings.String(keys.PeakShavingEntity)
	if err != nil {
		return
	}

	s := site.peak()

	s.mu.Lock()
	byd, owned := s.batteryType != batteryTypeMarstek, s.owned
	off := byd && s.enabled && (s.protSwitch == "" || s.protLimit == "" || s.protSoc == "")
	if off {
		s.enabled = false
		s.shaving = false
	}
	s.mu.Unlock()

	// peak shaving ran with the old setpoint and has nothing to run with now
	if off {
		site.log.WARN.Println("peak shaving: off until the entities of evcc Peak Fronius are set (Lastmanagement-Details → Peak Shaving)")
		settings.SetBool(keys.PeakShaving, false)
		site.publish(keys.PeakShaving, false)
	}

	if byd && owned {
		if entity != "" {
			free := site.peakFreeValue()

			set, err := site.numberSetter(entity, true, nil)
			if err == nil {
				err = set(free)
			}

			if err != nil {
				site.log.WARN.Printf("peak shaving: the old discharge entity %s was not set free: %v", entity, writeErrorText(err))
			} else {
				site.log.INFO.Printf("peak shaving: the old discharge entity %s set free (%.0fW)", entity, free)
			}
		}

		s.mu.Lock()
		s.owned = false
		s.mu.Unlock()

		if err := settings.Delete(keys.PeakShavingOwned); err != nil {
			site.log.ERROR.Printf("peak shaving: delete %s: %v", keys.PeakShavingOwned, err)
		}
	}

	if err := settings.Delete(keys.PeakShavingEntity); err != nil {
		site.log.ERROR.Printf("peak shaving: delete %s: %v", keys.PeakShavingEntity, err)
	}
}

// peakRuns reports whether peak shaving runs: on, set up and with a battery
func (site *Site) peakRuns() bool {
	s := site.peak()

	s.mu.Lock()
	run := s.enabled && s.configured()
	s.mu.Unlock()

	return run && site.batteryConfigured()
}
