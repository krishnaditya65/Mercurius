package tradesurveillance

import (
	"testing"
	"time"

	"mercurius/omsgateway/internal/audittrail"
)

func boolPointer(value bool) *bool { return &value }

// --- Spoofing: should trigger ------------------------------------------------

func TestDetectSpoofing_FlagsLargeAwayFromTouchOrderCancelledShortlyAfter(t *testing.T) {
	baseTime := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	entries := []audittrail.Entry{
		{
			EventType:                      audittrail.EventOrderFilled,
			InstrumentSymbol:               "DEMO-EQ",
			ClientAccountIdentifier:        "acct-spoof-1",
			BuyingClientAccountIdentifier:  "acct-spoof-1",
			SellingClientAccountIdentifier: "acct-other",
			ExecutedPriceInMinorUnits:      10000,
			ExecutedQuantity:               10,
			RecordedAtTime:                 baseTime,
		},
		{
			EventType:                         audittrail.EventOrderRoutedToMatchingEngine,
			ClientAccountIdentifier:           "acct-spoof-1",
			InstrumentSymbol:                  "DEMO-EQ",
			MatchingEngineOrderSequenceNumber: 100,
			OrderSideIsBuyNotSell:             boolPointer(true),
			OrderQuantity:                     1000,
			LimitPriceInMinorUnits:            9000, // 10% below the 10000 reference — well past the 1% threshold
			RecordedAtTime:                    baseTime.Add(1 * time.Second),
		},
		{
			EventType:                         audittrail.EventOrderCancelled,
			InstrumentSymbol:                  "DEMO-EQ",
			MatchingEngineOrderSequenceNumber: 100,
			RecordedAtTime:                    baseTime.Add(1500 * time.Millisecond),
		},
	}

	incidents := DetectSpoofing(entries, DefaultDetectorConfiguration())
	if len(incidents) != 1 {
		t.Fatalf("expected exactly 1 spoofing incident, got %d: %+v", len(incidents), incidents)
	}
	incident := incidents[0]
	if incident.MatchingEngineOrderSequenceNumber != 100 {
		t.Fatalf("expected flagged seq 100, got %d", incident.MatchingEngineOrderSequenceNumber)
	}
	if !incident.WasAwayFromTouch {
		t.Fatal("expected WasAwayFromTouch=true")
	}
	if incident.ReferencePriceInMinorUnits == nil || *incident.ReferencePriceInMinorUnits != 10000 {
		t.Fatalf("expected reference price 10000, got %v", incident.ReferencePriceInMinorUnits)
	}
	if len(incident.ReplayEvents) != 2 {
		t.Fatalf("expected 2 replay events (routed + cancelled), got %d", len(incident.ReplayEvents))
	}
}

func TestDetectSpoofing_FlagsFastCancelWhenNoReferencePriceAvailable(t *testing.T) {
	baseTime := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	entries := []audittrail.Entry{
		{
			EventType:                         audittrail.EventOrderRoutedToMatchingEngine,
			ClientAccountIdentifier:           "acct-spoof-2",
			InstrumentSymbol:                  "NEW-EQ",
			MatchingEngineOrderSequenceNumber: 200,
			OrderSideIsBuyNotSell:             boolPointer(false),
			OrderQuantity:                     600,
			LimitPriceInMinorUnits:            5000,
			RecordedAtTime:                    baseTime,
		},
		{
			EventType:                         audittrail.EventOrderCancelled,
			InstrumentSymbol:                  "NEW-EQ",
			MatchingEngineOrderSequenceNumber: 200,
			RecordedAtTime:                    baseTime.Add(50 * time.Millisecond), // well under the 150ms fast-cancel threshold
		},
	}

	incidents := DetectSpoofing(entries, DefaultDetectorConfiguration())
	if len(incidents) != 1 {
		t.Fatalf("expected exactly 1 spoofing incident, got %d", len(incidents))
	}
	if incidents[0].ReferencePriceInMinorUnits != nil {
		t.Fatalf("expected no reference price, got %v", incidents[0].ReferencePriceInMinorUnits)
	}
	if !incidents[0].WasFasterThanTypicalFillLatency {
		t.Fatal("expected WasFasterThanTypicalFillLatency=true")
	}
}

func TestDetectSpoofing_RecordsCorroboratingOppositeSideFollowUpFill(t *testing.T) {
	baseTime := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	entries := []audittrail.Entry{
		{
			EventType:                         audittrail.EventOrderRoutedToMatchingEngine,
			ClientAccountIdentifier:           "acct-spoof-3",
			InstrumentSymbol:                  "COR-EQ",
			MatchingEngineOrderSequenceNumber: 300,
			OrderSideIsBuyNotSell:             boolPointer(true),
			OrderQuantity:                     900,
			LimitPriceInMinorUnits:            5000,
			RecordedAtTime:                    baseTime,
		},
		{
			EventType:                         audittrail.EventOrderCancelled,
			InstrumentSymbol:                  "COR-EQ",
			MatchingEngineOrderSequenceNumber: 300,
			RecordedAtTime:                    baseTime.Add(50 * time.Millisecond),
		},
		{
			// Opposite-side (sell) fill for the same account shortly after cancellation.
			EventType:                      audittrail.EventOrderFilled,
			InstrumentSymbol:               "COR-EQ",
			ClientAccountIdentifier:        "acct-spoof-3",
			BuyingClientAccountIdentifier:  "acct-other",
			SellingClientAccountIdentifier: "acct-spoof-3",
			ExecutedPriceInMinorUnits:      5100,
			ExecutedQuantity:               900,
			RecordedAtTime:                 baseTime.Add(300 * time.Millisecond),
		},
	}

	incidents := DetectSpoofing(entries, DefaultDetectorConfiguration())
	if len(incidents) != 1 {
		t.Fatalf("expected exactly 1 spoofing incident, got %d", len(incidents))
	}
	if incidents[0].CorroboratingFollowUpEvidence == "" {
		t.Fatal("expected corroborating follow-up evidence to be recorded")
	}
}

// --- Spoofing: should NOT trigger (false-positive avoidance) ----------------

func TestDetectSpoofing_DoesNotFlagOrderThatRestsBeyondTheCancelWindow(t *testing.T) {
	baseTime := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	entries := []audittrail.Entry{
		{
			EventType:                      audittrail.EventOrderFilled,
			InstrumentSymbol:               "REST-EQ",
			ClientAccountIdentifier:        "acct-rest",
			BuyingClientAccountIdentifier:  "acct-rest",
			SellingClientAccountIdentifier: "acct-other",
			ExecutedPriceInMinorUnits:      10000,
			ExecutedQuantity:               10,
			RecordedAtTime:                 baseTime,
		},
		{
			EventType:                         audittrail.EventOrderRoutedToMatchingEngine,
			ClientAccountIdentifier:           "acct-rest",
			InstrumentSymbol:                  "REST-EQ",
			MatchingEngineOrderSequenceNumber: 400,
			OrderSideIsBuyNotSell:             boolPointer(true),
			OrderQuantity:                     1000,
			LimitPriceInMinorUnits:            9000, // away from touch
			RecordedAtTime:                    baseTime.Add(1 * time.Second),
		},
		{
			EventType:                         audittrail.EventOrderCancelled,
			InstrumentSymbol:                  "REST-EQ",
			MatchingEngineOrderSequenceNumber: 400,
			// Cancelled 10s after routing — genuinely rested, well beyond
			// the default 2s "shortly after" window.
			RecordedAtTime: baseTime.Add(11 * time.Second),
		},
	}

	incidents := DetectSpoofing(entries, DefaultDetectorConfiguration())
	if len(incidents) != 0 {
		t.Fatalf("expected 0 spoofing incidents for an order that genuinely rested, got %d: %+v", len(incidents), incidents)
	}
}

func TestDetectSpoofing_DoesNotFlagASmallOrderEvenIfAwayFromTouchAndFastCancelled(t *testing.T) {
	baseTime := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	entries := []audittrail.Entry{
		{
			EventType:                         audittrail.EventOrderRoutedToMatchingEngine,
			ClientAccountIdentifier:           "acct-small",
			InstrumentSymbol:                  "SMALL-EQ",
			MatchingEngineOrderSequenceNumber: 500,
			OrderSideIsBuyNotSell:             boolPointer(true),
			OrderQuantity:                     5, // far under SpoofingLargeOrderQuantityThreshold (500)
			LimitPriceInMinorUnits:            100,
			RecordedAtTime:                    baseTime,
		},
		{
			EventType:                         audittrail.EventOrderCancelled,
			InstrumentSymbol:                  "SMALL-EQ",
			MatchingEngineOrderSequenceNumber: 500,
			RecordedAtTime:                    baseTime.Add(10 * time.Millisecond),
		},
	}

	incidents := DetectSpoofing(entries, DefaultDetectorConfiguration())
	if len(incidents) != 0 {
		t.Fatalf("expected 0 spoofing incidents for a small order, got %d", len(incidents))
	}
}

func TestDetectSpoofing_DoesNotFlagAnOrderThatWasNeverCancelled(t *testing.T) {
	baseTime := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	entries := []audittrail.Entry{
		{
			EventType:                         audittrail.EventOrderRoutedToMatchingEngine,
			ClientAccountIdentifier:           "acct-resting-forever",
			InstrumentSymbol:                  "STILL-EQ",
			MatchingEngineOrderSequenceNumber: 600,
			OrderSideIsBuyNotSell:             boolPointer(true),
			OrderQuantity:                     5000,
			LimitPriceInMinorUnits:            100,
			RecordedAtTime:                    baseTime,
		},
	}

	incidents := DetectSpoofing(entries, DefaultDetectorConfiguration())
	if len(incidents) != 0 {
		t.Fatalf("expected 0 spoofing incidents for an order with no cancellation at all, got %d", len(incidents))
	}
}

func TestDetectSpoofing_DoesNotFlagAMarketOrder(t *testing.T) {
	baseTime := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	entries := []audittrail.Entry{
		{
			EventType:                         audittrail.EventOrderRoutedToMatchingEngine,
			ClientAccountIdentifier:           "acct-mkt",
			InstrumentSymbol:                  "MKT-EQ",
			MatchingEngineOrderSequenceNumber: 700,
			OrderSideIsBuyNotSell:             boolPointer(true),
			OrderQuantity:                     5000,
			OrderIsMarketOrderNotLimit:        true,
			RecordedAtTime:                    baseTime,
		},
		{
			EventType:                         audittrail.EventOrderCancelled,
			InstrumentSymbol:                  "MKT-EQ",
			MatchingEngineOrderSequenceNumber: 700,
			RecordedAtTime:                    baseTime.Add(10 * time.Millisecond),
		},
	}

	incidents := DetectSpoofing(entries, DefaultDetectorConfiguration())
	if len(incidents) != 0 {
		t.Fatalf("expected 0 spoofing incidents for a market order, got %d", len(incidents))
	}
}

// --- Layering: should trigger ------------------------------------------------

func TestDetectLayering_FlagsLadderFollowedByOppositeFillAndMassCancellation(t *testing.T) {
	baseTime := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	entries := []audittrail.Entry{
		{
			EventType:                         audittrail.EventOrderRoutedToMatchingEngine,
			ClientAccountIdentifier:           "acct-layer",
			InstrumentSymbol:                  "LAYER-EQ",
			MatchingEngineOrderSequenceNumber: 1,
			OrderSideIsBuyNotSell:             boolPointer(true),
			OrderQuantity:                     100,
			LimitPriceInMinorUnits:            9900,
			RecordedAtTime:                    baseTime,
		},
		{
			EventType:                         audittrail.EventOrderRoutedToMatchingEngine,
			ClientAccountIdentifier:           "acct-layer",
			InstrumentSymbol:                  "LAYER-EQ",
			MatchingEngineOrderSequenceNumber: 2,
			OrderSideIsBuyNotSell:             boolPointer(true),
			OrderQuantity:                     100,
			LimitPriceInMinorUnits:            9910,
			RecordedAtTime:                    baseTime.Add(200 * time.Millisecond),
		},
		{
			EventType:                         audittrail.EventOrderRoutedToMatchingEngine,
			ClientAccountIdentifier:           "acct-layer",
			InstrumentSymbol:                  "LAYER-EQ",
			MatchingEngineOrderSequenceNumber: 3,
			OrderSideIsBuyNotSell:             boolPointer(true),
			OrderQuantity:                     100,
			LimitPriceInMinorUnits:            9920,
			RecordedAtTime:                    baseTime.Add(400 * time.Millisecond),
		},
		{
			// Opposite-side (sell) fill for the same account.
			EventType:                      audittrail.EventOrderFilled,
			InstrumentSymbol:               "LAYER-EQ",
			ClientAccountIdentifier:        "acct-layer",
			BuyingClientAccountIdentifier:  "acct-other",
			SellingClientAccountIdentifier: "acct-layer",
			ExecutedPriceInMinorUnits:      9950,
			ExecutedQuantity:               50,
			RecordedAtTime:                 baseTime.Add(600 * time.Millisecond),
		},
		{
			EventType:                         audittrail.EventOrderCancelled,
			InstrumentSymbol:                  "LAYER-EQ",
			MatchingEngineOrderSequenceNumber: 1,
			RecordedAtTime:                    baseTime.Add(700 * time.Millisecond),
		},
		{
			EventType:                         audittrail.EventOrderCancelled,
			InstrumentSymbol:                  "LAYER-EQ",
			MatchingEngineOrderSequenceNumber: 2,
			RecordedAtTime:                    baseTime.Add(750 * time.Millisecond),
		},
		{
			EventType:                         audittrail.EventOrderCancelled,
			InstrumentSymbol:                  "LAYER-EQ",
			MatchingEngineOrderSequenceNumber: 3,
			RecordedAtTime:                    baseTime.Add(800 * time.Millisecond),
		},
	}

	incidents := DetectLayering(entries, DefaultDetectorConfiguration())
	if len(incidents) != 1 {
		t.Fatalf("expected exactly 1 layering incident, got %d: %+v", len(incidents), incidents)
	}
	incident := incidents[0]
	if len(incident.LayerOrders) != 3 {
		t.Fatalf("expected 3 layer orders, got %d", len(incident.LayerOrders))
	}
	if incident.DistinctPriceLevels != 3 {
		t.Fatalf("expected 3 distinct price levels, got %d", incident.DistinctPriceLevels)
	}
	if incident.CancelledOrderCount != 3 {
		t.Fatalf("expected all 3 orders cancelled, got %d", incident.CancelledOrderCount)
	}
	if incident.CancelledFraction != 1.0 {
		t.Fatalf("expected cancelled fraction 1.0, got %f", incident.CancelledFraction)
	}
	if len(incident.ReplayEvents) != 6 { // 3 routed + 3 cancelled
		t.Fatalf("expected 6 replay events, got %d", len(incident.ReplayEvents))
	}
}

// --- Layering: should NOT trigger (false-positive avoidance) ----------------

func TestDetectLayering_DoesNotFlagTooFewOrders(t *testing.T) {
	baseTime := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	entries := []audittrail.Entry{
		{
			EventType:                         audittrail.EventOrderRoutedToMatchingEngine,
			ClientAccountIdentifier:           "acct-few",
			InstrumentSymbol:                  "FEW-EQ",
			MatchingEngineOrderSequenceNumber: 10,
			OrderSideIsBuyNotSell:             boolPointer(true),
			OrderQuantity:                     100,
			LimitPriceInMinorUnits:            100,
			RecordedAtTime:                    baseTime,
		},
		{
			EventType:                         audittrail.EventOrderRoutedToMatchingEngine,
			ClientAccountIdentifier:           "acct-few",
			InstrumentSymbol:                  "FEW-EQ",
			MatchingEngineOrderSequenceNumber: 11,
			OrderSideIsBuyNotSell:             boolPointer(true),
			OrderQuantity:                     100,
			LimitPriceInMinorUnits:            110,
			RecordedAtTime:                    baseTime.Add(100 * time.Millisecond),
		},
		{
			EventType:                      audittrail.EventOrderFilled,
			InstrumentSymbol:               "FEW-EQ",
			ClientAccountIdentifier:        "acct-few",
			BuyingClientAccountIdentifier:  "acct-other",
			SellingClientAccountIdentifier: "acct-few",
			ExecutedPriceInMinorUnits:      120,
			ExecutedQuantity:               50,
			RecordedAtTime:                 baseTime.Add(200 * time.Millisecond),
		},
		{
			EventType:                         audittrail.EventOrderCancelled,
			InstrumentSymbol:                  "FEW-EQ",
			MatchingEngineOrderSequenceNumber: 10,
			RecordedAtTime:                    baseTime.Add(300 * time.Millisecond),
		},
		{
			EventType:                         audittrail.EventOrderCancelled,
			InstrumentSymbol:                  "FEW-EQ",
			MatchingEngineOrderSequenceNumber: 11,
			RecordedAtTime:                    baseTime.Add(350 * time.Millisecond),
		},
	}

	incidents := DetectLayering(entries, DefaultDetectorConfiguration())
	if len(incidents) != 0 {
		t.Fatalf("expected 0 layering incidents for only 2 orders, got %d", len(incidents))
	}
}

func TestDetectLayering_DoesNotFlagWhenNoOppositeSideFillOccurs(t *testing.T) {
	baseTime := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	entries := []audittrail.Entry{
		{
			EventType: audittrail.EventOrderRoutedToMatchingEngine, ClientAccountIdentifier: "acct-nofill",
			InstrumentSymbol: "NF-EQ", MatchingEngineOrderSequenceNumber: 20,
			OrderSideIsBuyNotSell: boolPointer(true), OrderQuantity: 100, LimitPriceInMinorUnits: 100,
			RecordedAtTime: baseTime,
		},
		{
			EventType: audittrail.EventOrderRoutedToMatchingEngine, ClientAccountIdentifier: "acct-nofill",
			InstrumentSymbol: "NF-EQ", MatchingEngineOrderSequenceNumber: 21,
			OrderSideIsBuyNotSell: boolPointer(true), OrderQuantity: 100, LimitPriceInMinorUnits: 110,
			RecordedAtTime: baseTime.Add(100 * time.Millisecond),
		},
		{
			EventType: audittrail.EventOrderRoutedToMatchingEngine, ClientAccountIdentifier: "acct-nofill",
			InstrumentSymbol: "NF-EQ", MatchingEngineOrderSequenceNumber: 22,
			OrderSideIsBuyNotSell: boolPointer(true), OrderQuantity: 100, LimitPriceInMinorUnits: 120,
			RecordedAtTime: baseTime.Add(200 * time.Millisecond),
		},
		// All three cancelled, but no opposite-side fill ever happened.
		{EventType: audittrail.EventOrderCancelled, InstrumentSymbol: "NF-EQ", MatchingEngineOrderSequenceNumber: 20, RecordedAtTime: baseTime.Add(300 * time.Millisecond)},
		{EventType: audittrail.EventOrderCancelled, InstrumentSymbol: "NF-EQ", MatchingEngineOrderSequenceNumber: 21, RecordedAtTime: baseTime.Add(350 * time.Millisecond)},
		{EventType: audittrail.EventOrderCancelled, InstrumentSymbol: "NF-EQ", MatchingEngineOrderSequenceNumber: 22, RecordedAtTime: baseTime.Add(400 * time.Millisecond)},
	}

	incidents := DetectLayering(entries, DefaultDetectorConfiguration())
	if len(incidents) != 0 {
		t.Fatalf("expected 0 layering incidents with no opposite-side fill, got %d", len(incidents))
	}
}

func TestDetectLayering_DoesNotFlagWhenOrdersAreNeverCancelled(t *testing.T) {
	baseTime := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	entries := []audittrail.Entry{
		{
			EventType: audittrail.EventOrderRoutedToMatchingEngine, ClientAccountIdentifier: "acct-genuine",
			InstrumentSymbol: "GEN-EQ", MatchingEngineOrderSequenceNumber: 30,
			OrderSideIsBuyNotSell: boolPointer(true), OrderQuantity: 100, LimitPriceInMinorUnits: 100,
			RecordedAtTime: baseTime,
		},
		{
			EventType: audittrail.EventOrderRoutedToMatchingEngine, ClientAccountIdentifier: "acct-genuine",
			InstrumentSymbol: "GEN-EQ", MatchingEngineOrderSequenceNumber: 31,
			OrderSideIsBuyNotSell: boolPointer(true), OrderQuantity: 100, LimitPriceInMinorUnits: 110,
			RecordedAtTime: baseTime.Add(100 * time.Millisecond),
		},
		{
			EventType: audittrail.EventOrderRoutedToMatchingEngine, ClientAccountIdentifier: "acct-genuine",
			InstrumentSymbol: "GEN-EQ", MatchingEngineOrderSequenceNumber: 32,
			OrderSideIsBuyNotSell: boolPointer(true), OrderQuantity: 100, LimitPriceInMinorUnits: 120,
			RecordedAtTime: baseTime.Add(200 * time.Millisecond),
		},
		{
			EventType:                      audittrail.EventOrderFilled,
			InstrumentSymbol:               "GEN-EQ",
			ClientAccountIdentifier:        "acct-genuine",
			BuyingClientAccountIdentifier:  "acct-other",
			SellingClientAccountIdentifier: "acct-genuine",
			ExecutedPriceInMinorUnits:      130,
			ExecutedQuantity:               50,
			RecordedAtTime:                 baseTime.Add(300 * time.Millisecond),
		},
		// None of the three buy orders get cancelled — they were genuine resting depth.
	}

	incidents := DetectLayering(entries, DefaultDetectorConfiguration())
	if len(incidents) != 0 {
		t.Fatalf("expected 0 layering incidents when nothing is ever cancelled, got %d", len(incidents))
	}
}

// --- Wash trades --------------------------------------------------------------

func TestDetectWashTrades_FlagsSameAccountOnBothSides(t *testing.T) {
	entries := []audittrail.Entry{
		{
			EventType:                         audittrail.EventOrderFilled,
			InstrumentSymbol:                  "WASH-EQ",
			MatchingEngineOrderSequenceNumber: 900,
			BuyingClientAccountIdentifier:     "acct-wash",
			SellingClientAccountIdentifier:    "acct-wash",
			ExecutedPriceInMinorUnits:         5000,
			ExecutedQuantity:                  25,
			RecordedAtTime:                    time.Now(),
		},
	}

	incidents := DetectWashTrades(entries)
	if len(incidents) != 1 {
		t.Fatalf("expected exactly 1 wash-trade incident, got %d", len(incidents))
	}
	if incidents[0].ClientAccountIdentifier != "acct-wash" {
		t.Fatalf("expected acct-wash, got %s", incidents[0].ClientAccountIdentifier)
	}
	if incidents[0].ExecutedQuantity != 25 || incidents[0].ExecutedPriceInMinorUnits != 5000 {
		t.Fatalf("unexpected evidence: %+v", incidents[0])
	}
}

func TestDetectWashTrades_DoesNotFlagTwoDifferentAccounts(t *testing.T) {
	entries := []audittrail.Entry{
		{
			EventType:                      audittrail.EventOrderFilled,
			InstrumentSymbol:               "NORMAL-EQ",
			BuyingClientAccountIdentifier:  "acct-buyer",
			SellingClientAccountIdentifier: "acct-seller",
			ExecutedPriceInMinorUnits:      5000,
			ExecutedQuantity:               25,
			RecordedAtTime:                 time.Now(),
		},
	}

	incidents := DetectWashTrades(entries)
	if len(incidents) != 0 {
		t.Fatalf("expected 0 wash-trade incidents for two different accounts, got %d", len(incidents))
	}
}

func TestDetectWashTrades_DoesNotFlagEntriesMissingAccountData(t *testing.T) {
	entries := []audittrail.Entry{
		{
			EventType:                 audittrail.EventOrderFilled,
			InstrumentSymbol:          "LEGACY-EQ",
			ExecutedPriceInMinorUnits: 5000,
			ExecutedQuantity:          25,
			DetailMessage:             "an entry appended before the structured Buying/SellingClientAccountIdentifier fields existed",
			RecordedAtTime:            time.Now(),
		},
	}

	incidents := DetectWashTrades(entries)
	if len(incidents) != 0 {
		t.Fatalf("expected 0 wash-trade incidents for an entry with no structured account data, got %d", len(incidents))
	}
}

// --- Replay ---------------------------------------------------------------

func TestReplayOrderSequenceNumber_ReturnsOnlyThatOrdersEventsInChronologicalOrder(t *testing.T) {
	baseTime := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	entries := []audittrail.Entry{
		{EventType: audittrail.EventOrderCancelled, InstrumentSymbol: "R-EQ", MatchingEngineOrderSequenceNumber: 1, RecordedAtTime: baseTime.Add(2 * time.Second)},
		{EventType: audittrail.EventOrderRoutedToMatchingEngine, InstrumentSymbol: "R-EQ", MatchingEngineOrderSequenceNumber: 1, RecordedAtTime: baseTime},
		{EventType: audittrail.EventOrderRoutedToMatchingEngine, InstrumentSymbol: "R-EQ", MatchingEngineOrderSequenceNumber: 2, RecordedAtTime: baseTime.Add(1 * time.Second)},
		{EventType: audittrail.EventOrderSubmitted, InstrumentSymbol: "R-EQ", RecordedAtTime: baseTime}, // not a replay-eligible event type
	}

	replay := ReplayOrderSequenceNumber(entries, "R-EQ", 1)
	if len(replay) != 2 {
		t.Fatalf("expected 2 replay events for seq 1, got %d: %+v", len(replay), replay)
	}
	if replay[0].EventType != audittrail.EventOrderRoutedToMatchingEngine || replay[1].EventType != audittrail.EventOrderCancelled {
		t.Fatalf("expected routed-then-cancelled order, got %+v", replay)
	}
}

// --- Engine integration -----------------------------------------------------

func TestRunAllDetectors_ComputesRealIncidentsFromSuppliedEntriesOnly(t *testing.T) {
	baseTime := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	entries := []audittrail.Entry{
		{
			EventType:                         audittrail.EventOrderFilled,
			InstrumentSymbol:                  "INT-EQ",
			MatchingEngineOrderSequenceNumber: 1000,
			ClientAccountIdentifier:           "acct-int",
			BuyingClientAccountIdentifier:     "acct-int",
			SellingClientAccountIdentifier:    "acct-int",
			ExecutedPriceInMinorUnits:         1000,
			ExecutedQuantity:                  1,
			RecordedAtTime:                    baseTime,
		},
	}

	engine := NewSurveillanceEngine(DefaultDetectorConfiguration())
	report := engine.RunAllDetectors("acct-int", entries, baseTime.Add(-time.Hour), baseTime.Add(time.Hour))

	if report.AccountIdentifier != "acct-int" {
		t.Fatalf("expected report scoped to acct-int, got %s", report.AccountIdentifier)
	}
	if len(report.WashTradeIncidents) != 1 {
		t.Fatalf("expected 1 wash trade incident from the engine, got %d", len(report.WashTradeIncidents))
	}
	if len(report.SpoofingIncidents) != 0 || len(report.LayeringIncidents) != 0 {
		t.Fatalf("expected no spurious spoofing/layering incidents, got %+v / %+v", report.SpoofingIncidents, report.LayeringIncidents)
	}
}

// TestScopeEntriesToAccount_IncludesCancellationsThatCarryNoAccountField
// is a regression test for a real bug found by exercising this package's
// HTTP endpoint end-to-end against a live matching-engine:
// audittrail.EventOrderCancelled entries carry no ClientAccountIdentifier
// at all (by design — see that event's doc comment), so a naive
// EntriesForAccount(accountId) filter silently drops every cancellation,
// which made DetectSpoofing/DetectLayering permanently blind to the
// account's own real cancellations when fed through RunAllDetectors.
func TestScopeEntriesToAccount_IncludesCancellationsThatCarryNoAccountField(t *testing.T) {
	baseTime := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	allEntries := []audittrail.Entry{
		{
			EventType:                         audittrail.EventOrderRoutedToMatchingEngine,
			ClientAccountIdentifier:           "acct-scope",
			InstrumentSymbol:                  "SCOPE-EQ",
			MatchingEngineOrderSequenceNumber: 55,
			OrderSideIsBuyNotSell:             boolPointer(true),
			OrderQuantity:                     1000,
			LimitPriceInMinorUnits:            50,
			RecordedAtTime:                    baseTime,
		},
		{
			// Deliberately carries NO ClientAccountIdentifier — this is
			// exactly how buildCancelOrderHandler really appends it.
			EventType:                         audittrail.EventOrderCancelled,
			InstrumentSymbol:                  "SCOPE-EQ",
			MatchingEngineOrderSequenceNumber: 55,
			RecordedAtTime:                    baseTime.Add(50 * time.Millisecond),
		},
		{
			// An unrelated account's cancellation of a DIFFERENT order —
			// must NOT leak into acct-scope's scoped view.
			EventType:                         audittrail.EventOrderCancelled,
			InstrumentSymbol:                  "SCOPE-EQ",
			MatchingEngineOrderSequenceNumber: 999,
			RecordedAtTime:                    baseTime.Add(60 * time.Millisecond),
		},
	}

	scoped := ScopeEntriesToAccount(allEntries, "acct-scope")
	if len(scoped) != 2 {
		t.Fatalf("expected 2 scoped entries (routed + this account's own cancel), got %d: %+v", len(scoped), scoped)
	}

	// And, critically, RunAllDetectors (fed the FULL entries, as
	// documented) must actually flag this as spoofing.
	engine := NewSurveillanceEngine(DefaultDetectorConfiguration())
	report := engine.RunAllDetectors("acct-scope", allEntries, baseTime.Add(-time.Hour), baseTime.Add(time.Hour))
	if len(report.SpoofingIncidents) != 1 {
		t.Fatalf("expected RunAllDetectors to flag the spoofing incident using the full entries, got %d incidents", len(report.SpoofingIncidents))
	}
}

func TestRunAllDetectors_WindowExcludesEntriesOutsideIt(t *testing.T) {
	baseTime := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	entries := []audittrail.Entry{
		{
			EventType:                      audittrail.EventOrderFilled,
			InstrumentSymbol:               "WIN-EQ",
			ClientAccountIdentifier:        "acct-win",
			BuyingClientAccountIdentifier:  "acct-win",
			SellingClientAccountIdentifier: "acct-win",
			ExecutedPriceInMinorUnits:      1000,
			ExecutedQuantity:               1,
			RecordedAtTime:                 baseTime.Add(-48 * time.Hour), // well outside the queried window
		},
	}

	engine := NewSurveillanceEngine(DefaultDetectorConfiguration())
	report := engine.RunAllDetectors("acct-win", entries, baseTime.Add(-time.Hour), baseTime.Add(time.Hour))

	if len(report.WashTradeIncidents) != 0 {
		t.Fatalf("expected the out-of-window wash trade to be excluded, got %d incidents", len(report.WashTradeIncidents))
	}
}
