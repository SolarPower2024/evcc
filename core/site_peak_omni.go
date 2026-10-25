package core

// Custom extension: peak shaving and grid charging with a Marstek battery behind
// the Home Assistant integration Omnibattery.
//
// Omnibattery regulates the battery itself (zero feed-in) and takes a setpoint
// only while the battery is under manual control. For this battery type evcc does
// two independent things, both at the end of each cycle (applyOmni):
//
//   - grid charging: it switches the manual control on, forces the mode Charge and
//     only then writes the charge power. Afterwards it writes the charge power 0
//     and releases the manual control, see applyOmniCharge.
//   - peak shaving: Omnibattery has a peak shaving of its own (capacity
//     protection) that, below a soc threshold, discharges only to keep the grid
//     under a limit and keeps charging from the pv surplus. evcc is the input of
//     it: it writes the reserve as the threshold and the allowed grid power of the
//     15 minute window as the limit, and turns it on while peak shaving runs, see
//     applyOmniProtection. Below the reserve evcc never takes the battery into
//     manual control.
//
// The battery type BYD works the same way through the integration evcc Peak
// Fronius, see site_peak_byd.go; what both share is in site_peak_ext.go.

import (
	"fmt"
	"slices"
	"strings"

	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/db/settings"
)

const (
	batteryTypeBYD     = "byd"
	batteryTypeMarstek = "marstek"
)

// omniMode is an option of the select "force mode" of Omnibattery
type omniMode string

const (
	omniIdle      omniMode = "None"
	omniCharge    omniMode = "Charge"
	omniDischarge omniMode = "Discharge"
)

// omniModes are the options the select has to offer
var omniModes = []omniMode{omniIdle, omniCharge, omniDischarge}

// minOmniProtSoc is the lowest threshold the soc threshold entity of Omnibattery
// takes
const minOmniProtSoc = 20.0

// omniEnabled reports whether grid charging with the battery type Marstek is set
// up: the type, the manual control switch and the force mode select
func (site *Site) omniEnabled() bool {
	s := site.peak()

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.batteryType == batteryTypeMarstek && s.manualEntity != "" && s.modeEntity != ""
}

// omniType reports whether the battery type is Marstek, set up or not. For this
// type evcc never writes a power without the manual control.
func (site *Site) omniType() bool {
	s := site.peak()

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.batteryType == batteryTypeMarstek
}

// omniOwned reports whether evcc controls the battery through the manual control
func (site *Site) omniOwned() bool {
	return site.omniType() && site.peakOwned()
}

// effectiveReserve is the reserve peak shaving works with. For the battery type
// Marstek it is at least Omnibattery's lowest soc threshold, so evcc's state of
// the reserve, the optimizer, Omnibattery and the ui agree also with a lower
// reserve set by a profile or the api; the value set stays stored for BYD.
// Called with s.mu held.
func (s *peakState) effectiveReserve() float64 {
	if s.batteryType == batteryTypeMarstek {
		return max(minOmniProtSoc, s.reserve)
	}

	return s.reserve
}

// omniWant returns whether evcc grid charges the battery and with which power.
// Peak shaving is not part of it: that is Omnibattery's own, see
// applyOmniProtection.
func (site *Site) omniWant() (charge bool, power float64) {
	s := site.peak()

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.chargeSetpoint > 0 && s.chargeSet != nil {
		return true, s.chargeSetpoint
	}

	return false, 0
}

// applyOmni brings the external controller to what evcc wants: Omnibattery (grid
// charging and peak shaving independent of each other) or the integration for BYD
// (peak shaving)
func (site *Site) applyOmni() {
	s := site.peak()

	// against a swap of the outputs and a second apply
	s.out.Lock()
	defer s.out.Unlock()

	if site.peakStopped() {
		return
	}

	site.applyOmniCharge()
	site.applyOmniProtection()
}

// applyOmniCharge brings the battery to grid charging: switch, then mode, then
// power, each only if Home Assistant shows something else. A step failing stops
// the rest for this cycle, the next cycle starts again. Not charging, it releases
// what evcc held (owned); a switch turned on by hand stays while evcc does not
// control. Called with s.out held.
func (site *Site) applyOmniCharge() {
	if !site.omniEnabled() {
		return
	}

	s := site.peak()

	charge, power := site.omniWant()
	if !charge {
		site.releaseOmni()
		return
	}

	s.mu.Lock()
	manual, modeEntity, out := s.manualEntity, s.modeEntity, s.chargeSet
	s.mu.Unlock()

	conn, err := site.haConnection()
	if !site.logWrite("marstek connect", "connect", err) {
		return
	}

	// 1. manual control: nothing else is accepted before
	state, _, err := omniRead(conn, manual)
	if !site.logWrite("marstek switch read", "read "+manual, err) {
		return
	}
	if !strings.EqualFold(state, "on") {
		if !site.logWrite("marstek switch", "switch on", conn.CallSwitchService(manual, true)) {
			return
		}
	}

	// from here evcc forces mode and power, also into a switch turned on by hand,
	// so it releases the switch afterwards rather than leaving a forced charge
	site.setPeakOwned(true)

	// 2. mode
	state, _, err = omniRead(conn, modeEntity)
	if !site.logWrite("marstek mode read", "read "+modeEntity, err) {
		return
	}
	if state != string(omniCharge) {
		if !site.logWrite("marstek mode", "mode "+string(omniCharge), conn.CallSelectService(modeEntity, string(omniCharge))) {
			return
		}
	}

	// 3. power
	if !site.writeOutput("grid charge power", out, power) {
		return
	}

	site.omniShow(string(omniCharge))
}

// releaseOmni zeroes the charge power and then switches the manual control off,
// if evcc switched it on, and reports whether it is released. The charge power
// would otherwise stay on its last value; the zero is only written where the
// entity shows another value, see numberSetter. A zero that fails keeps the
// switch on, the next cycle tries again; an entity Home Assistant does not know
// counts as written, see releaseOutput. The mode stays: Omnibattery overwrites
// it in automatic operation anyway. Called with s.out held.
func (site *Site) releaseOmni() bool {
	if !site.omniOwned() {
		site.omniShow("")
		return true
	}

	s := site.peak()

	s.mu.Lock()
	manual, chargeSet := s.manualEntity, s.chargeSet
	s.mu.Unlock()

	if chargeSet != nil && !site.releaseOutput("grid charge power", chargeSet, 0) {
		return false
	}

	if manual != "" && !site.omniSwitchOff("marstek switch", manual) {
		return false
	}

	site.setPeakOwned(false)
	site.omniShow("")

	return true
}

// omniShow publishes the forced mode evcc holds, empty = not controlling
func (site *Site) omniShow(mode string) {
	s := site.peak()

	s.mu.Lock()
	changed := s.omniShown != mode
	s.omniShown = mode
	s.mu.Unlock()

	if changed {
		site.log.DEBUG.Printf("marstek: manual control %q", mode)
		site.publish(keys.PeakShavingManual, mode)
	}
}

func (site *Site) publishOmniSettings() {
	s := site.peak()

	s.mu.Lock()
	typ, manual, mode, shown := s.batteryType, s.manualEntity, s.modeEntity, s.omniShown
	protSwitch, protLimit, protSoc := s.protSwitch, s.protLimit, s.protSoc
	s.mu.Unlock()

	if typ == "" {
		typ = batteryTypeBYD
	}

	site.publish(keys.PeakShavingBatteryType, typ)
	site.publish(keys.PeakShavingManualEntity, manual)
	site.publish(keys.PeakShavingModeEntity, mode)
	site.publish(keys.PeakShavingManual, shown)
	site.publish(keys.PeakShavingProtSwitch, protSwitch)
	site.publish(keys.PeakShavingProtLimit, protLimit)
	site.publish(keys.PeakShavingProtSoc, protSoc)
}

//
// api
//

// GetPeakShavingBatteryType returns the battery type, byd or marstek
func (site *Site) GetPeakShavingBatteryType() string {
	s := site.peak()

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.batteryType == "" {
		return batteryTypeBYD
	}

	return s.batteryType
}

// SetPeakShavingBatteryType sets the battery type: marstek drives Omnibattery and
// switches it to manual control while grid charging, byd drives the integration
// evcc Peak Fronius and asks it for grid charging with a request. What evcc held
// with the previous type is handed back first.
func (site *Site) SetPeakShavingBatteryType(typ string) error {
	if typ != batteryTypeBYD && typ != batteryTypeMarstek {
		return fmt.Errorf("unknown battery type: %s", typ)
	}

	if site.GetPeakShavingBatteryType() == typ {
		return nil
	}

	s := site.peak()

	// running peak shaving would otherwise stay on without writing anything
	if site.GetPeakShaving() && site.protMissing() {
		return errProtMissing
	}

	// handed back and swapped in one go, a cycle in between would write for the
	// previous type
	s.out.Lock()

	s.mu.Lock()
	previous := s.batteryType
	s.mu.Unlock()

	released := true

	switch {
	case previous == batteryTypeMarstek:
		// both are tried, the one failing stays for the next attempt
		omni := site.releaseOmni()
		prot := site.releaseProt()
		released = omni && prot

	default:
		released = site.releaseProt()
	}

	// what evcc holds would otherwise be left with the previous type
	if !released {
		s.out.Unlock()
		return errHandBack
	}

	s.mu.Lock()
	s.batteryType = typ
	reserve := s.effectiveReserve()
	s.mu.Unlock()

	s.out.Unlock()

	site.log.DEBUG.Println("set peak shaving battery type:", typ)
	settings.SetString(keys.PeakShavingBatteryType, typ)
	site.publish(keys.PeakShavingBatteryType, typ)
	site.publish(keys.PeakShavingReserve, reserve) // the reserve in effect depends on the type

	return nil
}

// GetPeakShavingManualEntity returns the switch of the manual control
func (site *Site) GetPeakShavingManualEntity() string {
	s := site.peak()

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.manualEntity
}

// SetPeakShavingManualEntity sets the switch of the manual control of Omnibattery,
// used for grid charging. A switch evcc turned on is turned off first.
func (site *Site) SetPeakShavingManualEntity(entity string) error {
	if entity != "" && !strings.HasPrefix(entity, "switch.") && !strings.HasPrefix(entity, "input_boolean.") {
		return fmt.Errorf("must be a switch or input_boolean entity: %s", entity)
	}

	s := site.peak()

	s.mu.Lock()
	changed := s.manualEntity != entity
	s.mu.Unlock()

	if !changed {
		return nil
	}

	// a switch evcc holds is released before it is forgotten
	s.out.Lock()
	if !site.releaseOmni() {
		s.out.Unlock()
		return errHandBack
	}
	s.mu.Lock()
	s.manualEntity = entity
	s.mu.Unlock()
	s.out.Unlock()

	site.log.DEBUG.Println("set peak shaving manual entity:", entity)
	settings.SetString(keys.PeakShavingManualEntity, entity)
	site.publish(keys.PeakShavingManualEntity, entity)

	return nil
}

// GetPeakShavingModeEntity returns the select of the forced mode
func (site *Site) GetPeakShavingModeEntity() string {
	s := site.peak()

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.modeEntity
}

// SetPeakShavingModeEntity sets the select of the forced mode of Omnibattery. It
// has to offer the options None, Charge and Discharge, as written here.
func (site *Site) SetPeakShavingModeEntity(entity string) error {
	if entity != "" {
		if !strings.HasPrefix(entity, "select.") && !strings.HasPrefix(entity, "input_select.") {
			return fmt.Errorf("must be a select or input_select entity: %s", entity)
		}

		conn, err := site.haConnection()
		if err != nil {
			return err
		}

		_, options, err := omniRead(conn, entity)
		if err != nil {
			return fmt.Errorf("%s: %w", entity, err)
		}
		for _, m := range omniModes {
			if !slices.Contains(options, string(m)) {
				return fmt.Errorf("%s has to offer the options %s, it offers %s", entity, joinModes(omniModes), strings.Join(options, ", "))
			}
		}
	}

	s := site.peak()

	s.mu.Lock()
	changed := s.modeEntity != entity
	s.mu.Unlock()

	if !changed {
		return nil
	}

	// a mode that cannot be forced any more: the manual control is released while
	// the switch is still known
	s.out.Lock()
	if entity == "" && !site.releaseOmni() {
		s.out.Unlock()
		return errHandBack
	}
	s.mu.Lock()
	s.modeEntity = entity
	s.mu.Unlock()
	s.out.Unlock()

	site.log.DEBUG.Println("set peak shaving mode entity:", entity)
	settings.SetString(keys.PeakShavingModeEntity, entity)
	site.publish(keys.PeakShavingModeEntity, entity)

	return nil
}

func joinModes(modes []omniMode) string {
	var s []string
	for _, m := range modes {
		s = append(s, string(m))
	}

	return strings.Join(s, ", ")
}
