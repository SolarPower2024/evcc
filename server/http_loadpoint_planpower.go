package server

// Custom extension: the plan routes show the power the plan is made with and
// what limits it, see core/loadpoint_planpower.go. evcc's response types stay
// as they are, the handlers embed them.

import (
	"github.com/evcc-io/evcc/core/loadpoint"
)

// planPowerReason is the part of a plan response that tells what limits the plan power
type planPowerReason struct {
	PowerReason string `json:"powerReason,omitempty"` // "peak" or "circuit", missing if nothing limits
}

// planPowerer is what a loadpoint offers that plans with a limited power
type planPowerer interface {
	PlanPower(maxPower float64) (float64, string)
}

// planPowerOf returns the power a plan for the loadpoint is made with and what
// limits it. Other loadpoint types (mocks) keep the power, without a reason.
func planPowerOf(lp loadpoint.API, power float64) (float64, string) {
	if p, ok := lp.(planPowerer); ok {
		return p.PlanPower(power)
	}
	return power, ""
}

// planWithReason is the plan response with the reason, left out if the optimizer made the plan
func planWithReason(res PlanResponse, reason string) any {
	if res.Optimizer {
		reason = ""
	}
	return struct {
		PlanResponse
		planPowerReason
	}{res, planPowerReason{reason}}
}

// planPreviewWithReason is the plan preview response with the reason
func planPreviewWithReason(res PlanPreviewResponse, reason string) any {
	return struct {
		PlanPreviewResponse
		planPowerReason
	}{res, planPowerReason{reason}}
}
