// Package corporateactionsprocessing is the REAL FEATURES.md §14
// "Corporate actions processing: dividends, splits, bonuses, mergers
// reflected automatically in holdings and cost basis" implementation —
// materially different from internal/corporateactionexplainer, which
// only turns a caller-SUPPLIED before/after outcome into a human-readable
// one-line explanation and never computes the outcome itself.
//
// This package is the thing that actually computes the outcome: given a
// real corporate-action event (a split ratio, a bonus ratio, a merger
// exchange ratio, or a cash-dividend-per-share amount) and an account's
// REAL prior holding (quantity + total cost basis), it derives the
// correct new quantity and new total cost basis using real accounting
// rules:
//
//   - Stock split (e.g. 2:1): quantity multiplies by the ratio, total
//     cost basis is UNCHANGED (only redistributed across more shares —
//     average cost per share falls proportionally).
//   - Bonus issue (e.g. 1:1): additional shares are added for free —
//     total cost basis is UNCHANGED (the new bonus shares carry zero
//     cost basis of their own), so average cost per share falls exactly
//     the same way a split's does, even though the mechanism (issuer
//     gives free shares vs. issuer subdivides existing shares) differs.
//   - Merger / share exchange (e.g. 2 old shares -> 1 new share):
//     quantity converts by the exchange ratio into the acquirer
//     instrument, and the ENTIRE prior total cost basis carries over
//     unchanged into the new holding (this package does not attempt
//     fractional cash-in-lieu settlement for a non-exact exchange ratio
//     — see ErrExchangeRatioProducesFractionalShares).
//   - Cash dividend (e.g. ₹5.00/share): holding quantity and cost basis
//     are COMPLETELY UNCHANGED — this is pure cash, credited to the
//     account's real ledger balance via ledgerclient's real
//     /journal-entries HTTP call, never touching the position book at
//     all.
//
// Ownership split, matching the rest of this repo's Tier separation
// (ARCHITECTURE.md §4/§6): oms-gateway owns the position/holdings book,
// so this package lives here and mutates HoldingsBook (this package's own
// cost-basis-aware book — positions.PositionBook has no cost-basis field
// at all, see that package's doc) plus, for quantity, syncs
// positions.PositionBook via SetPositionDirectly so the two stay
// consistent. Cash movement (dividends) is delegated to ledger's real
// HTTP API through the same ledgerclient oms-gateway already uses for
// trade settlement — corporate-actions processing never mutates cash
// balances itself.
//
// TODO(real build): in-memory, not persisted; no real corporate-actions
// feed triggers this automatically yet (same honest gap as
// internal/corporateactionexplainer) — today it is only reachable via a
// real HTTP endpoint a human or a future feed integration calls; no
// fractional-share cash-in-lieu handling for a merger ratio that doesn't
// divide evenly, and no rounding-remainder handling for a split/bonus
// ratio that doesn't divide evenly either — both are explicit, honest
// rejections (see the Err* values below) rather than silently truncating
// real shareholdings.
package corporateactionsprocessing

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// ActionType is a closed set of the real corporate-action categories
// this package processes.
type ActionType string

const (
	ActionTypeStockSplit   ActionType = "STOCK_SPLIT"
	ActionTypeBonusIssue   ActionType = "BONUS_ISSUE"
	ActionTypeMerger       ActionType = "MERGER"
	ActionTypeCashDividend ActionType = "CASH_DIVIDEND"
)

var (
	ErrNoExistingHolding                     = errors.New("corporateactionsprocessing: no existing holding for this account/instrument")
	ErrNonPositiveRatio                      = errors.New("corporateactionsprocessing: ratio numerator and denominator must both be positive")
	ErrNonPositiveDividendPerShare           = errors.New("corporateactionsprocessing: dividendPerShareInMinorUnits must be positive")
	ErrEmptyTargetInstrument                 = errors.New("corporateactionsprocessing: mergerTargetInstrumentSymbol is required for a merger")
	ErrExchangeRatioProducesFractionalShares = errors.New("corporateactionsprocessing: exchange ratio does not divide the held quantity evenly (fractional-share cash-in-lieu is not implemented)")
	ErrUnknownActionType                     = errors.New("corporateactionsprocessing: unknown action type")
	ErrNonPositiveSeedQuantity               = errors.New("corporateactionsprocessing: seed quantity must be positive")
)

// Holding is one account's real quantity + total cost basis for one
// instrument. AverageCostPerShareInMinorUnits is derived, never stored,
// so it can never drift out of sync with the two real numbers it's
// computed from.
type Holding struct {
	ClientAccountIdentifier string `json:"clientAccountIdentifier"`
	InstrumentSymbol        string `json:"instrumentSymbol"`
	QuantityHeld            int64  `json:"quantityHeld"`
	// TotalCostBasisInMinorUnits is the sum of what the account actually
	// paid for every share it holds of this instrument — the quantity
	// this package's whole job is to preserve correctly (splits/bonuses)
	// or carry over correctly (mergers), and to explicitly NOT touch at
	// all (dividends).
	TotalCostBasisInMinorUnits int64 `json:"totalCostBasisInMinorUnits"`
}

// AverageCostPerShareInMinorUnits is TotalCostBasisInMinorUnits /
// QuantityHeld, computed fresh every time it's read (integer division —
// this is a display/reporting figure, the real accounting quantity of
// record is always TotalCostBasisInMinorUnits). Returns 0 for a
// zero-quantity holding rather than dividing by zero.
func (holding Holding) AverageCostPerShareInMinorUnits() int64 {
	if holding.QuantityHeld == 0 {
		return 0
	}
	return holding.TotalCostBasisInMinorUnits / holding.QuantityHeld
}

// ProcessedAction is one real, recorded corporate action outcome —
// both the holding before and after, for a genuine audit trail.
type ProcessedAction struct {
	ActionType              ActionType `json:"actionType"`
	ClientAccountIdentifier string     `json:"clientAccountIdentifier"`
	InstrumentSymbol        string     `json:"instrumentSymbol"`
	HoldingBefore           Holding    `json:"holdingBefore"`
	HoldingAfter            Holding    `json:"holdingAfter"`
	// CashCreditedInMinorUnits is nonzero only for ActionTypeCashDividend
	// — the real amount credited to the account's ledger balance.
	CashCreditedInMinorUnits int64     `json:"cashCreditedInMinorUnits,omitempty"`
	ProcessedAtTime          time.Time `json:"processedAtTime"`
}

// HoldingsBook is a real, mutex-guarded, cost-basis-aware holdings store
// — the thing internal/positions.PositionBook explicitly does NOT
// implement (see that package's doc). Keyed by (account, instrument).
type HoldingsBook struct {
	mutexGuardingHoldings sync.RWMutex
	holdingsByKey         map[string]Holding
	processedActionsLog   []ProcessedAction
}

func NewHoldingsBook() *HoldingsBook {
	return &HoldingsBook{
		holdingsByKey: make(map[string]Holding),
	}
}

func holdingKey(clientAccountIdentifier string, instrumentSymbol string) string {
	return clientAccountIdentifier + "|" + instrumentSymbol
}

// SeedHolding establishes an account's real starting quantity + total
// cost basis for an instrument — the equivalent of "the account already
// bought these shares through ordinary trading before any corporate
// action in scope here happened". Overwrites any existing seed for the
// same (account, instrument): re-seeding is how a test or an ops
// operator corrects a starting position, not an accumulation operation.
func (holdingsBook *HoldingsBook) SeedHolding(clientAccountIdentifier string, instrumentSymbol string, quantity int64, totalCostBasisInMinorUnits int64) (Holding, error) {
	if quantity <= 0 {
		return Holding{}, ErrNonPositiveSeedQuantity
	}

	holdingsBook.mutexGuardingHoldings.Lock()
	defer holdingsBook.mutexGuardingHoldings.Unlock()

	holding := Holding{
		ClientAccountIdentifier:    clientAccountIdentifier,
		InstrumentSymbol:           instrumentSymbol,
		QuantityHeld:               quantity,
		TotalCostBasisInMinorUnits: totalCostBasisInMinorUnits,
	}
	holdingsBook.holdingsByKey[holdingKey(clientAccountIdentifier, instrumentSymbol)] = holding
	return holding, nil
}

// GetHolding returns an account's current real holding for one
// instrument.
func (holdingsBook *HoldingsBook) GetHolding(clientAccountIdentifier string, instrumentSymbol string) (Holding, bool) {
	holdingsBook.mutexGuardingHoldings.RLock()
	defer holdingsBook.mutexGuardingHoldings.RUnlock()

	holding, exists := holdingsBook.holdingsByKey[holdingKey(clientAccountIdentifier, instrumentSymbol)]
	return holding, exists
}

// ApplyStockSplit multiplies quantity by ratioNumerator/ratioDenominator
// (e.g. a 2:1 split is ratioNumerator=2, ratioDenominator=1) and leaves
// TotalCostBasisInMinorUnits completely unchanged — the defining
// accounting property of a split: the same total money invested is now
// spread across more shares, so average cost per share falls
// proportionally without a single rupee of real cost basis being
// created or destroyed.
func (holdingsBook *HoldingsBook) ApplyStockSplit(clientAccountIdentifier string, instrumentSymbol string, ratioNumerator int64, ratioDenominator int64, now time.Time) (ProcessedAction, error) {
	return holdingsBook.applyQuantityMultiplyingAction(ActionTypeStockSplit, clientAccountIdentifier, instrumentSymbol, ratioNumerator, ratioDenominator, now)
}

// ApplyBonusIssue computes bonusRatioNumerator/bonusRatioDenominator
// additional shares PER SHARE HELD (e.g. a 1:1 bonus issue is
// bonusRatioNumerator=1, bonusRatioDenominator=1 -> one new free share
// per share already held, doubling quantity) and adds them to the
// existing quantity. TotalCostBasisInMinorUnits is unchanged — the new
// bonus shares are issued for free (zero cost basis of their own), so
// the account's real total cost basis dilutes across a larger share
// count exactly like a split's does, even though a bonus issue's
// resulting multiplier is (1 + ratio) rather than a split's bare ratio.
func (holdingsBook *HoldingsBook) ApplyBonusIssue(clientAccountIdentifier string, instrumentSymbol string, bonusRatioNumerator int64, bonusRatioDenominator int64, now time.Time) (ProcessedAction, error) {
	return holdingsBook.applyQuantityMultiplyingAction(ActionTypeBonusIssue, clientAccountIdentifier, instrumentSymbol, bonusRatioNumerator+bonusRatioDenominator, bonusRatioDenominator, now)
}

// applyQuantityMultiplyingAction is the real shared math for both splits
// and bonus issues: both are "new quantity = old quantity *
// overallMultiplierNumerator / overallMultiplierDenominator, total cost
// basis unchanged" — ApplyBonusIssue has already folded "+1 share held"
// into overallMultiplierNumerator before calling this, so a split's
// literal ratio and a bonus's (1+ratio) multiplier share one exact
// implementation instead of two near-duplicate ones.
func (holdingsBook *HoldingsBook) applyQuantityMultiplyingAction(
	actionType ActionType,
	clientAccountIdentifier string,
	instrumentSymbol string,
	overallMultiplierNumerator int64,
	overallMultiplierDenominator int64,
	now time.Time,
) (ProcessedAction, error) {
	if overallMultiplierNumerator <= 0 || overallMultiplierDenominator <= 0 {
		return ProcessedAction{}, ErrNonPositiveRatio
	}

	holdingsBook.mutexGuardingHoldings.Lock()
	defer holdingsBook.mutexGuardingHoldings.Unlock()

	key := holdingKey(clientAccountIdentifier, instrumentSymbol)
	holdingBefore, exists := holdingsBook.holdingsByKey[key]
	if !exists {
		return ProcessedAction{}, ErrNoExistingHolding
	}

	scaledQuantity := holdingBefore.QuantityHeld * overallMultiplierNumerator
	if scaledQuantity%overallMultiplierDenominator != 0 {
		return ProcessedAction{}, ErrExchangeRatioProducesFractionalShares
	}
	newQuantity := scaledQuantity / overallMultiplierDenominator

	holdingAfter := Holding{
		ClientAccountIdentifier:    clientAccountIdentifier,
		InstrumentSymbol:           instrumentSymbol,
		QuantityHeld:               newQuantity,
		TotalCostBasisInMinorUnits: holdingBefore.TotalCostBasisInMinorUnits, // unchanged: the whole point.
	}
	holdingsBook.holdingsByKey[key] = holdingAfter

	processed := ProcessedAction{
		ActionType:              actionType,
		ClientAccountIdentifier: clientAccountIdentifier,
		InstrumentSymbol:        instrumentSymbol,
		HoldingBefore:           holdingBefore,
		HoldingAfter:            holdingAfter,
		ProcessedAtTime:         now,
	}
	holdingsBook.processedActionsLog = append(holdingsBook.processedActionsLog, processed)
	return processed, nil
}

// ApplyMerger converts a holding in instrumentSymbol into
// targetInstrumentSymbol at exchangeRatioNumerator (new shares) per
// exchangeRatioDenominator (old shares) — e.g. "2 old shares become 1
// new share" is exchangeRatioNumerator=1, exchangeRatioDenominator=2.
// The ENTIRE prior total cost basis carries over unchanged into the new
// holding (real accounting practice for a qualifying share-for-share
// merger — the investor's cost basis in the old security becomes their
// cost basis in the new one). The old instrument's holding is zeroed out
// (removed from the book entirely, not left as a phantom zero-quantity
// row). If the target instrument already has an existing holding for
// this account (e.g. the account already owned some of the acquirer
// separately), the converted quantity and cost basis are ADDED to it —
// a real, correct merge, not an overwrite that would silently destroy
// the account's pre-existing position in the acquirer.
func (holdingsBook *HoldingsBook) ApplyMerger(
	clientAccountIdentifier string,
	instrumentSymbol string,
	targetInstrumentSymbol string,
	exchangeRatioNumerator int64,
	exchangeRatioDenominator int64,
	now time.Time,
) (ProcessedAction, error) {
	if targetInstrumentSymbol == "" {
		return ProcessedAction{}, ErrEmptyTargetInstrument
	}
	if exchangeRatioNumerator <= 0 || exchangeRatioDenominator <= 0 {
		return ProcessedAction{}, ErrNonPositiveRatio
	}

	holdingsBook.mutexGuardingHoldings.Lock()
	defer holdingsBook.mutexGuardingHoldings.Unlock()

	sourceKey := holdingKey(clientAccountIdentifier, instrumentSymbol)
	holdingBefore, exists := holdingsBook.holdingsByKey[sourceKey]
	if !exists {
		return ProcessedAction{}, ErrNoExistingHolding
	}

	scaledQuantity := holdingBefore.QuantityHeld * exchangeRatioNumerator
	if scaledQuantity%exchangeRatioDenominator != 0 {
		return ProcessedAction{}, ErrExchangeRatioProducesFractionalShares
	}
	convertedQuantity := scaledQuantity / exchangeRatioDenominator

	targetKey := holdingKey(clientAccountIdentifier, targetInstrumentSymbol)
	existingTargetHolding := holdingsBook.holdingsByKey[targetKey] // zero value if absent, which is correct to add onto.

	targetHoldingAfter := Holding{
		ClientAccountIdentifier:    clientAccountIdentifier,
		InstrumentSymbol:           targetInstrumentSymbol,
		QuantityHeld:               existingTargetHolding.QuantityHeld + convertedQuantity,
		TotalCostBasisInMinorUnits: existingTargetHolding.TotalCostBasisInMinorUnits + holdingBefore.TotalCostBasisInMinorUnits,
	}
	holdingsBook.holdingsByKey[targetKey] = targetHoldingAfter
	delete(holdingsBook.holdingsByKey, sourceKey)

	processed := ProcessedAction{
		ActionType:              ActionTypeMerger,
		ClientAccountIdentifier: clientAccountIdentifier,
		InstrumentSymbol:        instrumentSymbol,
		HoldingBefore:           holdingBefore,
		HoldingAfter:            targetHoldingAfter,
		ProcessedAtTime:         now,
	}
	holdingsBook.processedActionsLog = append(holdingsBook.processedActionsLog, processed)
	return processed, nil
}

// ComputeCashDividendAmount returns the total cash dividend owed for the
// account's CURRENT quantity held (quantityHeld * dividendPerShare) and
// records the fact that a dividend was processed in the audit log, but —
// unlike every other Apply* method — makes NO change whatsoever to the
// Holding itself: a cash dividend never changes quantity or cost basis,
// only cash, and cash is ledger's job (see the cmd/server/main.go HTTP
// handler, which calls ledgerclient.PostDividendCreditJournalEntry with
// the amount this method returns).
func (holdingsBook *HoldingsBook) ComputeCashDividendAmount(clientAccountIdentifier string, instrumentSymbol string, dividendPerShareInMinorUnits int64, now time.Time) (ProcessedAction, error) {
	if dividendPerShareInMinorUnits <= 0 {
		return ProcessedAction{}, ErrNonPositiveDividendPerShare
	}

	holdingsBook.mutexGuardingHoldings.Lock()
	defer holdingsBook.mutexGuardingHoldings.Unlock()

	key := holdingKey(clientAccountIdentifier, instrumentSymbol)
	holding, exists := holdingsBook.holdingsByKey[key]
	if !exists {
		return ProcessedAction{}, ErrNoExistingHolding
	}

	totalDividend := holding.QuantityHeld * dividendPerShareInMinorUnits

	processed := ProcessedAction{
		ActionType:               ActionTypeCashDividend,
		ClientAccountIdentifier:  clientAccountIdentifier,
		InstrumentSymbol:         instrumentSymbol,
		HoldingBefore:            holding,
		HoldingAfter:             holding, // genuinely unchanged.
		CashCreditedInMinorUnits: totalDividend,
		ProcessedAtTime:          now,
	}
	holdingsBook.processedActionsLog = append(holdingsBook.processedActionsLog, processed)
	return processed, nil
}

// ProcessedActionsForAccount returns every real recorded action for one
// account, oldest first — the audit trail proving exactly how each
// holding reached its current quantity/cost-basis.
func (holdingsBook *HoldingsBook) ProcessedActionsForAccount(clientAccountIdentifier string) []ProcessedAction {
	holdingsBook.mutexGuardingHoldings.RLock()
	defer holdingsBook.mutexGuardingHoldings.RUnlock()

	var matching []ProcessedAction
	for _, action := range holdingsBook.processedActionsLog {
		if action.ClientAccountIdentifier == clientAccountIdentifier {
			matching = append(matching, action)
		}
	}
	return matching
}

// ValidateActionType is a small exported guard so the HTTP layer (and
// tests) can reject an unrecognized actionType before doing any real
// work, using the same closed set this package's Apply methods are
// named after.
func ValidateActionType(actionType ActionType) error {
	switch actionType {
	case ActionTypeStockSplit, ActionTypeBonusIssue, ActionTypeMerger, ActionTypeCashDividend:
		return nil
	default:
		return fmt.Errorf("%w: %s", ErrUnknownActionType, actionType)
	}
}
