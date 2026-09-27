package core

// Custom extension: the capacity tariff (Leistungstarif) and what peak shaving
// saves under it. The month's highest quarter hour is billed per kW, a higher
// price above a threshold, at least a minimum and a share of the agreed power.
// Set up under Lastmanagement-Details → Leistungstarif, the costs with and
// without the battery are shown under Mehr → Peak Shaving.
//
// Austria from 2027 (draft, amounts final in December 2026): about 33.82 EUR per
// kW and year up to 10 kW, twice that above, at least 20% of the agreed power
// and at least 2 kW.

import (
	"fmt"

	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/db/settings"
)

// peakTariff is the capacity tariff, a zero price switches it off
type peakTariff struct {
	Price      float64 `json:"price"`      // per kW and year up to the threshold
	Threshold  float64 `json:"threshold"`  // kW
	PriceAbove float64 `json:"priceAbove"` // per kW and year above the threshold
	Agreed     float64 `json:"agreed"`     // agreed power in kW, 0 = none
	MinShare   float64 `json:"minShare"`   // % of the agreed power billed at least
	Minimum    float64 `json:"minimum"`    // kW billed at least
}

// peakTariffMonth is one month's capacity cost with and without the battery
type peakTariffMonth struct {
	Month       string  `json:"month"`
	Billed      float64 `json:"billed"`      // kW billed
	Cost        float64 `json:"cost"`        // capacity cost of the month
	CostWithout float64 `json:"costWithout"` // the same without the battery
	Saving      float64 `json:"saving"`      // CostWithout - Cost, negative when grid charging raised the peak
}

// peakTariffState is what the ui gets
type peakTariffState struct {
	peakTariff
	Months []peakTariffMonth `json:"months"`
}

// peakTariffLimits are the valid ranges
var peakTariffLimits = map[string][2]float64{
	"price":      {0, 1000},
	"threshold":  {0, 1000},
	"priceAbove": {0, 2000},
	"agreed":     {0, 1000},
	"minShare":   {0, 100},
	"minimum":    {0, 1000},
}

// billed returns the kW billed for a peak in W
func (t peakTariff) billed(peak float64) float64 {
	return max(peak/1e3, t.Minimum, t.Agreed*t.MinShare/100)
}

// monthlyCost returns a month's capacity cost for a peak in W
func (t peakTariff) monthlyCost(peak float64) float64 {
	kw := t.billed(peak)
	return (min(kw, t.Threshold)*t.Price + max(0, kw-t.Threshold)*t.PriceAbove) / 12
}

// months returns the costs of the recorded months, newest first
func (t peakTariff) months(months []peakMonth) []peakTariffMonth {
	res := make([]peakTariffMonth, 0, len(months))
	if t.Price <= 0 && t.PriceAbove <= 0 {
		return res
	}

	for _, m := range months {
		if m.PeakAt.IsZero() {
			continue // no complete quarter hour yet
		}

		cost, without := t.monthlyCost(m.Peak), t.monthlyCost(max(m.Demand, 0))
		if m.DemandAt.IsZero() {
			without = cost
		}

		res = append(res, peakTariffMonth{
			Month:       m.Month,
			Billed:      t.billed(m.Peak),
			Cost:        cost,
			CostWithout: without,
			Saving:      without - cost,
		})
	}

	return res
}

// restorePeakTariff loads the capacity tariff, after the peak statistics
func (site *Site) restorePeakTariff() {
	var t peakTariff
	if err := settings.Json(keys.PeakTariff, &t); err == nil {
		s := site.peak()
		s.mu.Lock()
		s.tariff = t
		s.mu.Unlock()
	}

	site.publishPeakTariff()
}

func (site *Site) publishPeakTariff() {
	s := site.peak()

	s.mu.Lock()
	t := s.tariff
	months := append([]peakMonth(nil), s.months...)
	s.mu.Unlock()

	site.publish(keys.PeakTariff, peakTariffState{peakTariff: t, Months: t.months(months)})
}

// SetPeakTariff sets one value of the capacity tariff
func (site *Site) SetPeakTariff(name string, value float64) error {
	limit, ok := peakTariffLimits[name]
	if !ok {
		return fmt.Errorf("unknown setting: %s", name)
	}
	if value < limit[0] || value > limit[1] {
		return fmt.Errorf("%s must be between %g and %g", name, limit[0], limit[1])
	}

	s := site.peak()

	s.mu.Lock()
	switch name {
	case "price":
		s.tariff.Price = value
	case "threshold":
		s.tariff.Threshold = value
	case "priceAbove":
		s.tariff.PriceAbove = value
	case "agreed":
		s.tariff.Agreed = value
	case "minShare":
		s.tariff.MinShare = value
	case "minimum":
		s.tariff.Minimum = value
	}
	t := s.tariff
	s.mu.Unlock()

	site.log.DEBUG.Printf("set capacity tariff %s: %g", name, value)

	if err := settings.SetJson(keys.PeakTariff, t); err != nil {
		return err
	}

	site.publishPeakTariff()

	return nil
}
