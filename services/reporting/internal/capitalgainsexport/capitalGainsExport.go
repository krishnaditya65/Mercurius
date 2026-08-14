// Package capitalgainsexport implements FEATURES.md §21's "one-click
// capital gains statement export": a genuine CSV writer (encoding/csv,
// not JSON dressed up as CSV) over the real FIFO STCG/LTCG output from
// package capitalgains, for one account + Indian financial year.
package capitalgainsexport

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"time"

	"mercurius/reporting/internal/capitalgains"
)

// ColumnHeaders is the exact, stable CSV header row this package
// writes — documented here so callers/tests can assert against it
// directly.
var ColumnHeaders = []string{
	"instrumentSymbol",
	"quantity",
	"acquiredDate",
	"soldDate",
	"holdingPeriodDays",
	"gainType",
	"buyPriceInMinorUnits",
	"sellPriceInMinorUnits",
	"realizedGainInMinorUnits",
}

// WriteCsv renders summary's realized gains as a real, correctly
// quoted/escaped CSV document (via encoding/csv, so embedded commas,
// quotes, etc. in any future free-text field are handled properly, not
// just string-concatenated), one row per FIFO-matched lot, plus a
// trailing totals row.
func WriteCsv(summary capitalgains.Summary) ([]byte, error) {
	var buffer bytes.Buffer
	writer := csv.NewWriter(&buffer)

	if err := writer.Write(ColumnHeaders); err != nil {
		return nil, fmt.Errorf("capitalgainsexport: writing header row: %w", err)
	}

	for _, gain := range summary.RealizedGains {
		row := []string{
			gain.InstrumentSymbol,
			fmt.Sprintf("%d", gain.Quantity),
			gain.AcquiredAtTime.Format("2006-01-02"),
			gain.SoldAtTime.Format("2006-01-02"),
			fmt.Sprintf("%d", gain.HoldingPeriodDays),
			gain.GainType,
			fmt.Sprintf("%d", gain.BuyPriceInMinorUnits),
			fmt.Sprintf("%d", gain.SellPriceInMinorUnits),
			fmt.Sprintf("%d", gain.RealizedGainInMinorUnits),
		}
		if err := writer.Write(row); err != nil {
			return nil, fmt.Errorf("capitalgainsexport: writing row for %s: %w", gain.InstrumentSymbol, err)
		}
	}

	totalsRow := []string{
		"TOTAL", "", "", "", "", "STCG+LTCG", "", "",
		fmt.Sprintf("%d", summary.ShortTermTotalInMinorUnits+summary.LongTermTotalInMinorUnits),
	}
	if err := writer.Write(totalsRow); err != nil {
		return nil, fmt.Errorf("capitalgainsexport: writing totals row: %w", err)
	}
	stcgRow := []string{"TOTAL_STCG", "", "", "", "", capitalgains.GainTypeShortTerm, "", "", fmt.Sprintf("%d", summary.ShortTermTotalInMinorUnits)}
	ltcgRow := []string{"TOTAL_LTCG", "", "", "", "", capitalgains.GainTypeLongTerm, "", "", fmt.Sprintf("%d", summary.LongTermTotalInMinorUnits)}
	if err := writer.Write(stcgRow); err != nil {
		return nil, fmt.Errorf("capitalgainsexport: writing STCG totals row: %w", err)
	}
	if err := writer.Write(ltcgRow); err != nil {
		return nil, fmt.Errorf("capitalgainsexport: writing LTCG totals row: %w", err)
	}

	writer.Flush()
	if err := writer.Error(); err != nil {
		return nil, fmt.Errorf("capitalgainsexport: flushing CSV writer: %w", err)
	}
	return buffer.Bytes(), nil
}

// SuggestedFilename returns a stable, descriptive filename for one
// account + financial year's export.
func SuggestedFilename(accountIdentifier string, financialYearLabel string, generatedAt time.Time) string {
	return fmt.Sprintf("capital-gains-%s-FY%s-%s.csv", accountIdentifier, financialYearLabel, generatedAt.Format("20060102"))
}
