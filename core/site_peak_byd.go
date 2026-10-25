package core

// Custom extension: peak shaving and grid charging with a BYD battery behind a
// Fronius inverter, regulated by the Home Assistant integration evcc Peak Fronius.
//
// Like Omnibattery for the Marstek, the integration regulates the battery itself
// within seconds (it sets the discharge limit, the storage mode and the grid
// charge power through fronius_modbus); evcc is its input, see site_peak_ext.go:
// the soc threshold, the limit, the switch. Beyond that the integration takes a
// request, a select with the options normal, grid_charge and hold. evcc writes it
// from its battery mode at the end of each cycle (applyBydRequest): grid charging
// asks for grid_charge, the optimizer's or the user's hold for hold, anything else
// for normal. The grid charge power is the number entity of the battery grid
// charge power, as before.
//
// The limit is also the sign of life of evcc: the integration sets it to 0 at the
// start of every quarter hour and gives the battery free when evcc does not write
// it again within 2 minutes. So evcc never writes 0 (minBydLimit) and writes the
// limit again when the entity shows 0, and as long as it steers the battery with a
// request, also without peak shaving.
//
// What evcc held is handed back: the request goes back to normal, the switch off,
// the grid charge power to 0, see releaseRequest and releasePeak.
//
// Until this version evcc wrote the discharge power itself into a number entity
// (peakShavingEntity). That way is gone; a stored entity is released and removed
// at the start, see migratePeakEntity.

import (
	"fmt"
	"slices"
	"strings"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/db/settings"
)

// the options of the select "request" of the integration
const (
	requestNormal     = "normal"
	requestGridCharge = "grid_charge"
	requestHold       = "hold"
)

// bydRequests are the options the select has to offer
var bydRequests = []string{requestNormal, requestGridCharge, requestHold}

// bydLimitRule returns what the limit written to the integration has to fulfil:
// at least minBydLimit, as 0 means expired there, and written again when the
// entity shows 0. The other battery types have no such rule.
func (site *Site) bydLimitRule() (floor float64, zero bool) {
	if site.omniType() {
		return 0, false
	}

	return minBydLimit, true
}

// bydWant returns the request evcc's battery mode asks for. For the battery type
// Marstek it is always normal, the request does not exist there. While peak shaving
// runs and the optimizer holds the battery it is normal as well: hold would block
// the discharge and leave a peak uncovered until evcc reacts, the integration
// takes the held battery with the soc threshold of 100 % (site_peak_omni_hold.go)
// and still covers peaks. A hold without peak shaving stays hold.
func (site *Site) bydWant() string {
	if site.omniType() {
		return requestNormal
	}

	if site.peakRuns() && site.optimizerHolds() {
		return requestNormal
	}

	switch site.GetBatteryMode() {
	case api.BatteryCharge:
		return requestGridCharge
	case api.BatteryHold:
		return requestHold
	default:
		return requestNormal
	}
}

// applyBydRequest brings the request of the integration to the battery mode: it
// writes it where Home Assistant shows something else. While it is not normal, evcc
// holds it (owned); back at normal it is written once and released. A request set by
// hand stays while evcc holds nothing. Called with s.out held.
func (site *Site) applyBydRequest() {
	if site.omniType() {
		return
	}

	s := site.peak()
	want := site.bydWant()

	s.mu.Lock()
	entity, owned := s.requestEntity, s.owned
	warn := want != requestNormal && entity == "" && !s.requestWarned
	s.requestWarned = entity == "" && (s.requestWarned || warn)
	s.mu.Unlock()

	if warn {
		site.log.WARN.Println("battery mode: battery type BYD needs the request entity of evcc Peak Fronius for grid charging and holding (Lastmanagement-Details → Peak Shaving)")
	}

	if entity == "" || want == requestNormal && !owned {
		return
	}

	conn, err := site.haConnection()
	if !site.logWrite("byd connect", "connect", err) {
		return
	}

	state, _, err := omniRead(conn, entity)
	if !site.logWrite("byd request read", "read "+entity, err) {
		return
	}

	// from here evcc asks, also into a request set by hand
	if want != requestNormal {
		site.setPeakOwned(true)
	}

	if state != want &&
		!site.logWrite("byd request", "request "+want, conn.CallSelectService(entity, want)) {
		return
	}

	if want == requestNormal {
		site.setPeakOwned(false)
	}
}

// releaseRequest sets the request back to normal, if evcc holds it, and reports
// whether it is released. A request not readable (not available, timeout, error) is
// not written to, see omniSwitchOff: it stays held and the next cycle tries again.
// Called with s.out held.
func (site *Site) releaseRequest() bool {
	if site.omniType() || !site.peakOwned() {
		return true
	}

	s := site.peak()

	s.mu.Lock()
	entity := s.requestEntity
	s.mu.Unlock()

	// nothing left to hand back to, owned would block every change for good
	if entity == "" {
		site.setPeakOwned(false)
		return true
	}

	conn, err := site.haConnection()
	if !site.logWrite("byd connect", "connect", err) {
		return false
	}

	state, _, err := omniRead(conn, entity)
	switch {
	case notFound(err):
		site.log.WARN.Printf("byd request: %s not found, nothing to set back", entity)
	case !site.logWrite("byd request read", "read "+entity, err):
		return false
	case state != requestNormal && !site.logWrite("byd request", "request "+requestNormal, conn.CallSelectService(entity, requestNormal)):
		return false
	}

	site.setPeakOwned(false)

	return true
}

//
// api
//

// GetPeakShavingRequestEntity returns the select of the request
func (site *Site) GetPeakShavingRequestEntity() string {
	s := site.peak()

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.requestEntity
}

// SetPeakShavingRequestEntity sets the select of the request of the integration
// evcc Peak Fronius. It has to offer the options normal, grid_charge and hold, as
// written here. A request evcc holds is set back to normal first.
func (site *Site) SetPeakShavingRequestEntity(entity string) error {
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
		for _, o := range bydRequests {
			if !slices.Contains(options, o) {
				return fmt.Errorf("%s has to offer the options %s, it offers %s", entity, strings.Join(bydRequests, ", "), strings.Join(options, ", "))
			}
		}
	}

	s := site.peak()

	s.mu.Lock()
	changed := s.requestEntity != entity
	s.mu.Unlock()

	if !changed {
		return nil
	}

	// a request evcc holds is set back before it is forgotten
	s.out.Lock()
	if !site.releaseRequest() {
		s.out.Unlock()
		return errHandBack
	}
	s.mu.Lock()
	s.requestEntity = entity
	s.requestWarned = false
	s.mu.Unlock()
	s.out.Unlock()

	site.log.DEBUG.Println("set peak shaving request entity:", entity)
	settings.SetString(keys.PeakShavingRequestEntity, entity)
	site.publish(keys.PeakShavingRequestEntity, entity)

	return nil
}

// migratePeakEntity ends the old way of the battery type BYD, where evcc wrote the
// discharge power into a number entity (peakShavingEntity). If evcc still held the
// battery back through it (peakShavingOwned), the free value goes there once, a
// failure is logged and not repeated. Then the entity and the mark are deleted. For
// the battery type Marstek the mark means the manual control and stays. The stored
// free value goes with it, see dropFreeValue. Called at the start, after the
// settings are back.
func (site *Site) migratePeakEntity() {
	defer site.dropFreeValue()

	entity, err := settings.String(keys.PeakShavingEntity)
	if err != nil {
		return
	}

	s := site.peak()

	s.mu.Lock()
	byd, owned := s.batteryType != batteryTypeMarstek, s.owned
	s.mu.Unlock()

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
