package core

// Custom extension: the external peak control, shared by the battery types.
//
// For the battery types Marstek (Omnibattery) and BYD (the Home Assistant
// integration evcc Peak Fronius) evcc does not regulate the discharge itself. A
// controller in Home Assistant does, quickly; evcc is its input. evcc writes
//
//   - the soc threshold: the reserve, the maximum while the optimizer holds,
//     see site_peak_omni_hold.go,
//   - the limit: the grid power allowed in the running 15 minute window,
//   - the switch: on while peak shaving runs.
//
// and keeps the marks of what it turned on (protOwned) to release it again. This
// file holds that part and the settings of the three entities; what is particular
// to a battery type is in site_peak_omni.go (Marstek).

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/core/peak"
	"github.com/evcc-io/evcc/db/settings"
	"github.com/evcc-io/evcc/util/homeassistant"
	"github.com/evcc-io/evcc/util/request"
)

// errHandBack refuses a change while what evcc holds could not be handed back,
// the battery would otherwise stay held with nothing left to release it
var errHandBack = errors.New("could not hand the battery back, see the log")

// configured reports whether peak shaving has what it needs to run: the three
// entities of the external peak control. Called with mu held.
func (s *peakState) configured() bool {
	return s.protSwitch != "" && s.protLimit != "" && s.protSocSet != nil
}

// extName names the battery type in the log
func (site *Site) extName() string {
	if site.omniType() {
		return batteryTypeMarstek
	}

	return batteryTypeBYD
}

// minBydLimit is the lowest limit written to the integration of the battery type
// BYD: it takes 0 for a limit that ran out, see bydLimitRule
const minBydLimit = 100.0

// peakConfigured reports whether peak shaving has what it needs to run, see
// peakState.configured
func (site *Site) peakConfigured() bool {
	s := site.peak()

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.configured()
}

// applyOmniProtection brings the external peak control to what peak shaving
// wants: while it runs, the soc threshold is the reserve (Marstek: at least 20 %),
// the limit the grid power allowed in the running 15 minute window (without meter
// values the peak limit, following the peak included) and the switch on.
// Otherwise the switch is turned off, but only if evcc turned it on (protOwned);
// limit and threshold stay. Called with s.out held.
func (site *Site) applyOmniProtection() {
	s := site.peak()

	s.mu.Lock()
	want := s.enabled && s.configured() && site.batteryConfigured()
	owned, raised, sw, limitEntity, socSet := s.protOwned, s.holdSoc > 0, s.protSwitch, s.protLimit, s.protSocSet
	threshold := s.effectiveReserve()

	// the window of the allowed power is the running one: it is renewed by a cycle
	// with meter values only, and past its end (a meter failing at the end of the
	// window, up to metersLost) it would go on with the allowed power frozen in
	// the old window, up to twice the limit
	limit, basis := s.limit, omniLimitBasis(false, s.limit)
	if !s.metersLost && s.window.Start.Equal(s.clock.Now().Truncate(peak.Window)) {
		limit, basis = s.window.Allowed, omniLimitBasis(true, s.limit)
	}
	s.mu.Unlock()

	if !want {
		if owned || raised {
			site.releaseProt()
		}

		return
	}

	// the maximum while the optimizer holds, see site_peak_omni_hold.go
	threshold, hold, ok := site.omniThreshold(threshold)
	if !ok {
		return
	}

	// threshold and limit first, so the peak shaving never starts with old values
	if !site.omniWriteNumber("peak shaving soc threshold", "%", socSet, threshold) {
		return
	}
	if !hold {
		site.setHoldSoc(0)
	}
	conn, err := site.haConnection()
	if !site.logWrite(site.extName()+" connect", "connect", err) {
		return
	}

	if !site.omniWriteLimit(conn, limitEntity, limit, basis) {
		return
	}

	state, _, err := omniRead(conn, sw)
	if !site.logWrite(site.extName()+" peak shaving switch read", "read "+sw, err) {
		return
	}

	if !strings.EqualFold(state, "on") &&
		!site.logWrite(site.extName()+" peak shaving switch", "switch on", conn.CallSwitchService(sw, true)) {
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

// omniWriteLimit writes the limit, in whole watts, fitted to the entity and
// rounded down, when it is below the limit in the entity or at least
// minOmniLimitRise above it. Below the entity's minimum the minimum is written:
// Omnibattery takes no lower limit. An entity that cannot be read is not written,
// its minimum and step are unknown; the step fails and the next cycle tries again.
// The battery type BYD never gets less than minBydLimit and gets the limit again
// when the entity shows 0, see bydLimitRule.
// basis says in the log why the limit is what it is, see omniLimitBasis.
func (site *Site) omniWriteLimit(conn *homeassistant.Connection, entity string, limit float64, basis string) bool {
	r, current, err := numberState(conn, entity)
	if !site.logWrite("peak shaving limit read", "read "+entity, err) {
		return false
	}

	floor, zero := site.bydLimitRule()

	limit = r.Fit(math.Floor(max(limit, floor)), false)
	if v, perr := strconv.ParseFloat(current, 64); perr == nil && limit >= v && limit < v+minOmniLimitRise && !(zero && v == 0) {
		return true
	}

	if !site.logWrite("peak shaving limit", fmt.Sprintf("write %.0fW", limit), conn.CallNumberService(entity, limit)) {
		return false
	}

	if basis != "" {
		basis = " (" + basis + ")"
	}
	site.log.DEBUG.Printf("peak shaving limit: %.0fW%s", limit, basis)

	return true
}

// omniLimitBasis explains the limit written to Omnibattery, which differs from
// the peak limit set: within a window it is the grid power still allowed for the
// rest of it, so it is above the peak limit while less than the budget was drawn
// and below it after a surplus draw.
func omniLimitBasis(windowed bool, peakLimit float64) string {
	if windowed {
		return fmt.Sprintf("allowed for the rest of the 15 min window, peak limit %.0fW", peakLimit)
	}

	return fmt.Sprintf("no window, peak limit %.0fW", peakLimit)
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
		site.persistHeld()
	}
}

// releaseProt turns Omnibattery's peak shaving off, if evcc turned it on, and
// reports whether it is released. Limit and threshold stay, only a threshold the
// optimizer's hold raised goes back first. Called with s.out held.
func (site *Site) releaseProt() bool {
	if !site.restoreOmniThreshold() {
		return false
	}

	s := site.peak()

	s.mu.Lock()
	owned, sw := s.protOwned, s.protSwitch
	s.mu.Unlock()

	if !owned {
		return true
	}

	if sw != "" && !site.omniSwitchOff(site.extName()+" peak shaving switch", sw) {
		return false
	}

	site.setProtOwned(false)

	return true
}

// omniSwitchOff switches the entity off unless it is already, and reports
// whether it is. An entity not readable (not available, timeout, error) is not
// written to: Home Assistant answers a call to such an entity with success and does
// nothing, so the release would count as done while the switch stays on. What evcc
// holds stays held and the next cycle tries again. Only an entity Home Assistant
// does not know (404) counts as off: nothing is left to switch off, and the release
// would otherwise never land and block every change of the entities.
func (site *Site) omniSwitchOff(name, entity string) bool {
	conn, err := site.haConnection()
	if !site.logWrite(site.extName()+" connect", "connect", err) {
		return false
	}

	state, _, err := omniRead(conn, entity)
	if notFound(err) {
		site.log.WARN.Printf("%s: %s not found, nothing to switch off", name, entity)
		return true
	}

	if !site.logWrite(name+" read", "read "+entity, err) {
		return false
	}

	if strings.EqualFold(state, "off") {
		return true
	}

	return site.logWrite(name, "switch off", conn.CallSwitchService(entity, false))
}

// releaseOutput writes the value that hands an output back, like writeOutput, and
// reports whether it is handed back. Home Assistant not knowing the entity (404
// when reading it, deleted or renamed) counts as handed back: nothing is left to
// hand back, and the release would otherwise never land and keep the manual
// control of a Marstek on, or block every change of the entities. Any other
// failure leaves it held, the next cycle tries again.
func (site *Site) releaseOutput(name string, set func(float64) error, value float64) bool {
	if set == nil {
		return false
	}

	return site.writeOutput(name, func(v float64) error {
		err := set(v)

		if notFound(err) {
			site.log.WARN.Printf("%s: %v, nothing to hand back", name, err)
			return nil
		}

		return err
	}, value)
}

// notFound reports whether Home Assistant does not know the entity
func notFound(err error) bool {
	var se *request.StatusError

	return errors.As(err, &se) && se.StatusCode() == http.StatusNotFound
}

// omniRead reads the state and the options of a switch or select entity. As
// GetState does, it reports an entity that is unknown or not available as
// api.ErrNotAvailable: a call to it is answered with success and does nothing.
func omniRead(conn *homeassistant.Connection, entity string) (string, []string, error) {
	var res struct {
		State      string `json:"state"`
		Attributes struct {
			Options []string `json:"options"`
		} `json:"attributes"`
	}

	err := conn.GetJSON(fmt.Sprintf("%s/api/states/%s", conn.URI(), url.PathEscape(entity)), &res)
	if err == nil && notAvailable(res.State) {
		err = api.ErrNotAvailable
	}

	return res.State, res.Attributes.Options, err
}

// errProtMissing refuses peak shaving without what it needs
var (
	errProtMissing = errors.New("no peak shaving switch, limit or soc threshold entity configured")
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
	// a threshold the optimizer's hold raised goes back on the old entity, see
	// site_peak_omni_hold.go
	if set != nil && !site.restoreOmniThreshold() {
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

// protMissing reports whether one of the three entities of the external peak
// shaving is missing
func (site *Site) protMissing() bool {
	s := site.peak()

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.protSwitch == "" || s.protLimit == "" || s.protSoc == ""
}

// omniProtRemoved turns peak shaving off once it lost one of the entities of the
// external peak shaving: it cannot do anything without
func (site *Site) omniProtRemoved(entity string) error {
	if entity != "" || !site.protMissing() {
		return nil
	}

	return site.SetPeakShaving(false)
}
