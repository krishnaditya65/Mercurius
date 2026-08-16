package bondladderbuilder

import (
	"fmt"
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
//
// PaymentsPerYear support is intentionally narrow: this function's coupon
// schedule steps forward by a whole number of calendar months
// (12/PaymentsPerYear) at a time, so it only supports frequencies that
// evenly divide 12 (1, 2, 3, 4, 6, 12 — annual through monthly). Plain
// integer division silently truncates for any value that doesn't evenly
// divide 12 (e.g. 5, 7, 8, 9, 11), producing systematically wrong coupon
// dates without any error. internal/fixedincome's static seed catalog
// only ever uses 0 (T-Bill, handled above) or 2 (semi-annual) today, but
// to stop a future catalog addition with a non-divisor frequency from
// silently corrupting coupon dates, unsupportedPaymentsPerYear below
// panics loudly instead. If a genuinely non-divisor frequency is ever
// needed, this function must be rewritten to compute period boundaries
// from an explicit per-frequency month list rather than 12/PaymentsPerYear.
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

		monthsPerPeriod := monthsPerCouponPeriodOrPanic(bond)
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

// monthsPerCouponPeriodOrPanic returns 12/bond.PaymentsPerYear months, the
// calendar-month step UpcomingCoupons advances by each period. It panics
// for any PaymentsPerYear that does not evenly divide 12 — see
// UpcomingCoupons' doc comment for why: plain integer division would
// otherwise silently truncate (e.g. 12/5 == 2, not 2.4), producing
// systematically wrong coupon dates with no error at all. Callers must
// only reach this with bond.PaymentsPerYear > 0 (T-Bills are filtered out
// before this is called).
func monthsPerCouponPeriodOrPanic(bond fixedincome.Bond) int {
	if 12%bond.PaymentsPerYear != 0 {
		panic(fmt.Sprintf(
			"bondladderbuilder: bond %s has PaymentsPerYear=%d, which does not evenly divide 12 — "+
				"UpcomingCoupons' 12/PaymentsPerYear month-stepping schedule only supports frequencies "+
				"that evenly divide 12 (1, 2, 3, 4, 6, 12); see this function's doc comment",
			bond.BondId, bond.PaymentsPerYear,
		))
	}
	return 12 / bond.PaymentsPerYear
}
