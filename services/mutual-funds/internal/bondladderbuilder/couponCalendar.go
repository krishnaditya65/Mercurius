package bondladderbuilder

import (
	"math"
	"sort"
	"time"

	"mercurius/mutualFunds/internal/fixedincome"
)

// CouponReminder is one upcoming coupon payment date for one of a holder's
// bond positions.
type CouponReminder struct {
	BondId             string
	IssueName          string
	CouponDate         time.Time
	AmountInMinorUnits int64
}

// UpcomingCoupons computes a REAL coupon-payment calendar for every bond
// accountIdentifier holds (via BuildLadder), from asOf up to and including
// each bond's MaturityDate. Coupon dates are computed genuinely from each
// bond's own schedule: IssueDate, stepped forward by 12/PaymentsPerYear
// months at a time (calendar-aware, via time.Time.AddDate — the same
// pattern internal/sipscheduler uses for step-up anniversaries), until
// MaturityDate. A zero-coupon instrument (PaymentsPerYear == 0, i.e. a
// T-Bill) produces NO reminders — correctly: there is no periodic coupon,
// the entire return is realized at redemption, not on a coupon date.
//
// AmountInMinorUnits for each reminder is the HELD face value (not the
// catalog's full FaceValueInMinorUnits) times CouponRatePercent/100,
// divided by PaymentsPerYear — i.e. proportional to how much of that bond
// the account actually holds via its ladder(s).
//
// Sorted by CouponDate ascending, then BondId, for a deterministic
// response.
func (builder *Builder) UpcomingCoupons(catalog *fixedincome.BondCatalog, accountIdentifier string, asOf time.Time) []CouponReminder {
	holdings := builder.HoldingsForAccount(accountIdentifier)

	reminders := make([]CouponReminder, 0)
	for bondId, heldFaceValueInMinorUnits := range holdings {
		if heldFaceValueInMinorUnits <= 0 {
			continue
		}
		bond, wasFound := catalog.Lookup(bondId)
		if !wasFound || bond.PaymentsPerYear <= 0 || bond.CouponRatePercent <= 0 {
			continue
		}

		monthsPerPeriod := 12 / bond.PaymentsPerYear
		couponAmount := int64(math.Round(float64(heldFaceValueInMinorUnits) * (bond.CouponRatePercent / 100) / float64(bond.PaymentsPerYear)))

		couponDate := bond.IssueDate
		for {
			couponDate = couponDate.AddDate(0, monthsPerPeriod, 0)
			if couponDate.After(bond.MaturityDate) {
				break
			}
			if couponDate.After(asOf) {
				reminders = append(reminders, CouponReminder{
					BondId:             bond.BondId,
					IssueName:          bond.IssueName,
					CouponDate:         couponDate,
					AmountInMinorUnits: couponAmount,
				})
			}
		}
	}

	sort.Slice(reminders, func(i, j int) bool {
		if !reminders[i].CouponDate.Equal(reminders[j].CouponDate) {
			return reminders[i].CouponDate.Before(reminders[j].CouponDate)
		}
		return reminders[i].BondId < reminders[j].BondId
	})
	return reminders
}
