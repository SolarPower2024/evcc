package core

import (
	"testing"
	"time"

	"github.com/evcc-io/evcc/util"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Austria 2027 draft: 33.82 EUR/kW/year up to 10 kW, 67.64 above, at least 20%
// of the agreed power and 2 kW.
var austria2027 = peakTariff{Price: 33.82, Threshold: 10, PriceAbove: 67.64, MinShare: 20, Minimum: 2}

func TestPeakTariffCost(t *testing.T) {
	tr := austria2027

	assert.InDelta(t, 8*33.82/12, tr.monthlyCost(8000), 1e-9, "below the threshold")
	assert.InDelta(t, (10*33.82+2*67.64)/12, tr.monthlyCost(12000), 1e-9, "double above 10 kW")
	assert.InDelta(t, 2*33.82/12, tr.monthlyCost(500), 1e-9, "at least 2 kW")

	tr.Agreed = 15
	assert.Equal(t, 3.0, tr.billed(1000), "20% of 15 kW")
	assert.Equal(t, 4.0, tr.billed(4000))
}

func TestPeakTariffMonths(t *testing.T) {
	at := time.Date(2026, 9, 25, 19, 0, 0, 0, time.Local)
	months := []peakMonth{
		{Month: "2026-10"}, // no complete quarter hour yet
		{Month: "2026-09", Peak: 9000, PeakAt: at, Demand: 13000, DemandAt: at},
		{Month: "2026-08", Peak: 8000, PeakAt: at, Demand: 6000, DemandAt: at}, // grid charging raised it
		{Month: "2026-07", Peak: 5000, PeakAt: at},                             // without battery not recorded
	}

	assert.Empty(t, peakTariff{}.months(months), "off without a price")

	res := austria2027.months(months)
	require.Len(t, res, 3)

	sep := res[0]
	assert.Equal(t, "2026-09", sep.Month)
	assert.InDelta(t, 9*33.82/12, sep.Cost, 1e-9)
	assert.InDelta(t, (10*33.82+3*67.64)/12, sep.CostWithout, 1e-9)
	assert.InDelta(t, sep.CostWithout-sep.Cost, sep.Saving, 1e-9)
	assert.Greater(t, sep.Saving, 0.0)

	assert.Less(t, res[1].Saving, 0.0, "a higher peak with the battery costs")
	assert.Zero(t, res[2].Saving)
}

func TestSetPeakTariff(t *testing.T) {
	site := &Site{log: util.NewLogger("test")}

	require.Error(t, site.SetPeakTariff("unknown", 1))
	require.Error(t, site.SetPeakTariff("minShare", 101))
	require.NoError(t, site.SetPeakTariff("price", 33.82))
	require.NoError(t, site.SetPeakTariff("threshold", 10))

	assert.Equal(t, 33.82, site.peak().tariff.Price)
	assert.Equal(t, 10.0, site.peak().tariff.Threshold)
}
