package core

// Custom extension: what battery grid charging goes by. evcc charges below a
// price limit (dynamic tariff), the fork charges between a start and a stop soc.
// The choice decides which of the two the battery page shows and, when it is
// made, ends the other one: soc clears evcc's price limit, price switches the
// soc-based charging off (its thresholds stay). Both leaves everything as it is,
// it is the default. Nothing is cleared or switched at the start, only by the
// choice itself.

import (
	"errors"
	"fmt"

	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/db/settings"
)

const (
	gridModeBoth  = "both"
	gridModeSoc   = "soc"
	gridModePrice = "price"
)

// validBatteryGridChargeMode checks the choice, stored and set
func validBatteryGridChargeMode(mode string) error {
	switch mode {
	case gridModeBoth, gridModeSoc, gridModePrice:
		return nil
	}
	return fmt.Errorf("unknown grid charge mode: %s", mode)
}

// restoreBatteryGridChargeMode reads the stored choice back. A value that is
// none of the three is logged, deleted and replaced by both. Nothing is applied.
func (site *Site) restoreBatteryGridChargeMode() {
	if mode, err := settings.String(keys.BatteryGridChargeMode); err == nil {
		if err := validBatteryGridChargeMode(mode); err != nil {
			dropInvalid(site.log, keys.BatteryGridChargeMode, err)
		} else {
			s := site.lms()
			s.mu.Lock()
			s.gridMode = mode
			s.mu.Unlock()
		}
	} else if !errors.Is(err, settings.ErrNotFound) {
		dropInvalid(site.log, keys.BatteryGridChargeMode, err)
	}

	site.publish(keys.BatteryGridChargeMode, site.GetBatteryGridChargeMode())
}

// GetBatteryGridChargeMode returns what grid charging goes by: both, soc or price
func (site *Site) GetBatteryGridChargeMode() string {
	s := site.lms()

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.gridMode == "" {
		return gridModeBoth
	}

	return s.gridMode
}

// SetBatteryGridChargeMode sets what grid charging goes by. soc clears evcc's
// price limit, price switches the soc-based grid charging off.
func (site *Site) SetBatteryGridChargeMode(mode string) error {
	if err := validBatteryGridChargeMode(mode); err != nil {
		return err
	}

	site.log.DEBUG.Println("set battery grid charge mode:", mode)

	switch mode {
	case gridModeSoc:
		// evcc's own setter body: the limit is cleared also while the optimizer
		// is automatic, where it only reads as unset. Without battery control
		// there is no limit to clear.
		if err := site.setBatteryGridChargeLimit(nil); err != nil && !errors.Is(err, ErrBatteryControlNotAvailable) {
			return err
		}
	case gridModePrice:
		if err := site.SetBatterySocGridCharge(false); err != nil {
			return err
		}
	}

	s := site.lms()

	s.mu.Lock()
	s.gridMode = mode
	s.mu.Unlock()

	settings.SetString(keys.BatteryGridChargeMode, mode)
	site.publish(keys.BatteryGridChargeMode, mode)

	return nil
}
