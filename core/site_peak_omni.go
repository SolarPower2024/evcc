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
// The battery type BYD keeps writing the discharge power alone, see
// site_peakshaving.go.

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/db/settings"
	"github.com/evcc-io/evcc/util/homeassistant"
)

const (
	batteryTypeBYD     = "byd"
	batteryTypeMarstek = "marstek"
)

// errHandBack refuses a change while what evcc holds could not be handed back,
// the battery would otherwise stay held with nothing left to release it
var errHandBack = errors.New("could not hand the battery back, see the log")

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
// type evcc never writes a power or the free value without the manual control.
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

// omniTypeWithoutProt reports whether the battery type Marstek lacks one of the
// three entities of Omnibattery's peak shaving
func (site *Site) omniTypeWithoutProt() bool {
	s := site.peak()

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.batteryType == batteryTypeMarstek && (s.protSwitch == "" || s.protLimit == "" || s.protSoc == "")
}

// configured reports whether peak shaving has what it needs to run: the target
// of the discharge power, for the battery type Marstek the three entities of
// Omnibattery's peak shaving. Called with mu held.
func (s *peakState) configured() bool {
	if s.batteryType == batteryTypeMarstek {
		return s.protSwitch != "" && s.protLimit != "" && s.protSocSet != nil
	}

	return s.set != nil
}

// effectiveReserve is the reserve peak shaving works with. For the battery type
// Marstek it is at least Omnibattery's lowest soc threshold, so evcc's state of
// the reserve, the optimizer and Omnibattery agree also with a lower reserve set
// by a profile or the api. Called with s.mu held.
func (s *peakState) effectiveReserve() float64 {
	if s.batteryType == batteryTypeMarstek {
		return max(minOmniProtSoc, s.reserve)
	}

	return s.reserve
}

// peakConfigured reports whether peak shaving has what it needs to run, see
// peakState.configured
func (site *Site) peakConfigured() bool {
	s := site.peak()

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.configured()
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

// applyOmni brings Omnibattery to what evcc wants, grid charging and peak
// shaving independent of each other
func (site *Site) applyOmni() {
	s := site.peak()

	// against a swap of the outputs and a second apply
	s.out.Lock()
	defer s.out.Unlock()

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

// applyOmniProtection brings Omnibattery's peak shaving to what peak shaving
// wants: while it runs, the soc threshold is the reserve, at least 20 %, the limit
// the grid power allowed in the running 15 minute window (without meter values the
// peak limit, following the peak included) and the switch on. Otherwise the switch
// is turned off, but only if evcc turned it on (protOwned); limit and threshold
// stay. Called with s.out held.
func (site *Site) applyOmniProtection() {
	s := site.peak()

	s.mu.Lock()
	want := s.batteryType == batteryTypeMarstek && s.enabled && s.configured() && site.batteryConfigured()
	owned, sw, limitEntity, socSet := s.protOwned, s.protSwitch, s.protLimit, s.protSocSet
	threshold := s.effectiveReserve()

	// the window is there once a cycle metered it
	limit := s.limit
	if !s.metersLost && !s.window.Start.IsZero() {
		limit = s.window.Allowed
	}
	s.mu.Unlock()

	if !want {
		if owned {
			site.releaseProt()
		}

		return
	}

	// threshold and limit first, so the peak shaving never starts with old values
	if !site.omniWriteNumber("peak shaving soc threshold", "%", socSet, threshold) {
		return
	}
	conn, err := site.haConnection()
	if !site.logWrite("marstek connect", "connect", err) {
		return
	}

	if !site.omniWriteLimit(conn, limitEntity, limit) {
		return
	}

	state, _, err := omniRead(conn, sw)
	if !site.logWrite("marstek peak shaving switch read", "read "+sw, err) {
		return
	}

	if !strings.EqualFold(state, "on") &&
		!site.logWrite("marstek peak shaving switch", "switch on", conn.CallSwitchService(sw, true)) {
		return
	}

	// from here evcc drives it with its values, also a switch turned on by hand,
	// so it turns it off afterwards rather than leaving its last limit in place
	site.setProtOwned(true)
}

// minOmniLimitRise is how much the allowed power has to rise above the limit in
// the entity before it is written again. A falling limit is written right away,
// the peak has to stay covered; a rising one can wait, which saves Omnibattery a
// configuration write in nearly every cycle of a window.
const minOmniLimitRise = 500.0

// omniWriteLimit writes the limit, fitted to the entity and rounded down, when it
// is below the limit in the entity or at least minOmniLimitRise above it. Below
// the entity's minimum the minimum is written: Omnibattery takes no lower limit.
func (site *Site) omniWriteLimit(conn *homeassistant.Connection, entity string, limit float64) bool {
	r, current, err := numberState(conn, entity)

	if err == nil {
		limit = r.Fit(limit, false)
		if v, perr := strconv.ParseFloat(current, 64); perr == nil && limit >= v && limit < v+minOmniLimitRise {
			return true
		}
	}

	if !site.logWrite("peak shaving limit", fmt.Sprintf("write %.0fW", limit), conn.CallNumberService(entity, limit)) {
		return false
	}

	site.log.DEBUG.Printf("peak shaving limit: %.0fW", limit)

	return true
}

// omniWriteNumber writes a value through set, the setter skips what the entity
// already shows, and reports whether it landed
func (site *Site) omniWriteNumber(name, unit string, set func(float64) error, value float64) bool {
	if !site.logWrite(name, fmt.Sprintf("write %.0f%s", value, unit), set(value)) {
		return false
	}

	site.log.DEBUG.Printf("%s: %.0f%s", name, value, unit)

	return true
}

// setProtOwned records whether evcc turned on Omnibattery's peak shaving
func (site *Site) setProtOwned(owned bool) {
	s := site.peak()

	s.mu.Lock()
	changed := s.protOwned != owned
	s.protOwned = owned
	s.mu.Unlock()

	if changed {
		settings.SetBool(keys.PeakShavingProtOwned, owned)
	}
}

// releaseProt turns Omnibattery's peak shaving off, if evcc turned it on, and
// reports whether it is released. Limit and threshold stay. Called with s.out held.
func (site *Site) releaseProt() bool {
	s := site.peak()

	s.mu.Lock()
	owned, sw := s.protOwned, s.protSwitch
	s.mu.Unlock()

	if !owned {
		return true
	}

	if sw != "" && !site.omniSwitchOff("marstek peak shaving switch", sw) {
		return false
	}

	site.setProtOwned(false)

	return true
}

// releaseOmni zeroes the charge power and then switches the manual control off,
// if evcc switched it on, and reports whether it is released. The charge power
// would otherwise stay on its last value; the zero is only written where the
// entity shows another value, see numberSetter. A zero that fails keeps the
// switch on, the next cycle tries again. The mode stays: Omnibattery overwrites
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

	if chargeSet != nil && !site.writeOutput("grid charge power", chargeSet, 0) {
		return false
	}

	if manual != "" && !site.omniSwitchOff("marstek switch", manual) {
		return false
	}

	site.setPeakOwned(false)
	site.omniShow("")

	return true
}

// omniSwitchOff switches the entity off unless it is already, and reports
// whether it is. An entity not readable is written to anyway.
func (site *Site) omniSwitchOff(name, entity string) bool {
	conn, err := site.haConnection()
	if !site.logWrite("marstek connect", "connect", err) {
		return false
	}

	if state, _, err := omniRead(conn, entity); err == nil && strings.EqualFold(state, "off") {
		return true
	}

	return site.logWrite(name, "switch off", conn.CallSwitchService(entity, false))
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

// SetPeakShavingBatteryType sets the battery type: byd writes the discharge power,
// marstek drives Omnibattery's peak shaving and switches it to manual control
// while grid charging. What evcc held with the previous type is handed back first.
func (site *Site) SetPeakShavingBatteryType(typ string) error {
	if typ != batteryTypeBYD && typ != batteryTypeMarstek {
		return fmt.Errorf("unknown battery type: %s", typ)
	}

	if site.GetPeakShavingBatteryType() == typ {
		return nil
	}

	s := site.peak()

	// running peak shaving would otherwise stay on without writing anything
	s.mu.Lock()
	var missing error
	switch {
	case typ == batteryTypeMarstek && s.enabled && (s.protSwitch == "" || s.protLimit == "" || s.protSoc == ""):
		missing = errProtMissing
	case typ == batteryTypeBYD && s.enabled && s.set == nil:
		missing = errTargetMissing
	}
	s.mu.Unlock()
	if missing != nil {
		return missing
	}

	// handed back and swapped in one go, a cycle in between would write for the
	// previous type
	s.out.Lock()

	s.mu.Lock()
	previous, set := s.batteryType, s.set
	s.mu.Unlock()

	released := true

	switch {
	case previous == batteryTypeMarstek:
		// both are tried, the one failing stays for the next attempt
		omni := site.releaseOmni()
		prot := site.releaseProt()
		released = omni && prot

	case site.peakOwned():
		if released = site.writeOutput("peak shaving", set, site.peakFreeValue()); released {
			site.setPeakOwned(false)
		}
	}

	// what evcc holds would otherwise be left with the previous type
	if !released {
		s.out.Unlock()
		return errHandBack
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

// errProtMissing and errTargetMissing refuse peak shaving without what it needs
var (
	errProtMissing   = errors.New("no peak shaving switch, limit or soc threshold entity configured")
	errTargetMissing = errors.New("no target entity configured")
)

// GetPeakShavingProtSwitch returns the switch of Omnibattery's peak shaving
func (site *Site) GetPeakShavingProtSwitch() string {
	s := site.peak()

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.protSwitch
}

// SetPeakShavingProtSwitch sets the switch of Omnibattery's peak shaving. A switch
// evcc turned on is turned off first. Without it the battery type Marstek cannot
// shave peaks, so peak shaving is turned off then.
func (site *Site) SetPeakShavingProtSwitch(entity string) error {
	if entity != "" && !strings.HasPrefix(entity, "switch.") && !strings.HasPrefix(entity, "input_boolean.") {
		return fmt.Errorf("must be a switch or input_boolean entity: %s", entity)
	}

	s := site.peak()

	s.mu.Lock()
	changed := s.protSwitch != entity
	s.mu.Unlock()

	if !changed {
		return nil
	}

	// a switch evcc holds is released before it is forgotten
	s.out.Lock()
	if !site.releaseProt() {
		s.out.Unlock()
		return errHandBack
	}
	s.mu.Lock()
	s.protSwitch = entity
	s.mu.Unlock()
	s.out.Unlock()

	site.log.DEBUG.Println("set peak shaving switch:", entity)
	settings.SetString(keys.PeakShavingProtSwitch, entity)
	site.publish(keys.PeakShavingProtSwitch, entity)

	return site.omniProtRemoved(entity)
}

// GetPeakShavingProtLimit returns the number entity of Omnibattery's peak shaving
// limit
func (site *Site) GetPeakShavingProtLimit() string {
	s := site.peak()

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.protLimit
}

// SetPeakShavingProtLimit sets the number entity receiving the allowed grid power.
// Without it the battery type Marstek cannot shave peaks, so peak shaving is
// turned off and Omnibattery's peak shaving released then.
func (site *Site) SetPeakShavingProtLimit(entity string) error {
	s := site.peak()

	// written by omniWriteLimit, with its own rule for a rising limit
	return site.setProtNumber(entity, keys.PeakShavingProtLimit, &s.protLimit, nil)
}

// GetPeakShavingProtSoc returns the number entity of Omnibattery's peak shaving
// soc threshold
func (site *Site) GetPeakShavingProtSoc() string {
	s := site.peak()

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.protSoc
}

// SetPeakShavingProtSoc sets the number entity receiving the reserve. Without it
// the battery type Marstek cannot shave peaks, see SetPeakShavingProtLimit.
func (site *Site) SetPeakShavingProtSoc(entity string) error {
	s := site.peak()

	return site.setProtNumber(entity, keys.PeakShavingProtSoc, &s.protSoc, &s.protSocSet)
}

// setProtNumber sets one of the two number entities of Omnibattery's peak shaving,
// name points to it in the state. set, if given, receives the setter resolved
// from it, see protSocSetter.
func (site *Site) setProtNumber(entity, key string, name *string, set *func(float64) error) error {
	if entity != "" && !strings.HasPrefix(entity, "number.") && !strings.HasPrefix(entity, "input_number.") {
		return fmt.Errorf("must be a number or input_number entity: %s", entity)
	}

	s := site.peak()

	s.mu.Lock()
	changed := *name != entity
	s.mu.Unlock()

	if !changed {
		return nil
	}

	var setter func(float64) error

	if entity != "" {
		var err error
		if set != nil {
			setter, err = site.protSocSetter(entity)
		} else {
			_, err = site.haConnection()
		}
		if err != nil {
			return err
		}
	}

	// without the entity the peak shaving is turned off: released while the switch
	// is still known
	s.out.Lock()
	if entity == "" && !site.releaseProt() {
		s.out.Unlock()
		return errHandBack
	}
	s.mu.Lock()
	*name = entity
	if set != nil {
		*set = setter
	}
	s.mu.Unlock()
	s.out.Unlock()

	site.log.DEBUG.Println("set", key+":", entity)
	settings.SetString(key, entity)
	site.publish(key, entity)

	return site.omniProtRemoved(entity)
}

// protSocSetter returns the setter of the soc threshold entity. It rounds up to
// the step: the threshold has to cover the reserve. The write tolerance is in W
// and does not apply, every change of the reserve is written.
func (site *Site) protSocSetter(entity string) (func(float64) error, error) {
	return site.numberSetter(entity, true, nil)
}

// rebuildProtSetter resolves the setter of the soc threshold entity
func (site *Site) rebuildProtSetter() error {
	s := site.peak()

	s.mu.Lock()
	soc := s.protSoc
	s.mu.Unlock()

	var set func(float64) error

	if soc != "" {
		var err error
		if set, err = site.protSocSetter(soc); err != nil {
			return err
		}
	}

	s.mu.Lock()
	s.protSocSet = set
	s.mu.Unlock()

	return nil
}

// omniProtRemoved turns peak shaving off once the battery type Marstek lost one of
// the entities of Omnibattery's peak shaving: it cannot do anything without
func (site *Site) omniProtRemoved(entity string) error {
	if entity != "" || !site.omniTypeWithoutProt() {
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
