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
