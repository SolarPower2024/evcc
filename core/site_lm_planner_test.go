package core

import (
	"math"
	"testing"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/circuit"
	"github.com/evcc-io/evcc/core/planner"
	"github.com/evcc-io/evcc/tariff"
	"github.com/evcc-io/evcc/util"
	"github.com/evcc-io/evcc/util/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The battery holds the running slot while it grid charges, ranked by its
// priority; peak shaving holds everything above the limit.
func TestLmLedger(t *testing.T) {
	config.Reset()
	t.Cleanup(config.Reset)

	root, err := circuit.New(util.NewLogger("test"), "main", 0, 11000, nil, 0)
	require.NoError(t, err)
	require.NoError(t, config.Circuits().Add(config.NewStaticDevice(config.Named{Name: "main"}, api.Circuit(root))))

	site := &Site{log: util.NewLogger("test"), circuit: root}
	site.LoadManagement.Battery.CircuitRef = "main"
	site.LoadManagement.Battery.Power = 5000
	site.LoadManagement.Battery.Priority = 4

	ledger := planner.NewLedger()
	site.setLedger(ledger)

	slot := time.Now().Truncate(tariff.SlotDuration)
	rate := api.Rate{Start: slot, End: slot.Add(tariff.SlotDuration)}
	ev := func(prio int) planner.Owner {
		return planner.Owner{Id: 0, Priority: prio, Circuit: root, MaxPower: 11000}
	}

	assert.Equal(t, 11000.0, ledger.Available(ev(2), rate), "nothing reserved")

	site.updateLmLedger(true)
	assert.Equal(t, 6000.0, ledger.Available(ev(2), rate), "battery outranks")
	assert.Equal(t, 11000.0, ledger.Available(ev(5), rate), "battery ranks lower")
	later := api.Rate{Start: rate.End, End: rate.End.Add(tariff.SlotDuration)}
	assert.Equal(t, 11000.0, ledger.Available(ev(2), later), "only the running slot")

	site.updateLmLedger(false)
	assert.Equal(t, 11000.0, ledger.Available(ev(2), rate), "released")

	s := site.peak()
	s.enabled, s.limit = true, 7000

	site.updateLmLedger(false)
	assert.Equal(t, 7000.0, ledger.Available(ev(10), rate), "peak limit outranks all")
	day := api.Rate{Start: slot.Add(24 * time.Hour), End: slot.Add(24*time.Hour + tariff.SlotDuration)}
	assert.Equal(t, 7000.0, ledger.Available(ev(10), day))

	site.updateLmLedger(true)
	assert.Equal(t, 2000.0, ledger.Available(ev(2), rate), "peak limit and battery")

	s.enabled = false
	site.updateLmLedger(false)
	assert.Equal(t, 11000.0, ledger.Available(ev(10), rate), "released")
}

// Without circuits and peak shaving the fork adds nothing to the ledger.
func TestLmLedgerInertWhenUnused(t *testing.T) {
	config.Reset()
	t.Cleanup(config.Reset)

	(&Site{log: util.NewLogger("test")}).updateLmLedger(true) // no ledger: no-op

	site := &Site{log: util.NewLogger("test")}
	ledger := planner.NewLedger()
	site.setLedger(ledger)

	site.updateLmLedger(true)

	slot := time.Now().Truncate(tariff.SlotDuration)
	rate := api.Rate{Start: slot, End: slot.Add(tariff.SlotDuration)}
	assert.True(t, math.IsInf(ledger.Available(planner.Owner{Priority: 0}, rate), 1))
}
