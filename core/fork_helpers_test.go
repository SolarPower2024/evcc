package core

import (
	"testing"

	"github.com/evcc-io/evcc/core/types"
	"github.com/evcc-io/evcc/db"
	"github.com/evcc-io/evcc/db/settings"
)

// noSettingsDB keeps the settings in memory for the test. A test that leaves
// its database open (evcc's newDeleteTestSite does) would otherwise make every
// settings.Delete fail with "no such table", depending on the test order.
func noSettingsDB(t *testing.T) {
	t.Helper()
	prev := db.Instance
	db.Instance = nil
	t.Cleanup(func() { db.Instance = prev })
}

// setBatterySoc sets the soc as a successful read of the battery does
func setBatterySoc(site *Site, soc float64) {
	site.battery.Soc = soc
	site.battery.Devices = []types.Measurement{{Soc: &soc}}
}

// keepSettings restores the settings store after the test: keys it added are
// removed, values it changed are set back
func keepSettings(t *testing.T) {
	t.Helper()
	before := make(map[string]string)
	for _, s := range settings.All() {
		before[s.Key] = s.Value
	}
	t.Cleanup(func() {
		for _, s := range settings.All() {
			if v, ok := before[s.Key]; !ok {
				_ = settings.Delete(s.Key)
			} else if v != s.Value {
				settings.SetString(s.Key, v)
			}
		}
	})
}
