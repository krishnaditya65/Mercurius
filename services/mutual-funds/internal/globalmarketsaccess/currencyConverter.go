package globalmarketsaccess

import (
	"fmt"
	"math"
	"sync"
)

var ErrUnknownCurrencyPair = fmt.Errorf("no FX rate is configured for this currency pair")
var ErrInvalidFxRate = fmt.Errorf("FX rate must be strictly positive")

// CurrencyConverter reuses the CONCEPT of ledger's real
// internal/multicurrencywallet — a per-currency-pair FX rate table used to
// convert an amount from one currency's minor units to another's — WITHOUT
// calling that real service. A real build would integrate with ledger's
// actual multi-currency wallet (and its actual FX rate feed) rather than
// this package's own hardcoded, illustrative rate table.
type CurrencyConverter struct {
	mutexGuardingRates sync.RWMutex
	ratesByPair        map[string]float64 // "INR->USD" -> rate
}

// NewCurrencyConverter seeds a small, illustrative, hand-picked FX rate
// table — NOT a live feed.
func NewCurrencyConverter() *CurrencyConverter {
	return &CurrencyConverter{
		ratesByPair: map[string]float64{
			"INR->USD": 1.0 / 83.00, // ~83 INR per USD
			"USD->INR": 83.00,
		},
	}
}

func pairKey(fromCurrency, toCurrency string) string {
	return fromCurrency + "->" + toCurrency
}

// Rate returns the configured conversion rate from fromCurrency to
// toCurrency (multiply an amount in fromCurrency's minor units by this
// rate to get toCurrency's minor units, ASSUMING both currencies share the
// same minor-unit granularity, e.g. both x100 — true for INR paise and USD
// cents).
func (converter *CurrencyConverter) Rate(fromCurrency, toCurrency string) (float64, error) {
	if fromCurrency == toCurrency {
		return 1.0, nil
	}

	converter.mutexGuardingRates.RLock()
	defer converter.mutexGuardingRates.RUnlock()

	rate, wasFound := converter.ratesByPair[pairKey(fromCurrency, toCurrency)]
	if !wasFound {
		return 0, ErrUnknownCurrencyPair
	}
	return rate, nil
}

// UpdateRate overwrites the configured rate for one currency pair.
// Testing/demo-only hook, same caveat pattern as
// internal/fundcatalog.UpdateNav.
func (converter *CurrencyConverter) UpdateRate(fromCurrency, toCurrency string, newRate float64) error {
	if newRate <= 0 {
		return ErrInvalidFxRate
	}

	converter.mutexGuardingRates.Lock()
	defer converter.mutexGuardingRates.Unlock()

	converter.ratesByPair[pairKey(fromCurrency, toCurrency)] = newRate
	return nil
}

// Convert converts amountInMinorUnits from fromCurrency to toCurrency,
// rounding to the nearest whole minor unit.
func (converter *CurrencyConverter) Convert(amountInMinorUnits int64, fromCurrency, toCurrency string) (int64, error) {
	rate, rateError := converter.Rate(fromCurrency, toCurrency)
	if rateError != nil {
		return 0, rateError
	}
	return int64(math.Round(float64(amountInMinorUnits) * rate)), nil
}
