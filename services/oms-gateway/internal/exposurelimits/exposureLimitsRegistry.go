// Package exposurelimits implements FEATURES.md §12's "Per-user,
// per-segment exposure limits (configurable by risk team)": a real
// pre-trade check, alongside internal/riskengine's own available-margin
// check, that rejects an order that would push an account's cumulative
// notional exposure — overall, or within one instrument "segment" —
// past a risk-team-configured cap.
//
// DESIGN CHOICE — what "exposure" means here: this mirrors
// internal/algolimits' `maxNotionalPerDayInMinorUnits` reservation model
// (a cumulative running total incremented on every accepted order),
// NOT a live mark-to-market net position value. A true live-position
// exposure figure would need to net BUYs against SELLs and re-price
// against a current market price (internal/marktomarket) — real work
// this package deliberately does not duplicate. ReleaseExposure exists so
// a caller CAN wire in a reduction (e.g. when a position is later closed
// or a reducing/liquidating order is submitted — see
// internal/autoliquidation) but nothing calls it automatically in this
// build; every order's notional simply accumulates until explicitly
// released. This is an honest, documented simplification, the same shape
// internal/algolimits already uses for its own daily cap.
//
// DESIGN CHOICE — "segment": this repo has no pre-existing instrument
// segment concept, so this package introduces a simple, ILLUSTRATIVE one:
// ClassifySegment derives EQUITY / FUTURES_AND_OPTIONS / CURRENCY /
// OTHER from a documented instrument-symbol suffix convention
// (`-FUT`/`-OPT` -> FUTURES_AND_OPTIONS, `-CUR` -> CURRENCY, everything
// else -> EQUITY). This is NOT a real exchange segment taxonomy — a real
// build would classify from an actual instrument master, which doesn't
// exist in this repo (see internal/optionschain's and
// internal/marginengine's own "illustrative" caveats for the same
// pattern of standing in for real reference data that isn't here yet).
package exposurelimits

import (
	"errors"
	"fmt"
	"strings"
	"sync"
)

// ErrAccountExposureLimitExceeded is returned when an order would push
// an account's TOTAL cumulative notional exposure (across every segment)
// past its configured account-level limit.
var ErrAccountExposureLimitExceeded = errors.New("order would exceed the account's configured total exposure limit")

// ErrSegmentExposureLimitExceeded is returned when an order would push
// an account's cumulative notional exposure WITHIN one segment past that
// account+segment's configured limit.
var ErrSegmentExposureLimitExceeded = errors.New("order would exceed the account's configured per-segment exposure limit")

// Segment identifiers — see the package doc's "segment" design-choice
// note.
const (
	SegmentEquity            = "EQUITY"
	SegmentFuturesAndOptions = "FUTURES_AND_OPTIONS"
	SegmentCurrency          = "CURRENCY"
	SegmentOther             = "OTHER"
)

// ClassifySegment derives an ILLUSTRATIVE segment from an instrument
// symbol's suffix. See the package doc for why this isn't a real
// exchange taxonomy.
func ClassifySegment(instrumentSymbol string) string {
	upper := strings.ToUpper(instrumentSymbol)
	switch {
	case strings.HasSuffix(upper, "-FUT"), strings.HasSuffix(upper, "-OPT"):
		return SegmentFuturesAndOptions
	case strings.HasSuffix(upper, "-CUR"):
		return SegmentCurrency
	case strings.HasSuffix(upper, "-EQ"):
		return SegmentEquity
	default:
		return SegmentOther
	}
}

// LimitsRegistry is the mutex-guarded configured-limits + running-usage
// state machine.
type LimitsRegistry struct {
	mutexGuardingLimits sync.Mutex

	accountLimitInMinorUnits        map[string]int64
	accountSegmentLimitInMinorUnits map[string]map[string]int64

	currentAccountExposureInMinorUnits        map[string]int64
	currentAccountSegmentExposureInMinorUnits map[string]map[string]int64
}

func NewLimitsRegistry() *LimitsRegistry {
	return &LimitsRegistry{
		accountLimitInMinorUnits:                  make(map[string]int64),
		accountSegmentLimitInMinorUnits:           make(map[string]map[string]int64),
		currentAccountExposureInMinorUnits:        make(map[string]int64),
		currentAccountSegmentExposureInMinorUnits: make(map[string]map[string]int64),
	}
}

// SetAccountLimit configures (or clears, with 0) the account's total
// exposure cap across every segment. Risk-team-configurable, per
// FEATURES.md §12.
func (registry *LimitsRegistry) SetAccountLimit(accountIdentifier string, maxNotionalInMinorUnits int64) {
	registry.mutexGuardingLimits.Lock()
	defer registry.mutexGuardingLimits.Unlock()

	registry.accountLimitInMinorUnits[accountIdentifier] = maxNotionalInMinorUnits
}

// SetSegmentLimit configures the account's exposure cap WITHIN one
// segment.
func (registry *LimitsRegistry) SetSegmentLimit(accountIdentifier string, segment string, maxNotionalInMinorUnits int64) {
	registry.mutexGuardingLimits.Lock()
	defer registry.mutexGuardingLimits.Unlock()

	if registry.accountSegmentLimitInMinorUnits[accountIdentifier] == nil {
		registry.accountSegmentLimitInMinorUnits[accountIdentifier] = make(map[string]int64)
	}
	registry.accountSegmentLimitInMinorUnits[accountIdentifier][segment] = maxNotionalInMinorUnits
}

// AccountLimit returns the account's configured total limit and whether
// one has ever been set (an account with no configured limit is
// unconstrained, exactly like internal/algolimits' unconfigured
// strategies).
func (registry *LimitsRegistry) AccountLimit(accountIdentifier string) (int64, bool) {
	registry.mutexGuardingLimits.Lock()
	defer registry.mutexGuardingLimits.Unlock()

	limit, configured := registry.accountLimitInMinorUnits[accountIdentifier]
	return limit, configured
}

// SegmentLimit returns the account+segment's configured limit and
// whether one has ever been set.
func (registry *LimitsRegistry) SegmentLimit(accountIdentifier string, segment string) (int64, bool) {
	registry.mutexGuardingLimits.Lock()
	defer registry.mutexGuardingLimits.Unlock()

	limit, configured := registry.accountSegmentLimitInMinorUnits[accountIdentifier][segment]
	return limit, configured
}

// CurrentAccountExposure returns the account's current cumulative
// exposure across every segment.
func (registry *LimitsRegistry) CurrentAccountExposure(accountIdentifier string) int64 {
	registry.mutexGuardingLimits.Lock()
	defer registry.mutexGuardingLimits.Unlock()

	return registry.currentAccountExposureInMinorUnits[accountIdentifier]
}

// CurrentSegmentExposure returns the account's current cumulative
// exposure within one segment.
func (registry *LimitsRegistry) CurrentSegmentExposure(accountIdentifier string, segment string) int64 {
	registry.mutexGuardingLimits.Lock()
	defer registry.mutexGuardingLimits.Unlock()

	return registry.currentAccountSegmentExposureInMinorUnits[accountIdentifier][segment]
}

// CheckAndReserveExposure is the real pre-trade check: if adding
// orderNotionalInMinorUnits would push either the account's total
// exposure or its exposure within `segment` past a CONFIGURED limit (an
// unconfigured limit never rejects — same "unconfigured means
// unconstrained" convention internal/algolimits uses), the order is
// rejected and NEITHER running total is mutated. On success, both
// running totals are incremented atomically under the same lock — no
// other caller can observe a partially-reserved state.
func (registry *LimitsRegistry) CheckAndReserveExposure(
	accountIdentifier string,
	segment string,
	orderNotionalInMinorUnits int64,
) error {
	if orderNotionalInMinorUnits <= 0 {
		return nil
	}

	registry.mutexGuardingLimits.Lock()
	defer registry.mutexGuardingLimits.Unlock()

	currentAccountExposure := registry.currentAccountExposureInMinorUnits[accountIdentifier]
	projectedAccountExposure := currentAccountExposure + orderNotionalInMinorUnits

	if accountLimit, configured := registry.accountLimitInMinorUnits[accountIdentifier]; configured {
		if projectedAccountExposure > accountLimit {
			return fmt.Errorf(
				"%w: account %s exposure would become %d, limit is %d",
				ErrAccountExposureLimitExceeded, accountIdentifier, projectedAccountExposure, accountLimit,
			)
		}
	}

	currentSegmentExposure := registry.currentAccountSegmentExposureInMinorUnits[accountIdentifier][segment]
	projectedSegmentExposure := currentSegmentExposure + orderNotionalInMinorUnits

	if segmentLimit, configured := registry.accountSegmentLimitInMinorUnits[accountIdentifier][segment]; configured {
		if projectedSegmentExposure > segmentLimit {
			return fmt.Errorf(
				"%w: account %s segment %s exposure would become %d, limit is %d",
				ErrSegmentExposureLimitExceeded, accountIdentifier, segment, projectedSegmentExposure, segmentLimit,
			)
		}
	}

	registry.currentAccountExposureInMinorUnits[accountIdentifier] = projectedAccountExposure
	if registry.currentAccountSegmentExposureInMinorUnits[accountIdentifier] == nil {
		registry.currentAccountSegmentExposureInMinorUnits[accountIdentifier] = make(map[string]int64)
	}
	registry.currentAccountSegmentExposureInMinorUnits[accountIdentifier][segment] = projectedSegmentExposure
	return nil
}

// ReleaseExposure reverses a prior CheckAndReserveExposure reservation —
// see the package doc's design-choice note: not called automatically by
// anything in this build, but real and tested so a future caller (e.g. a
// position-close handler) can wire it in without adding new state.
// Floors both running totals at zero.
func (registry *LimitsRegistry) ReleaseExposure(accountIdentifier string, segment string, notionalInMinorUnits int64) {
	registry.mutexGuardingLimits.Lock()
	defer registry.mutexGuardingLimits.Unlock()

	registry.currentAccountExposureInMinorUnits[accountIdentifier] -= notionalInMinorUnits
	if registry.currentAccountExposureInMinorUnits[accountIdentifier] < 0 {
		registry.currentAccountExposureInMinorUnits[accountIdentifier] = 0
	}
	if registry.currentAccountSegmentExposureInMinorUnits[accountIdentifier] != nil {
		registry.currentAccountSegmentExposureInMinorUnits[accountIdentifier][segment] -= notionalInMinorUnits
		if registry.currentAccountSegmentExposureInMinorUnits[accountIdentifier][segment] < 0 {
			registry.currentAccountSegmentExposureInMinorUnits[accountIdentifier][segment] = 0
		}
	}
}
