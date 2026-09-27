package core

// Custom extension: battery identification. The optimizer and one-time grid
// charging plan with the battery's datasheet capacity and a fixed efficiency.
// Both are learned here from the 15 minute slots evcc stores for each battery
// (energy charged and discharged, soc at the slot start) over the last 60 days:
//
//   - a charging run over at least 20 % soc gives the energy put in per 100 %
//     soc, kc = capacity / η; a discharging run the energy taken out, kd =
//     capacity × η (η: one way efficiency, the same both ways)
//   - from the medians of both: capacity = √(kc × kd), round trip efficiency =
//     kd / kc
//
// Runs with a soc jump (recalibration), a gap or noticeable flow the other way
// are left out. Shown under Lastmanagement-Details → Batterie-Vermessung; the
// optimizer and one-time grid charging only use the values when switched on
// there and plausible. The optimizer's efficiency is one value for all
// batteries and vehicles, so it stays; only the capacity goes into its request.

import (
	"math"
	"slices"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/core/metrics"
	"github.com/evcc-io/evcc/db/settings"
	optimizer "github.com/evcc-io/optimizer/client"
)

const (
	identDays        = 60   // days of history
	identMinSpan     = 20.0 // % soc a run must cover
	identMaxOpposite = 0.05 // share of flow the other way a run may have
	identMaxSocStep  = 15.0 // % soc change within one slot counted as recalibration
	identMinFlow     = 0.01 // kWh per slot below which a slot is idle
	identIdleSlots   = 4    // idle slots that end a run, e.g. a full battery
	identMinRuns     = 3    // runs needed in each direction
	identRefresh     = 6 * time.Hour
)

// batteryIdentResult is one battery's identification
type batteryIdentResult struct {
	Name       string  `json:"name"`
	Title      string  `json:"title"`
	Configured float64 `json:"configured"` // kWh, datasheet
	Capacity   float64 `json:"capacity"`   // kWh measured, 0 = not yet
	Efficiency float64 `json:"efficiency"` // round trip 0..1, 0 = not yet
	Charges    int     `json:"charges"`    // charging runs found
	Discharges int     `json:"discharges"` // discharging runs found
	Valid      bool    `json:"valid"`      // enough runs and plausible
}

// batteryIdentState is what the ui shows
type batteryIdentState struct {
	Use       bool                 `json:"use"`
	Batteries []batteryIdentResult `json:"batteries"`
	Updated   time.Time            `json:"updated"`
}

// batteryRuns returns the energy per 100 % soc of every charging and
// discharging run in the slots, oldest first
func batteryRuns(slots []metrics.MeterSlot) (charge, discharge []float64) {
	type run struct {
		dir       int // 1 charging, -1 discharging
		startSoc  float64
		main, opp float64
		idle      int // idle slots in a row
	}

	var cur *run

	finish := func(endSoc float64) {
		if cur == nil {
			return
		}
		span := (endSoc - cur.startSoc) * float64(cur.dir)
		if span >= identMinSpan && cur.main > 0 && cur.opp <= identMaxOpposite*cur.main {
			if v := cur.main / (span / 100); cur.dir > 0 {
				charge = append(charge, v)
			} else {
				discharge = append(discharge, v)
			}
		}
		cur = nil
	}

	for i, s := range slots {
		contiguous := i > 0 && s.Start.Sub(slots[i-1].Start) == 15*time.Minute
		if !contiguous || s.Soc == nil || slots[i-1].Soc == nil || math.Abs(*s.Soc-*slots[i-1].Soc) > identMaxSocStep {
			cur = nil // no usable end for a running run
		}
		if s.Soc == nil {
			continue
		}

		dir := 0
		switch {
		case s.Energy > s.ReturnEnergy && s.Energy >= identMinFlow:
			dir = 1
		case s.ReturnEnergy > s.Energy && s.ReturnEnergy >= identMinFlow:
			dir = -1
		}

		// a run ends where the flow turns or stops for a while, its end soc is
		// this slot's start soc
		if cur != nil && dir != 0 && dir != cur.dir {
			finish(*s.Soc)
		}
		if cur != nil && dir == 0 {
			if cur.idle++; cur.idle >= identIdleSlots {
				finish(*s.Soc)
			}
			continue
		}
		if cur != nil {
			cur.idle = 0
		}

		if cur == nil {
			if dir == 0 {
				continue
			}
			cur = &run{dir: dir, startSoc: *s.Soc}
		}

		if cur.dir > 0 {
			cur.main += s.Energy
			cur.opp += s.ReturnEnergy
		} else {
			cur.main += s.ReturnEnergy
			cur.opp += s.Energy
		}
	}

	return charge, discharge
}

func median(v []float64) float64 {
	s := slices.Clone(v)
	slices.Sort(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

// identifyBattery estimates capacity and efficiency from a battery's slots
func identifyBattery(slots []metrics.MeterSlot, configured float64) batteryIdentResult {
	charge, discharge := batteryRuns(slots)
	res := batteryIdentResult{Configured: configured, Charges: len(charge), Discharges: len(discharge)}

	if len(charge) < identMinRuns || len(discharge) < identMinRuns {
		return res
	}

	kc, kd := median(charge), median(discharge)
	res.Capacity = math.Round(math.Sqrt(kc*kd)*100) / 100
	res.Efficiency = math.Round(kd/kc*1000) / 1000

	ratio := res.Capacity / configured
	res.Valid = configured > 0 && ratio >= 0.5 && ratio <= 1.2 && res.Efficiency >= 0.6 && res.Efficiency <= 1

	return res
}

// restoreBatteryIdent restores the setting and runs a first identification
func (site *Site) restoreBatteryIdent() {
	if v, err := settings.Bool(keys.BatteryIdentUse); err == nil {
		s := site.lms()
		s.mu.Lock()
		s.identUse = v
		s.mu.Unlock()
	}
}

// updateBatteryIdent identifies the batteries, every few hours
func (site *Site) updateBatteryIdent() {
	s := site.lms()

	s.mu.Lock()
	due := time.Since(s.identAt) >= identRefresh
	if due {
		s.identAt = time.Now()
	}
	s.mu.Unlock()

	if !due {
		return
	}

	var res []batteryIdentResult
	for _, dev := range site.batteryMeters {
		ref := dev.Config().Name

		c, ok := site.collectors[ref]
		if !ok {
			continue
		}

		var configured float64
		if m, ok := api.Cap[api.BatteryCapacity](dev.Instance()); ok {
			configured = m.Capacity()
		}

		slots, err := c.Slots(time.Now().AddDate(0, 0, -identDays))
		if err != nil {
			site.log.ERROR.Printf("battery identification %s: %v", ref, err)
			continue
		}

		r := identifyBattery(slots, configured)
		r.Name, r.Title = ref, deviceProperties(dev).Title
		if r.Valid {
			site.log.DEBUG.Printf("battery identification %s: %.2f kWh (configured %.2f kWh), round trip %.0f%%", ref, r.Capacity, configured, r.Efficiency*100)
		}
		res = append(res, r)
	}

	s.mu.Lock()
	s.ident = res
	s.mu.Unlock()

	site.publishBatteryIdent()
}

func (site *Site) publishBatteryIdent() {
	s := site.lms()

	s.mu.Lock()
	res := batteryIdentState{Use: s.identUse, Batteries: slices.Clone(s.ident), Updated: s.identAt}
	s.mu.Unlock()

	if res.Batteries == nil {
		res.Batteries = make([]batteryIdentResult, 0)
	}

	site.publish(keys.BatteryIdent, res)
}

// batteryIdentFor returns a battery's identification when it is used
func (site *Site) batteryIdentFor(name string) (batteryIdentResult, bool) {
	s := site.lms()

	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.identUse {
		return batteryIdentResult{}, false
	}
	for _, r := range s.ident {
		if r.Name == name && r.Valid {
			return r, true
		}
	}
	return batteryIdentResult{}, false
}

// applyBatteryIdent puts the measured capacity into a battery's optimizer
// request: every stored energy scales with it, so the soc values stay
func (site *Site) applyBatteryIdent(bat *optimizer.BatteryConfig, detail *batteryDetail) {
	r, ok := site.batteryIdentFor(detail.Name)
	if !ok || bat.SCapacity <= 0 {
		return
	}

	f := float32(r.Capacity*1e3) / bat.SCapacity
	bat.SCapacity *= f
	bat.SInitial *= f
	bat.SMin *= f
	bat.SMax *= f
	for i := range bat.SGoal {
		bat.SGoal[i] *= f
	}
	detail.Capacity = r.Capacity
}

// identChargeEta is the charging efficiency: measured where every battery has
// one in use (capacity weighted), else the one the optimizer assumes
func (site *Site) identChargeEta() float64 {
	var sum, weight float64
	for _, dev := range site.batteryMeters {
		r, ok := site.batteryIdentFor(dev.Config().Name)
		if !ok {
			return eta
		}
		sum += math.Sqrt(r.Efficiency) * r.Capacity
		weight += r.Capacity
	}
	if weight <= 0 {
		return eta
	}
	return sum / weight
}

func (site *Site) GetBatteryIdentUse() bool {
	s := site.lms()

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.identUse
}

// SetBatteryIdentUse switches using the measured values
func (site *Site) SetBatteryIdentUse(use bool) error {
	s := site.lms()

	s.mu.Lock()
	s.identUse = use
	s.mu.Unlock()

	site.log.DEBUG.Println("set battery identification use:", use)
	settings.SetBool(keys.BatteryIdentUse, use)

	site.publishBatteryIdent()
	site.Optimize() // custom: the optimizer inputs changed, see core/site_optimizer_lm.go

	return nil
}
