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
// evcc does not regulate the discharge itself: a controller in Home Assistant
// does (Omnibattery for a Marstek, the integration evcc Peak Fronius for a BYD),
// evcc is its input, see site_peak_ext.go. The window, the allowed power and the
// setpoint (shown and counted, not written) are computed in core/peak; this file
// feeds it and handles the settings, Home Assistant and the battery mode.

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
	chargePower float64 // assumed grid charge power in W, 0 = derive it
	circuit     string  // circuit the battery draws from, empty = not managed

	shaving  bool // hysteresis state: below the reserve
	covering bool // covering a peak right now, for the event log

	// owned is set once evcc took the battery into its control through the manual
	// control (Marstek) or a request other than normal (BYD) and cleared again once
	// it handed back. Only then there is something to hand back: an entity evcc
	// never controlled is left alone. Persisted, so a restart in the middle of a
	// control still hands back.
	owned    bool
	writeErr map[string]string // the last error logged per output, see writeOutput
	readErr  string            // the last error logged reading the energy sensor, see logEnergyRead

	// evcc is stopping: the cycle writes nothing any more while the shutdown
	// function hands back, see site_peak_shutdown.go
	stopped      bool
	shutdownOnce sync.Once

	// external peak control, see site_peak_ext.go: evcc drives the peak shaving of
	// Omnibattery (Marstek, site_peak_omni.go) or of the integration evcc Peak
	// Fronius (BYD, site_peak_byd.go) and holds the battery with the manual control
	// or a request while grid charging
	batteryType  string // "" and byd, or marstek
	manualEntity string // switch of the manual control
	modeEntity   string // select of the forced mode
	omniShown    string // the forced mode published, empty = not controlling

	protSwitch string              // switch of the external peak shaving
	protLimit  string              // number entity of its limit, see omniWriteLimit
	protSoc    string              // number entity of its soc threshold
	protSocSet func(float64) error // resolved from protSoc
	protOwned  bool                // evcc turned the switch on, persisted like owned
	holdSoc    float64             // threshold before the optimizer's hold raised it, 0 = not raised, persisted, see site_peak_omni_hold.go
	omniWarned bool                // grid charging without manual control logged, until it is set up again

	conn *homeassistant.Connection // shared, built on first use

	demand        float64   // grid demand without the battery in W, from the last cycle
	chargePause   time.Time // grid charging gives way to peak shaving until then
	batteryNeeded time.Time // the optimizer's hold gives way to a peak until then, see peakNeedsBattery

	updated    time.Time    // last cycle with meter values, see peakCheckMeters
	metersLost bool         // the meters failed for longer than peak.MaxGap
	read       peakReadings // outages evcc does not report, see site_peak_readings.go

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

	if v, ok := restoreFloat(site.log, keys.PeakShavingLimit, validPeakLimit); ok {
		s.mu.Lock()
		s.limit = v
		s.mu.Unlock()
	}
	if v, ok := restoreFloat(site.log, keys.PeakShavingReserve, validPeakReserve); ok {
		s.mu.Lock()
		s.reserve = v
		s.mu.Unlock()
	}
	if v, err := settings.Bool(keys.PeakShaving); err == nil {
		s.mu.Lock()
		s.enabled = v
		s.mu.Unlock()
	}

	if v, ok := restoreFloat(site.log, keys.PeakShavingChargePower, validPeakChargePower); ok {
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
	if v, err := settings.String(keys.PeakShavingProtSwitch); err == nil {
		s.mu.Lock()
		s.protSwitch = v
		s.mu.Unlock()
	}
	if v, err := settings.String(keys.PeakShavingProtLimit); err == nil {
		s.mu.Lock()
		s.protLimit = v
		s.mu.Unlock()
	}
	if v, err := settings.String(keys.PeakShavingProtSoc); err == nil {
		s.mu.Lock()
		s.protSoc = v
		s.mu.Unlock()
	}
	if v, err := settings.Bool(keys.PeakShavingProtOwned); err == nil {
		s.mu.Lock()
		s.protOwned = v
		s.mu.Unlock()
	}
	if v, err := settings.Float(keys.PeakShavingHoldSoc); err == nil {
		s.mu.Lock()
		s.holdSoc = v
		s.mu.Unlock()
	}

	// a start during a meter outage counts as without meter values from now on,
	// see peakCheckMeters, rather than keeping the setpoint written before it
	s.mu.Lock()
	s.updated = s.clock.Now()
	s.mu.Unlock()

	if err := site.rebuildChargeSetter(); err != nil {
		site.log.ERROR.Printf("grid charge power: %v", err)
	}
	if err := site.rebuildProtSetter(); err != nil {
		site.log.ERROR.Printf("peak shaving soc threshold: %v", err)
	}
	if err := site.rebuildEnergyGetter(); err != nil {
		site.log.ERROR.Printf("peak shaving energy: %v", err)
	}

	site.migratePeakEntity() // the old discharge setpoint way is gone, see site_peak_byd.go

	site.restorePeakMonths()
	site.restorePeakFollow()
	site.fillPeakBaselines()
	site.restorePeakTariff()
	site.publishPeakSettings()
	site.publishLmPriorities()

	site.registerPeakShutdown() // hands the battery back on stopping, see site_peak_shutdown.go
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

// rebuildChargeSetter resolves the grid charge power output from its entity
func (site *Site) rebuildChargeSetter() error {
	s := site.peak()

	s.mu.Lock()
	entity := s.chargeEntity
	s.mu.Unlock()

	var set func(float64) error

	if entity != "" {
		var err error
		if set, err = site.numberSetter(entity, false, site.peakWriteTolerance); err != nil { // a charge power has to stay within the limits
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

	log := util.NewLogger("peakshaving")

	conn, err = homeassistant.NewConnection(log, uri, "", false)
	if err != nil {
		return nil, err
	}

	guardHA(conn, log) // one timeout per cycle at most, see site_ha_guard.go

	s.mu.Lock()
	s.conn = conn
	s.mu.Unlock()

	return conn, nil
}

// numberSetter returns a setter writing to a Home Assistant number entity. Each
// value is fitted to the entity's min, max and step first, see peak.Range.Fit;
// up rounds to the next step above. These are read on every write, as an
// integration may only learn them from the device after it started. A value the
// entity already holds, within the tolerance, is not written again, see
// peak.Range.Unchanged: a device may store every write. As the comparison is
// against the entity, a value changed by hand is still corrected. tolerance
// returns the smallest change written; nil for an entity that is not a power, the
// write tolerance is in W. An entity that cannot be read is not written: its
// range is unknown, and a write to an entity Home Assistant has not loaded (yet)
// is answered with success and does nothing.
func (site *Site) numberSetter(entity string, up bool, tolerance func() float64) (func(float64) error, error) {
	conn, err := site.haConnection()
	if err != nil {
		return nil, err
	}

	return func(val float64) error {
		r, current, err := numberState(conn, entity)
		if err != nil {
			return fmt.Errorf("read %s: %w", entity, err)
		}

		var tol float64
		if tolerance != nil {
			tol = tolerance()
		}

		val, write := numberWrite(r, current, val, up, tol)
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

	// a state that is no number has no current value and gets the write
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

// notAvailable reports whether the state is that of an entity without a value
func notAvailable(state string) bool {
	return state == "unknown" || state == "unavailable"
}

// numberState reads the min, max and step attributes and the state of a number
// entity. An entity that is unknown or not available is reported as
// api.ErrNotAvailable, as GetState does.
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
	if err == nil && notAvailable(res.State) {
		err = api.ErrNotAvailable
	}

	return peak.Range(res.Attributes), res.State, err
}

func (site *Site) publishPeakSettings() {
	s := site.peak()

	s.mu.Lock()
	enabled, limit, reserve, charge, circuit := s.enabled, s.limit, s.effectiveReserve(), s.chargePower, s.circuit
	chargeEntity, energyEntity := s.chargeEntity, s.energyEntity
	s.mu.Unlock()

	site.publish(keys.PeakShaving, enabled)
	site.publish(keys.PeakShavingLimit, limit)
	site.publish(keys.PeakShavingReserve, reserve)
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

// peakHysteresis returns the soc band of the reserve. The battery type Marstek
// has none: Omnibattery switches exactly at the threshold, and evcc's state of
// the reserve has to change there as well. The battery type BYD has the fixed band
// of the integration, see bydHysteresis.
func (site *Site) peakHysteresis() float64 {
	if site.omniType() {
		return 0
	}

	return bydHysteresis
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
// limit, for the display and the statistics. Called once per cycle.
func (site *Site) updatePeakShaving(state siteState) {
	s := site.peak()
	defer site.savePeakMonths()

	ok, back := site.peakReadingsOk(state)

	s.mu.Lock()
	recovered := back || (ok && s.metersLost)
	if ok {
		s.metersLost = false
	}
	s.mu.Unlock()

	if recovered {
		site.log.INFO.Println("peak shaving: meters back")
	}

	if !ok {
		// the meter values cannot be trusted (no grid power, a grid power that stands
		// still, a battery power that cannot be read): like for a failing meter nothing
		// is metered and the setpoint stays, see peakCheckMeters
		site.updatePeakFollow()
		site.applyCircuitLimits() // load management switch and follow circuit, see site_lm_switch.go

		return
	}

	site.updatePeakWindow(state.gridPower, state.battery.Power)
	site.updatePeakFollow()
	site.applyCircuitLimits() // load management switch and follow circuit, see site_lm_switch.go

	s.mu.Lock()
	enabled, limit, reserve, configured, allowed := s.enabled, s.limit, s.effectiveReserve(), s.configured(), s.window.Allowed
	// read by peakPausesGridCharge later in the same cycle
	s.demand = state.gridPower + state.battery.Power
	s.mu.Unlock()

	if !enabled || !configured || !site.batteryConfigured() {
		// don't leave a stale reserve state behind: peakShavingActive gates grid
		// charging and the battery mode, and must not keep doing so once peak
		// shaving stopped running
		s.mu.Lock()
		s.shaving = false
		s.covering = false
		s.mu.Unlock()

		site.publish(keys.PeakShavingActive, false)

		return
	}

	soc, known := site.batterySocRead()
	hyst := site.peakHysteresis()

	s.mu.Lock()
	// below the reserve the battery is for peaks only. Without a read soc (start,
	// all reads failing) the state stays as it was.
	if known {
		s.shaving = peak.Reserved(s.shaving, soc, reserve, hyst)
	}
	shaving := s.shaving
	s.mu.Unlock()

	value := site.peakFreeValue()
	hold := site.optimizerHolds()

	switch {
	// no discharging while the battery charges from the grid. A peak pauses the
	// charging in this same cycle, see peakPausesGridCharge, so the value then
	// falls through to the regular one right away.
	case site.GetBatteryMode() == api.BatteryCharge && state.gridPower+state.battery.Power <= limit:
		value = 0

	case shaving:
		value = peak.Setpoint(state.gridPower, state.battery.Power, allowed)

	// the optimizer withholds discharging: only a peak is covered, see site_optimizer_gate.go
	case hold:
		value = peak.Setpoint(state.gridPower, state.battery.Power, allowed)
	}

	// log the start of a peak, not every cycle of it
	s.mu.Lock()
	covering := (shaving || hold) && value > 0
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
	// against a 5kW limit asks for exactly 10000W. It is written nowhere: the
	// external peak control regulates itself, see applyOmniProtection.
	site.publish(keys.PeakShavingActive, shaving)
	site.publish(keys.PeakShavingPower, value)
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
		site.persistHeld()
	}
}

// persistHeld stores the mark of what evcc holds at once. SetBool only keeps it
// in memory until the next flush (every minute and when evcc stops); a crash in
// between would lose the mark, and nothing would hand the battery back after the
// start. Called with s.out held and only on a change, blocks up to the busy
// timeout of the database at worst. A failing save is logged once per cause.
func (site *Site) persistHeld() {
	site.logWrite("peak shaving mark", "save", settings.Persist())
}

// peakOwned reports whether evcc holds the battery back, see peakState.owned
func (site *Site) peakOwned() bool {
	s := site.peak()

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.owned
}

// writeChargeValue writes the grid charge power setpoint
func (site *Site) writeChargeValue(value float64) {
	s := site.peak()

	s.out.Lock()
	defer s.out.Unlock()

	if site.peakStopped() {
		return
	}

	s.mu.Lock()
	set := s.chargeSet
	s.chargeSetpoint = value
	s.mu.Unlock()

	site.publish(keys.PeakShavingChargeSetpoint, value)

	// the battery type Marstek is written by applyOmni, after the manual control
	// and the mode; without them Omnibattery would refuse the power, so it is not
	// sent at all and the log says why, once until both are set up again
	if site.omniType() {
		enabled := site.omniEnabled()

		s.mu.Lock()
		warn := value > 0 && !enabled && !s.omniWarned
		s.omniWarned = !enabled && (s.omniWarned || warn)
		s.mu.Unlock()

		if warn {
			site.log.WARN.Println("grid charge power: battery type Marstek needs the manual control switch and the force mode for grid charging (Lastmanagement-Details → Peak Shaving)")
		}
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
// per cycle. A call refused by the guard, see haGuard, is not logged.
func (site *Site) logWrite(name, what string, err error) bool {
	// stopping, e.g. with the host: Home Assistant may refuse the call, expected;
	// also a call the guard refused, the hand back would otherwise fail unnoticed
	if err != nil && site.peakStopped() {
		site.log.INFO.Printf("%s: %s: %s (stopping)", name, what, writeErrorText(err))
		return false
	}

	// refused after a call timed out in this cycle, that call logged the cause
	if errors.Is(err, errHAHung) {
		site.log.TRACE.Printf("%s: %s: %v", name, what, err)
		return false
	}

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
// grid power. A 0 of the sensor is no reading, like evcc takes it for the grid
// meter.
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
		v, err := nonZeroEnergy(get())
		// a hung Home Assistant is logged once where it is detected
		if !errors.Is(err, errHAHung) {
			site.logEnergyRead(entity, err)
		}

		if err == nil {
			return v, peak.SourceEntity
		}
	}

	return 0, peak.SourcePower
}

// logEnergyRead reports the result of reading the energy sensor, every cycle. A
// failure is logged once as a warning and then at debug level until the read
// works again (once at info), like a failed write.
func (site *Site) logEnergyRead(entity string, err error) {
	s := site.peak()

	var text string
	if err != nil {
		text = err.Error()
	}

	s.mu.Lock()
	last := s.readErr
	s.readErr = text
	s.mu.Unlock()

	switch {
	case err == nil && last != "":
		site.log.INFO.Printf("peak shaving: energy %s: ok again", entity)
	case err == nil:
	case text == last:
		site.log.DEBUG.Printf("peak shaving: energy %s: %s", entity, text)
	default:
		site.log.WARN.Printf("peak shaving: energy %s: %s, using the grid power", entity, text)
	}
}

// setPeakGridEnergy takes the grid meter's import counter of this cycle, nil if
// the meter has none, and whether the grid power was read: evcc keeps the last
// value when it is not available, which peak shaving must not take for a reading,
// see peakReadingsOk
func (site *Site) setPeakGridEnergy(kWh *float64, powerRead bool) {
	s := site.peak()

	s.mu.Lock()
	defer s.mu.Unlock()

	s.gridEnergy = kWh
	s.read.gridUnread = !powerRead
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

	if w.GlitchNow {
		site.log.WARN.Printf("peak shaving: the %s energy counter jumped to %.3f kWh, using the grid power for now", source, energy)
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

	if !s.enabled || !s.configured() {
		return false
	}

	now := s.clock.Now()

	// without meter values the demand is unknown, so charging could create a peak
	if s.metersStale(now) {
		return true
	}

	if limit := s.chargeLimit(now); s.demand > limit {
		if !now.Before(s.chargePause) {
			site.log.DEBUG.Printf("battery grid charge: paused, demand %.0fW exceeds the %.0fW allowed (peak limit %.0fW)", s.demand, limit, s.limit)
			site.lmm().AddEvent(lm.Event{At: now, Type: lm.EventGridChargePaused, A: s.demand, B: limit})
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

// peakCheckMeters ends the reserve once the meters failed for longer than
// peak.MaxGap, so the battery is not held back for peaks it cannot see. Grid
// charging pauses meanwhile, see peakPausesGridCharge. Runs every cycle, also when
// the meters failed.
func (site *Site) peakCheckMeters() {
	s := site.peak()

	s.mu.Lock()
	lost := s.enabled && s.configured() && s.metersStale(s.clock.Now())
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

}

// chargeLimit is the grid power grid charging has to stay under: the peak limit,
// or what the running 15 minute window still allows if that is less, as an
// earlier peak in the window has used up its budget and charging on top would
// push the window over the limit. Without a metered current window (before its
// first sample, meters failing) the peak limit alone. Called with mu held.
func (s *peakState) chargeLimit(now time.Time) float64 {
	if s.window.Start.Equal(now.Truncate(peak.Window)) && !s.metersStale(now) {
		// the allowed power is the limit up to rounding while the window is on budget
		if allowed := s.window.Allowed; allowed < s.limit-1 {
			return allowed
		}
	}

	return s.limit
}

// peakChargeHeadroom returns how much grid charge power fits below the grid
// power the window allows (see chargeLimit) on top of the current demand. ok is
// false while peak shaving is off, there is no limit to fit under then.
func (site *Site) peakChargeHeadroom() (headroom float64, ok bool) {
	s := site.peak()

	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.enabled || !s.configured() {
		return 0, false
	}

	return max(0, s.chargeLimit(s.clock.Now())-s.demand), true
}

// updateBatteryModePeakAware keeps the battery in normal mode while the reserve
// is being held for peaks, as hold would block the discharge controller. This is
// the one place the fork overrides upstream's battery mode, and only below the
// reserve: grid charging has already been cleared against both the circuit and a
// running peak (see batteryGridChargeRequested), and a mode set from outside
// through the api stays the caller's decision.
func (site *Site) updateBatteryModePeakAware(gridCharge, gridDischarge bool, rate api.Rate) {
	// the last hook of the cycle: the calls to Home Assistant are done
	defer site.haCycleEnd()

	// everything the overview shows is decided now
	defer site.publishLmStatus(gridCharge)
	defer site.publishLmWallboxes()
	defer site.checkLmFollowing()
	defer site.applyOmni() // after the battery mode, see site_peak_omni.go

	site.peakCheckMeters()

	if gridCharge || site.optimizerCharges() || !site.peakShavingActive() || site.GetBatteryModeExternal() != api.BatteryUnknown {
		site.updateBatteryMode(gridCharge, gridDischarge, rate)
		return
	}

	// the optimizer's requests pass the gate only on the path above
	if site.optimizerInControl() {
		site.releaseGridCharge()
	}

	if site.GetBatteryMode() == api.BatteryNormal {
		site.clearExternalBatteryTimer()
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
	// turning off needs no battery: clearing an entity ends up here without one
	if val && !site.batteryConfigured() {
		return ErrBatteryNotConfigured
	}

	s := site.peak()

	if val && !site.peakConfigured() {
		return errProtMissing
	}

	site.log.DEBUG.Println("set peak shaving:", val)

	s.mu.Lock()
	changed := s.enabled != val
	s.enabled = val
	if !val {
		s.shaving = false
	}
	s.mu.Unlock()

	if changed {
		settings.SetBool(keys.PeakShaving, val)
		site.publish(keys.PeakShaving, val)
		site.Optimize() // custom: the optimizer inputs changed, see core/site_optimizer_lm.go
		// the circuit may take the peak limit, see site_lm_switch.go
		site.applyCircuitLimits()

		// hand control back when switching off
		if !val {
			site.applyOmni()
		}
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

	// swapped and handed back in one go, see SetPeakShavingChargeEntity
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
	if err := validPeakChargePower(power); err != nil {
		return err
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
		site.publishLmAdvanced() // phasesMissing depends on the circuit
	}

	return nil
}

func (site *Site) GetPeakShavingLimit() float64 {
	s := site.peak()

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.limit
}

// validPeakLimit checks the range of the peak limit, not its step: follow the peak
// stores the raised limit in steps of peakFollowStep
func validPeakLimit(limit float64) error {
	if limit < minPeakLimit || limit > maxPeakLimit {
		return fmt.Errorf("peak limit must be between %.0fW and %.0fW", minPeakLimit, maxPeakLimit)
	}
	return nil
}

// validPeakReserve checks the reserve soc
func validPeakReserve(soc float64) error {
	if soc <= 0 || soc >= 100 {
		return fmt.Errorf("invalid reserve soc: %.0f", soc)
	}
	return nil
}

// validPeakChargePower checks the assumed grid charge power
func validPeakChargePower(power float64) error {
	if power < 0 || power > maxPeakLimit {
		return fmt.Errorf("charge power must be between 0 and %.0fW", maxPeakLimit)
	}
	return nil
}

func (site *Site) SetPeakShavingLimit(limit float64) error {
	if err := validPeakLimit(limit); err != nil {
		return err
	}
	if math.Mod(limit, peakLimitStep) != 0 {
		return fmt.Errorf("peak limit must be a multiple of %.0fW", peakLimitStep)
	}

	site.raisePeakBaseline(limit) // the month's savings are counted above it, see site_peak_stats.go

	// following the peak: the limit set by hand is the base
	if site.peakFollowSetBase(limit) {
		// the circuit may take the peak limit, see site_lm_switch.go
		site.applyCircuitLimits()
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
		// the circuit may take the peak limit, see site_lm_switch.go
		site.applyCircuitLimits()
	}

	return nil
}

// GetPeakShavingReserve returns the reserve in effect, see effectiveReserve: for
// the battery type Marstek at least 20 %. The value set stays stored, for BYD.
func (site *Site) GetPeakShavingReserve() float64 {
	s := site.peak()

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.effectiveReserve()
}

func (site *Site) SetPeakShavingReserve(soc float64) error {
	if err := validPeakReserve(soc); err != nil {
		return err
	}

	s := site.peak()

	s.mu.Lock()
	changed := s.reserve != soc
	s.reserve = soc
	effective := s.effectiveReserve()
	s.mu.Unlock()

	if changed {
		site.log.DEBUG.Println("set peak shaving reserve:", soc)
		settings.SetFloat(keys.PeakShavingReserve, soc)
		site.publish(keys.PeakShavingReserve, effective)
		site.Optimize() // custom: the optimizer inputs changed, see core/site_optimizer_lm.go
	}

	return nil
}

// clearExternalBatteryTimer ends the leave of the external battery control when
// the reserve holds the battery in normal mode, as SetBatteryMode does: the timer
// of a lapsed external mode stays set until upstream applies normal mode, which the
// reserve does not do while the battery is already in it. A left over timer keeps
// the next external mode from starting its watchdog. SetBatteryMode itself is not
// called, it logs on every cycle.
func (site *Site) clearExternalBatteryTimer() {
	site.Lock()
	defer site.Unlock()

	if site.batteryModeExternal == api.BatteryUnknown {
		site.batteryModeExternalTimer = time.Time{}
	}
}
