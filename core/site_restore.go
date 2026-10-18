package core

// Custom extension: reading the fork's stored settings back at start. A value that
// cannot be read is not dropped silently: it is logged once and kept under
// <key>Invalid, the setting itself falls back to its default. The copy is never
// deleted, a later invalid value only replaces it.

import (
	"encoding/json"
	"errors"
	"reflect"

	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/db/settings"
	"github.com/evcc-io/evcc/util"
)

// invalidSuffix is appended to the key that holds the raw value of an unreadable setting
const invalidSuffix = "Invalid"

// keepInvalid saves the raw value of an unreadable setting under <key>Invalid
func keepInvalid(key string) {
	if raw, err := settings.String(key); err == nil {
		settings.SetString(key+invalidSuffix, raw)
	}
}

// restoreJson reads the json stored under key into v, a pointer. A missing or empty
// value is no error and leaves v alone. Anything unreadable is logged once, kept as
// <key>Invalid and leaves v alone as well; v is only set when the whole value reads.
func restoreJson(log *util.Logger, key string, v any) bool {
	tmp := reflect.New(reflect.TypeOf(v).Elem())

	if err := settings.Json(key, tmp.Interface()); err != nil {
		if !errors.Is(err, settings.ErrNotFound) {
			log.ERROR.Printf("restore %s: %v (raw value kept as %s%s)", key, err, key, invalidSuffix)
			keepInvalid(key)
		}
		return false
	}

	reflect.ValueOf(v).Elem().Set(tmp.Elem())
	return true
}

// restoreLmAdvancedValue reads the advanced settings field by field: a field of the
// wrong type is left out and logged, the others stay.
func restoreLmAdvancedValue(log *util.Logger) (lmAdvanced, bool) {
	var raw map[string]json.RawMessage
	if !restoreJson(log, keys.LmAdvanced, &raw) {
		return lmAdvanced{}, false
	}

	good := make(map[string]json.RawMessage, len(raw))
	bad := false

	for field, val := range raw {
		var probe lmAdvanced
		if err := json.Unmarshal(mustJSON(map[string]json.RawMessage{field: val}), &probe); err != nil {
			log.ERROR.Printf("restore %s: field %s: %v (left out)", keys.LmAdvanced, field, err)
			bad = true
			continue
		}
		good[field] = val
	}

	if bad {
		keepInvalid(keys.LmAdvanced)
	}

	var adv lmAdvanced
	_ = json.Unmarshal(mustJSON(good), &adv)

	return adv, true
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// restoreFloat reads a number stored under key. Missing is no error; a value that
// does not read or fails valid is logged once as a warning, deleted and left out, so
// the default applies. Setters and restore share the checks, see valid….
func restoreFloat(log *util.Logger, key string, valid func(float64) error) (float64, bool) {
	v, err := settings.Float(key)
	if err == nil {
		err = valid(v)
	}
	if err != nil {
		if !errors.Is(err, settings.ErrNotFound) {
			dropInvalid(log, key, err)
		}
		return 0, false
	}
	return v, true
}

// restoreInt is restoreFloat for a whole number
func restoreInt(log *util.Logger, key string, valid func(int) error) (int, bool) {
	v, err := settings.Int(key)
	if err == nil {
		err = valid(int(v))
	}
	if err != nil {
		if !errors.Is(err, settings.ErrNotFound) {
			dropInvalid(log, key, err)
		}
		return 0, false
	}
	return int(v), true
}

// dropInvalid warns about a stored value that is not valid and deletes it
func dropInvalid(log *util.Logger, key string, err error) {
	raw, _ := settings.String(key)
	log.WARN.Printf("restore %s: ignoring stored value %q: %v", key, raw, err)
	if err := settings.Delete(key); err != nil {
		log.ERROR.Printf("restore %s: delete: %v", key, err)
	}
}

// restoreSocPair reads the grid charge thresholds. Each one has to be in range, and
// start below stop; otherwise both stay at start and stop given.
func restoreSocPair(log *util.Logger, start, stop float64) (float64, float64) {
	s, sok := restoreFloat(log, keys.BatterySocGridChargeStart, validSocStart)
	e, eok := restoreFloat(log, keys.BatterySocGridChargeStop, validSocStop)

	ns, ne := start, stop
	if sok {
		ns = s
	}
	if eok {
		ne = e
	}

	if err := validSocPair(ns, ne); err != nil {
		if sok || eok {
			log.WARN.Printf("restore grid charge soc: %v, using %.0f/%.0f", err, start, stop)
			_ = settings.Delete(keys.BatterySocGridChargeStart)
			_ = settings.Delete(keys.BatterySocGridChargeStop)
		}
		return start, stop
	}

	return ns, ne
}

// dropInvalidLmAdvanced leaves out the fields of adv whose value the setter would
// refuse, and reports whether it did
func dropInvalidLmAdvanced(log *util.Logger, adv *lmAdvanced) bool {
	fields := map[string]**float64{
		"hysteresis": &adv.Hysteresis, "freeValue": &adv.FreeValue, "holdOff": &adv.HoldOff,
		"timeout": &adv.Timeout, "phases": &adv.Phases, "peakFreeze": &adv.PeakFreeze,
		"peakCap": &adv.PeakCap, "writeTolerance": &adv.WriteTolerance,
		"followCycles": &adv.FollowCycles, "gridChargeWindow": &adv.GridChargeWindow,
		"homeWeekday": &adv.HomeWeekday, "homeForecast": &adv.HomeForecast,
	}

	dropped := false
	for name, p := range fields {
		if *p == nil {
			continue
		}
		if err := validLmAdvanced(name, **p); err != nil {
			log.WARN.Printf("restore %s: ignoring stored %s %g: %v", keys.LmAdvanced, name, **p, err)
			*p = nil
			dropped = true
		}
	}

	return dropped
}

// onceTargetValid checks the stored one-time grid charge: target 0 is no order, else
// 1 to 100. Anything else is warned about and deleted.
func (site *Site) onceTargetValid(o gridChargeOnce) bool {
	if o.Target == 0 {
		return true
	}
	if err := validOnceTarget(o.Target); err != nil {
		dropInvalid(site.log, keys.BatteryGridChargeOnce, err)
		return false
	}
	return true
}

// moveHomeWeekday takes a stored homeWeekday over into homeForecast once, unless
// that is set, and clears it; reports whether adv changed
func moveHomeWeekday(adv *lmAdvanced) bool {
	if adv.HomeWeekday == nil {
		return false
	}
	if adv.HomeForecast == nil {
		adv.HomeForecast = adv.HomeWeekday
	}
	adv.HomeWeekday = nil
	return true
}

// dropPeakFollowCircuit removes the key lm3/lm4 stored the circuit under, once
// lmCircuit holds it. Persisted first: a crash in between must not lose the circuit.
func (site *Site) dropPeakFollowCircuit() {
	if _, err := settings.String(keys.PeakFollowCircuit); err != nil {
		return
	}
	if err := settings.Persist(); err != nil {
		site.log.ERROR.Printf("restore %s: %v", keys.LmCircuit, err)
		return
	}
	if err := settings.Delete(keys.PeakFollowCircuit); err != nil {
		site.log.ERROR.Printf("restore %s: delete: %v", keys.PeakFollowCircuit, err)
	}
}
