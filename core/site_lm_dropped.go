package core

import (
	"maps"

	"github.com/evcc-io/evcc/core/loadpoint"
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
