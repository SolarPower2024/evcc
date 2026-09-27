package metrics

// Custom extension: a meter's persisted slots, for the battery identification,
// see core/site_battery_ident.go.

import (
	"time"

	"github.com/evcc-io/evcc/db"
)

// MeterSlot is one persisted 15 minute slot of a meter
type MeterSlot struct {
	Start        time.Time
	Energy       float64  // kWh, import (battery: charged)
	ReturnEnergy float64  // kWh, export (battery: discharged)
	Soc          *float64 // % at the start of the slot
}

// Slots returns the slots from the given time on, oldest first, without
// recovered downtime
func (c *Collector) Slots(from time.Time) ([]MeterSlot, error) {
	var rows []meter
	if err := db.Instance.
		Where("meter = ? AND ts >= ? AND COALESCE(recovered, 0) = 0", c.entity.Id, from.Unix()).
		Order("ts").Find(&rows).Error; err != nil {
		return nil, err
	}

	res := make([]MeterSlot, 0, len(rows))
	for _, r := range rows {
		res = append(res, MeterSlot{
			Start:        time.Unix(r.Timestamp, 0),
			Energy:       r.Energy,
			ReturnEnergy: r.ReturnEnergy,
			Soc:          r.SocTemp,
		})
	}

	return res, nil
}
