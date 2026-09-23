package keys

// Keys added by this fork. They live here rather than in site.go, so that
// upstream changes to that file merge without conflicts.
const (
	// soc-based battery grid charging
	BatterySocGridCharge        = "batterySocGridCharge"
	BatterySocGridChargeStart   = "batterySocGridChargeStart"
	BatterySocGridChargeStop    = "batterySocGridChargeStop"
	BatterySocGridChargeRunning = "batterySocGridChargeRunning" // hysteresis state, not published

	// battery peak shaving
	PeakShaving                     = "peakShaving"
	PeakShavingLimit                = "peakShavingLimit"
	PeakShavingReserve              = "peakShavingReserve"
	PeakShavingEntity               = "peakShavingEntity"
	PeakShavingActive               = "peakShavingActive"
	PeakShavingChargePower          = "peakShavingChargePower"
	PeakShavingChargePowerEffective = "peakShavingChargePowerEffective"
	PeakShavingChargePowerSource    = "peakShavingChargePowerSource"
	PeakShavingCircuit              = "peakShavingCircuit"
	PeakShavingPower                = "peakShavingPower"
	PeakShavingWindowAvg            = "peakShavingWindowAvg"
	PeakShavingChargeEntity         = "peakShavingChargeEntity"   // grid charge power target
	PeakShavingChargeSetpoint       = "peakShavingChargeSetpoint" // grid charge power written, 0 = not charging

	// load management shed priorities
	LmPriorities = "lmPriorities"

	// last month whose feed-in price was finalized, YYYY-MM, see core/site_feedin.go
	FeedInFinalized = "feedInFinalized"
)
