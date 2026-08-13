package amlmonitoring

import (
	"testing"
	"time"
)

func newTestMonitor() *Monitor {
	return NewMonitor(MonitorConfig{
		LargeTransactionThresholdInMinorUnits:  1_000_000, // ₹10,000.00 in paise, illustrative
		StructuringReportThresholdInMinorUnits: 500_000,
		StructuringWindow:                      24 * time.Hour,
		VelocityMaxTransactionsInWindow:        3,
		VelocityWindow:                         time.Hour,
	}, []string{"Corrupt Official", "Sanctioned Person"})
}

func baseTime() time.Time {
	return time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
}

func TestSmallOrdinaryTransactionRaisesNoAlert(t *testing.T) {
	monitor := newTestMonitor()
	alerts := monitor.RecordTransaction("acct-001", 10_000, baseTime())
	if len(alerts) != 0 {
		t.Errorf("expected no alerts for a small transaction, got %v", alerts)
	}
}

func TestSingleLargeTransactionRaisesLargeTransactionAlert(t *testing.T) {
	monitor := newTestMonitor()
	alerts := monitor.RecordTransaction("acct-001", 1_000_000, baseTime())
	if len(alerts) != 1 || alerts[0].AlertType != AlertTypeLargeTransaction {
		t.Fatalf("expected exactly one LARGE_TRANSACTION alert, got %v", alerts)
	}
}

func TestNegativeAmountIsTreatedAsItsAbsoluteValue(t *testing.T) {
	monitor := newTestMonitor()
	alerts := monitor.RecordTransaction("acct-001", -1_500_000, baseTime())
	if len(alerts) != 1 || alerts[0].AlertType != AlertTypeLargeTransaction {
		t.Fatalf("expected a LARGE_TRANSACTION alert for a large payout too, got %v", alerts)
	}
}

func TestVelocityAlertFiresOnTheTransactionThatExceedsTheLimit(t *testing.T) {
	monitor := newTestMonitor()
	t0 := baseTime()

	// 3 transactions within the window is exactly at the limit — no alert.
	monitor.RecordTransaction("acct-001", 1000, t0)
	monitor.RecordTransaction("acct-001", 1000, t0.Add(10*time.Minute))
	alerts := monitor.RecordTransaction("acct-001", 1000, t0.Add(20*time.Minute))
	for _, alert := range alerts {
		if alert.AlertType == AlertTypeVelocity {
			t.Fatalf("did not expect a velocity alert at exactly the threshold, got %v", alerts)
		}
	}

	// The 4th transaction within the window crosses it.
	alerts = monitor.RecordTransaction("acct-001", 1000, t0.Add(30*time.Minute))
	foundVelocityAlert := false
	for _, alert := range alerts {
		if alert.AlertType == AlertTypeVelocity {
			foundVelocityAlert = true
		}
	}
	if !foundVelocityAlert {
		t.Errorf("expected a velocity alert on the 4th transaction within the window, got %v", alerts)
	}
}

func TestVelocityDoesNotCountTransactionsOutsideTheWindow(t *testing.T) {
	monitor := newTestMonitor()
	t0 := baseTime()

	monitor.RecordTransaction("acct-001", 1000, t0)
	monitor.RecordTransaction("acct-001", 1000, t0.Add(2*time.Hour))
	monitor.RecordTransaction("acct-001", 1000, t0.Add(4*time.Hour))
	alerts := monitor.RecordTransaction("acct-001", 1000, t0.Add(6*time.Hour))
	for _, alert := range alerts {
		if alert.AlertType == AlertTypeVelocity {
			t.Errorf("did not expect a velocity alert — transactions are spread far outside the window, got %v", alerts)
		}
	}
}

func TestStructuringAlertFiresWhenSubThresholdTransactionsSumOverTheLimit(t *testing.T) {
	monitor := newTestMonitor()
	t0 := baseTime()

	monitor.RecordTransaction("acct-001", 200_000, t0)
	alerts := monitor.RecordTransaction("acct-001", 200_000, t0.Add(time.Hour))
	for _, alert := range alerts {
		if alert.AlertType == AlertTypeStructuring {
			t.Fatalf("did not expect structuring alert yet, sum is only 400000, got %v", alerts)
		}
	}

	alerts = monitor.RecordTransaction("acct-001", 200_000, t0.Add(2*time.Hour))
	foundStructuringAlert := false
	for _, alert := range alerts {
		if alert.AlertType == AlertTypeStructuring {
			foundStructuringAlert = true
		}
	}
	if !foundStructuringAlert {
		t.Errorf("expected structuring alert once sum (600000) crosses threshold (500000), got %v", alerts)
	}
}

func TestStructuringDoesNotFireForASingleLargeTransactionAlone(t *testing.T) {
	monitor := newTestMonitor()
	alerts := monitor.RecordTransaction("acct-001", 600_000, baseTime())
	for _, alert := range alerts {
		if alert.AlertType == AlertTypeStructuring {
			t.Errorf("a single transaction crossing the threshold is not structuring, got %v", alerts)
		}
	}
}

func TestStructuringDoesNotFireForASingleSubThresholdTransaction(t *testing.T) {
	monitor := newTestMonitor()
	alerts := monitor.RecordTransaction("acct-001", 400_000, baseTime())
	for _, alert := range alerts {
		if alert.AlertType == AlertTypeStructuring {
			t.Errorf("a lone sub-threshold transaction is not structuring, got %v", alerts)
		}
	}
}

func TestStructuringDoesNotCountTransactionsOutsideTheWindow(t *testing.T) {
	monitor := newTestMonitor()
	t0 := baseTime()

	monitor.RecordTransaction("acct-001", 300_000, t0)
	alerts := monitor.RecordTransaction("acct-001", 300_000, t0.Add(48*time.Hour))
	for _, alert := range alerts {
		if alert.AlertType == AlertTypeStructuring {
			t.Errorf("transactions 48h apart are outside a 24h window, should not trigger structuring, got %v", alerts)
		}
	}
}

func TestScreenNameMatchesWatchListCaseInsensitively(t *testing.T) {
	monitor := newTestMonitor()
	alert, isMatch := monitor.ScreenName("acct-001", "corrupt OFFICIAL", baseTime())
	if !isMatch {
		t.Fatal("expected a case-insensitive PEP match")
	}
	if alert.AlertType != AlertTypePepMatch {
		t.Errorf("expected AlertTypePepMatch, got %v", alert.AlertType)
	}
}

func TestScreenNameNoMatchForAnOrdinaryName(t *testing.T) {
	monitor := newTestMonitor()
	_, isMatch := monitor.ScreenName("acct-001", "Ordinary Person", baseTime())
	if isMatch {
		t.Error("did not expect a PEP match for an unlisted name")
	}
}

func TestAlertsForAccountReturnsOnlyThatAccountsAlertsInOrder(t *testing.T) {
	monitor := newTestMonitor()
	t0 := baseTime()
	monitor.RecordTransaction("acct-001", 1_000_000, t0)
	monitor.RecordTransaction("acct-002", 2_000_000, t0.Add(time.Minute))
	monitor.RecordTransaction("acct-001", 1_500_000, t0.Add(2*time.Minute))

	alerts := monitor.AlertsForAccount("acct-001")
	if len(alerts) != 2 {
		t.Fatalf("expected 2 alerts for acct-001, got %d: %v", len(alerts), alerts)
	}
	for _, alert := range alerts {
		if alert.AccountIdentifier != "acct-001" {
			t.Errorf("leaked another account's alert: %v", alert)
		}
	}
	if !alerts[0].RaisedAt.Before(alerts[1].RaisedAt) {
		t.Errorf("expected alerts sorted oldest-first")
	}
}

func TestAlertsForAccountReturnsEmptyNotNilForAnAccountWithNoAlerts(t *testing.T) {
	monitor := newTestMonitor()
	alerts := monitor.AlertsForAccount("never-seen-account")
	if alerts == nil {
		t.Error("expected an empty slice, got nil")
	}
	if len(alerts) != 0 {
		t.Errorf("expected no alerts, got %v", alerts)
	}
}

func TestAllAlertsAggregatesAcrossAccounts(t *testing.T) {
	monitor := newTestMonitor()
	t0 := baseTime()
	monitor.RecordTransaction("acct-001", 1_000_000, t0)
	monitor.RecordTransaction("acct-002", 2_000_000, t0.Add(time.Minute))

	allAlerts := monitor.AllAlerts()
	if len(allAlerts) != 2 {
		t.Fatalf("expected 2 alerts total, got %d: %v", len(allAlerts), allAlerts)
	}
}

func TestAllAlertsReturnsEmptyNotNilWhenNothingHasBeenRaised(t *testing.T) {
	monitor := newTestMonitor()
	allAlerts := monitor.AllAlerts()
	if allAlerts == nil {
		t.Error("expected an empty slice, got nil")
	}
}
