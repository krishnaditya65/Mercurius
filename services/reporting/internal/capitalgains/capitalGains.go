// Package capitalgains computes real Indian-equity capital-gains tax
// figures (STCG/LTCG) from real fill history, via genuine FIFO
// lot-matching — not a fabricated or simplified average-cost
// approximation.
//
// DOCUMENTED HOLDING-PERIOD THRESHOLD: a listed-equity lot sold on or
// after exactly 12 months from its acquisition date is classified
// long-term (LTCG); sold before that, short-term (STCG). This mirrors
// the real Indian STCG/LTCG threshold for listed equity shares under
// Section 2(42A) of the Income Tax Act (long-term = held for more than
// 12 months). "12 months" is computed with calendar-accurate
// time.Time.AddDate(1, 0, 0), not a fixed 365-day constant, so it's
// correct across leap years.
//
// This package does NOT apply the STCG/LTCG *tax rates* themselves
// (currently 20%/12.5% respectively as of recent Indian budget changes,
// and liable to change by government notification independent of any
// code in this repo) — it computes the realized GAIN/LOSS amount and
// its STCG/LTCG classification only, which is the stable, computable
// part of this problem. Tax-rate application is left to whatever
// consumes this report, exactly like chargescalculator's own "rates
// will drift, don't hardcode tax computation on top of this" posture.
package capitalgains

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"mercurius/reporting/internal/filltrail"
)

const (
	GainTypeShortTerm = "STCG"
	GainTypeLongTerm  = "LTCG"
)

// buyLot is one still-open (partially or fully unmatched) acquisition,
// tracked per instrument in FIFO acquisition order.
type buyLot struct {
	remainingQuantity uint64
	priceInMinorUnits int64
	acquiredAtTime    time.Time
}

// RealizedGain is one real, closed (buy-lot-portion, sell) match —
// FIFO's fundamental unit of output. A single sell that spans multiple
// buy lots produces multiple RealizedGain records, one per lot it
// consumed from.
type RealizedGain struct {
	InstrumentSymbol         string    `json:"instrumentSymbol"`
	Quantity                 uint64    `json:"quantity"`
	AcquiredAtTime           time.Time `json:"acquiredAtTime"`
	SoldAtTime               time.Time `json:"soldAtTime"`
	BuyPriceInMinorUnits     int64     `json:"buyPriceInMinorUnits"`
	SellPriceInMinorUnits    int64     `json:"sellPriceInMinorUnits"`
	HoldingPeriodDays        int       `json:"holdingPeriodDays"`
	GainType                 string    `json:"gainType"`
	RealizedGainInMinorUnits int64     `json:"realizedGainInMinorUnits"`
}

// ErrSellExceedsHeldQuantity is returned (as part of a wrapped error) when
// a SELL fill's quantity exceeds what FIFO has open buy lots to cover —
// this package does not model short-selling; every SELL must be covered
// by a prior real BUY fill for the same account+instrument.
var ErrSellExceedsHeldQuantity = errors.New("capitalgains: sell quantity exceeds open buy-lot quantity (short selling is not modeled)")

// classifyHoldingPeriod returns the gain type and the holding period in
// days for one lot acquired at acquiredAtTime and sold at soldAtTime.
func classifyHoldingPeriod(acquiredAtTime time.Time, soldAtTime time.Time) (gainType string, holdingPeriodDays int) {
	longTermCutoff := acquiredAtTime.AddDate(1, 0, 0)
	holdingPeriodDays = int(soldAtTime.Sub(acquiredAtTime).Hours() / 24)
	if !soldAtTime.Before(longTermCutoff) {
		return GainTypeLongTerm, holdingPeriodDays
	}
	return GainTypeShortTerm, holdingPeriodDays
}

// ComputeFifoRealizedGains replays every fill (across all instruments,
// any time range — callers should pass FULL account fill history so
// buy lots from before a reporting window are still available to match
// against) in chronological order and performs genuine FIFO lot
// matching per instrument: each SELL consumes the oldest still-open BUY
// lot(s) first. Returns one RealizedGain per (sell, consumed-lot)
// match, in the order matches occurred.
func ComputeFifoRealizedGains(fills []filltrail.Fill) ([]RealizedGain, error) {
	fillsByInstrument := make(map[string][]filltrail.Fill)
	for _, fill := range fills {
		fillsByInstrument[fill.InstrumentSymbol] = append(fillsByInstrument[fill.InstrumentSymbol], fill)
	}

	var allRealizedGains []RealizedGain

	for instrumentSymbol, instrumentFills := range fillsByInstrument {
		sortedFills := append([]filltrail.Fill(nil), instrumentFills...)
		sort.Slice(sortedFills, func(i, j int) bool {
			return sortedFills[i].ExecutedAtTime.Before(sortedFills[j].ExecutedAtTime)
		})

		var openLots []*buyLot

		for _, fill := range sortedFills {
			switch fill.Side {
			case filltrail.SideBuy:
				openLots = append(openLots, &buyLot{
					remainingQuantity: fill.Quantity,
					priceInMinorUnits: fill.PriceInMinorUnits,
					acquiredAtTime:    fill.ExecutedAtTime,
				})

			case filltrail.SideSell:
				remainingToSell := fill.Quantity
				for remainingToSell > 0 {
					if len(openLots) == 0 {
						return allRealizedGains, fmt.Errorf(
							"%w: %s sell of %d @ %s has %d unmatched units",
							ErrSellExceedsHeldQuantity, instrumentSymbol, fill.Quantity,
							fill.ExecutedAtTime.Format(time.RFC3339), remainingToSell,
						)
					}

					oldestLot := openLots[0]
					matchedQuantity := oldestLot.remainingQuantity
					if remainingToSell < matchedQuantity {
						matchedQuantity = remainingToSell
					}

					gainType, holdingPeriodDays := classifyHoldingPeriod(oldestLot.acquiredAtTime, fill.ExecutedAtTime)
					realizedGainInMinorUnits := (fill.PriceInMinorUnits - oldestLot.priceInMinorUnits) * int64(matchedQuantity)

					allRealizedGains = append(allRealizedGains, RealizedGain{
						InstrumentSymbol:         instrumentSymbol,
						Quantity:                 matchedQuantity,
						AcquiredAtTime:           oldestLot.acquiredAtTime,
						SoldAtTime:               fill.ExecutedAtTime,
						BuyPriceInMinorUnits:     oldestLot.priceInMinorUnits,
						SellPriceInMinorUnits:    fill.PriceInMinorUnits,
						HoldingPeriodDays:        holdingPeriodDays,
						GainType:                 gainType,
						RealizedGainInMinorUnits: realizedGainInMinorUnits,
					})

					oldestLot.remainingQuantity -= matchedQuantity
					remainingToSell -= matchedQuantity
					if oldestLot.remainingQuantity == 0 {
						openLots = openLots[1:]
					}
				}
			}
		}
	}

	sort.Slice(allRealizedGains, func(i, j int) bool {
		return allRealizedGains[i].SoldAtTime.Before(allRealizedGains[j].SoldAtTime)
	})

	return allRealizedGains, nil
}

// Summary is the aggregated STCG/LTCG report for one account + Indian
// financial year (April 1 – March 31).
type Summary struct {
	AccountIdentifier          string         `json:"accountIdentifier"`
	FinancialYear              string         `json:"financialYear"`
	FinancialYearStartDate     time.Time      `json:"financialYearStartDate"`
	FinancialYearEndDate       time.Time      `json:"financialYearEndDate"`
	RealizedGains              []RealizedGain `json:"realizedGains"`
	ShortTermTotalInMinorUnits int64          `json:"shortTermTotalInMinorUnits"`
	LongTermTotalInMinorUnits  int64          `json:"longTermTotalInMinorUnits"`
}

// AggregateForFinancialYear filters realizedGains to those sold within
// [financialYearStart, financialYearEnd] and sums each into the
// STCG/LTCG totals.
func AggregateForFinancialYear(
	accountIdentifier string,
	financialYearLabel string,
	realizedGains []RealizedGain,
	financialYearStart time.Time,
	financialYearEnd time.Time,
) Summary {
	summary := Summary{
		AccountIdentifier:      accountIdentifier,
		FinancialYear:          financialYearLabel,
		FinancialYearStartDate: financialYearStart,
		FinancialYearEndDate:   financialYearEnd,
	}

	for _, gain := range realizedGains {
		if gain.SoldAtTime.Before(financialYearStart) || gain.SoldAtTime.After(financialYearEnd) {
			continue
		}
		summary.RealizedGains = append(summary.RealizedGains, gain)
		switch gain.GainType {
		case GainTypeShortTerm:
			summary.ShortTermTotalInMinorUnits += gain.RealizedGainInMinorUnits
		case GainTypeLongTerm:
			summary.LongTermTotalInMinorUnits += gain.RealizedGainInMinorUnits
		}
	}

	return summary
}

// IndianFinancialYearRange parses a financial-year label of the form
// "2024-25" into its concrete [April 1 2024 00:00:00, March 31 2025
// 23:59:59.999999999] UTC bounds — the standard Indian financial year.
func IndianFinancialYearRange(financialYearLabel string) (start time.Time, end time.Time, err error) {
	if len(financialYearLabel) != 7 || financialYearLabel[4] != '-' {
		return time.Time{}, time.Time{}, fmt.Errorf("capitalgains: financialYear must look like \"2024-25\", got %q", financialYearLabel)
	}
	startYear, parseErr := parseFourDigitYear(financialYearLabel[0:4])
	if parseErr != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("capitalgains: invalid financialYear %q: %w", financialYearLabel, parseErr)
	}
	endYearSuffix, parseErr := parseTwoDigitYearSuffix(financialYearLabel[5:7])
	if parseErr != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("capitalgains: invalid financialYear %q: %w", financialYearLabel, parseErr)
	}
	expectedEndYearSuffix := (startYear + 1) % 100
	if endYearSuffix != expectedEndYearSuffix {
		return time.Time{}, time.Time{}, fmt.Errorf(
			"capitalgains: financialYear %q is not consecutive (expected %d-%02d)",
			financialYearLabel, startYear, expectedEndYearSuffix,
		)
	}

	start = time.Date(startYear, time.April, 1, 0, 0, 0, 0, time.UTC)
	end = time.Date(startYear+1, time.March, 31, 23, 59, 59, 999999999, time.UTC)
	return start, end, nil
}

func parseFourDigitYear(s string) (int, error) {
	var year int
	_, err := fmt.Sscanf(s, "%04d", &year)
	if err != nil || year < 1000 {
		return 0, fmt.Errorf("not a 4-digit year: %q", s)
	}
	return year, nil
}

func parseTwoDigitYearSuffix(s string) (int, error) {
	var suffix int
	_, err := fmt.Sscanf(s, "%02d", &suffix)
	if err != nil {
		return 0, fmt.Errorf("not a 2-digit year suffix: %q", s)
	}
	return suffix, nil
}
