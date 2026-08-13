// Package amlmonitoring implements real, rule-based anti-money-laundering
// transaction monitoring — FEATURES.md §1: "AML transaction monitoring
// (unusual pattern flags, PEP screening)". It watches the same real
// money-movement events (deposits, withdrawals) other ledger packages
// already produce and raises genuine alerts using three concrete,
// testable rules: a single transaction over a large-value threshold, a
// burst of transactions within a short time window (velocity), and
// "structuring" — several transactions each individually under a
// reporting threshold whose sum within a time window crosses it (the
// textbook money-laundering technique of splitting a large transaction
// into many small ones to dodge a reporting requirement).
//
// This is NOT a real AML system. TODO(real build): thresholds here are
// illustrative constants, not derived from any actual regulatory
// reporting limit (e.g. India's ₹10 lakh cash transaction reporting
// threshold under PMLA) or tuned against real transaction data; PEP
// screening is a static, hardcoded name list, not a real sanctions/PEP
// database with fuzzy matching; there's no case management workflow for
// an alert once raised (no assign/investigate/close/escalate-to-STR
// lifecycle) and no persistence — alerts live only as long as the
// process does.
package amlmonitoring

import (
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// AlertType distinguishes which rule raised an alert.
type AlertType string

const (
	AlertTypeLargeTransaction AlertType = "LARGE_TRANSACTION"
	AlertTypeVelocity         AlertType = "VELOCITY"
	AlertTypeStructuring      AlertType = "STRUCTURING"
	AlertTypePepMatch         AlertType = "PEP_MATCH"
)

// TransactionRecord is one monitored money movement.
type TransactionRecord struct {
	AccountIdentifier  string
	AmountInMinorUnits int64
	OccurredAt         time.Time
}

// Alert is a real, rule-raised flag for a compliance officer to review.
type Alert struct {
	AccountIdentifier string
	AlertType         AlertType
	Description       string
	RaisedAt          time.Time
}

// MonitorConfig holds the illustrative thresholds every rule is judged
// against. All are deliberately explicit constructor arguments rather
// than package-level constants, so tests (and a future real build) can
// exercise different regimes without recompiling.
type MonitorConfig struct {
	// LargeTransactionThresholdInMinorUnits: a single transaction at or
	// above this amount is immediately flagged.
	LargeTransactionThresholdInMinorUnits int64
	// StructuringReportThresholdInMinorUnits: the amount a sequence of
	// small transactions is suspected of being deliberately split to
	// stay under.
	StructuringReportThresholdInMinorUnits int64
	// StructuringWindow: the time window within which multiple
	// sub-threshold transactions summing over the report threshold are
	// considered potential structuring.
	StructuringWindow time.Duration
	// VelocityMaxTransactionsInWindow: more than this many transactions
	// for one account within VelocityWindow raises a velocity alert.
	VelocityMaxTransactionsInWindow int
	VelocityWindow                  time.Duration
}

// Monitor is safe for concurrent use. It holds no reference to any
// ledger — callers (e.g. an HTTP handler, after a deposit or withdrawal
// successfully posts) explicitly report transactions to it via
// RecordTransaction, keeping this package decoupled from any specific
// money-movement mechanism.
type Monitor struct {
	config MonitorConfig

	mutexGuardingState        sync.Mutex
	transactionsByAccountId   map[string][]TransactionRecord
	alertsByAccountId         map[string][]Alert
	politicallyExposedPersons map[string]string // lowercased full name -> original listed name
}

// NewMonitor builds a Monitor with the given thresholds and an initial
// PEP watch list (full names, matched case-insensitively).
func NewMonitor(config MonitorConfig, politicallyExposedPersonNames []string) *Monitor {
	pepByLowercasedName := make(map[string]string, len(politicallyExposedPersonNames))
	for _, name := range politicallyExposedPersonNames {
		pepByLowercasedName[strings.ToLower(strings.TrimSpace(name))] = name
	}
	return &Monitor{
		config:                    config,
		transactionsByAccountId:   make(map[string][]TransactionRecord),
		alertsByAccountId:         make(map[string][]Alert),
		politicallyExposedPersons: pepByLowercasedName,
	}
}

// RecordTransaction logs one real money movement and evaluates every
// rule against the account's history, raising whatever alerts apply.
// amountInMinorUnits should be the absolute value of the movement (a
// ₹50,000 withdrawal and a ₹50,000 deposit are equally monitorable).
func (monitor *Monitor) RecordTransaction(accountIdentifier string, amountInMinorUnits int64, occurredAt time.Time) []Alert {
	if amountInMinorUnits < 0 {
		amountInMinorUnits = -amountInMinorUnits
	}

	monitor.mutexGuardingState.Lock()
	defer monitor.mutexGuardingState.Unlock()

	record := TransactionRecord{
		AccountIdentifier:  accountIdentifier,
		AmountInMinorUnits: amountInMinorUnits,
		OccurredAt:         occurredAt,
	}
	monitor.transactionsByAccountId[accountIdentifier] = append(monitor.transactionsByAccountId[accountIdentifier], record)

	var newAlerts []Alert
	if amountInMinorUnits >= monitor.config.LargeTransactionThresholdInMinorUnits {
		newAlerts = append(newAlerts, Alert{
			AccountIdentifier: accountIdentifier,
			AlertType:         AlertTypeLargeTransaction,
			Description: "single transaction of " + formatMinorUnits(amountInMinorUnits) +
				" meets or exceeds the large-transaction threshold of " + formatMinorUnits(monitor.config.LargeTransactionThresholdInMinorUnits),
			RaisedAt: occurredAt,
		})
	}

	if velocityAlert, triggered := monitor.evaluateVelocity(accountIdentifier, occurredAt); triggered {
		newAlerts = append(newAlerts, velocityAlert)
	}

	if structuringAlert, triggered := monitor.evaluateStructuring(accountIdentifier, occurredAt); triggered {
		newAlerts = append(newAlerts, structuringAlert)
	}

	if len(newAlerts) > 0 {
		monitor.alertsByAccountId[accountIdentifier] = append(monitor.alertsByAccountId[accountIdentifier], newAlerts...)
	}
	return newAlerts
}

// evaluateVelocity must be called with mutexGuardingState already held.
func (monitor *Monitor) evaluateVelocity(accountIdentifier string, occurredAt time.Time) (Alert, bool) {
	windowStart := occurredAt.Add(-monitor.config.VelocityWindow)
	countInWindow := 0
	for _, record := range monitor.transactionsByAccountId[accountIdentifier] {
		if !record.OccurredAt.Before(windowStart) && !record.OccurredAt.After(occurredAt) {
			countInWindow++
		}
	}
	if countInWindow <= monitor.config.VelocityMaxTransactionsInWindow {
		return Alert{}, false
	}
	return Alert{
		AccountIdentifier: accountIdentifier,
		AlertType:         AlertTypeVelocity,
		Description: formatCount(countInWindow) + " transactions within " + monitor.config.VelocityWindow.String() +
			" exceeds the velocity threshold of " + formatCount(monitor.config.VelocityMaxTransactionsInWindow),
		RaisedAt: occurredAt,
	}, true
}

// evaluateStructuring must be called with mutexGuardingState already
// held. Flags a pattern of individually-sub-threshold transactions
// whose sum within the structuring window crosses the report threshold
// — it deliberately does NOT fire on a single transaction that alone
// crosses the threshold (that's AlertTypeLargeTransaction's job, and a
// single large transaction isn't structuring).
func (monitor *Monitor) evaluateStructuring(accountIdentifier string, occurredAt time.Time) (Alert, bool) {
	windowStart := occurredAt.Add(-monitor.config.StructuringWindow)
	sumInWindow := int64(0)
	countOfSubThresholdTransactions := 0
	for _, record := range monitor.transactionsByAccountId[accountIdentifier] {
		if record.OccurredAt.Before(windowStart) || record.OccurredAt.After(occurredAt) {
			continue
		}
		if record.AmountInMinorUnits >= monitor.config.StructuringReportThresholdInMinorUnits {
			// A transaction that alone crosses the threshold isn't part
			// of a structuring pattern — it's just a large transaction.
			continue
		}
		sumInWindow += record.AmountInMinorUnits
		countOfSubThresholdTransactions++
	}

	if sumInWindow < monitor.config.StructuringReportThresholdInMinorUnits || countOfSubThresholdTransactions < 2 {
		return Alert{}, false
	}
	return Alert{
		AccountIdentifier: accountIdentifier,
		AlertType:         AlertTypeStructuring,
		Description: formatCount(countOfSubThresholdTransactions) + " sub-threshold transactions totaling " + formatMinorUnits(sumInWindow) +
			" within " + monitor.config.StructuringWindow.String() + " cross the reporting threshold of " +
			formatMinorUnits(monitor.config.StructuringReportThresholdInMinorUnits) + " — possible structuring",
		RaisedAt: occurredAt,
	}, true
}

// ScreenName checks a full name against the static PEP watch list,
// case-insensitively. A match doesn't itself accuse anyone of anything
// — it's the same "worth an extra look" signal a real PEP screen
// produces, which is why it's returned as an Alert like everything
// else, not a hard block.
func (monitor *Monitor) ScreenName(accountIdentifier string, fullName string, occurredAt time.Time) (Alert, bool) {
	monitor.mutexGuardingState.Lock()
	defer monitor.mutexGuardingState.Unlock()

	listedName, isMatch := monitor.politicallyExposedPersons[strings.ToLower(strings.TrimSpace(fullName))]
	if !isMatch {
		return Alert{}, false
	}

	alert := Alert{
		AccountIdentifier: accountIdentifier,
		AlertType:         AlertTypePepMatch,
		Description:       "name matches politically-exposed-person watch list entry: " + listedName,
		RaisedAt:          occurredAt,
	}
	monitor.alertsByAccountId[accountIdentifier] = append(monitor.alertsByAccountId[accountIdentifier], alert)
	return alert, true
}

// AlertsForAccount returns every alert raised for accountIdentifier so
// far, oldest first.
func (monitor *Monitor) AlertsForAccount(accountIdentifier string) []Alert {
	monitor.mutexGuardingState.Lock()
	defer monitor.mutexGuardingState.Unlock()

	alerts := monitor.alertsByAccountId[accountIdentifier]
	sorted := make([]Alert, len(alerts))
	copy(sorted, alerts)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].RaisedAt.Before(sorted[j].RaisedAt) })
	return sorted
}

// AllAlerts returns every alert raised across every account, oldest
// first — the actual review-queue view a compliance officer needs.
func (monitor *Monitor) AllAlerts() []Alert {
	monitor.mutexGuardingState.Lock()
	defer monitor.mutexGuardingState.Unlock()

	var allAlerts []Alert
	for _, alerts := range monitor.alertsByAccountId {
		allAlerts = append(allAlerts, alerts...)
	}
	sort.Slice(allAlerts, func(i, j int) bool {
		if allAlerts[i].RaisedAt.Equal(allAlerts[j].RaisedAt) {
			return allAlerts[i].AccountIdentifier < allAlerts[j].AccountIdentifier
		}
		return allAlerts[i].RaisedAt.Before(allAlerts[j].RaisedAt)
	})
	if allAlerts == nil {
		allAlerts = []Alert{}
	}
	return allAlerts
}

func formatMinorUnits(amountInMinorUnits int64) string {
	return strconv.FormatInt(amountInMinorUnits, 10) + " minor units"
}

func formatCount(count int) string {
	return strconv.Itoa(count)
}
