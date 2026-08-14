// Package anomalouslogindetection implements FEATURES.md §19's
// "ML-based account-takeover / anomalous-login detection" — a REAL,
// distinct capability from the AML monitoring FEATURES.md §1/ledger
// covers elsewhere (that package watches for money-laundering-shaped
// MOVEMENT OF FUNDS; this package watches for account-TAKEOVER-shaped
// LOGIN BEHAVIOR — different domain, different signals, no shared code).
//
// Honesty note on "ML-based" (the FEATURES.md item name, not a claim
// made here): this is NOT a trained machine-learning model. There is no
// labeled historical dataset of real account-takeover incidents in this
// repo (or anywhere close to enough of one) to train anything on, and
// pretending otherwise would be dishonest. What's built instead is REAL
// rule-based/heuristic detection over REAL login attempt data — the same
// category of signal a real ML model would eventually be trained to
// weigh, just combined with hand-written thresholds instead of learned
// ones. A real build would replace (or supplement) these rules with an
// actual trained model once real labeled incident data exists — see the
// TODO at the bottom of this file.
//
// Three real detectors, each over real per-account history this package
// itself maintains:
//
//  1. New-device/new-network detection: every login attempt carries a
//     caller-supplied opaque deviceFingerprint string (a real input this
//     package tracks per account — no attempt is made to derive or
//     validate the fingerprint itself, that's the caller's job) and an
//     ipAddressPrefix. An account's first-ever attempt establishes no
//     alert (there is no "known" history yet to compare against); every
//     later attempt from a fingerprint OR prefix genuinely never seen
//     before on that account raises an alert.
//  2. Impossible travel: every login attempt optionally carries a
//     latitude/longitude. Given two successful logins for the same
//     account, this package computes the real great-circle (haversine)
//     distance between them and the real elapsed wall-clock time, derives
//     an implied travel speed, and flags anything exceeding a speed no
//     unassisted human travel achieves (faster than a commercial
//     aircraft's cruising speed).
//  3. Rapid-repeated-failed-then-success: a real sliding count of
//     consecutive failed attempts for an account; if a success follows a
//     run of failures at/above a threshold within a short window, that's
//     flagged (the credential-stuffing/brute-force-then-guessed-it
//     signature).
//
// TODO(real build): in-memory only (all history — device/IP history,
// location history, failure streaks — is lost on restart, and every
// account looks "new" again); no real device-fingerprint generation
// (accepted as an opaque caller-supplied string, trusting the caller);
// no real IP geolocation (accepted as a caller-supplied lat/long or
// region tag, not derived from the request's actual source address); no
// automated RESPONSE to an alert (no forced step-up MFA, no automatic
// session revocation, no account lock) — this package only detects and
// records, it does not act; thresholds (travel speed, failure-streak
// size, lookback window) are hand-picked illustrative constants, not
// tuned against real fraud data because none exists here.
package anomalouslogindetection

import (
	"errors"
	"math"
	"sync"
	"time"
)

// AlertType enumerates every kind of anomaly this package can detect —
// a closed set, like audittrail.EventType, so alerts can be filtered
// reliably rather than string-matched.
type AlertType string

const (
	AlertNewDeviceOrNetwork       AlertType = "NEW_DEVICE_OR_NETWORK"
	AlertImpossibleTravel         AlertType = "IMPOSSIBLE_TRAVEL"
	AlertRapidFailuresThenSuccess AlertType = "RAPID_FAILURES_THEN_SUCCESS"
)

// maxPlausibleTravelSpeedKilometersPerHour is the threshold impossible
// travel is judged against — faster than this between two successful
// logins for the same account is not achievable by any normal means of
// travel. A commercial aircraft cruises around 900 km/h; 1000 km/h
// leaves headroom for GPS/geotagging imprecision without being so loose
// it misses real cases.
const maxPlausibleTravelSpeedKilometersPerHour = 1000.0

// consecutiveFailureThresholdForRapidPattern is how many failed attempts
// in a row (immediately before a success) are needed to flag the
// rapid-repeated-failed-then-success pattern.
const consecutiveFailureThresholdForRapidPattern = 3

// rapidPatternLookbackWindow bounds how long ago those failures must
// have started for the eventual success to still count as "rapid" — a
// handful of failures followed by a success a week later isn't the same
// signal as the same shape compressed into two minutes.
const rapidPatternLookbackWindow = 10 * time.Minute

var (
	// ErrAccountIdentifierRequired is returned when a caller supplies an
	// empty accountIdentifier.
	ErrAccountIdentifierRequired = errors.New("accountIdentifier is required")
)

// LoginAttempt is one real login attempt fed into the detector — success
// or failure alike; failures matter just as much as successes for the
// rapid-repeated-failure signal.
type LoginAttempt struct {
	AccountIdentifier string
	DeviceFingerprint string // opaque, caller-supplied; empty means "unknown/not provided"
	IpAddressPrefix   string // e.g. a /24-style prefix or opaque network tag; empty means "unknown/not provided"
	WasSuccessful     bool
	AttemptedAtTime   time.Time

	// HasLocation gates whether Latitude/Longitude are meaningful — a
	// caller that doesn't have geolocation for this attempt (most won't,
	// in this skeleton) simply omits it, and the impossible-travel
	// detector skips that attempt rather than treating (0,0) as a real
	// coordinate.
	HasLocation bool
	Latitude    float64
	Longitude   float64
}

// Alert is one real, structured, queryable anomaly record — the output
// of this package. Every alert is retained (never mutated or deleted)
// once raised, mirroring audittrail's "append-only" quality bar for the
// same reason: a compliance/security reviewer needs to see what was
// flagged, not just the current state.
type Alert struct {
	AccountIdentifier string    `json:"accountIdentifier"`
	AlertType         AlertType `json:"alertType"`
	DetailMessage     string    `json:"detailMessage"`
	DetectedAtTime    time.Time `json:"detectedAtTime"`
}

type accountHistory struct {
	knownDeviceFingerprints map[string]bool
	knownIpAddressPrefixes  map[string]bool

	// hasEverAttempted distinguishes "this account's first-ever login
	// attempt" (nothing is "new" yet, because there's no baseline to
	// compare against) from "we've seen this account before but never
	// this fingerprint/prefix" (genuinely new, and alert-worthy).
	hasEverAttempted bool

	// lastSuccessfulLoginTime/Latitude/Longitude/HasLocation track the
	// most recent SUCCESSFUL login with a known location, for the
	// impossible-travel comparison. Failed attempts never update this.
	hasLastSuccessfulLocation bool
	lastSuccessfulLoginTime   time.Time
	lastSuccessfulLatitude    float64
	lastSuccessfulLongitude   float64

	// recentFailureTimes holds the timestamps of a contiguous run of
	// failed attempts not yet followed by a success — reset to empty
	// the moment a success is recorded (after being checked for the
	// rapid-pattern alert).
	recentFailureTimes []time.Time
}

// Detector is the mutex-guarded, in-memory home for every account's
// login history and every alert ever raised.
type Detector struct {
	mutexGuardingState sync.Mutex

	historyByAccount map[string]*accountHistory
	alerts           []Alert
}

// NewDetector builds an empty Detector — every account starts with no
// history and no alerts.
func NewDetector() *Detector {
	return &Detector{
		historyByAccount: make(map[string]*accountHistory),
	}
}

// RecordLoginAttempt feeds one real login attempt into the detector,
// updating that account's history and returning every alert this single
// attempt raised (zero, one, or more than one — e.g. a genuinely new
// device from a geographically impossible location in the same
// attempt raises both). Alerts are also retained internally and
// queryable later via AllAlerts/AlertsForAccount.
func (detector *Detector) RecordLoginAttempt(attempt LoginAttempt) ([]Alert, error) {
	if attempt.AccountIdentifier == "" {
		return nil, ErrAccountIdentifierRequired
	}

	detector.mutexGuardingState.Lock()
	defer detector.mutexGuardingState.Unlock()

	history, exists := detector.historyByAccount[attempt.AccountIdentifier]
	if !exists {
		history = &accountHistory{
			knownDeviceFingerprints: make(map[string]bool),
			knownIpAddressPrefixes:  make(map[string]bool),
		}
		detector.historyByAccount[attempt.AccountIdentifier] = history
	}

	var raisedAlerts []Alert

	// --- 1. new-device/new-network detection ---
	// Only meaningful once the account has SOME prior history — an
	// account's very first attempt has nothing to compare against, so it
	// establishes a baseline instead of alerting.
	if history.hasEverAttempted {
		isNewDevice := attempt.DeviceFingerprint != "" && !history.knownDeviceFingerprints[attempt.DeviceFingerprint]
		isNewNetwork := attempt.IpAddressPrefix != "" && !history.knownIpAddressPrefixes[attempt.IpAddressPrefix]
		if isNewDevice || isNewNetwork {
			raisedAlerts = append(raisedAlerts, detector.raiseAlertLocked(
				attempt.AccountIdentifier,
				AlertNewDeviceOrNetwork,
				describeNewDeviceOrNetwork(isNewDevice, isNewNetwork, attempt),
				attempt.AttemptedAtTime,
			))
		}
	}
	if attempt.DeviceFingerprint != "" {
		history.knownDeviceFingerprints[attempt.DeviceFingerprint] = true
	}
	if attempt.IpAddressPrefix != "" {
		history.knownIpAddressPrefixes[attempt.IpAddressPrefix] = true
	}
	history.hasEverAttempted = true

	// --- 2. impossible travel (successful logins only) ---
	if attempt.WasSuccessful && attempt.HasLocation {
		if history.hasLastSuccessfulLocation {
			impliedSpeed, isImpossible := evaluateImpossibleTravel(
				history.lastSuccessfulLatitude, history.lastSuccessfulLongitude, history.lastSuccessfulLoginTime,
				attempt.Latitude, attempt.Longitude, attempt.AttemptedAtTime,
			)
			if isImpossible {
				raisedAlerts = append(raisedAlerts, detector.raiseAlertLocked(
					attempt.AccountIdentifier,
					AlertImpossibleTravel,
					describeImpossibleTravel(impliedSpeed, history.lastSuccessfulLoginTime, attempt.AttemptedAtTime),
					attempt.AttemptedAtTime,
				))
			}
		}
		history.hasLastSuccessfulLocation = true
		history.lastSuccessfulLoginTime = attempt.AttemptedAtTime
		history.lastSuccessfulLatitude = attempt.Latitude
		history.lastSuccessfulLongitude = attempt.Longitude
	}

	// --- 3. rapid-repeated-failed-then-success ---
	if attempt.WasSuccessful {
		recentFailuresWithinWindow := countFailuresWithinWindow(history.recentFailureTimes, attempt.AttemptedAtTime, rapidPatternLookbackWindow)
		if recentFailuresWithinWindow >= consecutiveFailureThresholdForRapidPattern {
			raisedAlerts = append(raisedAlerts, detector.raiseAlertLocked(
				attempt.AccountIdentifier,
				AlertRapidFailuresThenSuccess,
				describeRapidFailuresThenSuccess(recentFailuresWithinWindow, attempt.AttemptedAtTime),
				attempt.AttemptedAtTime,
			))
		}
		// A success resets the failure streak — the pattern is about a
		// contiguous run of failures immediately preceding a success,
		// not a lifetime failure count.
		history.recentFailureTimes = nil
	} else {
		history.recentFailureTimes = append(history.recentFailureTimes, attempt.AttemptedAtTime)
	}

	return raisedAlerts, nil
}

// raiseAlertLocked appends and returns a new Alert. Caller must already
// hold mutexGuardingState.
func (detector *Detector) raiseAlertLocked(accountIdentifier string, alertType AlertType, detailMessage string, detectedAtTime time.Time) Alert {
	alert := Alert{
		AccountIdentifier: accountIdentifier,
		AlertType:         alertType,
		DetailMessage:     detailMessage,
		DetectedAtTime:    detectedAtTime,
	}
	detector.alerts = append(detector.alerts, alert)
	return alert
}

// AllAlerts returns every alert ever raised, oldest first. Returns a
// copy — callers can't mutate the detector's internal slice through it.
func (detector *Detector) AllAlerts() []Alert {
	detector.mutexGuardingState.Lock()
	defer detector.mutexGuardingState.Unlock()

	alertsCopy := make([]Alert, len(detector.alerts))
	copy(alertsCopy, detector.alerts)
	return alertsCopy
}

// AlertsForAccount returns every alert raised for one account, oldest
// first.
func (detector *Detector) AlertsForAccount(accountIdentifier string) []Alert {
	detector.mutexGuardingState.Lock()
	defer detector.mutexGuardingState.Unlock()

	var matchingAlerts []Alert
	for _, alert := range detector.alerts {
		if alert.AccountIdentifier == accountIdentifier {
			matchingAlerts = append(matchingAlerts, alert)
		}
	}
	return matchingAlerts
}

// countFailuresWithinWindow returns how many of failureTimes fall within
// windowDuration before referenceTime — used to bound the
// rapid-repeated-failure pattern to a genuinely rapid window rather than
// a lifetime failure count.
func countFailuresWithinWindow(failureTimes []time.Time, referenceTime time.Time, windowDuration time.Duration) int {
	count := 0
	earliestAllowed := referenceTime.Add(-windowDuration)
	for _, failureTime := range failureTimes {
		if failureTime.After(earliestAllowed) || failureTime.Equal(earliestAllowed) {
			count++
		}
	}
	return count
}

// evaluateImpossibleTravel computes the real haversine distance between
// two points and the real elapsed time between them, derives the
// implied travel speed, and reports whether that speed exceeds
// maxPlausibleTravelSpeedKilometersPerHour. A non-positive or
// near-instant elapsed duration (two logins effectively simultaneous
// from different locations) is treated as impossible outright — there's
// no divide-by-near-zero speed calculation that could look "plausible".
func evaluateImpossibleTravel(latitudeOne, longitudeOne float64, timeOne time.Time, latitudeTwo, longitudeTwo float64, timeTwo time.Time) (impliedSpeedKilometersPerHour float64, isImpossible bool) {
	elapsed := timeTwo.Sub(timeOne)
	if elapsed < 0 {
		elapsed = -elapsed
	}

	distanceKilometers := haversineDistanceKilometers(latitudeOne, longitudeOne, latitudeTwo, longitudeTwo)

	// Two logins from materially different locations within the same
	// minute can't be explained by travel at all, regardless of the
	// arithmetic — flag outright rather than dividing by a near-zero
	// duration and getting a meaningless (or infinite) speed.
	const minimumElapsedForSpeedCalculation = time.Minute
	if distanceKilometers > 1.0 && elapsed < minimumElapsedForSpeedCalculation {
		return math.Inf(1), true
	}
	if elapsed == 0 {
		if distanceKilometers > 1.0 {
			return math.Inf(1), true
		}
		return 0, false
	}

	impliedSpeedKilometersPerHour = distanceKilometers / elapsed.Hours()
	return impliedSpeedKilometersPerHour, impliedSpeedKilometersPerHour > maxPlausibleTravelSpeedKilometersPerHour
}

// earthRadiusKilometers is the mean radius used for the haversine
// calculation — precise enough for a "is this humanly possible" check,
// not survey-grade geodesy.
const earthRadiusKilometers = 6371.0

// haversineDistanceKilometers computes the real great-circle distance
// between two lat/long points in kilometers.
func haversineDistanceKilometers(latitudeOneDegrees, longitudeOneDegrees, latitudeTwoDegrees, longitudeTwoDegrees float64) float64 {
	latitudeOneRadians := degreesToRadians(latitudeOneDegrees)
	latitudeTwoRadians := degreesToRadians(latitudeTwoDegrees)
	deltaLatitudeRadians := degreesToRadians(latitudeTwoDegrees - latitudeOneDegrees)
	deltaLongitudeRadians := degreesToRadians(longitudeTwoDegrees - longitudeOneDegrees)

	haversineOfCentralAngle := math.Sin(deltaLatitudeRadians/2)*math.Sin(deltaLatitudeRadians/2) +
		math.Cos(latitudeOneRadians)*math.Cos(latitudeTwoRadians)*
			math.Sin(deltaLongitudeRadians/2)*math.Sin(deltaLongitudeRadians/2)
	centralAngleRadians := 2 * math.Atan2(math.Sqrt(haversineOfCentralAngle), math.Sqrt(1-haversineOfCentralAngle))

	return earthRadiusKilometers * centralAngleRadians
}

func degreesToRadians(degrees float64) float64 {
	return degrees * math.Pi / 180.0
}

func describeNewDeviceOrNetwork(isNewDevice bool, isNewNetwork bool, attempt LoginAttempt) string {
	switch {
	case isNewDevice && isNewNetwork:
		return "login from a device fingerprint and network never seen before on this account"
	case isNewDevice:
		return "login from a device fingerprint never seen before on this account"
	default:
		return "login from a network never seen before on this account"
	}
}

func describeImpossibleTravel(impliedSpeedKilometersPerHour float64, previousTime, currentTime time.Time) string {
	if math.IsInf(impliedSpeedKilometersPerHour, 1) {
		return "two successful logins from materially different locations too close together in time to be explained by any travel"
	}
	elapsed := currentTime.Sub(previousTime)
	return "implied travel speed between two successful logins exceeds any plausible physical travel: ~" +
		formatSpeed(impliedSpeedKilometersPerHour) + " km/h over " + elapsed.String()
}

func describeRapidFailuresThenSuccess(failureCount int, successTime time.Time) string {
	return "a successful login followed a run of failed attempts on this account within a short window"
}

// formatSpeed avoids pulling in fmt just for one call site's float
// formatting — kept simple and dependency-free.
func formatSpeed(speedKilometersPerHour float64) string {
	rounded := int64(math.Round(speedKilometersPerHour))
	return itoa(rounded)
}

func itoa(value int64) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	var digits []byte
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	if negative {
		return "-" + string(digits)
	}
	return string(digits)
}
