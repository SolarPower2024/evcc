package core

import "github.com/evcc-io/evcc/core/keys"

// Custom extension: tells the ui that the charger switches a heater in stages,
// see charger/switchstages.go. Its mode buttons then read Off/Smart/On as for
// upstream's switch devices, without the switch device feature, which would
// drop the current settings and change load management.

// stagedCharger is implemented by charger.SwitchStages
type stagedCharger interface {
	Stages() int
}

// publishStages publishes if the charger switches in stages
func (lp *Loadpoint) publishStages() {
	_, ok := lp.charger.(stagedCharger)
	lp.publish(keys.ChargerStages, ok)
}
