package core

// Custom extension: the fork's loads in the planner's circuit ledger (evcc PR
// 34044), ranked by the same priority as everywhere else, see site_lm_priority.go.
//
// The planner only knows loadpoint plans. Two inputs are added to its ledger,
// both released as soon as they no longer apply:
//
//   - the battery while it grid charges, for the running slot, ranked by its
//     priority, so lower ranked plans go around it
//   - while peak shaving is on, the part of the root circuit above the peak
//     limit, ranked above everything, so plans stay within the limit
//
// Without circuits, battery circuit and peak shaving the ledger holds nothing
// of ours and the planner behaves as upstream.

import (
	"math"
	"time"

	"github.com/evcc-io/evcc/api"
	"github.com/evcc-io/evcc/core/planner"
	"github.com/evcc-io/evcc/tariff"
)

// ledger owner ids of the fork's reservations, below the loadpoints' 0..n
const (
	ledgerBatteryId = -1
	ledgerPeakId    = -2
)

// ledgerHorizon is how far ahead the peak limit is reserved
const ledgerHorizon = 48 * time.Hour

// setLedger keeps the planner's ledger for the fork's reservations
func (site *Site) setLedger(l *planner.Ledger) {
	s := site.lms()

	s.mu.Lock()
	defer s.mu.Unlock()

	s.ledger = l
}

// updateLmLedger refreshes the fork's reservations in the planner's ledger.
// Called once per cycle.
func (site *Site) updateLmLedger(gridCharge bool) {
	s := site.lms()

	s.mu.Lock()
	l := s.ledger
	s.mu.Unlock()

	if l == nil {
		return
	}

	now := time.Now()
	slot := now.Truncate(tariff.SlotDuration)

	site.reserveBattery(l, gridCharge, slot)
	site.reservePeakLimit(l, slot)
}

// reserveBattery holds the running slot for the battery while it grid charges
// on a circuit
func (site *Site) reserveBattery(l *planner.Ledger, gridCharge bool, slot time.Time) {
	owner := planner.Owner{Id: ledgerBatteryId}

	c := site.lmBatteryCircuit()
	power, _ := site.lmBatteryChargePower()

	if !gridCharge || c == nil || power <= 0 {
		l.Reserve(owner, nil, nil)
		return
	}

	end := slot.Add(tariff.SlotDuration)

	owner.Priority = site.lmBatteryPriority()
	owner.Target = end
	owner.Circuit = c
	owner.MaxPower = power

	l.Reserve(owner, api.Rates{{Start: slot, End: end}}, nil)
}

// reservePeakLimit holds the part of the root circuit above the peak limit while
// peak shaving is on
func (site *Site) reservePeakLimit(l *planner.Ledger, slot time.Time) {
	owner := planner.Owner{Id: ledgerPeakId}

	root := site.circuit
	limit := site.GetPeakShavingLimit()

	var above float64
	if root != nil && site.GetPeakShaving() && limit > 0 {
		above = root.GetMaxPower() - limit
	}

	if above <= 0 {
		l.Reserve(owner, nil, nil)
		return
	}

	owner.Priority = math.MaxInt
	owner.Circuit = root
	owner.MaxPower = above

	l.Reserve(owner, api.Rates{{Start: slot, End: slot.Add(ledgerHorizon)}}, nil)
}
