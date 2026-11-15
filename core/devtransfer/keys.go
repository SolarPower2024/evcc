package devtransfer

import "github.com/evcc-io/evcc/core/keys"

// DevTransferApplied is the key of the last give back, shown by evcc custom
const DevTransferApplied = "devTransferApplied"

// stateKeys are the settings entries that are state or statistics, not
// something set in the UI: giving back the settings leaves them as they are.
// Every fork key in core/keys/site_custom.go is either here or in settingKeys,
// TestForkKeysClassified makes a new one choose.
var stateKeys = []string{
	DevTransferApplied,
	keys.PeakMonths,
	keys.PeakResetAt,
	keys.PeakResetPeak,
	keys.LmEvents,
	keys.LmExportForecastID,
	keys.BatterySocGridChargeRunning,
	keys.BatteryGridChargeOnce,
	keys.PeakShavingOwned,
	keys.PeakShavingProtOwned,
	keys.PeakShavingHoldSoc,
	keys.SnowCoverAuto,
	keys.SnowSeen,
	keys.GridPeakMeter,

	// published only, never stored
	keys.PeakShavingLimit,
	keys.PeakShavingManual,
	keys.PeakShavingActive,
	keys.PeakShavingChargePower,
	keys.PeakShavingChargePowerEffective,
	keys.PeakShavingChargePowerSource,
	keys.PeakShavingPower,
	keys.PeakShavingWindowAvg,
	keys.PeakShavingAllowed,
	keys.PeakShavingWindowEnd,
	keys.PeakShavingSource,
	keys.PeakStatus,
	keys.PeakShavingChargeSetpoint,
	keys.LmStatus,
	keys.LmProfileWallboxes,
	keys.BatteryIdent,
	keys.TariffFeedInEeg,
	keys.SnowAutoAvailable,
}

// settingKeys are the fork keys that are set in the UI and replaced when the
// settings are given back
var settingKeys = []string{
	keys.BatterySocGridCharge,
	keys.BatterySocGridChargeStart,
	keys.BatterySocGridChargeStop,
	keys.BatteryGridChargeMode,
	keys.PeakShaving,
	keys.PeakShavingReserve,
	keys.PeakShavingBatteryType,
	keys.PeakShavingManualEntity,
	keys.PeakShavingModeEntity,
	keys.PeakShavingProtSwitch,
	keys.PeakShavingProtLimit,
	keys.PeakShavingProtSoc,
	keys.PeakShavingCircuit,
	keys.PeakShavingChargeEntity,
	keys.PeakTariff,
	keys.LmPriorities,
	keys.LmShedGuard,
	keys.LmShedProtected,
	keys.LmAdvanced,
	keys.LmHomeProfile,
	keys.BatteryIdentUse,
	keys.LmProfiles,
	keys.LmProfileActive,
	keys.FeedInEegEntity,
	keys.ImportEegEntity,
	keys.LogFile,
	keys.SnowCover,
	keys.SnowAuto,
}
