package core

// Custom extension: the import from an energy community (EEG) in the grid chart.
//
// The grid meter of such a plant often shows the supplier's import only, the
// part from the energy community is counted as pv ("PV Papa"). A Home Assistant
// energy counter meters the import from the community. It is recorded like any
// other meter, per 15 minute slot, by a collector of group "meter" (title
// "import-eeg"), which upstream keeps out of every balance, and shown as a green
// bar of its own in the grid chart of the energy page (route feedinsplit).
//
// Display only: balances, costs, self-sufficiency, charging sessions and every
// control stay as they are. Without a counter nothing here runs.

import (
	"errors"
	"time"

	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/core/metrics"
	"github.com/evcc-io/evcc/db"
	"github.com/evcc-io/evcc/db/settings"
)

func (site *Site) importEeg() *feedInEegState {
	return &site.lms().importEeg
}

// restoreImportEeg resolves the persisted counter. It is not validated against
// Home Assistant here, so an unreachable instance at boot does not drop it.
func (site *Site) restoreImportEeg() {
	if entity, err := settings.String(keys.ImportEegEntity); err == nil && entity != "" {
		if err := site.applyImportEegEntity(entity, false); err != nil {
			site.log.ERROR.Printf("import eeg: %v", err)
		}
	}

	site.publish(keys.ImportEegEntity, site.GetImportEegEntity())
}

// GetImportEegEntity returns the Home Assistant counter of the EEG import
func (site *Site) GetImportEegEntity() string {
	s := site.importEeg()

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.entity
}

// SetImportEegEntity sets the Home Assistant energy counter of the EEG import,
// in kWh, Wh or MWh. Empty turns the display off.
func (site *Site) SetImportEegEntity(entity string) error {
	if err := site.checkEegEntity(entity); err != nil {
		return err
	}

	if entity == site.GetImportEegEntity() {
		return nil
	}

	if err := site.applyImportEegEntity(entity, true); err != nil {
		return err
	}

	site.log.DEBUG.Println("set import eeg entity:", entity)
	settings.SetString(keys.ImportEegEntity, entity)
	site.publish(keys.ImportEegEntity, entity)

	return nil
}

// applyImportEegEntity resolves the counter. A changed counter starts a fresh
// recording without the previous counter's reading, so the difference between
// the two is not taken as import. The running slot is dropped then.
func (site *Site) applyImportEegEntity(entity string, changed bool) error {
	var get func() (float64, error)

	if entity != "" {
		conn, err := site.haConnection()
		if err != nil {
			// keep the name, so the ui shows what is configured, as for the peak entities
			s := site.importEeg()
			s.mu.Lock()
			s.entity, s.get = entity, nil
			s.mu.Unlock()
			return err
		}
		// GetFloatState converts Wh and MWh to kWh
		get = func() (float64, error) { return conn.GetFloatState(entity) }
	}

	s := site.importEeg()

	s.mu.Lock()
	defer s.mu.Unlock()

	if (s.collector == nil || changed) && db.Instance != nil {
		var opt []func(*metrics.Accumulator)
		if s.clock != nil {
			opt = append(opt, metrics.WithClock(s.clock))
		}

		// titled with its name, so the energy page can leave it out of its meters
		c, err := metrics.NewCollector(metrics.Meter, metrics.ImportEeg, metrics.ImportEeg, opt...)
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

// updateImportEeg records the EEG import counter. Called every cycle, does
// nothing without a counter.
func (site *Site) updateImportEeg() {
	s := site.importEeg()

	now := time.Now()
	if s.clock != nil {
		now = s.clock.Now()
	}

	s.mu.Lock()
	entity, get, c, wait := s.entity, s.get, s.collector, now.Before(s.retryAt)
	s.mu.Unlock()

	if get == nil || c == nil || wait {
		return
	}

	v, err := get()

	// refused after a call timed out in this cycle, that call logged the cause
	if errors.Is(err, errHAHung) {
		return
	}

	s.mu.Lock()
	warn, recovered := err != nil && !s.failing, err == nil && s.failing
	s.failing = err != nil
	if err != nil && isTimeout(err) {
		s.retryAt = now.Add(eegRetry)
	}
	s.mu.Unlock()

	switch {
	case warn:
		site.log.WARN.Printf("import eeg: %s: %v", entity, err)
	case recovered:
		site.log.INFO.Printf("import eeg: %s readable again", entity)
	}

	if err != nil {
		return
	}

	if err := c.SetEnergyMeterTotal(v); err != nil {
		site.log.ERROR.Printf("import eeg: %v", err)
	}
}
