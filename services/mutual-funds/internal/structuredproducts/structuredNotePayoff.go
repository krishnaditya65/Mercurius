package structuredproducts

import "math"

// CalculatePayoff computes a note's REAL payoff at maturity given the
// underlying index's total return over the note's tenor
// (underlyingIndexReturnPercent, e.g. 12.5 for +12.5%) and the amount
// originally subscribed (principalInMinorUnits). The rule, matching the
// note's own definition:
//
//   - If underlyingIndexReturnPercent <= 0: the note's
//     PrincipalProtectionPercent% of principal is returned — for every note
//     in this catalog that's 100%, i.e. the FULL principal, regardless of
//     how far the index fell. This is the "capital protection" leg: no
//     downside participation at all, by construction.
//
//   - If underlyingIndexReturnPercent > 0: the investor participates in
//     ParticipationRatePercent% of that positive return
//     (participatedReturnPercent = underlyingIndexReturnPercent *
//     ParticipationRatePercent / 100), capped at CapPercent% total return
//     (effectiveReturnPercent = min(participatedReturnPercent, CapPercent)),
//     and the payoff is principal * (1 + effectiveReturnPercent/100).
//
// Returns the payout amount and whether the CapPercent ceiling was the
// binding constraint (WasCapped).
func CalculatePayoff(note Note, principalInMinorUnits int64, underlyingIndexReturnPercent float64) (payoutInMinorUnits int64, effectiveReturnPercent float64, wasCapped bool) {
	if underlyingIndexReturnPercent <= 0 {
		protectedPayout := float64(principalInMinorUnits) * (note.PrincipalProtectionPercent / 100)
		return int64(math.Round(protectedPayout)), 0, false
	}

	participatedReturnPercent := underlyingIndexReturnPercent * (note.ParticipationRatePercent / 100)
	effectiveReturnPercent = participatedReturnPercent
	if participatedReturnPercent > note.CapPercent {
		effectiveReturnPercent = note.CapPercent
		wasCapped = true
	}

	payout := float64(principalInMinorUnits) * (1 + effectiveReturnPercent/100)
	return int64(math.Round(payout)), effectiveReturnPercent, wasCapped
}
