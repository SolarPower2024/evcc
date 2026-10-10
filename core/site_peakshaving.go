package core

// Custom extension: battery peak shaving for a demand charge (Leistungspreis).
//
// The battery's lower soc range is held back as a reserve. Above the reserve the
// battery runs ordinary self-consumption and the controller is told it may
// discharge freely. Below it, the battery is only allowed to cover what exceeds
// the peak limit, so the reserve is spent on demand peaks rather than base load.
//
// The limit applies to the average of the clock-aligned 15 minute window, which is
// what the demand charge is billed on, not to the momentary grid power: energy
// not drawn earlier in the window may be drawn later, so a short spike is only
// covered when the window as a whole would end above the limit. The energy drawn
// comes from the grid meter's import counter, else from a Home Assistant energy
// sensor, else from the grid power of each cycle.
//
// evcc only computes the setpoint and writes it to a number entity; the actual
// discharge is done by the Home Assistant automation reading that entity. The
// window, the allowed power and the setpoint are computed in core/peak; this
// file feeds it and handles the settings, Home Assistant and the battery mode.

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/benbjohnson/clock"
	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/core/lm"
	"github.com/evcc-io/evcc/core/peak"
	"github.com/evcc-io/evcc/db/settings"
	"github.com/evcc-io/evcc/util"
	"github.com/evcc-io/evcc/util/config"
	"github.com/evcc-io/evcc/util/homeassistant"
	"github.com/evcc-io/evcc/util/request"
)

const (
	defaultPeakLimit   = 5000.0 // W
	defaultPeakReserve = 30.0   // %

	minPeakLimit  = 2000.0 // W
	maxPeakLimit  = 20000.0
	peakLimitStep = 500.0
)

// peakState is the runtime state of peak shaving
type peakState struct {
	once  sync.Once
	mu    sync.Mutex
	out   sync.Mutex // a write to an output against swapping that output
	clock clock.Clock

	enabled     bool    // peak shaving switch
	limit       float64 // grid peak limit in W
	reserve     float64 // soc below which the battery is reserved for peaks
	entity      string  // Home Assistant number entity receiving the setpoint
	chargePower float64 // assumed grid charge power in W, 0 = derive it
	circuit     string  // circuit the battery draws from, empty = not managed

	shaving    bool // hysteresis state: below the reserve
	covering   bool // covering a peak right now, for the event log
	handedBack bool // free value written since the last setpoint, nothing more to send while off

	// owned is set once evcc wrote a value that holds the battery back (anything but
	// the free value) and cleared again once it handed back. Only then there is
	// something to hand back: an entity evcc never controlled is left alone.
	// Persisted, so a restart in the middle of a control still hands back.
	owned    bool
	writeErr map[string]string // the last error logged per output, see writeOutput

	// battery type Marstek (Omnibattery), see site_peak_omni.go: evcc also switches
	// the battery to manual control while it controls
	batteryType   string  // "" and byd: only the power is written, marstek
	manualEntity  string  // switch of the manual control
	modeEntity    string  // select of the forced mode
	omniPeakValue float64 // the discharge setpoint of this cycle, written by applyOmni
	omniShown     string  // the forced mode published, empty = not controlling

	conn *homeassistant.Connection // shared, built on first use

	demand      float64   // grid demand without the battery in W, from the last cycle
	chargePause time.Time // grid charging gives way to peak shaving until then

	updated    time.Time // last cycle with meter values, see peakCheckMeters
	metersLost bool      // the meters failed for longer than peak.MaxGap

	set func(float64) error // resolved from config

	// follow the peak, see site_peak_follow.go
	follow       bool
	followBuffer float64 // W below the month's peak
	followBase   float64 // W, the limit set by hand

	tariff peakTariff // capacity tariff, see site_peak_tariff.go

	// grid charge power control: the battery charges at a power evcc writes to
	// this entity, sized to stay below the peak limit and within the circuit
	chargeEntity   string
	chargeSet      func(float64) error
	chargeSetpoint float64 // last computed setpoint in W, 0 = not charging

	meter  peak.Meter // the running 15 minute window
	window peak.State // its state after the last sample

	// energy counters for the window, in the order they are used
	gridEnergy   *float64                // grid meter import in kWh, from this cycle
	energyEntity string                  // Home Assistant energy sensor
	energyGet    func() (float64, error) // resolved from energyEntity, kWh

	months      []peakMonth // statistics, newest first, see site_peak_stats.go
	monthsDirty bool
}

// peak returns the peak shaving state, applying defaults on first use
func (site *Site) peak() *peakState {
	site.custom.peak.once.Do(func() {
		site.custom.peak.limit = defaultPeakLimit
		site.custom.peak.reserve = defaultPeakReserve
		if site.custom.peak.clock == nil {
			site.custom.peak.clock = clock.New()
		}
	})
	return &site.custom.peak
}

// restorePeakSettings restores the persisted peak shaving settings and resolves
// the output plugin
func (site *Site) restorePeakSettings() {
	s := site.peak()

	if v, err := settings.Float(keys.PeakShavingLimit); err == nil {
		s.mu.Lock()
		s.limit = v
		s.mu.Unlock()
	}
	if v, err := settings.Float(keys.PeakShavingReserve); err == nil {
		s.mu.Lock()
		s.reserve = v
		s.mu.Unlock()
	}
	if v, err := settings.Bool(keys.PeakShaving); err == nil {
		s.mu.Lock()
		s.enabled = v
		s.mu.Unlock()
	}

	if v, err := settings.String(keys.PeakShavingEntity); err == nil {
		s.mu.Lock()
		s.entity = v
		s.mu.Unlock()
	}
	if v, err := settings.Float(keys.PeakShavingChargePower); err == nil {
		s.mu.Lock()
		s.chargePower = v
		s.mu.Unlock()
	}
	if v, err := settings.String(keys.PeakShavingCircuit); err == nil {
		s.mu.Lock()
		s.circuit = v
		s.mu.Unlock()
	}
	if v, err := settings.String(keys.PeakShavingChargeEntity); err == nil {
		s.mu.Lock()
		s.chargeEntity = v
		s.mu.Unlock()
	}
	if v, err := settings.String(keys.PeakShavingEnergyEntity); err == nil {
		s.mu.Lock()
		s.energyEntity = v
		s.mu.Unlock()
	}

	if v, err := settings.String(keys.PeakShavingBatteryType); err == nil {
		s.mu.Lock()
		s.batteryType = v
		s.mu.Unlock()
	}
	if v, err := settings.String(keys.PeakShavingManualEntity); err == nil {
		s.mu.Lock()
		s.manualEntity = v
		s.mu.Unlock()
	}
	if v, err := settings.String(keys.PeakShavingModeEntity); err == nil {
		s.mu.Lock()
		s.modeEntity = v
		s.mu.Unlock()
	}
	if v, err := settings.Bool(keys.PeakShavingOwned); err == nil {
		s.mu.Lock()
		s.owned = v
		s.mu.Unlock()
	}

	// a start during a meter outage counts as without meter values from now on,
	// see peakCheckMeters, rather than keeping the setpoint written before it
	s.mu.Lock()
	s.updated = s.clock.Now()
	s.mu.Unlock()

	if err := site.rebuildPeakSetter(); err != nil {
		site.log.ERROR.Printf("peak shaving: %v", err)
	}
	if err := site.rebuildChargeSetter(); err != nil {
		site.log.ERROR.Printf("grid charge power: %v", err)
	}
	if err := site.rebuildEnergyGetter(); err != nil {
		site.log.ERROR.Printf("peak shaving energy: %v", err)
	}

	site.restorePeakMonths()
	site.restorePeakFollow()
	site.fillPeakBaselines()
	site.restorePeakTariff()
	site.publishPeakSettings()
	site.publishLmPriorities()
}

// peakURI returns the Home Assistant endpoint. Running as an add-on, the
// supervisor provides both the endpoint and the token, so nothing has to be
// configured.
func (site *Site) peakURI() (string, error) {
	if os.Getenv(homeassistant.SupervisorToken) != "" {
		return homeassistant.SupervisorURI, nil
	}

	return "", errors.New("no Home Assistant connection: only available in the Home Assistant add-on")
}

// rebuildPeakSetter resolves the output from the configured entity
func (site *Site) rebuildPeakSetter() error {
	s := site.peak()

	s.mu.Lock()
	entity := s.entity
	s.mu.Unlock()

	if entity == "" {
		s.mu.Lock()
		s.set = nil
		s.mu.Unlock()

		return nil
	}

	set, err := site.numberSetter(entity, true) // a discharge setpoint has to cover the peak
	if err != nil {
		return err
	}

	s.mu.Lock()
	s.set = set
	s.handedBack = false
	s.mu.Unlock()

	return nil
}

// rebuildChargeSetter resolves the grid charge power output from its entity
func (site *Site) rebuildChargeSetter() error {
	s := site.peak()

	s.mu.Lock()
	entity := s.chargeEntity
	s.mu.Unlock()

	var set func(float64) error

	if entity != "" {
		var err error
		if set, err = site.numberSetter(entity, false); err != nil { // a charge power has to stay within the limits
			return err
		}
	}

	s.mu.Lock()
	s.chargeSet = set
	s.mu.Unlock()

	return nil
}

// rebuildEnergyGetter resolves the energy sensor
func (site *Site) rebuildEnergyGetter() error {
	s := site.peak()

	s.mu.Lock()
	entity := s.energyEntity
	s.mu.Unlock()

	var get func() (float64, error)

	if entity != "" {
		conn, err := site.haConnection()
		if err != nil {
			return err
		}
		get = func() (float64, error) { return conn.GetFloatState(entity) }
	}

	s.mu.Lock()
	s.energyGet = get
	s.mu.Unlock()

	return nil
}

func (site *Site) haConnection() (*homeassistant.Connection, error) {
	s := site.peak()

	s.mu.Lock()
	conn := s.conn
	s.mu.Unlock()

	if conn != nil {
		return conn, nil
	}

	uri, err := site.peakURI()
	if err != nil {
		return nil, err
	}

	conn, err = homeassistant.NewConnection(util.NewLogger("peakshaving"), uri, "", false)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	s.conn = conn
	s.mu.Unlock()

	return conn, nil
}

// numberSetter returns a setter writing to a Home Assistant number entity. Each
// value is fitted to the entity's min, max and step first, see peak.Range.Fit;
// up rounds to the next step above. These are read on every write, as an
// integration may only learn them from the device after it started. A value the
// entity already holds, within the write tolerance, is not written again, see
// peak.Range.Unchanged: a device may store every write. As the comparison is
// against the entity, a value changed by hand is still corrected.
func (site *Site) numberSetter(entity string, up bool) (func(float64) error, error) {
	conn, err := site.haConnection()
	if err != nil {
		return nil, err
	}

	return func(val float64) error {
		r, current, err := numberState(conn, entity)
		if err != nil {
			return conn.CallNumberService(entity, val)
		}

		val, write := numberWrite(r, current, val, up, site.peakWriteTolerance())
		if !write {
			return nil
		}

		return conn.CallNumberService(entity, val)
	}, nil
}

// numberWrite returns the value fitted to the entity and whether it needs writing
// given the entity's current state
func numberWrite(r peak.Range, current string, val float64, up bool, tolerance float64) (float64, bool) {
	val = r.Fit(val, up)

	// an unavailable entity has no current value and gets the write
	v, err := strconv.ParseFloat(current, 64)

	return val, err != nil || !r.Unchanged(v, val, tolerance)
}

// peakWriteTolerance is the smallest change written to a peak shaving entity
func (site *Site) peakWriteTolerance() float64 {
	if v := site.advanced().WriteTolerance; v != nil {
		return *v
	}
	return 0
}

// numberState reads the min, max and step attributes and the state of a number
// entity
func numberState(conn *homeassistant.Connection, entity string) (peak.Range, string, error) {
	var res struct {
		State      string `json:"state"`
		Attributes struct {
			Min  float64 `json:"min"`
			Max  float64 `json:"max"`
			Step float64 `json:"step"`
		} `json:"attributes"`
	}

	uri := fmt.Sprintf("%s/api/states/%s", conn.URI(), url.PathEscape(entity))
	err := conn.GetJSON(uri, &res)

	return peak.Range(res.Attributes), res.State, err
}

func (site *Site) publishPeakSettings() {
	s := site.peak()

	s.mu.Lock()
	enabled, limit, reserve, entity, charge, circuit := s.enabled, s.limit, s.reserve, s.entity, s.chargePower, s.circuit
	chargeEntity, energyEntity := s.chargeEntity, s.energyEntity
	s.mu.Unlock()

	site.publish(keys.PeakShaving, enabled)
	site.publish(keys.PeakShavingLimit, limit)
	site.publish(keys.PeakShavingReserve, reserve)
	site.publish(keys.PeakShavingEntity, entity)
	site.publish(keys.PeakShavingChargePower, charge)
	site.publish(keys.PeakShavingCircuit, circuit)
	site.publish(keys.PeakShavingChargeEntity, chargeEntity)
	site.publish(keys.PeakShavingEnergyEntity, energyEntity)

	site.publishOmniSettings()
	site.publishChargePower()
}

// publishChargePower reports the charge power actually in use and where it came
// from, so the assumption the grid charge gate makes is visible in the ui
func (site *Site) publishChargePower() {
	effective, source := site.lmBatteryChargePower()

	site.publish(keys.PeakShavingChargePowerEffective, effective)
	site.publish(keys.PeakShavingChargePowerSource, source)
}

// peakFreeValue returns the value signalling unrestricted discharge
func (site *Site) peakFreeValue() float64 {
	if v := site.advanced().FreeValue; v != nil {
		return *v
	}
	return peak.DefaultFreeValue
}

// peakFreeze returns the minute of the window from which the allowed power no
// longer grows
func (site *Site) peakFreeze() time.Duration {
	if v := site.advanced().PeakFreeze; v != nil {
		return time.Duration(*v) * time.Minute
	}
	return peak.DefaultFreeze
}

// peakCap returns the maximum allowed power as a multiple of the limit
func (site *Site) peakCap() float64 {
	if v := site.advanced().PeakCap; v != nil {
		return *v
	}
	return peak.DefaultCap
}

func (site *Site) peakHysteresis() float64 {
	if v := site.advanced().Hysteresis; v != nil {
		return *v
	}
	return peak.DefaultHysteresis
}

// peakShavingActive reports whether the battery is currently held back for peaks.
// Used to keep the battery in normal mode so the discharge controller is not
// blocked, see updateBatteryModePeakAware.
func (site *Site) peakShavingActive() bool {
	s := site.peak()

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.enabled && s.shaving
}

// updatePeakShaving computes the battery power required to stay below the peak
// limit and writes it to the configured number entity. Called once per cycle.
func (site *Site) updatePeakShaving(state siteState) {
	s := site.peak()
	defer site.savePeakMonths()

	s.mu.Lock()
	s.updated = s.clock.Now()
	recovered := s.metersLost
	s.metersLost = false
	s.mu.Unlock()

	if recovered {
		site.log.INFO.Println("peak shaving: meters back")
	}

	site.updatePeakWindow(state.gridPower, state.battery.Power)
	site.updatePeakFollow()
	site.applyCircuitLimits() // load management switch and follow circuit, see site_lm_switch.go

	s.mu.Lock()
	enabled, limit, reserve, set, allowed := s.enabled, s.limit, s.reserve, s.set, s.window.Allowed
	// read by peakPausesGridCharge later in the same cycle
	s.demand = state.gridPower + state.battery.Power
	s.mu.Unlock()

	if !enabled || set == nil || !site.batteryConfigured() {
		// don't leave a stale reserve state behind: peakShavingActive gates grid
		// charging and the battery mode, and must not keep doing so once peak
		// shaving stopped running
		s.mu.Lock()
		s.shaving = false
		s.covering = false
		s.mu.Unlock()

		site.publish(keys.PeakShavingActive, false)

		// hand control back once, retried until the write lands
		site.handBackPeak()

		return
	}

	soc := site.GetBatterySoc()
	hyst := site.peakHysteresis()

	s.mu.Lock()
	// below the reserve the battery is for peaks only
	s.shaving = peak.Reserved(s.shaving, soc, reserve, hyst)
	shaving := s.shaving
	s.mu.Unlock()

	value := site.peakFreeValue()

	switch {
	// no discharging while the battery charges from the grid. A peak pauses the
	// charging in this same cycle, see peakPausesGridCharge, so the value then
	// falls through to the regular one right away.
	case site.GetBatteryMode() == api.BatteryCharge && state.gridPower+state.battery.Power <= limit:
		value = 0

	case shaving:
		value = peak.Setpoint(state.gridPower, state.battery.Power, allowed)
	}

	// log the start of a peak, not every cycle of it
	s.mu.Lock()
	covering := shaving && value > 0
	started := covering && !s.covering
	s.covering = covering
	if started {
		s.recordPeakIntervention(s.clock.Now())
	}
	s.mu.Unlock()

	if started {
		site.lmm().AddEvent(lm.Event{At: s.clock.Now(), Type: lm.EventPeak, A: state.gridPower + state.battery.Power, B: limit})
	}

	// published explicitly rather than left for the ui to infer from the value:
	// a setpoint can legitimately equal the free value, e.g. a 15kW demand
	// against a 5kW limit asks for exactly 10000W
	site.publish(keys.PeakShavingActive, shaving)
	site.publish(keys.PeakShavingPower, value)

	// the battery type Marstek is switched and written at the end of the cycle,
	// see applyOmni
	if site.omniType() {
		s.mu.Lock()
		s.omniPeakValue = value
		s.mu.Unlock()
	} else {
		site.writePeakValue(value)
	}

	// also covers a setpoint written while switching off, which then gets
	// replaced by the free value in the next cycle
	s.mu.Lock()
	s.handedBack = false
	s.mu.Unlock()
}

// writePeakValue writes the setpoint
func (site *Site) writePeakValue(value float64) bool {
	s := site.peak()

	s.out.Lock()
	defer s.out.Unlock()

	s.mu.Lock()
	set := s.set
	s.mu.Unlock()

	if !site.writeOutput("peak shaving", set, value) {
		return false
	}

	site.setPeakOwned(value != site.peakFreeValue())

	return true
}

// setPeakOwned records whether evcc holds the battery back through the entity,
// see peakState.owned
func (site *Site) setPeakOwned(owned bool) {
	s := site.peak()

	s.mu.Lock()
	changed := s.owned != owned
	s.owned = owned
	s.mu.Unlock()

	if changed {
		settings.SetBool(keys.PeakShavingOwned, owned)
	}
}

// peakOwned reports whether evcc holds the battery back, see peakState.owned
func (site *Site) peakOwned() bool {
	s := site.peak()

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.owned
}

// handBackPeak writes the free value, but only if evcc held the battery back
// before: an entity evcc never controlled is not touched. While peak shaving is
// off nothing else is sent, a single write is enough.
func (site *Site) handBackPeak() {
	// the battery type Marstek is released by applyOmni at the end of the cycle,
	// the free value is never written to it
	if site.omniType() {
		return
	}

	s := site.peak()

	s.mu.Lock()
	done := s.handedBack
	s.mu.Unlock()

	if done || !site.peakOwned() || !site.writePeakValue(site.peakFreeValue()) {
		return
	}

	s.mu.Lock()
	s.handedBack = true
	s.mu.Unlock()
}

// writeChargeValue writes the grid charge power setpoint
func (site *Site) writeChargeValue(value float64) {
	s := site.peak()

	s.out.Lock()
	defer s.out.Unlock()

	s.mu.Lock()
	set := s.chargeSet
	s.chargeSetpoint = value
	s.mu.Unlock()

	site.publish(keys.PeakShavingChargeSetpoint, value)

	// the battery type Marstek is written by applyOmni, after the manual control
	// and the mode
	if site.omniType() {
		return
	}

	site.writeOutput("grid charge power", set, value)
}

// writeOutput writes a value through set and reports whether it landed. It is
// called every cycle; the setter compares with the entity and skips an unchanged
// value, so an entity changed by hand, by an automation or by a Home Assistant
// restart is still corrected in the next cycle.
func (site *Site) writeOutput(name string, set func(float64) error, value float64) bool {
	if set == nil {
		return false
	}

	err := set(value)
	if !site.logWrite(name, fmt.Sprintf("write %.0fW", value), err) {
		return false
	}

	site.log.DEBUG.Printf("%s: %.0fW", name, value)

	return true
}

// logWrite reports the result of a write to an output and returns whether it
// landed. The same error is logged once as an error and then at debug level until
// a write succeeds again, so a rejected write does not fill the log with one line
// per cycle.
func (site *Site) logWrite(name, what string, err error) bool {
	s := site.peak()

	s.mu.Lock()
	last, failed := s.writeErr[name]

	var text string
	if err != nil {
		text = writeErrorText(err)
		if s.writeErr == nil {
			s.writeErr = make(map[string]string)
		}
		s.writeErr[name] = text
	} else {
		delete(s.writeErr, name)
	}
	s.mu.Unlock()

	switch {
	case err == nil && failed:
		site.log.INFO.Printf("%s: %s ok again", name, what)
	case err == nil:
	case failed && text == last:
		site.log.DEBUG.Printf("%s: %s: %s", name, what, text)
	default:
		site.log.ERROR.Printf("%s: %s: %s", name, what, text)
	}

	return err == nil
}

// maxErrorBody is the length of an error response shown in the log
const maxErrorBody = 200

// writeErrorText returns the text of a failed write. It appends the response of
// the server, shortened to one line. Home Assistant answers an exception of an
// integration with a general 500 only, the reason is in its own log.
func writeErrorText(err error) string {
	text := err.Error()

	var se *request.StatusError
	if !errors.As(err, &se) {
		return text
	}

	if body := strings.Join(strings.Fields(string(se.Body())), " "); body != "" {
		if r := []rune(body); len(r) > maxErrorBody {
			body = string(r[:maxErrorBody]) + "..."
		}
		text += ": " + body
	}

	if resp := se.Response(); resp != nil && resp.Request != nil && se.StatusCode() == http.StatusInternalServerError &&
		strings.Contains(resp.Request.URL.Path, "/api/services/") {
		text += " (details in the Home Assistant log)"
	}

	return text
}

// peakEnergy returns the grid import counter in kWh and where it came from: the
// grid meter, else the Home Assistant sensor. Without either, the source is the
// grid power.
func (site *Site) peakEnergy() (float64, string) {
	s := site.peak()

	s.mu.Lock()
	meter, get, entity := s.gridEnergy, s.energyGet, s.energyEntity
	s.gridEnergy = nil // only valid for the cycle it was read in
	s.mu.Unlock()

	if meter != nil {
		return *meter, peak.SourceMeter
	}

	if get != nil {
		v, err := get()
		if err == nil {
			return v, peak.SourceEntity
		}
		site.log.WARN.Printf("peak shaving: energy %s: %v, using the grid power", entity, err)
	}

	return 0, peak.SourcePower
}

// setPeakGridEnergy takes the grid meter's import counter of this cycle, nil if
// the meter has none
func (site *Site) setPeakGridEnergy(kWh *float64) {
	s := site.peak()

	s.mu.Lock()
	defer s.mu.Unlock()

	s.gridEnergy = kWh
}

// updatePeakWindow meters the running 15 minute window with the energy source of
// this cycle and keeps the grid power it allows. A completed window goes into the
// monthly statistics.
func (site *Site) updatePeakWindow(gridPower, batteryPower float64) {
	s := site.peak()
	energy, source := site.peakEnergy()
	set := peak.Settings{Freeze: site.peakFreeze(), Cap: site.peakCap()}

	s.mu.Lock()
	set.Limit = s.limit
	w := s.meter.Update(peak.Sample{Now: s.clock.Now(), GridPower: gridPower, BatteryPower: batteryPower, Energy: energy, Source: source}, set)
	if c := w.Completed; c != nil {
		s.recordPeakWindow(c.Start, c.DrawnWs, c.DemandWs)
	}
	s.window = w
	s.mu.Unlock()

	if w.StaleNow {
		site.log.WARN.Printf("peak shaving: the %s energy counter stopped updating, using the grid power until the window ends", source)
	}

	site.publish(keys.PeakShavingWindowAvg, w.Avg)
	site.publish(keys.PeakShavingAllowed, w.Allowed)
	site.publish(keys.PeakShavingSource, w.Source)
	site.publish(keys.PeakShavingWindowEnd, w.End())
}

// peakPausesGridCharge reports whether grid charging has to give way to peak
// shaving. While the demand without the battery is above the peak limit, the
// battery is needed to cover it, and charging it from the grid at the same time
// would only add to the peak.
//
// The charge power itself is deliberately not counted against the peak limit:
// the charger alone may well draw more than the limit, and whether it fits is
// the circuit's call, see batteryCircuitAllows. After a peak, charging stays
// off for the hold-off, so a demand hovering around the limit does not flip
// the battery between charging and discharging every cycle.
func (site *Site) peakPausesGridCharge() bool {
	s := site.peak()

	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.enabled || s.set == nil {
		return false
	}

	now := s.clock.Now()

	// without meter values the demand is unknown, so charging could create a peak
	if s.metersStale(now) {
		return true
	}

	if s.demand > s.limit {
		if !now.Before(s.chargePause) {
			site.log.DEBUG.Printf("battery grid charge: paused, demand %.0fW exceeds the %.0fW peak limit", s.demand, s.limit)
			site.lmm().AddEvent(lm.Event{At: now, Type: lm.EventGridChargePaused, A: s.demand, B: s.limit})
		}
		s.chargePause = now.Add(site.lmHoldOff())
		return true
	}

	return now.Before(s.chargePause)
}

// metersStale reports whether peak shaving ran without meter values for longer
// than peak.MaxGap, counted from the last good cycle or the start. updatePeakShaving
// only runs once the meters were read, so the setpoint of the last good cycle
// would otherwise stay in the entity for as long as the meters fail. Called with
// mu held.
func (s *peakState) metersStale(now time.Time) bool {
	return !s.updated.IsZero() && now.Sub(s.updated) > peak.MaxGap
}

// peakCheckMeters hands control back to the battery once the meters failed for
// longer than peak.MaxGap: the free value lets it cover any demand by ordinary
// self-consumption, peaks included, where a stale setpoint of 0 would keep it
// blocked. Grid charging pauses meanwhile, see peakPausesGridCharge. Runs every
// cycle, also when the meters failed.
func (site *Site) peakCheckMeters() {
	s := site.peak()

	s.mu.Lock()
	lost := s.enabled && s.set != nil && s.metersStale(s.clock.Now())
	started := lost && !s.metersLost
	if lost {
		s.metersLost = true
		s.shaving = false
		s.covering = false
	}
	s.mu.Unlock()

	if !lost {
		return
	}

	if started {
		site.log.WARN.Printf("peak shaving: no meter values for over %s, battery runs freely and grid charging pauses until they are back", peak.MaxGap)
		site.publish(keys.PeakShavingActive, false)
	}

	site.handBackPeak()
}

// peakChargeHeadroom returns how much grid charge power fits below the peak
// limit on top of the current demand. ok is false while peak shaving is off,
// there is no limit to fit under then.
func (site *Site) peakChargeHeadroom() (headroom float64, ok bool) {
	s := site.peak()

	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.enabled || s.set == nil {
		return 0, false
	}

	return max(0, s.limit-s.demand), true
}

// updateBatteryModePeakAware keeps the battery in normal mode while the reserve
// is being held for peaks, as hold would block the discharge controller. This is
// the one place the fork overrides upstream's battery mode, and only below the
// reserve: grid charging has already been cleared against both the circuit and a
// running peak (see batteryGridChargeRequested), and a mode set from outside
// through the api stays the caller's decision.
func (site *Site) updateBatteryModePeakAware(gridCharge, gridDischarge bool, rate api.Rate) {
	// the last hook of the cycle: everything the overview shows is decided now
	defer site.publishLmStatus(gridCharge)
	defer site.publishLmWallboxes()
	defer site.checkLmFollowing()
	defer site.applyOmni() // after the battery mode, see site_peak_omni.go

	site.peakCheckMeters()

	if gridCharge || !site.peakShavingActive() || site.GetBatteryModeExternal() != api.BatteryUnknown {
		site.updateBatteryMode(gridCharge, gridDischarge, rate)
		return
	}

	if site.GetBatteryMode() == api.BatteryNormal {
		return
	}

	site.log.DEBUG.Println("battery mode: peak shaving reserve")

	if err := site.applyBatteryMode(api.BatteryNormal); err != nil {
		site.log.ERROR.Println("battery mode:", err)
		return
	}

	site.SetBatteryMode(api.BatteryNormal)
}

//
// api
//

func (site *Site) GetPeakShaving() bool {
	s := site.peak()

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.enabled
}

func (site *Site) SetPeakShaving(val bool) error {
	if !site.batteryConfigured() {
		return ErrBatteryNotConfigured
	}

	s := site.peak()

	s.mu.Lock()
	configured := s.set != nil
	s.mu.Unlock()

	if val && !configured {
		return errors.New("no target entity configured")
	}
	if val && site.omniTypeWithoutEntities() {
		return errors.New("no manual switch or mode entity configured")
	}

	site.log.DEBUG.Println("set peak shaving:", val)

	s.mu.Lock()
	changed := s.enabled != val
	s.enabled = val
	if !val {
		s.shaving = false
		s.handedBack = false
	}
	s.mu.Unlock()

	if changed {
		settings.SetBool(keys.PeakShaving, val)
		site.publish(keys.PeakShaving, val)
		site.Optimize() // custom: the optimizer inputs changed, see core/site_optimizer_lm.go

		// hand control back when switching off
		if !val {
			site.handBackPeak()
			site.applyOmni()
		}
	}

	return nil
}

func (site *Site) GetPeakShavingEntity() string {
	s := site.peak()

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.entity
}

// SetPeakShavingEntity sets the Home Assistant number entity receiving the
// setpoint and rebuilds the connection
func (site *Site) SetPeakShavingEntity(entity string) error {
	if entity != "" && !strings.HasPrefix(entity, "number.") && !strings.HasPrefix(entity, "input_number.") {
		return fmt.Errorf("must be a number or input_number entity: %s", entity)
	}

	s := site.peak()

	s.mu.Lock()
	changed := s.entity != entity
	previous := s.entity
	s.entity = entity
	s.mu.Unlock()

	if !changed {
		return nil
	}

	// swapped and handed back in one go, a cycle writing in between could put its
	// setpoint into the previous target after the free value
	s.out.Lock()
	omni := site.omniType()
	s.mu.Lock()
	previousSet := s.set
	s.mu.Unlock()

	err := site.rebuildPeakSetter()
	switch {
	case err != nil || !site.peakOwned():
	case omni:
		// the battery type Marstek never gets the free value; without a target
		// applyOmni is gone, so the manual control is released here
		if entity == "" {
			site.releaseOmni()
		}
	default:
		// the previous target would otherwise keep the last setpoint
		if site.writeOutput("peak shaving", previousSet, site.peakFreeValue()) {
			site.setPeakOwned(false)
		}
	}
	s.out.Unlock()

	if err != nil {
		// keep the working target rather than leaving peak shaving mute
		s.mu.Lock()
		s.entity = previous
		s.mu.Unlock()

		return err
	}

	site.log.DEBUG.Println("set peak shaving entity:", entity)
	settings.SetString(keys.PeakShavingEntity, entity)
	site.publish(keys.PeakShavingEntity, entity)

	// an empty target cannot do anything, so don't pretend it is running
	if entity == "" {
		return site.SetPeakShaving(false)
	}

	return nil
}

// GetPeakShavingChargeEntity returns the entity receiving the grid charge power
func (site *Site) GetPeakShavingChargeEntity() string {
	s := site.peak()

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.chargeEntity
}

// SetPeakShavingChargeEntity sets the Home Assistant number entity receiving the
// grid charge power. With it, grid charging is throttled to stay below the peak
// limit instead of being switched off; empty returns to on/off charging.
func (site *Site) SetPeakShavingChargeEntity(entity string) error {
	if entity != "" && !strings.HasPrefix(entity, "number.") && !strings.HasPrefix(entity, "input_number.") {
		return fmt.Errorf("must be a number or input_number entity: %s", entity)
	}

	s := site.peak()

	s.mu.Lock()
	changed := s.chargeEntity != entity
	previous := s.chargeEntity
	s.chargeEntity = entity
	s.mu.Unlock()

	if !changed {
		return nil
	}

	// swapped and handed back in one go, see SetPeakShavingEntity
	s.out.Lock()
	s.mu.Lock()
	previousSet := s.chargeSet
	s.mu.Unlock()

	err := site.rebuildChargeSetter()
	if err == nil {
		// the previous target would otherwise keep charging at the last setpoint,
		// which the battery type Marstek only accepted in manual control
		if !site.omniType() || site.peakOwned() {
			site.writeOutput("grid charge power", previousSet, 0)
		}

		// on/off charging writes no setpoint, the overview would keep the last one
		s.mu.Lock()
		s.chargeSetpoint = 0
		s.mu.Unlock()
	}
	s.out.Unlock()

	if err != nil {
		s.mu.Lock()
		s.chargeEntity = previous
		s.mu.Unlock()

		return err
	}

	site.publish(keys.PeakShavingChargeSetpoint, 0.0)

	site.log.DEBUG.Println("set grid charge power entity:", entity)
	settings.SetString(keys.PeakShavingChargeEntity, entity)
	site.publish(keys.PeakShavingChargeEntity, entity)

	return nil
}

// GetPeakShavingEnergyEntity returns the Home Assistant energy sensor the window
// is metered with when the grid meter has no import counter
func (site *Site) GetPeakShavingEnergyEntity() string {
	s := site.peak()

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.energyEntity
}

// SetPeakShavingEnergyEntity sets the Home Assistant energy sensor, a counter of
// the grid import in kWh or Wh. Empty returns to the grid power.
func (site *Site) SetPeakShavingEnergyEntity(entity string) error {
	if entity != "" {
		if !strings.HasPrefix(entity, "sensor.") && !strings.HasPrefix(entity, "input_number.") {
			return fmt.Errorf("must be a sensor or input_number entity: %s", entity)
		}

		conn, err := site.haConnection()
		if err != nil {
			return err
		}
		state, err := conn.GetState(entity)
		if err != nil {
			return fmt.Errorf("%s: %w", entity, err)
		}
		if unit := state.Attributes.UnitOfMeasurement; unit != "kWh" && unit != "Wh" {
			return fmt.Errorf("%s must be an energy counter in kWh or Wh, not %q", entity, unit)
		}
	}

	s := site.peak()

	s.mu.Lock()
	changed := s.energyEntity != entity
	previous := s.energyEntity
	s.energyEntity = entity
	s.mu.Unlock()

	if !changed {
		return nil
	}

	if err := site.rebuildEnergyGetter(); err != nil {
		s.mu.Lock()
		s.energyEntity = previous
		s.mu.Unlock()

		return err
	}

	site.log.DEBUG.Println("set peak shaving energy entity:", entity)
	settings.SetString(keys.PeakShavingEnergyEntity, entity)
	site.publish(keys.PeakShavingEnergyEntity, entity)

	return nil
}

// chargePowerControlled reports whether the grid charge power is set through an
// entity rather than charging being switched on or off
func (site *Site) chargePowerControlled() bool {
	s := site.peak()

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.chargeSet != nil
}

// GetPeakShavingChargePower returns the assumed grid charge power, 0 = derived
func (site *Site) GetPeakShavingChargePower() float64 {
	s := site.peak()

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.chargePower
}

// SetPeakShavingChargePower sets the assumed grid charge power in W. Zero falls
// back to the battery meters' maxchargepower.
func (site *Site) SetPeakShavingChargePower(power float64) error {
	if power < 0 || power > maxPeakLimit {
		return fmt.Errorf("charge power must be between 0 and %.0fW", maxPeakLimit)
	}

	s := site.peak()

	s.mu.Lock()
	changed := s.chargePower != power
	s.chargePower = power
	s.mu.Unlock()

	if changed {
		site.log.DEBUG.Println("set peak shaving charge power:", power)
		settings.SetFloat(keys.PeakShavingChargePower, power)
		site.publish(keys.PeakShavingChargePower, power)
		site.publishChargePower()
	}

	return nil
}

// GetPeakShavingCircuit returns the circuit the battery draws from
func (site *Site) GetPeakShavingCircuit() string {
	s := site.peak()

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.circuit
}

// SetPeakShavingCircuit assigns the battery to a circuit. That link is what
// makes the battery take part in load management and what the grid charge gate
// checks against; empty takes it out of load management.
func (site *Site) SetPeakShavingCircuit(name string) error {
	if name != "" {
		if _, err := config.Circuits().ByName(name); err != nil {
			return fmt.Errorf("unknown circuit: %s", name)
		}
	}

	s := site.peak()

	s.mu.Lock()
	changed := s.circuit != name
	s.circuit = name
	s.mu.Unlock()

	if changed {
		site.log.DEBUG.Println("set peak shaving circuit:", name)
		settings.SetString(keys.PeakShavingCircuit, name)
		site.publish(keys.PeakShavingCircuit, name)

		// the battery only appears among the priorities once it is on a circuit
		site.publishLmPriorities()
	}

	return nil
}

func (site *Site) GetPeakShavingLimit() float64 {
	s := site.peak()

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.limit
}

func (site *Site) SetPeakShavingLimit(limit float64) error {
	if limit < minPeakLimit || limit > maxPeakLimit {
		return fmt.Errorf("peak limit must be between %.0fW and %.0fW", minPeakLimit, maxPeakLimit)
	}
	if math.Mod(limit, peakLimitStep) != 0 {
		return fmt.Errorf("peak limit must be a multiple of %.0fW", peakLimitStep)
	}

	site.raisePeakBaseline(limit) // the month's savings are counted above it, see site_peak_stats.go

	// following the peak: the limit set by hand is the base
	if site.peakFollowSetBase(limit) {
		return nil
	}

	s := site.peak()

	s.mu.Lock()
	changed := s.limit != limit
	s.limit = limit
	s.mu.Unlock()

	if changed {
		site.log.DEBUG.Println("set peak shaving limit:", limit)
		settings.SetFloat(keys.PeakShavingLimit, limit)
		site.publish(keys.PeakShavingLimit, limit)
		site.Optimize() // custom: the optimizer inputs changed, see core/site_optimizer_lm.go
	}

	return nil
}

func (site *Site) GetPeakShavingReserve() float64 {
	s := site.peak()

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.reserve
}

func (site *Site) SetPeakShavingReserve(soc float64) error {
	if soc <= 0 || soc >= 100 {
		return fmt.Errorf("invalid reserve soc: %.0f", soc)
	}

	s := site.peak()

	s.mu.Lock()
	changed := s.reserve != soc
	s.reserve = soc
	s.mu.Unlock()

	if changed {
		site.log.DEBUG.Println("set peak shaving reserve:", soc)
		settings.SetFloat(keys.PeakShavingReserve, soc)
		site.publish(keys.PeakShavingReserve, soc)
		site.Optimize() // custom: the optimizer inputs changed, see core/site_optimizer_lm.go
	}

	return nil
}
