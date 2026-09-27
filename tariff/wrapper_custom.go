package tariff

// Custom extension: access to wrapped tariffs, so capabilities beyond
// api.Tariff can be found on the tariff underneath, see core/site_feedin.go.

import "github.com/evcc-io/evcc/api"

var (
	_ interface{ Unwrap() api.Tariff } = (*Wrapper)(nil)
	_ interface{ Unwrap() api.Tariff } = (*SlotWrapper)(nil)
)

// Unwrap returns the wrapped tariff once it could be created, nil until then
func (v *Wrapper) Unwrap() api.Tariff {
	v.mu.Lock()
	defer v.mu.Unlock()

	return v.instance()
}

// Unwrap returns the tariff whose rates are split into slots
func (t *SlotWrapper) Unwrap() api.Tariff {
	return t.Tariff
}
