package keys

// Keys added by this fork. They live here rather than in site.go, so that
// upstream changes to that file merge without conflicts.
const (
	// soc-based battery grid charging
	BatterySocGridCharge        = "batterySocGridCharge"
	BatterySocGridChargeStart   = "batterySocGridChargeStart"
	BatterySocGridChargeStop    = "batterySocGridChargeStop"
	BatterySocGridChargeRunning = "batterySocGridChargeRunning" // hysteresis state, not published
	BatteryGridChargeOnce       = "batteryGridChargeOnce"       // one-time grid charging, see core/site_lm_once.go

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
	PeakMonths                      = "peakMonths"                // monthly peak statistics
	PeakShavingChargeEntity         = "peakShavingChargeEntity"   // grid charge power target
	PeakShavingChargeSetpoint       = "peakShavingChargeSetpoint" // grid charge power written, 0 = not charging
	PeakFollow                      = "peakFollow"                // follow the peak, see core/site_peak_follow.go
	PeakFollowBuffer                = "peakFollowBuffer"
	PeakFollowBase                  = "peakFollowBase"
	PeakFollowCircuit               = "peakFollowCircuit" // lm3/lm4, taken over into LmCircuit
	PeakTariff                      = "peakTariff"        // capacity tariff, see core/site_peak_tariff.go

	// load management shed priorities
	LmPriorities = "lmPriorities"
	// loadpoint load management priorities taken over into the loadpoint priority, see core/site_lm_planner.go
	LmPrioritiesUnified = "lmPrioritiesUnified"

	// load management shed guard: minutes a shed loadpoint stays off, and which
	LmShedGuard     = "lmShedGuard"
	LmShedProtected = "lmShedProtected"

	// advanced load management settings, see core/site_lm_advanced.go
	LmAdvanced = "lmAdvanced"

	// load management switched off, see core/site_lm_switch.go
	LmOff     = "lmOff"
	LmCircuit = "lmCircuit" // the load management (peak) circuit

	// load management overview: every load's state and the event log, see core/site_lm_status.go
	LmStatus = "lmStatus"

	// battery profiles, see core/site_lm_profiles.go
	LmProfiles         = "lmProfiles"
	LmProfileActive    = "lmProfileActive"
	LmProfileWallboxes = "lmProfileWallboxes" // loadpoints a profile can set the solar share of

	// export under a second feed-in tariff, see core/site_feedin_eeg.go
	FeedInEegEntity = "feedInEegEntity" // Home Assistant energy counter of the EEG export
	TariffFeedInEeg = "tariffFeedInEeg" // published: current EEG price
)
