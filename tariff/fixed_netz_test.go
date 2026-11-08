package tariff

import (
	"testing"
	"time"

	"github.com/benbjohnson/clock"
	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/util/templates"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v4"
)

// fixedFromTemplate renders the template to a fixed tariff and returns it with the rendered config
func fixedFromTemplate(t *testing.T, name string, values map[string]any) (*Fixed, map[string]any) {
	t.Helper()

	tmpl, err := templates.ByName(templates.Tariff, name)
	require.NoError(t, err)

	b, _, err := tmpl.RenderResult(templates.Tariff, templates.RenderModeInstance, values)
	require.NoError(t, err)

	var other map[string]any
	require.NoError(t, yaml.Unmarshal(b, &other), string(b))
	require.Equal(t, "fixed", other["type"])
	delete(other, "type")

	tf, err := NewFixedFromConfig(other)
	require.NoError(t, err, string(b))

	f := tf.(*Fixed)
	// wednesday in july
	f.clock = clock.NewMock()
	f.clock.(*clock.Mock).Set(time.Date(2026, 7, 8, 0, 0, 0, 0, time.Local))

	return f, other
}

func rateAt(t *testing.T, f *Fixed, hour int) float64 {
	t.Helper()

	rates, err := f.Rates()
	require.NoError(t, err)

	ts := time.Date(2026, 7, 8, hour, 0, 0, 0, time.Local)
	for _, r := range rates {
		if !r.Start.After(ts) && ts.Before(r.End) {
			return r.Value
		}
	}

	t.Fatalf("no rate at %v", ts)
	return 0
}

func netzValues(zones ...map[string]any) map[string]any {
	values := map[string]any{
		"price":      0.12,
		"gridfee":    0.085,
		"levies":     0.022,
		"taxpercent": 20.0,
	}
	if len(zones) > 0 {
		var list []any
		for _, z := range zones {
			list = append(list, z)
		}
		values["gridfeeZones"] = list
	}
	return values
}

var (
	pctZone = map[string]any{"mode": "pct", "value": 20.0, "months": "apr,may,jun,jul,aug,sep", "hours": "10:00-16:00"}
	absZone = map[string]any{"mode": "abs", "value": 0.06, "hours": "22:00-06:00"}
)

func TestFixedNetzNoZone(t *testing.T) {
	f, other := fixedFromTemplate(t, "fixed-netz", netzValues())

	assert.Equal(t, api.TariffTypePriceStatic, f.Type())
	assert.NotContains(t, other, "chargesZones")
	assert.InDelta(t, 0.2724, rateAt(t, f, 12), 1e-9)
	assert.InDelta(t, 0.2724, rateAt(t, f, 20), 1e-9)
}

func TestFixedNetzPercentZone(t *testing.T) {
	f, _ := fixedFromTemplate(t, "fixed-netz", netzValues(pctZone))

	assert.Equal(t, api.TariffTypePriceForecast, f.Type())
	assert.InDelta(t, 0.2520, rateAt(t, f, 12), 1e-9, "(12 + 8.5*0.8 + 2.2) * 1.2")
	assert.InDelta(t, 0.2724, rateAt(t, f, 20), 1e-9)
}

func TestFixedNetzAbsoluteZone(t *testing.T) {
	f, _ := fixedFromTemplate(t, "fixed-netz", netzValues(absZone))

	assert.InDelta(t, 0.2424, rateAt(t, f, 23), 1e-9, "(12 + 6.0 + 2.2) * 1.2")
	assert.InDelta(t, 0.2424, rateAt(t, f, 3), 1e-9)
	assert.InDelta(t, 0.2724, rateAt(t, f, 12), 1e-9)
}

func TestFixedNetzBothZones(t *testing.T) {
	f, _ := fixedFromTemplate(t, "fixed-netz", netzValues(pctZone, absZone))

	assert.InDelta(t, 0.2520, rateAt(t, f, 12), 1e-9)
	assert.InDelta(t, 0.2424, rateAt(t, f, 23), 1e-9)
	assert.InDelta(t, 0.2724, rateAt(t, f, 20), 1e-9)
}

func TestFixedNetzOverlapLastWins(t *testing.T) {
	all := map[string]any{"mode": "abs", "value": 0.06}
	f, _ := fixedFromTemplate(t, "fixed-netz", netzValues(all, pctZone))
	assert.InDelta(t, 0.2520, rateAt(t, f, 12), 1e-9, "later percent zone wins")
	assert.InDelta(t, 0.2424, rateAt(t, f, 20), 1e-9)

	f, _ = fixedFromTemplate(t, "fixed-netz", netzValues(pctZone, all))
	assert.InDelta(t, 0.2424, rateAt(t, f, 12), 1e-9, "later absolute zone wins")
}

func TestFixedNetzPercentFollowsGridFee(t *testing.T) {
	values := netzValues(pctZone)
	values["gridfee"] = 0.09

	f, _ := fixedFromTemplate(t, "fixed-netz", values)
	assert.InDelta(t, 0.2568, rateAt(t, f, 12), 1e-9, "(12 + 9.0*0.8 + 2.2) * 1.2")
	assert.InDelta(t, 0.2784, rateAt(t, f, 20), 1e-9)
}

func TestFixedNetzTax(t *testing.T) {
	for _, tax := range []any{nil, "", 0.0, "0"} {
		values := map[string]any{"price": 0.144, "gridfee": 0.102, "levies": 0.0264}
		if tax != nil {
			values["taxpercent"] = tax
		}

		f, other := fixedFromTemplate(t, "fixed-netz", values)
		assert.NotContains(t, other, "tax", "tax %v", tax)
		assert.InDelta(t, 0.2724, rateAt(t, f, 12), 1e-9, "tax %v", tax)
	}

	// gross amounts with a zone: no tax on top
	values := map[string]any{"price": 0.144, "gridfee": 0.102, "levies": 0.0264}
	values["gridfeeZones"] = []any{pctZone}
	f, _ := fixedFromTemplate(t, "fixed-netz", values)
	assert.InDelta(t, 0.144+0.102*0.8+0.0264, rateAt(t, f, 12), 1e-9)

	f, other := fixedFromTemplate(t, "fixed-netz", netzValues())
	assert.InDelta(t, 0.2, other["tax"], 1e-9)
	assert.InDelta(t, 0.2724, rateAt(t, f, 12), 1e-9)
}

func TestFixedNetzDefaults(t *testing.T) {
	f, other := fixedFromTemplate(t, "fixed-netz", map[string]any{"price": 0.3})
	assert.NotContains(t, other, "tax")
	assert.InDelta(t, 0.3, rateAt(t, f, 12), 1e-9)
}

// the existing fixed template stays unchanged
func TestFixedTemplateUnchanged(t *testing.T) {
	f, other := fixedFromTemplate(t, "fixed", map[string]any{"price": 0.3, "charges": 0.05, "tax": 0.2})
	assert.NotContains(t, other, "zones")
	assert.Equal(t, api.TariffTypePriceStatic, f.Type())
	assert.InDelta(t, 0.42, rateAt(t, f, 12), 1e-9)
}
