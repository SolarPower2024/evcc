package core

// Custom extension: export sold under two feed-in tariffs.
//
// Part of the export goes to an energy community (EEG) at a fixed price, the
// rest at the standard feed-in tariff (OeMAG). A Home Assistant energy counter
// meters the EEG part. It is recorded like any other meter, per 15 minute slot,
// by a collector of group "meter", which upstream keeps out of every balance.
// The standard part is the grid meter's export minus EEG, derived from the
// persisted slots, see core/metrics/feedin_eeg_custom.go. The EEG price is the
// tariff assigned as "feedInEeg" and persisted per slot as well.
//
// Only counters are involved. The grid meter's power, which drives PV control,
// load management and peak shaving, is not touched, and self-consumed energy
// and the solar share of charging sessions stay valued at the standard feed-in
// tariff. Without a counter nothing here runs and evcc behaves as upstream.

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/benbjohnson/clock"
	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/core/metrics"
	"github.com/evcc-io/evcc/db"
	"github.com/evcc-io/evcc/db/settings"
	"github.com/evcc-io/evcc/tariff"
)

type feedInEegState struct {
	mu        sync.Mutex
	entity    string                  // Home Assistant energy counter, empty = off
	get       func() (float64, error) // resolved from entity, kWh
	collector *metrics.Collector      // records the counter per slot
	priceSlot time.Time               // last slot whose price was persisted
	failing   bool                    // a failing read was logged
	clock     clock.Clock             // for tests, nil = real time
}

func (site *Site) eeg() *feedInEegState {
	return &site.lms().eeg
}

// feedInEegTariff is the tariff assigned as second feed-in tariff, nil if none
func (site *Site) feedInEegTariff() api.Tariff {
	if site.tariffs == nil {
		return nil
	}
	return site.tariffs.FeedInEeg
}

// restoreFeedInEeg resolves the persisted counter. It is not validated against
// Home Assistant here, so an unreachable instance at boot does not drop it.
func (site *Site) restoreFeedInEeg() {
	if entity, err := settings.String(keys.FeedInEegEntity); err == nil && entity != "" {
		if err := site.applyFeedInEegEntity(entity, false); err != nil {
			site.log.ERROR.Printf("feed-in eeg: %v", err)
		}
	}

	site.publish(keys.FeedInEegEntity, site.GetFeedInEegEntity())
	site.publishFeedInEegPrice()
}

// GetFeedInEegEntity returns the Home Assistant counter of the EEG export
func (site *Site) GetFeedInEegEntity() string {
	s := site.eeg()

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.entity
}

// SetFeedInEegEntity sets the Home Assistant energy counter of the EEG export,
// in kWh, Wh or MWh. Empty turns the split off.
func (site *Site) SetFeedInEegEntity(entity string) error {
	if entity != "" {
		if !strings.HasPrefix(entity, "sensor.") && !strings.HasPrefix(entity, "input_number.") {
			return fmt.Errorf("must be a sensor or input_number entity: %s", entity)
		}

		conn, err := site.haConnection()
		if err != nil {
			return err
		}
		state, err := conn.GetState(entity)
		if err != nil {
			return fmt.Errorf("%s: %w", entity, err)
		}
		if unit := state.Attributes.UnitOfMeasurement; unit != "kWh" && unit != "Wh" && unit != "MWh" {
			return fmt.Errorf("%s must be an energy counter in kWh or Wh, not %q", entity, unit)
		}
	}

	if entity == site.GetFeedInEegEntity() {
		return nil
	}

	if err := site.applyFeedInEegEntity(entity, true); err != nil {
		return err
	}

	site.log.DEBUG.Println("set feed-in eeg entity:", entity)
	settings.SetString(keys.FeedInEegEntity, entity)
	site.publish(keys.FeedInEegEntity, entity)

	return nil
}

// applyFeedInEegEntity resolves the counter. A changed counter starts a fresh
// recording without the previous counter's reading, so the difference between
// the two is not taken as export. The running slot is dropped then.
func (site *Site) applyFeedInEegEntity(entity string, changed bool) error {
	var get func() (float64, error)

	if entity != "" {
		conn, err := site.haConnection()
		if err != nil {
			return err
		}
		// GetFloatState converts Wh and MWh to kWh
		get = func() (float64, error) { return conn.GetFloatState(entity) }
	}

	s := site.eeg()

	s.mu.Lock()
	defer s.mu.Unlock()

	if (s.collector == nil || changed) && db.Instance != nil {
		var opt []func(*metrics.Accumulator)
		if s.clock != nil {
			opt = append(opt, metrics.WithClock(s.clock))
		}

		c, err := metrics.NewCollector(metrics.Meter, metrics.FeedInEeg, "EEG", opt...)
		if err != nil {
			return err
		}

		// drops the reading restored from the database
		if changed {
			if err := c.SetCapabilities(false, false); err != nil {
				return err
			}
		}

		s.collector = c
	}

	s.entity, s.get, s.failing = entity, get, false

	return nil
}

// updateFeedInEeg records the EEG counter and the EEG price of the slot. Called
// every cycle, does nothing without a counter.
func (site *Site) updateFeedInEeg() {
	s := site.eeg()

	s.mu.Lock()
	entity, get, c := s.entity, s.get, s.collector
	s.mu.Unlock()

	if get == nil || c == nil {
		return
	}

	v, err := get()

	s.mu.Lock()
	warn, recovered := err != nil && !s.failing, err == nil && s.failing
	s.failing = err != nil
	s.mu.Unlock()

	switch {
	case warn:
		site.log.WARN.Printf("feed-in eeg: %s: %v", entity, err)
	case recovered:
		site.log.INFO.Printf("feed-in eeg: %s readable again", entity)
	}

	if err != nil {
		return
	}

	if err := c.SetEnergyMeterTotal(v); err != nil {
		site.log.ERROR.Printf("feed-in eeg: %v", err)
	}

	site.persistFeedInEegPrice(time.Now())
}

// persistFeedInEegPrice stores the EEG price once per slot
func (site *Site) persistFeedInEegPrice(now time.Time) {
	t := site.feedInEegTariff()
	if t == nil {
		return
	}

	slot := now.Truncate(tariff.SlotDuration)

	s := site.eeg()

	s.mu.Lock()
	done := !slot.After(s.priceSlot)
	s.mu.Unlock()

	if done {
		return
	}

	r, err := tariff.At(t, slot)
	if err != nil {
		return
	}

	if err := metrics.PersistEegPrice(slot, r.Value); err != nil {
		site.log.ERROR.Printf("feed-in eeg: persist price: %v", err)
		return
	}

	s.mu.Lock()
	s.priceSlot = slot
	s.mu.Unlock()

	site.publish(keys.TariffFeedInEeg, r.Value)
}

// publishFeedInEegPrice publishes the current EEG price, if any
func (site *Site) publishFeedInEegPrice() {
	if v, err := tariff.Now(site.feedInEegTariff()); err == nil {
		site.publish(keys.TariffFeedInEeg, v)
	}
}
