package core

import (
	"errors"
	"maps"

	"github.com/evcc-io/evcc/core/keys"
	"github.com/evcc-io/evcc/core/loadpoint"
	"github.com/evcc-io/evcc/db/settings"
	"github.com/evcc-io/evcc/util/config"
)

// dropStoredLoadpointKeys removes the keys the fork no longer reads from the stored
// config of the database loadpoints, see loadpoint.DroppedKeys. Loadpoints from
// the yaml file are the user's own and stay untouched.
func (site *Site) dropStoredLoadpointKeys() {
	for _, dev := range config.Loadpoints().Devices() {
		cd, ok := dev.(config.ConfigurableDevice[loadpoint.API])
		if !ok {
			continue
		}

		conf := cd.Config()

		var dropped []string
		other := maps.Clone(conf.Other)
		for _, key := range loadpoint.DroppedKeys() {
			if _, ok := other[key]; ok {
				delete(other, key)
				dropped = append(dropped, key)
			}
		}

		if len(dropped) == 0 {
			continue
		}

		if err := cd.Update(other, cd.Instance()); err != nil {
			site.log.ERROR.Printf("loadpoint %s: remove unused config %v: %v", conf.Name, dropped, err)
			continue
		}

		site.log.INFO.Printf("loadpoint %s: removed unused config %v", conf.Name, dropped)
	}
}

// dropLmOff removes the stored switch "load management off", which is gone: the
// circuits limit with their configured value. A switch left on "off" is noted
// in the log; a value that cannot be read is logged and removed as well.
func (site *Site) dropLmOff() {
	off, err := settings.Bool(keys.LmOff)
	switch {
	case errors.Is(err, settings.ErrNotFound):
		return
	case err != nil:
		site.log.WARN.Printf("load management switch removed, stored value unreadable: %v", err)
	case off:
		site.log.INFO.Println("load management switch removed, it was off: the circuits limit with their configured value again")
	}

	if err := settings.Delete(keys.LmOff); err != nil {
		site.log.ERROR.Printf("load management switch: remove stored value: %v", err)
	}
}

// dropLmCircuit removes the stored load management (peak) circuit and the switch
// "load management off", which are gone: the circuits limit with the power limit
// of their configuration, as in evcc. A circuit changed at runtime had its
// configured value only in memory, so it starts with it. lm3/lm4 stored the
// circuit under peakFollowCircuit.
func (site *Site) dropLmCircuit() {
	for _, key := range []string{keys.LmCircuit, keys.PeakFollowCircuit} {
		name, err := settings.String(key)
		if err != nil {
			continue
		}
		if name != "" {
			site.log.INFO.Printf("load management circuit %s removed: the circuits limit with their configured value", name)
		}
		if err := settings.Delete(key); err != nil {
			site.log.ERROR.Printf("load management circuit: remove stored value %s: %v", key, err)
		}
	}

	site.dropLmOff()
}
