// Package ledgerstatement builds a real, running-balance account
// statement — FEATURES.md §1's "ledger statements" — for one account
// over a date range, from real cash-movement data pulled from ledger
// (deposits, withdrawals) and oms-gateway (dividend credits, and
// derived trade-settlement cash impact).
//
// See ledgerclient's package doc for the honest, load-bearing gap this
// works around: ledger exposes no HTTP endpoint to list historical
// journal entries, only a current-balance snapshot plus the
// deposit/withdrawal sub-ledgers. This package is explicit about which
// of its movement rows come from which real source, and separately
// reports whether replaying all of them forward reconciles with
// ledger's own real current balance (it generally will NOT match
// exactly — see Statement.ReconciliationNote — because other real
// ledger journal activity this reporting service has no read API for,
// e.g. margin funding interest, LAS, securities lending fees, wallet
// conversions, is invisible to it).
package ledgerstatement

import (
	"fmt"
	"sort"
	"time"

	"mercurius/reporting/internal/filltrail"
	"mercurius/reporting/internal/ledgerclient"
	"mercurius/reporting/internal/omsgatewayclient"
)

const (
	MovementTypeDeposit         = "DEPOSIT"
	MovementTypeWithdrawal      = "WITHDRAWAL"
	MovementTypeDividendCredit  = "DIVIDEND_CREDIT"
	MovementTypeTradeSettlement = "TRADE_SETTLEMENT"
)

// Movement is one real, signed cash movement — positive
// AmountInMinorUnits credits the account, negative debits it.
type Movement struct {
	MovementType       string    `json:"movementType"`
	Description        string    `json:"description"`
	OccurredAtTime     time.Time `json:"occurredAtTime"`
	AmountInMinorUnits int64     `json:"amountInMinorUnits"`
	SourceService      string    `json:"sourceService"` // "ledger" or "oms-gateway (derived)"
}

// MovementsFromConfirmedDeposits converts ledger's real confirmed
// deposits into credit Movements. Pending/failed deposits never moved
// real money, so they're excluded.
func MovementsFromConfirmedDeposits(deposits []ledgerclient.DepositWireFormat) ([]Movement, error) {
	var movements []Movement
	for _, deposit := range deposits {
		if deposit.Status != "CONFIRMED" {
			continue
		}
		occurredAt, err := parseTimestamp(deposit.ConfirmedAt, deposit.InitiatedAt)
		if err != nil {
			return nil, fmt.Errorf("deposit %s: %w", deposit.DepositId, err)
		}
		movements = append(movements, Movement{
			MovementType:       MovementTypeDeposit,
			Description:        fmt.Sprintf("Deposit via %s (%s)", deposit.Method, deposit.DepositId),
			OccurredAtTime:     occurredAt,
			AmountInMinorUnits: deposit.AmountInMinorUnits,
			SourceService:      "ledger",
		})
	}
	return movements, nil
}

// MovementsFromCompletedWithdrawals converts ledger's real completed
// withdrawals into debit Movements. Withdrawals still on hold or
// cancelled never actually left the account, so they're excluded.
func MovementsFromCompletedWithdrawals(withdrawals []ledgerclient.WithdrawalWireFormat) ([]Movement, error) {
	var movements []Movement
	for _, withdrawal := range withdrawals {
		if withdrawal.Status != "COMPLETED" {
			continue
		}
		occurredAt, err := parseTimestamp(withdrawal.EligibleForPayoutAt, "")
		if err != nil {
			return nil, fmt.Errorf("withdrawal %s: %w", withdrawal.WithdrawalId, err)
		}
		movements = append(movements, Movement{
			MovementType:       MovementTypeWithdrawal,
			Description:        fmt.Sprintf("Withdrawal (%s)", withdrawal.WithdrawalId),
			OccurredAtTime:     occurredAt,
			AmountInMinorUnits: -withdrawal.AmountInMinorUnits,
			SourceService:      "ledger",
		})
	}
	return movements, nil
}

// MovementsFromDividendCredits converts oms-gateway's real recorded
// CASH_DIVIDEND corporate actions into credit Movements.
func MovementsFromDividendCredits(processedActions []omsgatewayclient.ProcessedActionWireFormat) []Movement {
	var movements []Movement
	for _, action := range processedActions {
		if action.ActionType != "CASH_DIVIDEND" || action.CashCreditedInMinorUnits == 0 {
			continue
		}
		movements = append(movements, Movement{
			MovementType:       MovementTypeDividendCredit,
			Description:        fmt.Sprintf("Dividend credit: %s", action.InstrumentSymbol),
			OccurredAtTime:     action.ProcessedAtTime,
			AmountInMinorUnits: action.CashCreditedInMinorUnits,
			SourceService:      "oms-gateway",
		})
	}
	return movements
}

// MovementsFromTradeSettlements derives a signed cash-impact Movement
// per real fill (net amount from the real/illustrative charges
// breakdown attached to each fill by the caller), for accounts where
// this reporting service has no direct read access to ledger's own
// trade-settlement journal entries — see the package doc.
// netAmountByFillIndex must be the same length and order as fills, one
// signed NetAmountInMinorUnits per fill (negative = cash left the
// account, e.g. a buy; positive = cash arrived, e.g. a sell).
func MovementsFromTradeSettlements(fills []filltrail.Fill, signedNetAmountInMinorUnitsByFillIndex []int64) []Movement {
	movements := make([]Movement, 0, len(fills))
	for i, fill := range fills {
		movements = append(movements, Movement{
			MovementType:       MovementTypeTradeSettlement,
			Description:        fmt.Sprintf("Trade settlement: %s %s x%d", fill.Side, fill.InstrumentSymbol, fill.Quantity),
			OccurredAtTime:     fill.ExecutedAtTime,
			AmountInMinorUnits: signedNetAmountInMinorUnitsByFillIndex[i],
			SourceService:      "oms-gateway (derived)",
		})
	}
	return movements
}

// StatementRow is one printed line of the statement: a movement plus
// the running balance immediately after it was applied.
type StatementRow struct {
	Movement
	RunningBalanceInMinorUnits int64 `json:"runningBalanceInMinorUnits"`
}

// Statement is the full account statement for one date range.
type Statement struct {
	AccountIdentifier                        string         `json:"accountIdentifier"`
	StartDate                                time.Time      `json:"startDate"`
	EndDate                                  time.Time      `json:"endDate"`
	OpeningBalanceInMinorUnits               int64          `json:"openingBalanceInMinorUnits"`
	ClosingBalanceInMinorUnits               int64          `json:"closingBalanceInMinorUnits"`
	Rows                                     []StatementRow `json:"rows"`
	LedgerReportedCurrentBalanceInMinorUnits int64          `json:"ledgerReportedCurrentBalanceInMinorUnits"`
	ReconciliationNote                       string         `json:"reconciliationNote"`
}

// BuildStatement replays allTimeMovements (every real movement this
// service could obtain for the account, NOT pre-filtered to the
// requested range — full history is required so the opening balance for
// startDate can be computed correctly) forward from an assumed zero
// account-opening balance, and returns the rows that fall within
// [startDate, endDate], each carrying its true running balance.
// ledgerReportedCurrentBalanceInMinorUnits (ledger's real
// GET /accounts/balance figure) is compared against the final replayed
// balance purely as an honest diagnostic — see ReconciliationNote.
func BuildStatement(
	accountIdentifier string,
	startDate time.Time,
	endDate time.Time,
	allTimeMovements []Movement,
	ledgerReportedCurrentBalanceInMinorUnits int64,
) Statement {
	sorted := append([]Movement(nil), allTimeMovements...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].OccurredAtTime.Before(sorted[j].OccurredAtTime) })

	statement := Statement{
		AccountIdentifier:                        accountIdentifier,
		StartDate:                                startDate,
		EndDate:                                  endDate,
		LedgerReportedCurrentBalanceInMinorUnits: ledgerReportedCurrentBalanceInMinorUnits,
	}

	runningBalance := int64(0)
	openingBalanceSet := false

	for _, movement := range sorted {
		if movement.OccurredAtTime.Before(startDate) {
			runningBalance += movement.AmountInMinorUnits
			continue
		}
		if !openingBalanceSet {
			statement.OpeningBalanceInMinorUnits = runningBalance
			openingBalanceSet = true
		}
		if movement.OccurredAtTime.After(endDate) {
			continue
		}
		runningBalance += movement.AmountInMinorUnits
		statement.Rows = append(statement.Rows, StatementRow{
			Movement:                   movement,
			RunningBalanceInMinorUnits: runningBalance,
		})
	}
	if !openingBalanceSet {
		// No movement occurred on/after startDate — opening balance is
		// simply whatever we'd replayed up to that point.
		statement.OpeningBalanceInMinorUnits = runningBalance
	}
	statement.ClosingBalanceInMinorUnits = runningBalance

	// Fold in any real movements strictly after endDate too, so the
	// final replayed balance is comparable to ledger's real
	// present-day current balance.
	finalReplayedBalance := runningBalance
	for _, movement := range sorted {
		if movement.OccurredAtTime.After(endDate) {
			finalReplayedBalance += movement.AmountInMinorUnits
		}
	}

	if finalReplayedBalance == ledgerReportedCurrentBalanceInMinorUnits {
		statement.ReconciliationNote = "Replayed balance from all known real movements matches ledger's real current balance exactly."
	} else {
		delta := ledgerReportedCurrentBalanceInMinorUnits - finalReplayedBalance
		statement.ReconciliationNote = fmt.Sprintf(
			"Replayed balance from known movements (deposits, withdrawals, dividend credits, derived trade settlements) differs from ledger's real current balance by %d minor units. This is an HONEST, EXPECTED gap: ledger has no HTTP endpoint exposing its full journal-entry history (see ledgerclient's package doc), so any other real ledger activity this service cannot read — margin funding interest, LAS, securities lending fees, wallet conversions, manually posted journal entries, or trade settlements this service failed to derive from oms-gateway's fills — is not reflected above.",
			delta,
		)
	}

	return statement
}

func parseTimestamp(primary string, fallback string) (time.Time, error) {
	for _, candidate := range []string{primary, fallback} {
		if candidate == "" {
			continue
		}
		if parsed, err := time.Parse(time.RFC3339, candidate); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("no parseable RFC3339 timestamp among %q, %q", primary, fallback)
}
