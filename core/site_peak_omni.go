package core

// Custom extension: peak shaving and grid charging with a Marstek battery behind
// the Home Assistant integration Omnibattery.
//
// Omnibattery regulates the battery itself (zero feed-in) and takes a setpoint
// only while the battery is under manual control. So for this battery type evcc
// switches the manual control on, forces the mode and only then writes the
// power, all of it only while evcc controls: charging from the grid or covering a
// peak from the reserve. Otherwise it releases the manual control and Omnibattery
// regulates again. The battery type BYD keeps writing the power alone, see
// site_peakshaving.go.
//
// Once per cycle, at the end (updateBatteryModePeakAware), evcc decides one wish
// from the grid charge setpoint and the peak shaving state, see omniWant, and
// applyOmni brings Home Assistant to it, writing only what differs.

import (
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/db/settings"
	"github.com/evcc-io/evcc/util/homeassistant"
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

// omniEnabled reports whether the battery type Marstek is set up: the switch, the
// mode and the discharge power entity are there
func (site *Site) omniEnabled() bool {
	s := site.peak()

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.batteryType == batteryTypeMarstek && s.manualEntity != "" && s.modeEntity != "" && s.set != nil
}

// omniType reports whether the battery type is Marstek, set up or not. For this
// type evcc never writes a power or the free value without the manual control.
func (site *Site) omniType() bool {
	s := site.peak()

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.batteryType == batteryTypeMarstek
}

// omniOwned reports whether evcc turned the manual control on
func (site *Site) omniOwned() bool {
	return site.omniType() && site.peakOwned()
}

// omniTypeWithoutEntities reports whether the battery type Marstek lacks its
// switch or mode
func (site *Site) omniTypeWithoutEntities() bool {
	s := site.peak()

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.batteryType == batteryTypeMarstek && (s.manualEntity == "" || s.modeEntity == "")
}

// omniWant returns what evcc wants of the battery, from top to bottom:
//
//	grid charging with a setpoint   switch on, mode Charge, charge power
//	below the reserve, a peak       switch on, mode Discharge, discharge power
//	below the reserve, no peak      switch on, mode None, nothing written
//	otherwise                       switch off (release), the rest stays
//
// out writes the power, nil if there is none.
func (site *Site) omniWant() (on bool, mode omniMode, power float64, out func(float64) error) {
	s := site.peak()

	s.mu.Lock()
	defer s.mu.Unlock()

	switch {
	case s.chargeSetpoint > 0 && s.chargeSet != nil:
		return true, omniCharge, s.chargeSetpoint, s.chargeSet

	case s.enabled && s.shaving && !s.metersLost && s.omniPeakValue > 0:
		return true, omniDischarge, s.omniPeakValue, s.set

	case s.enabled && s.shaving && !s.metersLost:
		return true, omniIdle, 0, nil
	}

	return false, "", 0, nil
}

// applyOmni brings the battery to what evcc wants: switch, then mode, then power,
// each only if Home Assistant shows something else. A step failing stops the rest
// for this cycle, the next cycle starts again. Released only if evcc switched the
// manual control on (owned); a switch turned on by hand stays while evcc does not
// control.
func (site *Site) applyOmni() {
	if !site.omniEnabled() {
		return
	}

	s := site.peak()

	// against a swap of the outputs and a second apply
	s.out.Lock()
	defer s.out.Unlock()

	if !site.omniEnabled() {
		return
	}

	on, mode, power, out := site.omniWant()
	if !on {
		site.releaseOmni()
		return
	}

	s.mu.Lock()
	manual, modeEntity := s.manualEntity, s.modeEntity
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
		site.setPeakOwned(true)
	}

	// 2. mode
	state, _, err = omniRead(conn, modeEntity)
	if !site.logWrite("marstek mode read", "read "+modeEntity, err) {
		return
	}
	if state != string(mode) {
		if !site.logWrite("marstek mode", "mode "+string(mode), conn.CallSelectService(modeEntity, string(mode))) {
			return
		}
	}

	// 3. power
	if out != nil {
		name := "peak shaving"
		if mode == omniCharge {
			name = "grid charge power"
		}
		if !site.writeOutput(name, out, power) {
			return
		}
	}

	site.omniShow(string(mode))
}

// releaseOmni switches the manual control off, if evcc switched it on, and
// reports whether it is released. The mode and the power stay: Omnibattery
// overwrites them in automatic operation anyway. Called with s.out held.
func (site *Site) releaseOmni() bool {
	if !site.omniOwned() {
		site.omniShow("")
		return true
	}

	s := site.peak()

	s.mu.Lock()
	manual := s.manualEntity
	s.mu.Unlock()

	if manual != "" && !site.omniSwitchOff(manual) {
		return false
	}

	site.setPeakOwned(false)
	site.omniShow("")

	return true
}

// omniSwitchOff switches the entity off unless it is already, and reports
// whether it is. An entity not readable is written to anyway.
func (site *Site) omniSwitchOff(entity string) bool {
	conn, err := site.haConnection()
	if !site.logWrite("marstek connect", "connect", err) {
		return false
	}

	if state, _, err := omniRead(conn, entity); err == nil && strings.EqualFold(state, "off") {
		return true
	}

	return site.logWrite("marstek switch", "switch off", conn.CallSwitchService(entity, false))
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

// omniRead reads the state and the options of a switch or select entity
func omniRead(conn *homeassistant.Connection, entity string) (string, []string, error) {
	var res struct {
		State      string `json:"state"`
		Attributes struct {
			Options []string `json:"options"`
		} `json:"attributes"`
	}

	err := conn.GetJSON(fmt.Sprintf("%s/api/states/%s", conn.URI(), url.PathEscape(entity)), &res)

	return res.State, res.Attributes.Options, err
}

func (site *Site) publishOmniSettings() {
	s := site.peak()

	s.mu.Lock()
	typ, manual, mode, shown := s.batteryType, s.manualEntity, s.modeEntity, s.omniShown
	s.mu.Unlock()

	if typ == "" {
		typ = batteryTypeBYD
	}

	site.publish(keys.PeakShavingBatteryType, typ)
	site.publish(keys.PeakShavingManualEntity, manual)
	site.publish(keys.PeakShavingModeEntity, mode)
	site.publish(keys.PeakShavingManual, shown)
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

// SetPeakShavingBatteryType sets the battery type: byd writes the power only,
// marstek also switches Omnibattery to manual control. What evcc held with the
// previous type is handed back first.
func (site *Site) SetPeakShavingBatteryType(typ string) error {
	if typ != batteryTypeBYD && typ != batteryTypeMarstek {
		return fmt.Errorf("unknown battery type: %s", typ)
	}

	if site.GetPeakShavingBatteryType() == typ {
		return nil
	}

	s := site.peak()

	// handed back and swapped in one go, a cycle in between would write for the
	// previous type
	s.out.Lock()

	if site.peakOwned() {
		s.mu.Lock()
		previous, set := s.batteryType, s.set
		s.mu.Unlock()

		if previous == batteryTypeMarstek {
			site.releaseOmni()
		} else if site.writeOutput("peak shaving", set, site.peakFreeValue()) {
			site.setPeakOwned(false)
		}
	}

	s.mu.Lock()
	s.batteryType = typ
	s.handedBack = false
	s.mu.Unlock()

	s.out.Unlock()

	site.log.DEBUG.Println("set peak shaving battery type:", typ)
	settings.SetString(keys.PeakShavingBatteryType, typ)
	site.publish(keys.PeakShavingBatteryType, typ)

	return nil
}

// GetPeakShavingManualEntity returns the switch of the manual control
func (site *Site) GetPeakShavingManualEntity() string {
	s := site.peak()

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.manualEntity
}

// SetPeakShavingManualEntity sets the switch of the manual control of Omnibattery.
// A switch evcc turned on is turned off first. Without a switch the battery type
// Marstek cannot run, so peak shaving is turned off then.
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

	s.out.Lock()
	site.releaseOmni()
	s.mu.Lock()
	s.manualEntity = entity
	s.mu.Unlock()
	s.out.Unlock()

	site.log.DEBUG.Println("set peak shaving manual entity:", entity)
	settings.SetString(keys.PeakShavingManualEntity, entity)
	site.publish(keys.PeakShavingManualEntity, entity)

	return site.omniEntityRemoved(entity)
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
	if entity == "" {
		site.releaseOmni()
	}
	s.mu.Lock()
	s.modeEntity = entity
	s.mu.Unlock()
	s.out.Unlock()

	site.log.DEBUG.Println("set peak shaving mode entity:", entity)
	settings.SetString(keys.PeakShavingModeEntity, entity)
	site.publish(keys.PeakShavingModeEntity, entity)

	return site.omniEntityRemoved(entity)
}

// omniEntityRemoved turns peak shaving off once the battery type Marstek lost
// its switch or mode: it cannot do anything without
func (site *Site) omniEntityRemoved(entity string) error {
	if entity != "" || !site.omniTypeWithoutEntities() {
		return nil
	}

	return site.SetPeakShaving(false)
}

func joinModes(modes []omniMode) string {
	var s []string
	for _, m := range modes {
		s = append(s, string(m))
	}

	return strings.Join(s, ", ")
}
