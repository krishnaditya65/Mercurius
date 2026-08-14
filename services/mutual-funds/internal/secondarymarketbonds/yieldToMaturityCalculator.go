// Package secondarymarketbonds is a real secondary-market bond browser plus
// a genuine Yield-to-Maturity (YTM) calculator over internal/fixedincome's
// illustrative catalog — FEATURES.md §5, "Fixed Income", item 2
// ("Secondary market bond browsing + YTM calculator").
//
// LOUD CAVEAT, same honesty pattern as the rest of this service: THIS IS
// NOT CONNECTED TO ANY REAL SECONDARY BOND MARKET. There is no CCIL/NDS-OM
// (the real institutional secondary market for G-Secs) or exchange (NSE/
// BSE retail bond trading) integration anywhere in this repo. "Current
// secondary-market price" here is this package's own in-memory, entirely
// illustrative price per bond.
//
// The YTM math itself, however, IS real: CalculateYieldToMaturity solves
// the standard bond-pricing equation
//
//	price = Σ_{t=1..n} coupon/(1+y/m)^t + faceValue/(1+y/m)^n
//
// for the periodic yield y (then annualizes it as y*m) via Newton-Raphson,
// using the closed-form analytic derivative of price with respect to y
// (not a numeric finite-difference approximation) for each iteration step.
package secondarymarketbonds

import (
	"fmt"
	"math"
	"time"
)

var ErrInvalidPrice = fmt.Errorf("price must be strictly positive")
var ErrInvalidFaceValue = fmt.Errorf("face value must be strictly positive")
var ErrInvalidMaturity = fmt.Errorf("maturity date must be after the valuation date")
var ErrInvalidPaymentsPerYear = fmt.Errorf("paymentsPerYear must be positive for a coupon-bearing bond")
var ErrDidNotConverge = fmt.Errorf("newton-raphson did not converge to a yield within the iteration budget")

const (
	maxNewtonRaphsonIterations = 200
	// convergenceTolerance is expressed as a fraction of price (e.g. 1e-9
	// means "within one-billionth of the quoted price"), not an absolute
	// minor-unit amount, so it scales correctly across bonds of very
	// different face values.
	convergenceTolerance = 1e-9
	// initialYieldGuess seeds Newton-Raphson at a plausible starting yield
	// (10% annualized) — the iteration converges quadratically once close
	// to the root, so the exact starting point only affects how many
	// iterations are needed, never the final answer.
	initialYieldGuess = 0.10
)

// CalculateYieldToMaturity solves for the ANNUALIZED yield to maturity of a
// bond given its current clean price, coupon rate, face value, and payment
// frequency, valued as of asOf. periodsRemaining is derived from
// maturityDate and paymentsPerYear: the number of whole coupon periods
// between asOf and maturityDate, rounded to the nearest integer (a real
// bond desk would instead count exact coupon dates; this catalog doesn't
// track a full coupon-date schedule inside this function, only IssueDate/
// MaturityDate — internal/bondladderbuilder's coupon calendar does track
// the real per-date schedule for a holder's positions).
//
// A zero-coupon instrument (couponRatePercent == 0, e.g. a T-Bill) is
// solved via the closed-form
//
//	y = (faceValue / price)^(1/years) - 1
//
// directly — no iteration needed, and no rounding error from an iterative
// solve, since the equation is already linear in a single unknown power.
func CalculateYieldToMaturity(
	faceValueInMinorUnits int64,
	couponRatePercent float64,
	paymentsPerYear int,
	priceInMinorUnits int64,
	maturityDate time.Time,
	asOf time.Time,
) (annualizedYieldPercent float64, periodsRemaining int, err error) {
	if priceInMinorUnits <= 0 {
		return 0, 0, ErrInvalidPrice
	}
	if faceValueInMinorUnits <= 0 {
		return 0, 0, ErrInvalidFaceValue
	}
	if !maturityDate.After(asOf) {
		return 0, 0, ErrInvalidMaturity
	}

	yearsToMaturity := maturityDate.Sub(asOf).Hours() / (24 * 365.25)

	if couponRatePercent == 0 {
		// Pure discount instrument (T-Bill): price = faceValue / (1+y)^years.
		years := yearsToMaturity
		yield := math.Pow(float64(faceValueInMinorUnits)/float64(priceInMinorUnits), 1/years) - 1
		return yield * 100, 0, nil
	}

	if paymentsPerYear <= 0 {
		return 0, 0, ErrInvalidPaymentsPerYear
	}

	periods := int(math.Round(yearsToMaturity * float64(paymentsPerYear)))
	if periods < 1 {
		periods = 1
	}
	couponPerPeriod := float64(faceValueInMinorUnits) * (couponRatePercent / 100) / float64(paymentsPerYear)
	faceValue := float64(faceValueInMinorUnits)
	price := float64(priceInMinorUnits)

	periodicYield := initialYieldGuess / float64(paymentsPerYear)
	for iteration := 0; iteration < maxNewtonRaphsonIterations; iteration++ {
		priceAtYield, derivativeAtYield := priceAndDerivative(couponPerPeriod, faceValue, periodicYield, periods)
		difference := priceAtYield - price

		if math.Abs(difference/price) < convergenceTolerance {
			return periodicYield * float64(paymentsPerYear) * 100, periods, nil
		}
		if derivativeAtYield == 0 {
			break
		}

		nextPeriodicYield := periodicYield - difference/derivativeAtYield
		// Guard against Newton-Raphson stepping to a non-physical yield
		// (<=-100% periodic) which would blow up (1+y)^t.
		if nextPeriodicYield <= -1 {
			nextPeriodicYield = periodicYield / 2
		}
		periodicYield = nextPeriodicYield
	}

	return 0, 0, ErrDidNotConverge
}

// priceAndDerivative returns the bond's clean price and dPrice/dPeriodicYield
// at periodicYield, evaluated analytically (not via finite differences):
//
//	price(y)  = Σ_{t=1..n} C/(1+y)^t + F/(1+y)^n
//	price'(y) = Σ_{t=1..n} -t*C/(1+y)^(t+1) + -n*F/(1+y)^(n+1)
func priceAndDerivative(couponPerPeriod float64, faceValue float64, periodicYield float64, periods int) (price float64, derivative float64) {
	onePlusYield := 1 + periodicYield
	for t := 1; t <= periods; t++ {
		discountFactor := math.Pow(onePlusYield, float64(t))
		price += couponPerPeriod / discountFactor
		derivative += -float64(t) * couponPerPeriod / (discountFactor * onePlusYield)
	}
	finalDiscountFactor := math.Pow(onePlusYield, float64(periods))
	price += faceValue / finalDiscountFactor
	derivative += -float64(periods) * faceValue / (finalDiscountFactor * onePlusYield)
	return price, derivative
}

// PriceGivenYield is the inverse operation to CalculateYieldToMaturity: it
// prices a coupon-bearing bond at a given annualized yield — exposed
// publicly so tests (and callers) can construct a price from a KNOWN
// target yield and then verify CalculateYieldToMaturity recovers it,
// without that round-trip being circular (the hand-worked test cases in
// this package's tests do NOT use this function to derive their expected
// values; they use independent closed-form identities instead — see the
// test file's comments).
func PriceGivenYield(faceValueInMinorUnits int64, couponRatePercent float64, paymentsPerYear int, periods int, annualizedYieldPercent float64) int64 {
	couponPerPeriod := float64(faceValueInMinorUnits) * (couponRatePercent / 100) / float64(paymentsPerYear)
	periodicYield := annualizedYieldPercent / 100 / float64(paymentsPerYear)
	price, _ := priceAndDerivative(couponPerPeriod, float64(faceValueInMinorUnits), periodicYield, periods)
	return int64(math.Round(price))
}
