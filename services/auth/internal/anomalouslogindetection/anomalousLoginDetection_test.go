package anomalouslogindetection

import (
	"testing"
	"time"
)

func TestFirstEverLoginAttemptRaisesNoNewDeviceAlert(t *testing.T) {
	detector := NewDetector()
	alerts, err := detector.RecordLoginAttempt(LoginAttempt{
		AccountIdentifier: "acct-001",
		DeviceFingerprint: "device-abc",
		IpAddressPrefix:   "203.0.113.0/24",
		WasSuccessful:     true,
		AttemptedAtTime:   time.Now(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(alerts) != 0 {
		t.Fatalf("expected no alerts on an account's very first attempt, got %v", alerts)
	}
}

func TestSameDeviceAndNetworkAgainRaisesNoAlert(t *testing.T) {
	detector := NewDetector()
	now := time.Now()
	detector.RecordLoginAttempt(LoginAttempt{
		AccountIdentifier: "acct-001",
		DeviceFingerprint: "device-abc",
		IpAddressPrefix:   "203.0.113.0/24",
		WasSuccessful:     true,
		AttemptedAtTime:   now,
	})
	alerts, _ := detector.RecordLoginAttempt(LoginAttempt{
		AccountIdentifier: "acct-001",
		DeviceFingerprint: "device-abc",
		IpAddressPrefix:   "203.0.113.0/24",
		WasSuccessful:     true,
		AttemptedAtTime:   now.Add(time.Hour),
	})
	if len(alerts) != 0 {
		t.Fatalf("expected no alerts for a repeat of the same device/network, got %v", alerts)
	}
}

func TestNewDeviceFingerprintRaisesAlert(t *testing.T) {
	detector := NewDetector()
	now := time.Now()
	detector.RecordLoginAttempt(LoginAttempt{
		AccountIdentifier: "acct-001",
		DeviceFingerprint: "device-abc",
		IpAddressPrefix:   "203.0.113.0/24",
		WasSuccessful:     true,
		AttemptedAtTime:   now,
	})
	alerts, err := detector.RecordLoginAttempt(LoginAttempt{
		AccountIdentifier: "acct-001",
		DeviceFingerprint: "device-XYZ-never-seen",
		IpAddressPrefix:   "203.0.113.0/24",
		WasSuccessful:     true,
		AttemptedAtTime:   now.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(alerts) != 1 || alerts[0].AlertType != AlertNewDeviceOrNetwork {
		t.Fatalf("expected exactly one NEW_DEVICE_OR_NETWORK alert, got %v", alerts)
	}
}

func TestNewNetworkAloneRaisesAlert(t *testing.T) {
	detector := NewDetector()
	now := time.Now()
	detector.RecordLoginAttempt(LoginAttempt{
		AccountIdentifier: "acct-001",
		DeviceFingerprint: "device-abc",
		IpAddressPrefix:   "203.0.113.0/24",
		WasSuccessful:     true,
		AttemptedAtTime:   now,
	})
	alerts, _ := detector.RecordLoginAttempt(LoginAttempt{
		AccountIdentifier: "acct-001",
		DeviceFingerprint: "device-abc",
		IpAddressPrefix:   "198.51.100.0/24",
		WasSuccessful:     true,
		AttemptedAtTime:   now.Add(time.Hour),
	})
	if len(alerts) != 1 || alerts[0].AlertType != AlertNewDeviceOrNetwork {
		t.Fatalf("expected exactly one NEW_DEVICE_OR_NETWORK alert for a new network, got %v", alerts)
	}
}

func TestTwoAccountsHaveIndependentDeviceHistory(t *testing.T) {
	detector := NewDetector()
	now := time.Now()
	detector.RecordLoginAttempt(LoginAttempt{
		AccountIdentifier: "acct-001",
		DeviceFingerprint: "device-abc",
		WasSuccessful:     true,
		AttemptedAtTime:   now,
	})
	alerts, _ := detector.RecordLoginAttempt(LoginAttempt{
		AccountIdentifier: "acct-002",
		DeviceFingerprint: "device-abc",
		WasSuccessful:     true,
		AttemptedAtTime:   now,
	})
	if len(alerts) != 0 {
		t.Fatalf("expected acct-002's first attempt to raise no alert regardless of acct-001's history, got %v", alerts)
	}
}

func TestImpossibleTravelBetweenDistantLocationsIsFlagged(t *testing.T) {
	detector := NewDetector()
	now := time.Now()
	// New York City
	detector.RecordLoginAttempt(LoginAttempt{
		AccountIdentifier: "acct-001",
		WasSuccessful:     true,
		AttemptedAtTime:   now,
		HasLocation:       true,
		Latitude:          40.7128,
		Longitude:         -74.0060,
	})
	// Tokyo, 5 minutes later — ~10,850 km, physically impossible in 5 minutes
	alerts, err := detector.RecordLoginAttempt(LoginAttempt{
		AccountIdentifier: "acct-001",
		WasSuccessful:     true,
		AttemptedAtTime:   now.Add(5 * time.Minute),
		HasLocation:       true,
		Latitude:          35.6762,
		Longitude:         139.6503,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	found := false
	for _, alert := range alerts {
		if alert.AlertType == AlertImpossibleTravel {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected an IMPOSSIBLE_TRAVEL alert, got %v", alerts)
	}
}

func TestPlausibleTravelIsNotFlagged(t *testing.T) {
	detector := NewDetector()
	now := time.Now()
	// New York City
	detector.RecordLoginAttempt(LoginAttempt{
		AccountIdentifier: "acct-001",
		WasSuccessful:     true,
		AttemptedAtTime:   now,
		HasLocation:       true,
		Latitude:          40.7128,
		Longitude:         -74.0060,
	})
	// Boston (~300km away), 6 hours later — trivially plausible (even walking distance over that time)
	alerts, err := detector.RecordLoginAttempt(LoginAttempt{
		AccountIdentifier: "acct-001",
		WasSuccessful:     true,
		AttemptedAtTime:   now.Add(6 * time.Hour),
		HasLocation:       true,
		Latitude:          42.3601,
		Longitude:         -71.0589,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, alert := range alerts {
		if alert.AlertType == AlertImpossibleTravel {
			t.Fatalf("expected no IMPOSSIBLE_TRAVEL alert for plausible travel, got %v", alerts)
		}
	}
}

func TestSameLocationIsNeverImpossibleTravel(t *testing.T) {
	detector := NewDetector()
	now := time.Now()
	detector.RecordLoginAttempt(LoginAttempt{
		AccountIdentifier: "acct-001",
		WasSuccessful:     true,
		AttemptedAtTime:   now,
		HasLocation:       true,
		Latitude:          40.7128,
		Longitude:         -74.0060,
	})
	alerts, _ := detector.RecordLoginAttempt(LoginAttempt{
		AccountIdentifier: "acct-001",
		WasSuccessful:     true,
		AttemptedAtTime:   now.Add(time.Second),
		HasLocation:       true,
		Latitude:          40.7128,
		Longitude:         -74.0060,
	})
	for _, alert := range alerts {
		if alert.AlertType == AlertImpossibleTravel {
			t.Fatalf("expected no IMPOSSIBLE_TRAVEL alert for an identical location, got %v", alerts)
		}
	}
}

func TestFailedLoginsDoNotUpdateLocationHistory(t *testing.T) {
	detector := NewDetector()
	now := time.Now()
	detector.RecordLoginAttempt(LoginAttempt{
		AccountIdentifier: "acct-001",
		WasSuccessful:     true,
		AttemptedAtTime:   now,
		HasLocation:       true,
		Latitude:          40.7128,
		Longitude:         -74.0060,
	})
	// A FAILED login from Tokyo shouldn't update the "last successful
	// location" baseline.
	detector.RecordLoginAttempt(LoginAttempt{
		AccountIdentifier: "acct-001",
		WasSuccessful:     false,
		AttemptedAtTime:   now.Add(time.Minute),
		HasLocation:       true,
		Latitude:          35.6762,
		Longitude:         139.6503,
	})
	// A second successful NYC login shortly after should NOT be flagged,
	// because the last SUCCESSFUL location is still NYC.
	alerts, _ := detector.RecordLoginAttempt(LoginAttempt{
		AccountIdentifier: "acct-001",
		WasSuccessful:     true,
		AttemptedAtTime:   now.Add(2 * time.Minute),
		HasLocation:       true,
		Latitude:          40.7128,
		Longitude:         -74.0060,
	})
	for _, alert := range alerts {
		if alert.AlertType == AlertImpossibleTravel {
			t.Fatalf("expected failed attempts to not affect the impossible-travel baseline, got %v", alerts)
		}
	}
}

func TestRapidFailuresThenSuccessIsFlagged(t *testing.T) {
	detector := NewDetector()
	now := time.Now()
	for i := 0; i < consecutiveFailureThresholdForRapidPattern; i++ {
		detector.RecordLoginAttempt(LoginAttempt{
			AccountIdentifier: "acct-001",
			WasSuccessful:     false,
			AttemptedAtTime:   now.Add(time.Duration(i) * time.Second),
		})
	}
	alerts, err := detector.RecordLoginAttempt(LoginAttempt{
		AccountIdentifier: "acct-001",
		WasSuccessful:     true,
		AttemptedAtTime:   now.Add(10 * time.Second),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	found := false
	for _, alert := range alerts {
		if alert.AlertType == AlertRapidFailuresThenSuccess {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a RAPID_FAILURES_THEN_SUCCESS alert, got %v", alerts)
	}
}

func TestFewFailuresThenSuccessIsNotFlagged(t *testing.T) {
	detector := NewDetector()
	now := time.Now()
	detector.RecordLoginAttempt(LoginAttempt{
		AccountIdentifier: "acct-001",
		WasSuccessful:     false,
		AttemptedAtTime:   now,
	})
	alerts, _ := detector.RecordLoginAttempt(LoginAttempt{
		AccountIdentifier: "acct-001",
		WasSuccessful:     true,
		AttemptedAtTime:   now.Add(time.Second),
	})
	for _, alert := range alerts {
		if alert.AlertType == AlertRapidFailuresThenSuccess {
			t.Fatalf("expected no RAPID_FAILURES_THEN_SUCCESS alert for just one failure, got %v", alerts)
		}
	}
}

func TestFailuresOutsideLookbackWindowAreNotCounted(t *testing.T) {
	detector := NewDetector()
	now := time.Now()
	for i := 0; i < consecutiveFailureThresholdForRapidPattern; i++ {
		detector.RecordLoginAttempt(LoginAttempt{
			AccountIdentifier: "acct-001",
			WasSuccessful:     false,
			AttemptedAtTime:   now.Add(time.Duration(i) * time.Second),
		})
	}
	// Success arrives long after the lookback window — the failures are
	// "stale" and shouldn't count toward the rapid pattern.
	alerts, _ := detector.RecordLoginAttempt(LoginAttempt{
		AccountIdentifier: "acct-001",
		WasSuccessful:     true,
		AttemptedAtTime:   now.Add(rapidPatternLookbackWindow + time.Hour),
	})
	for _, alert := range alerts {
		if alert.AlertType == AlertRapidFailuresThenSuccess {
			t.Fatalf("expected stale failures outside the lookback window to not be flagged, got %v", alerts)
		}
	}
}

func TestSuccessResetsTheFailureStreak(t *testing.T) {
	detector := NewDetector()
	now := time.Now()
	// One failure, then a success — streak resets.
	detector.RecordLoginAttempt(LoginAttempt{
		AccountIdentifier: "acct-001",
		WasSuccessful:     false,
		AttemptedAtTime:   now,
	})
	detector.RecordLoginAttempt(LoginAttempt{
		AccountIdentifier: "acct-001",
		WasSuccessful:     true,
		AttemptedAtTime:   now.Add(time.Second),
	})
	// One more failure, then another success — still below threshold
	// because the streak reset.
	detector.RecordLoginAttempt(LoginAttempt{
		AccountIdentifier: "acct-001",
		WasSuccessful:     false,
		AttemptedAtTime:   now.Add(2 * time.Second),
	})
	alerts, _ := detector.RecordLoginAttempt(LoginAttempt{
		AccountIdentifier: "acct-001",
		WasSuccessful:     true,
		AttemptedAtTime:   now.Add(3 * time.Second),
	})
	for _, alert := range alerts {
		if alert.AlertType == AlertRapidFailuresThenSuccess {
			t.Fatalf("expected the failure streak to have reset after the earlier success, got %v", alerts)
		}
	}
}

func TestRecordLoginAttemptRequiresAccountIdentifier(t *testing.T) {
	detector := NewDetector()
	_, err := detector.RecordLoginAttempt(LoginAttempt{WasSuccessful: true, AttemptedAtTime: time.Now()})
	if err != ErrAccountIdentifierRequired {
		t.Fatalf("expected ErrAccountIdentifierRequired, got %v", err)
	}
}

func TestAllAlertsReturnsEveryAlertAcrossAccounts(t *testing.T) {
	detector := NewDetector()
	now := time.Now()
	detector.RecordLoginAttempt(LoginAttempt{AccountIdentifier: "acct-001", DeviceFingerprint: "d1", WasSuccessful: true, AttemptedAtTime: now})
	detector.RecordLoginAttempt(LoginAttempt{AccountIdentifier: "acct-001", DeviceFingerprint: "d2", WasSuccessful: true, AttemptedAtTime: now.Add(time.Minute)})
	detector.RecordLoginAttempt(LoginAttempt{AccountIdentifier: "acct-002", DeviceFingerprint: "d3", WasSuccessful: true, AttemptedAtTime: now})
	detector.RecordLoginAttempt(LoginAttempt{AccountIdentifier: "acct-002", DeviceFingerprint: "d4", WasSuccessful: true, AttemptedAtTime: now.Add(time.Minute)})

	allAlerts := detector.AllAlerts()
	if len(allAlerts) != 2 {
		t.Fatalf("expected 2 total alerts across both accounts, got %d: %v", len(allAlerts), allAlerts)
	}
}

func TestAlertsForAccountFiltersToOneAccount(t *testing.T) {
	detector := NewDetector()
	now := time.Now()
	detector.RecordLoginAttempt(LoginAttempt{AccountIdentifier: "acct-001", DeviceFingerprint: "d1", WasSuccessful: true, AttemptedAtTime: now})
	detector.RecordLoginAttempt(LoginAttempt{AccountIdentifier: "acct-001", DeviceFingerprint: "d2", WasSuccessful: true, AttemptedAtTime: now.Add(time.Minute)})
	detector.RecordLoginAttempt(LoginAttempt{AccountIdentifier: "acct-002", DeviceFingerprint: "d3", WasSuccessful: true, AttemptedAtTime: now})
	detector.RecordLoginAttempt(LoginAttempt{AccountIdentifier: "acct-002", DeviceFingerprint: "d4", WasSuccessful: true, AttemptedAtTime: now.Add(time.Minute)})

	acct1Alerts := detector.AlertsForAccount("acct-001")
	if len(acct1Alerts) != 1 {
		t.Fatalf("expected exactly 1 alert for acct-001, got %d: %v", len(acct1Alerts), acct1Alerts)
	}
	for _, alert := range acct1Alerts {
		if alert.AccountIdentifier != "acct-001" {
			t.Fatalf("AlertsForAccount leaked another account's alert: %v", alert)
		}
	}
}

func TestAllAlertsReturnsACopyNotTheInternalSlice(t *testing.T) {
	detector := NewDetector()
	now := time.Now()
	detector.RecordLoginAttempt(LoginAttempt{AccountIdentifier: "acct-001", DeviceFingerprint: "d1", WasSuccessful: true, AttemptedAtTime: now})
	detector.RecordLoginAttempt(LoginAttempt{AccountIdentifier: "acct-001", DeviceFingerprint: "d2", WasSuccessful: true, AttemptedAtTime: now.Add(time.Minute)})

	firstFetch := detector.AllAlerts()
	firstFetch[0].DetailMessage = "mutated"

	secondFetch := detector.AllAlerts()
	if secondFetch[0].DetailMessage == "mutated" {
		t.Fatal("expected AllAlerts to return a copy, not a reference to internal state")
	}
}

func TestEmptyDeviceFingerprintAndNetworkNeverRaiseNewDeviceAlert(t *testing.T) {
	detector := NewDetector()
	now := time.Now()
	detector.RecordLoginAttempt(LoginAttempt{AccountIdentifier: "acct-001", WasSuccessful: true, AttemptedAtTime: now})
	alerts, _ := detector.RecordLoginAttempt(LoginAttempt{AccountIdentifier: "acct-001", WasSuccessful: true, AttemptedAtTime: now.Add(time.Hour)})
	for _, alert := range alerts {
		if alert.AlertType == AlertNewDeviceOrNetwork {
			t.Fatalf("expected no NEW_DEVICE_OR_NETWORK alert when neither fingerprint nor prefix was ever supplied, got %v", alerts)
		}
	}
}
