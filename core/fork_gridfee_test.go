package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func readRepoFile(t *testing.T, name string) string {
	t.Helper()

	b, err := os.ReadFile(filepath.Join("..", filepath.FromSlash(name)))
	require.NoError(t, err)
	return string(b)
}

// TestGridFeeZonesHook pins what the split grid tariff relies on in evcc's
// PropertyField: the fork's branch comes before evcc's generic zones branch
// (which is also true for this param), and the param and the zone keys the
// form writes are the ones the template reads.
func TestGridFeeZonesHook(t *testing.T) {
	field := readRepoFile(t, "assets/js/components/Config/PropertyField.vue")

	fork := strings.Index(field, "<GridFeeZonesField")
	evcc := strings.Index(field, "<PropertyZonesField")
	require.NotEqual(t, -1, fork)
	require.NotEqual(t, -1, evcc)
	assert.Less(t, fork, evcc, "the grid fee branch has to come first")
	assert.Contains(t, field, `property === 'gridfeeZones'`)
	assert.Contains(t, field, `return this.type === "Zones"`, "evcc's zones branch the hook precedes")

	tmpl := readRepoFile(t, "templates/definition/tariff/fixed-netz.yaml")
	assert.Contains(t, tmpl, "name: gridfeeZones\n    type: zones")
	for _, key := range []string{".mode", ".value", ".days", ".hours", ".months"} {
		assert.Contains(t, tmpl, key, "template reads zone key")
	}

	form := readRepoFile(t, "assets/js/components/Config/GridFeeZoneForm.vue")
	for _, key := range []string{"mode:", "value,", "days:", "months:", "hours:"} {
		assert.Contains(t, form, key, "form writes zone key")
	}
}

// TestGridFeeI18n keeps the fork's texts in both languages
func TestGridFeeI18n(t *testing.T) {
	for _, lang := range []string{"de", "en"} {
		s := readRepoFile(t, "i18n/"+lang+".json")
		for _, key := range []string{"\"lmgridfee\"", "\"modeAbs\"", "\"modePct\"", "\"summaryPct\"", "\"valueAbs\"", "\"valuePct\""} {
			assert.Contains(t, s, key, lang)
		}
	}
}
