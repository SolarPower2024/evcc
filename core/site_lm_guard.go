package core

// Custom extension: the shed guard settings. A protected loadpoint that load
// management had to shed stays off for the configured minutes, so it does not
// flap while the demand hovers around the limit. The guard itself is in package
// core/lm and applied in core/loadpoint_lm.go.

import (
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/core/lm"
	"github.com/evcc-io/evcc/db/settings"
	"github.com/evcc-io/evcc/util/config"
)

const lmMaxGuardMinutes = 120

// restoreLmGuard restores the persisted shed guard settings
func (site *Site) restoreLmGuard() {
	s := site.lms()

	if v, err := settings.Int(keys.LmShedGuard); err == nil {
		s.mu.Lock()
		s.guardMinutes = int(v)
		s.mu.Unlock()
	}

	var names []string
	if err := settings.Json(keys.LmShedProtected, &names); err == nil {
		s.mu.Lock()
		s.guarded = make(map[string]bool, len(names))
		for _, name := range names {
			s.guarded[name] = true
		}
		s.mu.Unlock()
	}

	lm.SetGuardLookup(site.lmGuardLookup)

	site.publishLmGuard()
}

// lmGuardLookup returns how long a load stays off after it was shed, 0 if it is
// not protected
func (site *Site) lmGuardLookup(l lm.Load) time.Duration {
	name := site.lmLoadName(l)
	if name == "" {
		return 0
	}

	s := site.lms()

	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.guarded[name] {
		return 0
	}

	return time.Duration(s.guardMinutes) * time.Minute
}

// lmGuardedNames returns the protected loadpoints, sorted
func (s *lmState) lmGuardedNames() []string {
	return slices.Sorted(maps.Keys(s.guarded))
}

func (site *Site) publishLmGuard() {
	s := site.lms()

	s.mu.Lock()
	minutes, names := s.guardMinutes, s.lmGuardedNames()
	s.mu.Unlock()

	site.publish(keys.LmShedGuard, minutes)
	site.publish(keys.LmShedProtected, names)
}

// GetLmShedGuard returns how many minutes a protected loadpoint stays off after
// it was shed
func (site *Site) GetLmShedGuard() int {
	s := site.lms()

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.guardMinutes
}

// SetLmShedGuard sets how many minutes a protected loadpoint stays off after it
// was shed, 0 turns the guard off
func (site *Site) SetLmShedGuard(minutes int) error {
	if minutes < 0 || minutes > lmMaxGuardMinutes {
		return fmt.Errorf("guard must be between 0 and %d minutes", lmMaxGuardMinutes)
	}

	s := site.lms()

	s.mu.Lock()
	changed := s.guardMinutes != minutes
	s.guardMinutes = minutes
	s.mu.Unlock()

	if changed {
		site.log.DEBUG.Printf("set load management shed guard: %dm", minutes)
		settings.SetInt(keys.LmShedGuard, int64(minutes))
		site.publish(keys.LmShedGuard, minutes)
	}

	return nil
}

// SetLmShedProtected adds a loadpoint to the shed guard or removes it
func (site *Site) SetLmShedProtected(name string, protected bool) error {
	if _, err := config.Loadpoints().ByName(name); err != nil {
		return fmt.Errorf("unknown loadpoint: %s", name)
	}

	s := site.lms()

	s.mu.Lock()
	if s.guarded == nil {
		s.guarded = make(map[string]bool)
	}
	if protected {
		s.guarded[name] = true
	} else {
		delete(s.guarded, name)
	}
	names := s.lmGuardedNames()
	s.mu.Unlock()

	site.log.DEBUG.Printf("set load management shed guard: %s = %t", name, protected)

	if err := settings.SetJson(keys.LmShedProtected, names); err != nil {
		return err
	}

	site.publish(keys.LmShedProtected, names)

	return nil
}
