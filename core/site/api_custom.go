package site

// CustomAPI is the part of the site api added by this fork. It lives in its own
// file so that upstream changes to API merge without conflicts; API embeds it.
type CustomAPI interface {
	// soc-based grid charging, see core/site_lm.go
	GetBatterySocGridCharge() bool
	SetBatterySocGridCharge(bool) error
	GetBatterySocGridChargeStart() float64
	SetBatterySocGridChargeStart(float64) error
	GetBatterySocGridChargeStop() float64
	SetBatterySocGridChargeStop(float64) error

	// load management shed priorities, see core/site_lm.go
	SetLmPriority(name string, prio int) error

	// peak shaving, see core/site_peakshaving.go
	GetPeakShaving() bool
	SetPeakShaving(bool) error
	GetPeakShavingLimit() float64
	SetPeakShavingLimit(float64) error
	GetPeakShavingEntity() string
	SetPeakShavingEntity(string) error
	GetPeakShavingChargeEntity() string
	SetPeakShavingChargeEntity(string) error
	GetPeakShavingChargePower() float64
	SetPeakShavingChargePower(float64) error
	GetPeakShavingCircuit() string
	SetPeakShavingCircuit(string) error
	GetPeakShavingReserve() float64
	SetPeakShavingReserve(float64) error
}
