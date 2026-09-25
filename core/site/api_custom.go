package site

import "github.com/evcc-io/evcc/core/lm/profile"

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

	// load management shed guard, see core/site_lm_guard.go
	GetLmShedGuard() int
	SetLmShedGuard(int) error
	SetLmShedProtected(name string, protected bool) error

	// advanced load management settings, see core/site_lm_advanced.go
	SetLmAdvanced(name string, value float64) error

	// battery profiles, see core/site_lm_profiles.go
	SaveLmProfile(profile.Profile) (profile.Profile, error)
	DeleteLmProfile(id string) error
	ApplyLmProfile(id string) error

	// feed-in price published after the fact, see core/site_feedin.go
	FinalizeFeedIn(month string, market float64) error

	// peak shaving, see core/site_peakshaving.go
	GetPeakShaving() bool
	SetPeakShaving(bool) error
	GetPeakShavingLimit() float64
	SetPeakShavingLimit(float64) error
	GetPeakShavingEntity() string
	SetPeakShavingEntity(string) error
	GetPeakShavingChargeEntity() string
	SetPeakShavingChargeEntity(string) error
	GetPeakShavingEnergyEntity() string
	SetPeakShavingEnergyEntity(string) error
	GetPeakShavingChargePower() float64
	SetPeakShavingChargePower(float64) error
	GetPeakShavingCircuit() string
	SetPeakShavingCircuit(string) error
	GetPeakShavingReserve() float64
	SetPeakShavingReserve(float64) error
}
