package core

import (
	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/loadpoint"
	"github.com/evcc-io/evcc/vehicle"
)

// Custom extension: evcc warns that a vehicle poll mode other than "charging"
// may deplete the battery or lead to API misuse. A vehicle from Home Assistant
// is polled from Home Assistant only, not from the manufacturer, so the warning
// does not apply to it. The loadpoint does not know which vehicle will plug in,
// so it warns unless every vehicle of the site is one from Home Assistant.

// warnPollMode logs evcc's poll mode warning, unless there is no vehicle or all
// of them come from Home Assistant
func (lp *Loadpoint) warnPollMode() {
	if lp.Soc.Poll.Mode == loadpoint.PollCharging {
		return
	}

	vehicles := lp.coordinatedVehicles()
	if len(vehicles) == 0 || allHomeAssistant(vehicles) {
		return
	}

	lp.log.WARN.Printf("poll mode '%s' may deplete your battery or lead to API misuse. USE AT YOUR OWN RISK.", lp.Soc.Poll)
}

// allHomeAssistant reports if every vehicle is a Home Assistant vehicle, whether
// configured in the ui, in a template or as type homeassistant in yaml: all of
// them end up as the one vehicle type, behind a wrapper while it is created
func allHomeAssistant(vehicles []api.Vehicle) bool {
	for _, v := range vehicles {
		if !api.HasCap[*vehicle.HomeAssistant](v) {
			return false
		}
	}

	return true
}
