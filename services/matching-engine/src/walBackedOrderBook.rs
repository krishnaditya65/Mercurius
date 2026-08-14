// Wraps `OrderBookCore` with write-ahead logging, so every
// order-book-mutating call also durably records itself before the caller
// gets an acknowledgement back. `OrderBookCore` itself stays completely
// unaware of the WAL — it is (and per its own doc comment, must stay) a
// pure in-memory data structure with no I/O, which is what keeps it
// testable and (eventually) allocation-free on the hot path. This struct
// is the seam where durability gets bolted on, not the order book itself.
//
// ORDERING / DURABILITY GUARANTEE: for both `submitIncomingOrder` and
// `cancelOrder`, this struct mutates the in-memory `OrderBookCore` FIRST,
// then durably logs (fsync'd) what just happened, and only after that
// returns `Ok` to the caller. `main.rs` does not send its response back
// over the wire until it has that `Ok` — so a crash can never cause a
// client to observe an acknowledged order that the WAL doesn't know
// about. What this ordering does NOT protect against is a crash that
// happens between mutating memory and finishing the fsync: in that
// narrow window, the in-memory book is already updated but the disk isn't
// yet, and the whole process (and its unpersisted memory) is about to be
// gone anyway, so nothing is lost — the WAL, once recovered, faithfully
// reflects "no such order ever happened," which is correct, since it was
// also never acknowledged to anyone. See the README's "Known gaps"
// section for the one real edge case this doesn't fully cover (a
// multi-record append, e.g. an OrderAccepted followed by several
// TradeExecuted records, that fails partway through).

use std::io;
use std::path::{Path, PathBuf};
use std::time::{SystemTime, UNIX_EPOCH};

use crate::orderBookCore::{FullOrderBookStateSnapshotForTesting, OrderBookCore};
use crate::orderTypes::{IncomingOrderRequest, OrderStatusQueryResult, OrderSubmissionOutcome};
use crate::writeAheadLog::{
    WalEventRecord, WriteAheadLogWriter, readAllEventRecordsFromWalFile,
    replayWalEventRecordsIntoFreshOrderBook,
};

pub struct WalBackedOrderBook {
    orderBookCore: OrderBookCore,
    walWriter: WriteAheadLogWriter,
    walFilePath: PathBuf,
}

impl WalBackedOrderBook {
    /// Startup entry point: if `walFilePath` already exists and has at
    /// least one event in it, recovers by replaying it into a fresh book;
    /// otherwise starts a brand-new empty book and a brand-new WAL file at
    /// that path. This is what `main.rs` calls once, at process start.
    pub fn openRecoveringIfPresent(instrumentSymbol: &str, walFilePath: &Path) -> io::Result<Self> {
        let walFileHasExistingContent =
            walFilePath.exists() && std::fs::metadata(walFilePath)?.len() > 0;

        let orderBookCore = if walFileHasExistingContent {
            let existingEventRecords = readAllEventRecordsFromWalFile(walFilePath)?;
            let (recoveredOrderBook, _tradesReplayed) =
                replayWalEventRecordsIntoFreshOrderBook(instrumentSymbol, &existingEventRecords);
            eprintln!(
                "walBackedOrderBook: recovered {} event(s) from {walFilePath:?}",
                existingEventRecords.len()
            );
            recoveredOrderBook
        } else {
            OrderBookCore::newEmptyOrderBook(instrumentSymbol)
        };

        let walWriter = WriteAheadLogWriter::openForAppending(walFilePath)?;

        Ok(WalBackedOrderBook {
            orderBookCore,
            walWriter,
            walFilePath: walFilePath.to_path_buf(),
        })
    }

    /// Always starts a fresh empty book and a fresh WAL file at
    /// `walFilePath`, deleting any pre-existing file there first. Used by
    /// tests and by the deterministic-replay harness, which want a known
    /// clean starting point rather than recovery semantics.
    pub fn createFreshWithNewWalFile(
        instrumentSymbol: &str,
        walFilePath: &Path,
    ) -> io::Result<Self> {
        let _ = std::fs::remove_file(walFilePath);
        let walWriter = WriteAheadLogWriter::openForAppending(walFilePath)?;
        Ok(WalBackedOrderBook {
            orderBookCore: OrderBookCore::newEmptyOrderBook(instrumentSymbol),
            walWriter,
            walFilePath: walFilePath.to_path_buf(),
        })
    }

    /// Applies `incomingOrderRequest` to the live book, then durably logs
    /// an `OrderAccepted` record (plus one `TradeExecuted` record per
    /// trade produced) before returning. An `Err` here means the mutation
    /// already happened in memory but the WAL write did NOT durably
    /// complete — see the module doc comment above for why that's still
    /// safe as long as the caller (correctly) withholds acknowledgement
    /// to the network on `Err`.
    pub fn submitIncomingOrder(
        &mut self,
        incomingOrderRequest: IncomingOrderRequest,
    ) -> io::Result<OrderSubmissionOutcome> {
        let orderAcceptedRecordSource = incomingOrderRequest.clone();
        let submissionOutcome = self.orderBookCore.submitIncomingOrder(incomingOrderRequest);

        let acceptedAtEpochMillis = currentEpochMillis();
        self.walWriter
            .appendEvent(&WalEventRecord::orderAcceptedFrom(
                submissionOutcome.assignedOrderSequenceNumber,
                &orderAcceptedRecordSource,
                acceptedAtEpochMillis,
            ))?;
        for tradeExecutionEvent in &submissionOutcome.tradeExecutionEvents {
            self.walWriter
                .appendEvent(&WalEventRecord::tradeExecutedFrom(
                    tradeExecutionEvent,
                    currentEpochMillis(),
                ))?;
        }

        Ok(submissionOutcome)
    }

    /// Applies the cancellation to the live book, then durably logs an
    /// `OrderCancelled` record before returning.
    pub fn cancelOrder(&mut self, orderSequenceNumberToCancel: u64) -> io::Result<bool> {
        let wasOrderCancelled = self.orderBookCore.cancelOrder(orderSequenceNumberToCancel);
        self.walWriter
            .appendEvent(&WalEventRecord::OrderCancelled {
                orderSequenceNumberToCancel,
                wasOrderCancelled,
                epochMillis: currentEpochMillis(),
            })?;
        Ok(wasOrderCancelled)
    }

    // --- Read-only pass-throughs: queries never mutate the book, so they
    // never touch the WAL either. ---

    pub fn queryOrderStatus(&self, orderSequenceNumberToQuery: u64) -> OrderStatusQueryResult {
        self.orderBookCore
            .queryOrderStatus(orderSequenceNumberToQuery)
    }

    pub fn currentBookDepthSnapshot(&self) -> Vec<(bool, i64, u64)> {
        self.orderBookCore.currentBookDepthSnapshot()
    }

    pub fn pendingStopOrderCount(&self) -> usize {
        self.orderBookCore.pendingStopOrderCount()
    }

    pub fn fullBookStateSnapshotForTesting(&self) -> FullOrderBookStateSnapshotForTesting {
        self.orderBookCore.fullBookStateSnapshotForTesting()
    }

    pub fn walFilePath(&self) -> &Path {
        &self.walFilePath
    }
}

/// Wall-clock milliseconds since the Unix epoch, stamped onto every WAL
/// event as it's durably logged — see `WalEventRecord::OrderAccepted`'s
/// `epochMillis` doc comment for why this matters (FEATURES.md §20
/// "Historical DOM replay for a chosen instrument/time window"). Falls
/// back to `0` on a clock error (the same "unknown time" sentinel
/// documented on that field) rather than panicking — a durability-critical
/// WAL append must never fail just because the system clock is confused.
fn currentEpochMillis() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|duration| duration.as_millis() as u64)
        .unwrap_or(0)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::orderTypes::{OrderSide, OrderType};
    use std::fs;

    fn freshTempWalFilePathForTest(testName: &str) -> PathBuf {
        let mut walFilePath = std::env::temp_dir();
        walFilePath.push(format!(
            "matchingEngineWalBackedBookTest-{testName}-{}.jsonl",
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
    fn openRecoveringIfPresentStartsFreshWhenNoWalFileExistsYet() {
        let walFilePath = freshTempWalFilePathForTest("freshStart");
        let walBackedBook = WalBackedOrderBook::openRecoveringIfPresent("TEST", &walFilePath)
            .expect("should create fresh");
        assert!(walBackedBook.currentBookDepthSnapshot().is_empty());
        fs::remove_file(&walFilePath).ok();
    }

    #[test]
    fn simulatedCrashAndRestartRecoversIdenticalBookStateViaOpenRecoveringIfPresent() {
        let walFilePath = freshTempWalFilePathForTest("crashAndRestart");

        // "Before the crash": a live process builds up some book state.
        {
            let mut walBackedBook =
                WalBackedOrderBook::createFreshWithNewWalFile("TEST", &walFilePath)
                    .expect("create fresh");
            walBackedBook
                .submitIncomingOrder(sampleLimitOrder("seller", OrderSide::Sell, 100, 10))
                .unwrap();
            walBackedBook
                .submitIncomingOrder(sampleLimitOrder("buyer1", OrderSide::Buy, 100, 4))
                .unwrap();
            let restingBuyOutcome = walBackedBook
                .submitIncomingOrder(sampleLimitOrder("buyer2", OrderSide::Buy, 90, 3))
                .unwrap();
            walBackedBook
                .cancelOrder(restingBuyOutcome.assignedOrderSequenceNumber)
                .unwrap();
            // Process instance is dropped here — nothing more happens to
            // it, simulating an unclean crash right after the last
            // acknowledged WAL write.
        }

        // "After the crash": a brand-new process instance recovers from
        // the same file.
        let recoveredWalBackedBook =
            WalBackedOrderBook::openRecoveringIfPresent("TEST", &walFilePath)
                .expect("should recover");

        let recoveredSnapshot = recoveredWalBackedBook.fullBookStateSnapshotForTesting();
        // The 100-level sell had 10, 4 were bought — 6 should remain.
        assert_eq!(
            recoveredSnapshot.restingSellOrdersByPriceAscending[0].1[0].remainingQuantity,
            6
        );
        // buyer2's 90-level order was cancelled before the "crash" — must
        // not reappear.
        assert!(
            recoveredSnapshot
                .restingBuyOrdersByPriceDescending
                .is_empty()
        );

        fs::remove_file(&walFilePath).ok();
    }

    #[test]
    fn reopeningARecoveredBookContinuesAppendingToTheSameWalFileNotOverwritingIt() {
        let walFilePath = freshTempWalFilePathForTest("continueAppending");

        {
            let mut walBackedBook =
                WalBackedOrderBook::createFreshWithNewWalFile("TEST", &walFilePath)
                    .expect("create fresh");
            walBackedBook
                .submitIncomingOrder(sampleLimitOrder("buyer1", OrderSide::Buy, 90, 5))
                .unwrap();
        }

        {
            let mut recoveredBook =
                WalBackedOrderBook::openRecoveringIfPresent("TEST", &walFilePath).expect("recover");
            recoveredBook
                .submitIncomingOrder(sampleLimitOrder("buyer2", OrderSide::Buy, 95, 3))
                .unwrap();
        }

        // A third recovery must see BOTH orders — proving the second
        // instance appended rather than truncating/overwriting.
        let finalRecoveredBook = WalBackedOrderBook::openRecoveringIfPresent("TEST", &walFilePath)
            .expect("recover again");
        assert_eq!(
            finalRecoveredBook
                .fullBookStateSnapshotForTesting()
                .restingBuyOrdersByPriceDescending
                .len(),
            2
        );

        fs::remove_file(&walFilePath).ok();
    }
}
