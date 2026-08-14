// This file extends internal/marginfunding with FEATURES.md §21's
// "margin/leverage interest cost calculator shown live": a real "cost so
// far" figure (interest accrued from the account's real recorded
// disbursement start time through `asOfTime`) AND a real "projected cost
// if held N more days" figure, both using the exact same
// CalculateIllustrativeAccruedInterest simple-interest formula this
// package already had — no new/different rate, no new math model, just
// a live-clock-aware wrapper around it. See that function's own doc
// comment for the illustrative-rate caveat, which applies identically
// here.
package marginfunding

import "time"

// daysElapsedFloorZero returns the whole number of days between start
// and end (end - start), floored at 0 — a negative gap (end before
// start, e.g. a caller passing asOfTime before disbursedAtTime by
// mistake) is treated as zero elapsed days rather than producing a
// nonsensical negative interest figure.
func daysElapsedFloorZero(start time.Time, end time.Time) int64 {
	elapsed := end.Sub(start)
	if elapsed <= 0 {
		return 0
	}
	return int64(elapsed.Hours() / 24)
}

// CalculateCostSoFarInMinorUnits computes the real interest cost accrued
// on principalInMinorUnits from disbursedAtTime through asOfTime, using
// CalculateIllustrativeAccruedInterest. Returns 0 if asOfTime is not
// after disbursedAtTime, or principal is non-positive.
func CalculateCostSoFarInMinorUnits(principalInMinorUnits int64, disbursedAtTime time.Time, asOfTime time.Time) int64 {
	daysElapsed := daysElapsedFloorZero(disbursedAtTime, asOfTime)
	return CalculateIllustrativeAccruedInterest(principalInMinorUnits, daysElapsed)
}

// CalculateProjectedCostInMinorUnits computes the TOTAL interest cost —
// from disbursedAtTime all the way through (asOfTime +
// additionalDaysToProject) — assuming principalInMinorUnits stays
// exactly constant over the projected additional days (no further draws
// or repayments). This is the "if held N more days" figure: it already
// INCLUDES the cost-so-far portion, not just the incremental piece, so a
// caller wanting only the incremental projected cost should subtract
// CalculateCostSoFarInMinorUnits's result from this one.
//
// additionalDaysToProject must be >= 0 — a negative value is clamped to
// 0 (equivalent to CalculateCostSoFarInMinorUnits).
func CalculateProjectedCostInMinorUnits(principalInMinorUnits int64, disbursedAtTime time.Time, asOfTime time.Time, additionalDaysToProject int64) int64 {
	if additionalDaysToProject < 0 {
		additionalDaysToProject = 0
	}
	totalDays := daysElapsedFloorZero(disbursedAtTime, asOfTime) + additionalDaysToProject
	return CalculateIllustrativeAccruedInterest(principalInMinorUnits, totalDays)
}

// LiveInterestCostSnapshot bundles both figures for a single API
// response.
type LiveInterestCostSnapshot struct {
	OutstandingPrincipalInMinorUnits     int64     `json:"outstandingPrincipalInMinorUnits"`
	DisbursedAtTime                      time.Time `json:"disbursedAtTime,omitempty"`
	AsOfTime                             time.Time `json:"asOfTime"`
	DaysElapsedSoFar                     int64     `json:"daysElapsedSoFar"`
	CostSoFarInMinorUnits                int64     `json:"costSoFarInMinorUnits"`
	AdditionalDaysProjected              int64     `json:"additionalDaysProjected"`
	ProjectedTotalCostInMinorUnits       int64     `json:"projectedTotalCostInMinorUnits"`
	ProjectedIncrementalCostInMinorUnits int64     `json:"projectedIncrementalCostInMinorUnits"`
}

// BuildLiveInterestCostSnapshot is a pure convenience wrapper combining
// both calculators into one response shape for the HTTP handler.
func BuildLiveInterestCostSnapshot(
	principalInMinorUnits int64,
	disbursedAtTime time.Time,
	asOfTime time.Time,
	additionalDaysToProject int64,
) LiveInterestCostSnapshot {
	costSoFar := CalculateCostSoFarInMinorUnits(principalInMinorUnits, disbursedAtTime, asOfTime)
	projectedTotal := CalculateProjectedCostInMinorUnits(principalInMinorUnits, disbursedAtTime, asOfTime, additionalDaysToProject)
	return LiveInterestCostSnapshot{
		OutstandingPrincipalInMinorUnits:     principalInMinorUnits,
		DisbursedAtTime:                      disbursedAtTime,
		AsOfTime:                             asOfTime,
		DaysElapsedSoFar:                     daysElapsedFloorZero(disbursedAtTime, asOfTime),
		CostSoFarInMinorUnits:                costSoFar,
		AdditionalDaysProjected:              additionalDaysToProject,
		ProjectedTotalCostInMinorUnits:       projectedTotal,
		ProjectedIncrementalCostInMinorUnits: projectedTotal - costSoFar,
	}
}
