// FEATURES.md §9: "Deterministic replay test harness (same input sequence
// -> same book state, always)". Builds directly on `writeAheadLog.rs` /
// `walBackedOrderBook.rs`: takes an arbitrary scripted sequence of
// order-book operations, runs it once through a live `WalBackedOrderBook`
// (which durably logs every mutating step as it goes), then takes the WAL
// file that run produced and replays it into a brand-new `OrderBookCore`,
// and asserts the two end states are IDENTICAL — not just "same visible
// depth," but order-for-order, price-level-for-price-level, via
// `fullBookStateSnapshotForTesting`.
//
// This module is `pub` (not just `#[cfg(test)]`) so it's a genuinely
// reusable harness — any future test anywhere in this crate can call
// `assertDeterministicReplayMatchesLiveBook` with its own scripted
// sequence. Its own test module below then uses it across several
// hand-built scenarios AND across many pseudo-randomly generated
// sequences, for the "run this across several different generated
// sequences, not just one lucky case" confidence the feature asks for.
//
// No property-testing crate (e.g. proptest) is a dependency of this crate
// today (see Cargo.toml — just serde/serde_json), and pulling one in for
// a single feature would be a disproportionately heavy addition to a
// crate this size. Instead, `generatePseudoRandomOperationSequence` below
// is a small hand-rolled deterministic PRNG (a splitmix64-style
// generator) seeded per test run — good enough to explore many distinct
// sequences reproducibly without a new dependency.

use std::path::Path;

use crate::orderTypes::{IncomingOrderRequest, OrderSide, OrderType};
use crate::walBackedOrderBook::WalBackedOrderBook;
use crate::writeAheadLog::{
    readAllEventRecordsFromWalFile, replayWalEventRecordsIntoFreshOrderBook,
};

/// One step in a scripted operation sequence. `CancelPreviousSubmission`
/// refers back to an earlier `Submit` by its 0-based position among ALL
/// `Submit` steps in the sequence so far — the actual assigned order id
/// (which can only be known once the order has actually been submitted)
/// is resolved at execution time, not generation time.
#[derive(Debug, Clone)]
pub enum ScriptedOperation {
    Submit(IncomingOrderRequest),
    CancelPreviousSubmission { submissionIndex: usize },
}

/// Executes `operations` in order against `walBackedOrderBook`, resolving
/// each `CancelPreviousSubmission` to the real id that submission was
/// assigned. Returns every id assigned, in submission order (useful for
/// callers/tests that want to assert on specific orders afterward).
pub fn executeScriptedOperations(
    walBackedOrderBook: &mut WalBackedOrderBook,
    operations: &[ScriptedOperation],
) -> Vec<u64> {
    let mut assignedOrderSequenceNumbers = Vec::new();

    for operation in operations {
        match operation {
            ScriptedOperation::Submit(orderRequest) => {
                let outcome = walBackedOrderBook
                    .submitIncomingOrder(orderRequest.clone())
                    .expect("WAL write should succeed against a fresh temp file in tests");
                assignedOrderSequenceNumbers.push(outcome.assignedOrderSequenceNumber);
            }
            ScriptedOperation::CancelPreviousSubmission { submissionIndex } => {
                let orderIdToCancel = assignedOrderSequenceNumbers[*submissionIndex];
                walBackedOrderBook
                    .cancelOrder(orderIdToCancel)
                    .expect("WAL write should succeed against a fresh temp file in tests");
            }
        }
    }

    assignedOrderSequenceNumbers
}

/// The reusable harness itself: runs `operations` through a live
/// WAL-backed book at `walFilePath` (always starting fresh), then
/// separately reads that same WAL file back and replays it into a
/// brand-new `OrderBookCore`, then asserts the two final states are
/// exactly equal. Panics (via `assert_eq!`) on mismatch, same as any
/// other test assertion — callers are expected to invoke this from within
/// `#[test]` functions.
pub fn assertDeterministicReplayMatchesLiveBook(
    instrumentSymbol: &str,
    walFilePath: &Path,
    operations: &[ScriptedOperation],
) {
    let mut liveWalBackedBook =
        WalBackedOrderBook::createFreshWithNewWalFile(instrumentSymbol, walFilePath)
            .expect("should create a fresh WAL-backed book");
    executeScriptedOperations(&mut liveWalBackedBook, operations);
    let liveFinalState = liveWalBackedBook.fullBookStateSnapshotForTesting();

    let eventRecords =
        readAllEventRecordsFromWalFile(walFilePath).expect("should read back the WAL just written");
    let (replayedOrderBook, _tradesProducedDuringReplay) =
        replayWalEventRecordsIntoFreshOrderBook(instrumentSymbol, &eventRecords);
    let replayedFinalState = replayedOrderBook.fullBookStateSnapshotForTesting();

    assert_eq!(
        liveFinalState,
        replayedFinalState,
        "WAL replay of {} operation(s) against {walFilePath:?} produced a DIFFERENT final book state than the live run — \
         replay is not deterministic",
        operations.len()
    );
}

/// A tiny, dependency-free, deterministic PRNG (splitmix64) — NOT
/// cryptographic, just needs to be fast and reproducible from a seed so a
/// failing test can be reported with the seed that reproduces it.
struct SplitMix64 {
    state: u64,
}

impl SplitMix64 {
    fn seeded(seed: u64) -> Self {
        SplitMix64 { state: seed }
    }

    fn nextU64(&mut self) -> u64 {
        self.state = self.state.wrapping_add(0x9E3779B97F4A7C15);
        let mut result = self.state;
        result = (result ^ (result >> 30)).wrapping_mul(0xBF58476D1CE4E5B9);
        result = (result ^ (result >> 27)).wrapping_mul(0x94D049BB133111EB);
        result ^ (result >> 31)
    }

    fn nextInRange(&mut self, exclusiveUpperBound: u64) -> u64 {
        self.nextU64() % exclusiveUpperBound
    }
}

/// Generates a pseudo-random but fully reproducible (given `seed`)
/// sequence of `operationCount` scripted operations: a mix of Limit,
/// Market, and stop orders on both sides across a handful of price
/// levels and accounts, plus occasional cancellations of earlier
/// submissions — deliberately varied enough to exercise crossing,
/// partial fills, resting, multi-level books, and stop-order arming/
/// triggering, all in one generated sequence.
pub fn generatePseudoRandomOperationSequence(
    seed: u64,
    operationCount: usize,
) -> Vec<ScriptedOperation> {
    let mut randomNumberGenerator = SplitMix64::seeded(seed);
    let mut operations = Vec::with_capacity(operationCount);
    let mut submissionCount = 0usize;

    let candidateAccountIds = ["acctA", "acctB", "acctC", "acctD"];
    let candidatePrices: [i64; 5] = [80, 90, 100, 110, 120];

    for _ in 0..operationCount {
        // ~15% chance of a cancel (only once at least one submission
        // exists to cancel).
        let shouldCancel = submissionCount > 0 && randomNumberGenerator.nextInRange(100) < 15;

        if shouldCancel {
            let submissionIndexToCancel =
                randomNumberGenerator.nextInRange(submissionCount as u64) as usize;
            operations.push(ScriptedOperation::CancelPreviousSubmission {
                submissionIndex: submissionIndexToCancel,
            });
            continue;
        }

        let orderSide = if randomNumberGenerator.nextInRange(2) == 0 {
            OrderSide::Buy
        } else {
            OrderSide::Sell
        };
        let orderTypeRoll = randomNumberGenerator.nextInRange(10);
        let orderType = match orderTypeRoll {
            0..=6 => OrderType::Limit,
            7..=8 => OrderType::Market,
            9 if orderSide == OrderSide::Buy => OrderType::StopLossLimit,
            _ => OrderType::StopLossMarket,
        };
        let clientAccountId = candidateAccountIds
            [randomNumberGenerator.nextInRange(candidateAccountIds.len() as u64) as usize];
        let priceIndex = randomNumberGenerator.nextInRange(candidatePrices.len() as u64) as usize;
        let limitPriceInMinorUnits = candidatePrices[priceIndex];
        let orderQuantity = 1 + randomNumberGenerator.nextInRange(20);

        let stopTriggerPriceInMinorUnits = match orderType {
            OrderType::StopLossLimit | OrderType::StopLossMarket => Some(
                candidatePrices
                    [randomNumberGenerator.nextInRange(candidatePrices.len() as u64) as usize],
            ),
            _ => None,
        };

        operations.push(ScriptedOperation::Submit(IncomingOrderRequest {
            clientAccountId: clientAccountId.to_string(),
            orderSide,
            orderType,
            limitPriceInMinorUnits,
            stopTriggerPriceInMinorUnits,
            orderQuantity,
            orderSequenceNumber: 0,
        }));
        submissionCount += 1;
    }

    operations
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::fs;
    use std::path::PathBuf;

    fn freshTempWalFilePathForTest(testName: &str) -> PathBuf {
        let mut walFilePath = std::env::temp_dir();
        walFilePath.push(format!(
            "matchingEngineDeterministicReplayHarnessTest-{testName}-{}.jsonl",
            std::process::id()
        ));
        let _ = fs::remove_file(&walFilePath);
        walFilePath
    }

    fn sampleLimitOrder(
        clientAccountId: &str,
        orderSide: OrderSide,
        price: i64,
        quantity: u64,
    ) -> IncomingOrderRequest {
        IncomingOrderRequest {
            clientAccountId: clientAccountId.to_string(),
            orderSide,
            orderType: OrderType::Limit,
            limitPriceInMinorUnits: price,
            stopTriggerPriceInMinorUnits: None,
            orderQuantity: quantity,
            orderSequenceNumber: 0,
        }
    }

    #[test]
    fn handWrittenSequenceOne_simpleRestAndCrossIsDeterministicUnderReplay() {
        let walFilePath = freshTempWalFilePathForTest("handWritten1");
        let operations = vec![
            ScriptedOperation::Submit(sampleLimitOrder("seller", OrderSide::Sell, 100, 10)),
            ScriptedOperation::Submit(sampleLimitOrder("buyer", OrderSide::Buy, 100, 4)),
        ];
        assertDeterministicReplayMatchesLiveBook("TEST", &walFilePath, &operations);
        fs::remove_file(&walFilePath).ok();
    }

    #[test]
    fn handWrittenSequenceTwo_cancelsInterleavedWithRestsAreDeterministicUnderReplay() {
        let walFilePath = freshTempWalFilePathForTest("handWritten2");
        let operations = vec![
            ScriptedOperation::Submit(sampleLimitOrder("buyer1", OrderSide::Buy, 90, 5)),
            ScriptedOperation::Submit(sampleLimitOrder("buyer2", OrderSide::Buy, 95, 3)),
            ScriptedOperation::CancelPreviousSubmission { submissionIndex: 0 },
            ScriptedOperation::Submit(sampleLimitOrder("seller1", OrderSide::Sell, 95, 2)),
            ScriptedOperation::Submit(sampleLimitOrder("buyer3", OrderSide::Buy, 90, 7)),
            ScriptedOperation::CancelPreviousSubmission { submissionIndex: 3 },
        ];
        assertDeterministicReplayMatchesLiveBook("TEST", &walFilePath, &operations);
        fs::remove_file(&walFilePath).ok();
    }

    #[test]
    fn handWrittenSequenceThree_multiLevelCrossingAndMarketOrdersAreDeterministicUnderReplay() {
        let walFilePath = freshTempWalFilePathForTest("handWritten3");
        let operations = vec![
            ScriptedOperation::Submit(sampleLimitOrder("seller1", OrderSide::Sell, 100, 5)),
            ScriptedOperation::Submit(sampleLimitOrder("seller2", OrderSide::Sell, 105, 5)),
            ScriptedOperation::Submit(sampleLimitOrder("seller3", OrderSide::Sell, 110, 5)),
            ScriptedOperation::Submit(IncomingOrderRequest {
                clientAccountId: "sweepBuyer".into(),
                orderSide: OrderSide::Buy,
                orderType: OrderType::Market,
                limitPriceInMinorUnits: 0,
                stopTriggerPriceInMinorUnits: None,
                orderQuantity: 12, // sweeps across all three levels, partially into the third
                orderSequenceNumber: 0,
            }),
        ];
        assertDeterministicReplayMatchesLiveBook("TEST", &walFilePath, &operations);
        fs::remove_file(&walFilePath).ok();
    }

    #[test]
    fn handWrittenSequenceFour_cascadingStopOrdersAreDeterministicUnderReplay() {
        let walFilePath = freshTempWalFilePathForTest("handWritten4");
        let operations = vec![
            ScriptedOperation::Submit(sampleLimitOrder("cheapSeller", OrderSide::Sell, 85, 3)),
            ScriptedOperation::Submit(IncomingOrderRequest {
                clientAccountId: "longHolder1".into(),
                orderSide: OrderSide::Sell,
                orderType: OrderType::StopLossMarket,
                limitPriceInMinorUnits: 0,
                stopTriggerPriceInMinorUnits: Some(90),
                orderQuantity: 5,
                orderSequenceNumber: 0,
            }),
            ScriptedOperation::Submit(IncomingOrderRequest {
                clientAccountId: "longHolder2".into(),
                orderSide: OrderSide::Sell,
                orderType: OrderType::StopLossLimit,
                limitPriceInMinorUnits: 70,
                stopTriggerPriceInMinorUnits: Some(80),
                orderQuantity: 4,
                orderSequenceNumber: 0,
            }),
            ScriptedOperation::Submit(sampleLimitOrder("dipBuyer", OrderSide::Buy, 85, 3)),
        ];
        assertDeterministicReplayMatchesLiveBook("TEST", &walFilePath, &operations);
        fs::remove_file(&walFilePath).ok();
    }

    #[test]
    fn handWrittenSequenceFive_emptySequenceIsTriviallyDeterministic() {
        let walFilePath = freshTempWalFilePathForTest("handWritten5");
        assertDeterministicReplayMatchesLiveBook("TEST", &walFilePath, &[]);
        fs::remove_file(&walFilePath).ok();
    }

    /// The property-based-style repetition: many distinct pseudo-random
    /// sequences (different seeds, different lengths), each independently
    /// asserted deterministic under replay. A failure here prints exactly
    /// which seed/length reproduces it.
    #[test]
    fn manyPseudoRandomSequencesAreAllDeterministicUnderReplay() {
        let seedsAndLengths: [(u64, usize); 12] = [
            (1, 10),
            (2, 25),
            (3, 50),
            (42, 40),
            (1337, 60),
            (0xDEADBEEF, 30),
            (7, 5),
            (99, 80),
            (123456789, 45),
            (2026, 35),
            (777, 15),
            (555_555, 70),
        ];

        for (seed, operationCount) in seedsAndLengths {
            let walFilePath =
                freshTempWalFilePathForTest(&format!("prng-seed{seed}-len{operationCount}"));
            let operations = generatePseudoRandomOperationSequence(seed, operationCount);
            assertDeterministicReplayMatchesLiveBook("TEST", &walFilePath, &operations);
            fs::remove_file(&walFilePath).ok();
        }
    }
}
