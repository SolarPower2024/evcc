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
	PeakShavingAllowed              = "peakShavingAllowed"        // grid power allowed for the rest of the window
	PeakShavingWindowEnd            = "peakShavingWindowEnd"      // end of the running window
	PeakShavingSource               = "peakShavingSource"         // where the window's energy comes from: meter, entity or power
	PeakShavingEnergyEntity         = "peakShavingEnergyEntity"   // Home Assistant grid import counter
	PeakShavingChargeEntity         = "peakShavingChargeEntity"   // grid charge power target
	PeakShavingChargeSetpoint       = "peakShavingChargeSetpoint" // grid charge power written, 0 = not charging

	// load management shed priorities
	LmPriorities = "lmPriorities"

	// load management shed guard: minutes a shed loadpoint stays off, and which
	LmShedGuard     = "lmShedGuard"
	LmShedProtected = "lmShedProtected"

	// advanced load management settings, see core/site_lm_advanced.go
	LmAdvanced = "lmAdvanced"

	// load management overview: every load's state and the event log, see core/site_lm_status.go
	LmStatus = "lmStatus"

	// battery profiles, see core/site_lm_profiles.go
	LmProfiles         = "lmProfiles"
	LmProfileActive    = "lmProfileActive"
	LmProfileWallboxes = "lmProfileWallboxes" // loadpoints a profile can set the solar share of

	// last month whose feed-in price was finalized, YYYY-MM, see core/site_feedin.go
	FeedInFinalized = "feedInFinalized"
	FeedInHistory   = "feedInHistory" // finalized months, see core/site_feedin.go
	FeedInFinal     = "feedInFinal"   // published: finalize day, market price and history
)
