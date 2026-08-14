package fixedincome

import (
	"fmt"
	"time"
)

// AuctionCalendarEntry is one illustrative fixture entry in a hand-rolled
// "RBI auction calendar" stand-in — see the package doc comment's loud
// caveat. A real auction calendar is published by RBI ahead of each
// quarter; this is a handful of fixed dates tied to this package's own
// fictitious catalog bonds, purely so internal/primarymarketbidding has
// something deterministic to open/close bidding windows against.
type AuctionCalendarEntry struct {
	BondId                     string
	ScheduledAuctionDate       time.Time
	NotifiedAmountInMinorUnits int64
}

// SeedAuctionCalendar returns a fixed, illustrative auction calendar — one
// scheduled auction per G-Sec and T-Bill in the catalog (SGBs are sold via
// a subscription window in reality, not a competitive-bid auction, so
// they're deliberately excluded here).
func SeedAuctionCalendar() []AuctionCalendarEntry {
	mustParseDate := func(value string) time.Time {
		parsed, parseError := time.Parse("2006-01-02", value)
		if parseError != nil {
			panic(fmt.Sprintf("fixedincome: invalid fixture date %q: %v", value, parseError))
		}
		return parsed
	}

	return []AuctionCalendarEntry{
		{BondId: "GSEC-07.10-2028", ScheduledAuctionDate: mustParseDate("2026-08-21"), NotifiedAmountInMinorUnits: 5_000_000_00},
		{BondId: "GSEC-07.26-2031", ScheduledAuctionDate: mustParseDate("2026-08-28"), NotifiedAmountInMinorUnits: 4_000_000_00},
		{BondId: "GSEC-06.90-2036", ScheduledAuctionDate: mustParseDate("2026-09-04"), NotifiedAmountInMinorUnits: 3_000_000_00},
		{BondId: "TBILL-91D-NOV26", ScheduledAuctionDate: mustParseDate("2026-08-19"), NotifiedAmountInMinorUnits: 2_000_000_00},
		{BondId: "TBILL-364D-AUG27", ScheduledAuctionDate: mustParseDate("2026-08-19"), NotifiedAmountInMinorUnits: 1_500_000_00},
	}
}
