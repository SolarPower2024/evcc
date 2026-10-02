package core

import (
	"testing"

	"github.com/evcc-io/evcc/db"
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
