package core

// Custom extension: one-time grid charging up to a soc.
//
// Started by hand on the battery page, it grid-charges the home battery once up
// to the chosen soc and then switches itself off. Either right away, or by a
// time of day at the cheapest slots before it, chosen by the upstream planner
// from the planner tariff. It survives a restart and can be cancelled. It is
// independent of the soc-based grid charging and passes the same gate (peak,
// circuit, charge power setpoint).
//
// With the optimizer in automatic mode it is an optimizer input instead: the
// target soc as goal, right away at the earliest step the allowed charge power
// can reach it, or at the chosen time. See site_optimizer_lm.go.

import (
	"errors"
	"fmt"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/core/planner"
	"github.com/evcc-io/evcc/db/settings"
	"github.com/evcc-io/evcc/util"
)

// gridChargeOnce is the one-time grid charge as stored and published
type gridChargeOnce struct {
	Target float64   `json:"target"`           // soc %, 0 = off
	Until  time.Time `json:"until,omitzero"`   // cheapest slots before this time, zero = right away
	Active bool      `json:"active,omitempty"` // charging in this slot
}

// maxGridChargeOnceAhead is how far ahead the time of day may be
const maxGridChargeOnceAhead = 48 * time.Hour

func (site *Site) gridChargeOnce() gridChargeOnce {
	s := site.lms()

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.gridOnce
}

func (site *Site) setGridChargeOnce(o gridChargeOnce) {
	s := site.lms()

	s.mu.Lock()
	s.gridOnce = o
	s.mu.Unlock()

	if err := settings.SetJson(keys.BatteryGridChargeOnce, o); err != nil {
		site.log.ERROR.Printf("battery grid charge once: %v", err)
	}
	site.publish(keys.BatteryGridChargeOnce, o)
}

// restoreGridChargeOnce continues a one-time grid charge across a restart
func (site *Site) restoreGridChargeOnce() {
	var o gridChargeOnce
	if err := settings.Json(keys.BatteryGridChargeOnce, &o); err == nil {
		s := site.lms()
		s.mu.Lock()
		s.gridOnce = o
		s.mu.Unlock()
	}

	site.publish(keys.BatteryGridChargeOnce, site.gridChargeOnce())
}

// SetBatteryGridChargeOnce starts one-time grid charging up to target soc, right
// away with a zero until, else at the cheapest slots before until
func (site *Site) SetBatteryGridChargeOnce(target float64, until time.Time) error {
	if !site.batteryConfigured() {
		return errors.New("battery not configured")
	}
	if target < 1 || target > 100 {
		return errors.New("target soc must be between 1 and 100")
	}
	if soc := site.GetBatterySoc(); target <= soc {
		return fmt.Errorf("battery is already at %.0f%%", soc)
	}
	if !until.IsZero() && (!until.After(time.Now()) || time.Until(until) > maxGridChargeOnceAhead) {
		return errors.New("time must be within the next 48 hours")
	}

	site.log.INFO.Printf("battery grid charge once: up to %.0f%%%s", target, onceUntilText(until))
	site.setGridChargeOnce(gridChargeOnce{Target: target, Until: until})
	site.Optimize()

	return nil
}

// CancelBatteryGridChargeOnce stops one-time grid charging
func (site *Site) CancelBatteryGridChargeOnce() error {
	if site.gridChargeOnce().Target == 0 {
		return nil
	}

	site.log.INFO.Println("battery grid charge once: cancelled")
	site.setGridChargeOnce(gridChargeOnce{})
	site.Optimize()

	return nil
}

func onceUntilText(until time.Time) string {
	if until.IsZero() {
		return ", right away"
	}
	return ", by " + until.Local().Format("15:04")
}

// batteryGridChargeOnceActive reports whether one-time grid charging charges in
// this cycle, and ends it once the target soc is reached
func (site *Site) batteryGridChargeOnceActive() bool {
	o := site.gridChargeOnce()
	if o.Target == 0 {
		return false
	}

	soc := site.GetBatterySoc()
	if soc >= o.Target {
		site.log.INFO.Printf("battery grid charge once: %.0f%% reached", o.Target)
		site.setGridChargeOnce(gridChargeOnce{})
		return false
	}

	active := o.Until.IsZero() || !time.Now().Before(o.Until) || site.onceSlotActive(o, soc)

	if active != o.Active {
		o.Active = active
		site.setGridChargeOnce(o)
	}

	return active
}

// onceRequiredDuration is how long charging from soc to the target takes at the
// grid charge power, 0 if unknown
func (site *Site) onceRequiredDuration(target, soc float64) time.Duration {
	power, _ := site.lmBatteryChargePower()
	capacity := site.batteryCapacityKWh()
	if power <= 0 || capacity <= 0 {
		return 0
	}

	hours := (target - soc) / 100 * capacity * 1e3 / power
	return time.Duration(hours * float64(time.Hour))
}

// onceSlotActive reports whether now is one of the cheapest slots the planner
// picks to reach the target by the time. Without a known duration or tariff it
// charges right away.
func (site *Site) onceSlotActive(o gridChargeOnce, soc float64) bool {
	required := site.onceRequiredDuration(o.Target, soc)
	if required <= 0 {
		return true
	}

	var tariff api.Tariff
	if site.tariffs != nil {
		tariff = site.GetTariff(api.TariffUsagePlanner)
	}

	plan := planner.New(util.NewLogger("gridcharge"), tariff).Plan(required, 0, o.Until, false)

	now := time.Now()
	for _, slot := range plan {
		if !now.Before(slot.Start) && now.Before(slot.End) {
			return true
		}
	}

	return false
}

// batteryCapacityKWh returns the capacity of all batteries, 0 if one is unknown
func (site *Site) batteryCapacityKWh() float64 {
	var res float64
	for _, dev := range site.batteryMeters {
		m, ok := api.Cap[api.BatteryCapacity](dev.Instance())
		if !ok || m.Capacity() <= 0 {
			return 0
		}
		res += m.Capacity()
	}
	return res
}
