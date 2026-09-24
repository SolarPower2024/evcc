package profile

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func f(v float64) *float64 { return &v }

func TestValidate(t *testing.T) {
	winter := Profile{
		Name: "Winter", Icon: "snow",
		GridChargeStart: f(20), GridChargeStop: f(60),
		PrioritySoc: f(50), BufferSoc: f(80), BufferStartSoc: f(90),
		PeakReserve: f(30), PeakLimit: f(8500),
		SolarShare: map[string]float64{"db:1": 50},
	}
	assert.NoError(t, winter.Validate())
	assert.NoError(t, Profile{Name: "Urlaub"}.Validate(), "all values left out")

	for _, tc := range []struct {
		name string
		mod  func(*Profile)
	}{
		{"no name", func(p *Profile) { p.Name = "  " }},
		{"long name", func(p *Profile) { p.Name = "Ein sehr langer Profilname mit Umlauten äöü" }},
		{"unknown icon", func(p *Profile) { p.Icon = "rocket" }},
		{"start above stop", func(p *Profile) { p.GridChargeStart = f(70) }},
		{"soc above 100", func(p *Profile) { p.GridChargeStop = f(101) }},
		{"priority above buffer", func(p *Profile) { p.PrioritySoc = f(85) }},
		{"buffer above buffer start", func(p *Profile) { p.BufferStartSoc = f(70) }},
		{"reserve 0", func(p *Profile) { p.PeakReserve = f(0) }},
		{"limit not in 500 W steps", func(p *Profile) { p.PeakLimit = f(8200) }},
		{"limit too low", func(p *Profile) { p.PeakLimit = f(1500) }},
		{"solar share above 100", func(p *Profile) { p.SolarShare = map[string]float64{"db:1": 110} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := winter
			tc.mod(&p)
			assert.Error(t, p.Validate())
		})
	}
}

func TestCheckBatteryUsage(t *testing.T) {
	assert.NoError(t, CheckBatteryUsage(50, 80, 90))
	assert.NoError(t, CheckBatteryUsage(50, 0, 0), "buffers off")
	assert.NoError(t, CheckBatteryUsage(80, 80, 80), "equal is fine")
	assert.NoError(t, CheckBatteryUsage(90, 0, 50), "without a buffer the start is not checked against the priority")
	assert.Error(t, CheckBatteryUsage(90, 80, 0))
	assert.Error(t, CheckBatteryUsage(50, 90, 80))
}
