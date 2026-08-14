// Event-sourced write-ahead log (WAL) for crash recovery — FEATURES.md §9
// "Event sourcing + WAL replay for crash recovery".
//
// FILE FORMAT: newline-delimited JSON (NDJSON) — one `WalEventRecord` per
// line, oldest first, appended only (never rewritten in place). Every
// `appendEvent` call does a `write_all` of the JSON line followed by
// `sync_all()` (a real fsync, not just a libc `write()` that might still
// be sitting in the OS page cache) before returning — so by the time a
// caller gets `Ok(())` back, that event has genuinely reached durable
// storage, not just this process's memory. This deliberately does NOT log
// intent before mutating the in-memory `OrderBookCore` (see
// `walBackedOrderBook.rs` for exactly where in the call sequence logging
// happens and why) — but it DOES guarantee the log entry is durable
// before the caller (`main.rs`) ever acknowledges the order back to the
// network, which is the durability guarantee that actually matters here.
//
// RECOVERY: `replayWalEventRecordsIntoFreshOrderBook` takes every event
// record, in file order, and re-issues it as a command
// (`OrderBookCore::submitIncomingOrder` / `OrderBookCore::cancelOrder`)
// against a brand-new, empty `OrderBookCore`. It does NOT replay
// `TradeExecuted` records directly — those are written to the WAL purely
// as a durable audit trail of what happened, but they are a deterministic
// BYPRODUCT of replaying the `OrderAccepted`/`OrderCancelled` commands
// that caused them (matching is a pure function of book state + the
// incoming command), so re-deriving them by re-running
// `submitIncomingOrder` is both correct and simpler than trying to force
// specific trades to happen. Tests below cross-check the trades produced
// during replay against the `TradeExecuted` records that were actually
// logged live, as an extra correctness signal beyond "the final book
// state matches."
//
// This intentionally logs COMMANDS (what was submitted / what was asked
// to be cancelled), not book-internal side effects like which specific
// resting order got partially filled — `OrderBookCore::submitIncomingOrder`
// is a deterministic function of (current book state, incoming command),
// and current book state is itself fully determined by every earlier
// command, so replaying the command sequence alone is sufficient to
// reconstruct identical final state, order-for-order.

use std::fs::{File, OpenOptions};
use std::io::{self, BufRead, BufReader, Write};
use std::path::Path;

use serde::{Deserialize, Serialize};

use crate::orderBookCore::OrderBookCore;
use crate::orderTypes::{IncomingOrderRequest, OrderSide, OrderType, TradeExecutionEvent};

/// One durably-logged event. `#[serde(tag = "eventType")]` gives each line
/// an explicit, human-greppable discriminant (e.g. `cat` the WAL file and
/// you can `grep '"eventType":"OrderAccepted"'`) rather than relying on
/// serde's untagged-enum field-shape guessing.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(tag = "eventType")]
pub enum WalEventRecord {
    /// A new order was accepted by the book (whether it ended up resting,
    /// fully filling, or parking as a pending stop order — replaying this
    /// through `submitIncomingOrder` reproduces whichever of those
    /// happened, deterministically). `assignedOrderSequenceNumber` is
    /// carried for audit/debugging convenience only — replay does not
    /// need it, because a fresh `OrderBookCore` allocates ids in the same
    /// order the commands are replayed in, so it naturally re-derives the
    /// same id without being told what it was.
    OrderAccepted {
        assignedOrderSequenceNumber: u64,
        clientAccountId: String,
        orderSide: OrderSide,
        orderType: OrderType,
        limitPriceInMinorUnits: i64,
        stopTriggerPriceInMinorUnits: Option<i64>,
        orderQuantity: u64,
        /// Wall-clock time (milliseconds since the Unix epoch) this event
        /// was durably logged, stamped by `WalBackedOrderBook` right
        /// before the append. Additive field (FEATURES.md §20 "Historical
        /// DOM replay for a chosen instrument/time window") — `#[serde(default)]`
        /// so a WAL file written before this field existed still reads
        /// back cleanly (as `0`, i.e. "unknown time," rather than a hard
        /// parse failure). Not needed for correctness of book-state
        /// replay (`replayWalEventRecordsIntoFreshOrderBook` never reads
        /// it) — it exists purely so a time-windowed DOM replay can know
        /// which snapshots fall inside the requested window.
        #[serde(default)]
        epochMillis: u64,
    },
    /// A cancellation was requested against `orderSequenceNumberToCancel`.
    /// `wasOrderCancelled` records whether it actually matched anything
    /// live at the time (audit only — replay re-derives this itself by
    /// calling `cancelOrder` again).
    OrderCancelled {
        orderSequenceNumberToCancel: u64,
        wasOrderCancelled: bool,
        /// See `OrderAccepted::epochMillis`.
        #[serde(default)]
        epochMillis: u64,
    },
    /// One executed trade, produced as a side effect of the most recently
    /// logged `OrderAccepted` (which may itself have cascaded through
    /// several triggered stop orders, in which case several
    /// `TradeExecuted` records follow one `OrderAccepted`). Audit trail
    /// only — never replayed directly, see module docs above.
    TradeExecuted {
        buyingClientAccountId: String,
        sellingClientAccountId: String,
        executedPriceInMinorUnits: i64,
        executedQuantity: u64,
        /// See `TradeExecutionEvent::isBuyAggressor` (FEATURES.md §20
        /// "Order-flow footprint charts"). `#[serde(default)]` for the
        /// same backward-compatibility reason as `epochMillis` below.
        #[serde(default)]
        isBuyAggressor: bool,
        /// See `OrderAccepted::epochMillis`.
        #[serde(default)]
        epochMillis: u64,
    },
}

impl WalEventRecord {
    pub fn orderAcceptedFrom(
        assignedOrderSequenceNumber: u64,
        order: &IncomingOrderRequest,
        epochMillis: u64,
    ) -> Self {
        WalEventRecord::OrderAccepted {
            assignedOrderSequenceNumber,
            clientAccountId: order.clientAccountId.clone(),
            orderSide: order.orderSide,
            orderType: order.orderType,
            limitPriceInMinorUnits: order.limitPriceInMinorUnits,
            stopTriggerPriceInMinorUnits: order.stopTriggerPriceInMinorUnits,
            orderQuantity: order.orderQuantity,
            epochMillis,
        }
    }

    pub fn tradeExecutedFrom(tradeExecutionEvent: &TradeExecutionEvent, epochMillis: u64) -> Self {
        WalEventRecord::TradeExecuted {
            buyingClientAccountId: tradeExecutionEvent.buyingClientAccountId.clone(),
            sellingClientAccountId: tradeExecutionEvent.sellingClientAccountId.clone(),
            executedPriceInMinorUnits: tradeExecutionEvent.executedPriceInMinorUnits,
            executedQuantity: tradeExecutionEvent.executedQuantity,
            isBuyAggressor: tradeExecutionEvent.isBuyAggressor,
            epochMillis,
        }
    }
}

/// Append-only, fsync-per-event writer. Nothing about this struct is
/// clever — one `File` opened in append mode, one line written and
/// fsynced per call. That simplicity is deliberate: this doesn't need to
/// be a fancy WAL library, it needs to be genuinely durable and easy to
/// reason about.
pub struct WriteAheadLogWriter {
    walFile: File,
}

impl WriteAheadLogWriter {
    /// Opens (creating if necessary) `walFilePath` for appending. Safe to
    /// call on a path that already has events in it — new events are
    /// appended after whatever is already there, never overwriting it.
    pub fn openForAppending(walFilePath: &Path) -> io::Result<Self> {
        let walFile = OpenOptions::new()
            .create(true)
            .append(true)
            .open(walFilePath)?;
        Ok(WriteAheadLogWriter { walFile })
    }

    /// Serializes `eventRecord` as one JSON line, writes it, flushes, and
    /// fsyncs (`sync_all`, which — unlike `sync_data` — also durably
    /// persists the file's new length, not just its bytes) before
    /// returning. An `Err` here means the event was NOT durably recorded
    /// — see `walBackedOrderBook.rs` for how callers must treat that.
    pub fn appendEvent(&mut self, eventRecord: &WalEventRecord) -> io::Result<()> {
        let mut serializedLine = serde_json::to_string(eventRecord)
            .map_err(|jsonError| io::Error::new(io::ErrorKind::InvalidData, jsonError))?;
        serializedLine.push('\n');
        self.walFile.write_all(serializedLine.as_bytes())?;
        self.walFile.flush()?;
        self.walFile.sync_all()?;
        Ok(())
    }
}

/// Reads every event record out of `walFilePath`, oldest first. Tolerates
/// exactly one specific form of corruption: a truncated/partial final
/// line, which is exactly what a crash mid-`write_all` (before the next
/// `sync_all` ever completed) would leave behind. Any parse failure on a
/// line that ISN'T the last line is treated as real corruption and
/// returned as an error — a torn write should only ever be possible at
/// the tail of the file, given every completed append is fsynced before
/// the next one starts.
pub fn readAllEventRecordsFromWalFile(walFilePath: &Path) -> io::Result<Vec<WalEventRecord>> {
    let walFile = File::open(walFilePath)?;
    let allLines: Vec<String> = BufReader::new(walFile).lines().collect::<io::Result<_>>()?;

    let mut eventRecords = Vec::with_capacity(allLines.len());
    let lastLineIndex = allLines.len().checked_sub(1);

    for (lineIndex, line) in allLines.into_iter().enumerate() {
        if line.trim().is_empty() {
            continue;
        }
        match serde_json::from_str::<WalEventRecord>(&line) {
            Ok(eventRecord) => eventRecords.push(eventRecord),
            Err(parseError) => {
                if Some(lineIndex) == lastLineIndex {
                    // Torn trailing write from a mid-append crash — the
                    // safe recovery behavior is to discard it (it was
                    // never acknowledged to any caller, since
                    // `appendEvent` never returned `Ok` for it) and
                    // recover everything durably committed before it.
                    eprintln!(
                        "writeAheadLog: discarding unparseable trailing WAL line \
                         (likely a torn write from a mid-append crash): {parseError}"
                    );
                } else {
                    return Err(io::Error::new(
                        io::ErrorKind::InvalidData,
                        format!(
                            "WAL corruption: unparseable event record on line {} of {walFilePath:?}: {parseError}",
                            lineIndex + 1
                        ),
                    ));
                }
            }
        }
    }

    Ok(eventRecords)
}

/// Replays `eventRecords`, in order, into a brand-new `OrderBookCore` for
/// `instrumentSymbol`, by re-issuing each `OrderAccepted` as a
/// `submitIncomingOrder` call and each `OrderCancelled` as a `cancelOrder`
/// call. `TradeExecuted` records are skipped (see module docs) but every
/// trade actually produced DURING replay is still collected and returned
/// alongside the reconstructed book, so callers/tests can cross-check it
/// against the `TradeExecuted` records that were logged live.
pub fn replayWalEventRecordsIntoFreshOrderBook(
    instrumentSymbol: &str,
    eventRecords: &[WalEventRecord],
) -> (OrderBookCore, Vec<TradeExecutionEvent>) {
    let mut reconstructedOrderBook = OrderBookCore::newEmptyOrderBook(instrumentSymbol);
    let mut tradeExecutionEventsProducedDuringReplay = Vec::new();

    for eventRecord in eventRecords {
        match eventRecord {
            WalEventRecord::OrderAccepted {
                clientAccountId,
                orderSide,
                orderType,
                limitPriceInMinorUnits,
                stopTriggerPriceInMinorUnits,
                orderQuantity,
                // Intentionally unused: a fresh book replaying commands in
                // the same order allocates the same id on its own — see
                // module docs.
                assignedOrderSequenceNumber: _,
                // Not needed for book-state replay — see the field's doc
                // comment. Used by `replayWalEventRecordsCollectingDepthSnapshotsInWindow`
                // below instead.
                epochMillis: _,
            } => {
                let replayedOrderRequest = IncomingOrderRequest {
                    clientAccountId: clientAccountId.clone(),
                    orderSide: *orderSide,
                    orderType: *orderType,
                    limitPriceInMinorUnits: *limitPriceInMinorUnits,
                    stopTriggerPriceInMinorUnits: *stopTriggerPriceInMinorUnits,
                    orderQuantity: *orderQuantity,
                    orderSequenceNumber: 0, // overwritten by submitIncomingOrder, same as live intake
                };
                let outcome = reconstructedOrderBook.submitIncomingOrder(replayedOrderRequest);
                tradeExecutionEventsProducedDuringReplay.extend(outcome.tradeExecutionEvents);
            }
            WalEventRecord::OrderCancelled {
                orderSequenceNumberToCancel,
                wasOrderCancelled: _,
                epochMillis: _,
            } => {
                reconstructedOrderBook.cancelOrder(*orderSequenceNumberToCancel);
            }
            WalEventRecord::TradeExecuted { .. } => {
                // Audit-only — a deterministic byproduct of the
                // OrderAccepted event that produced it, not itself
                // replayed.
            }
        }
    }

    (
        reconstructedOrderBook,
        tradeExecutionEventsProducedDuringReplay,
    )
}

/// One point-in-time order-book depth snapshot produced during a
/// time-windowed DOM (Depth Of Market) replay — FEATURES.md §20
/// "Historical DOM replay for a chosen instrument/time window". Genuinely
/// reuses the WAL + `replayWalEventRecordsIntoFreshOrderBook`'s
/// deterministic-replay machinery above rather than reinventing it: see
/// `replayWalEventRecordsCollectingDepthSnapshotsInWindow`.
#[derive(Debug, Clone, PartialEq, Serialize)]
pub struct DomReplaySnapshot {
    pub epochMillis: u64,
    /// Position of the WAL event that produced this snapshot, 0-based —
    /// a stable tie-breaker/ordering key for snapshots that share the
    /// same `epochMillis` (millisecond resolution can't always
    /// distinguish two events logged back-to-back).
    pub walEventIndex: usize,
    /// Best bid first (highest price first).
    pub bidLevelsBestFirst: Vec<(i64, u64)>,
    /// Best ask first (lowest price first).
    pub askLevelsBestFirst: Vec<(i64, u64)>,
}

/// Replays `eventRecords` from the very start of the WAL (book state
/// depends on the FULL history, not just the requested window — genuine
/// deterministic replay, not a fabricated animation seeded from nowhere),
/// exactly the same command-replay loop as
/// `replayWalEventRecordsIntoFreshOrderBook` uses, but additionally
/// captures a `DomReplaySnapshot` of `currentBookDepthSnapshot()` after
/// every mutating event (`OrderAccepted`/`OrderCancelled`) whose
/// `epochMillis` falls within `[startEpochMillis, endEpochMillis]`
/// (inclusive both ends). This is what lets a caller ask for "what did
/// the DOM look like between 10:00 and 10:05" without needing to replay
/// and inspect the whole WAL by hand.
///
/// A record with `epochMillis == 0` (i.e. logged before this field
/// existed, or never stamped) is never considered "in window" unless the
/// caller explicitly passes `startEpochMillis == 0` — same honest
/// treatment as any other unknown/default value in this codebase.
pub fn replayWalEventRecordsCollectingDepthSnapshotsInWindow(
    instrumentSymbol: &str,
    eventRecords: &[WalEventRecord],
    startEpochMillis: u64,
    endEpochMillis: u64,
) -> Vec<DomReplaySnapshot> {
    let mut reconstructedOrderBook = OrderBookCore::newEmptyOrderBook(instrumentSymbol);
    let mut snapshotsInWindow = Vec::new();

    for (walEventIndex, eventRecord) in eventRecords.iter().enumerate() {
        let eventEpochMillisOpt = match eventRecord {
            WalEventRecord::OrderAccepted {
                clientAccountId,
                orderSide,
                orderType,
                limitPriceInMinorUnits,
                stopTriggerPriceInMinorUnits,
                orderQuantity,
                assignedOrderSequenceNumber: _,
                epochMillis,
            } => {
                let replayedOrderRequest = IncomingOrderRequest {
                    clientAccountId: clientAccountId.clone(),
                    orderSide: *orderSide,
                    orderType: *orderType,
                    limitPriceInMinorUnits: *limitPriceInMinorUnits,
                    stopTriggerPriceInMinorUnits: *stopTriggerPriceInMinorUnits,
                    orderQuantity: *orderQuantity,
                    orderSequenceNumber: 0,
                };
                reconstructedOrderBook.submitIncomingOrder(replayedOrderRequest);
                Some(*epochMillis)
            }
            WalEventRecord::OrderCancelled {
                orderSequenceNumberToCancel,
                wasOrderCancelled: _,
                epochMillis,
            } => {
                reconstructedOrderBook.cancelOrder(*orderSequenceNumberToCancel);
                Some(*epochMillis)
            }
            WalEventRecord::TradeExecuted { .. } => None,
        };

        if let Some(eventEpochMillis) = eventEpochMillisOpt
            && eventEpochMillis >= startEpochMillis
            && eventEpochMillis <= endEpochMillis
        {
            let depthSnapshot = reconstructedOrderBook.currentBookDepthSnapshot();
            let mut bidLevelsBestFirst: Vec<(i64, u64)> = depthSnapshot
                .iter()
                .filter(|(isBidSide, _, _)| *isBidSide)
                .map(|(_, price, quantity)| (*price, *quantity))
                .collect();
            bidLevelsBestFirst.sort_by(|a, b| b.0.cmp(&a.0));
            let mut askLevelsBestFirst: Vec<(i64, u64)> = depthSnapshot
                .iter()
                .filter(|(isBidSide, _, _)| !*isBidSide)
                .map(|(_, price, quantity)| (*price, *quantity))
                .collect();
            askLevelsBestFirst.sort_by(|a, b| a.0.cmp(&b.0));

            snapshotsInWindow.push(DomReplaySnapshot {
                epochMillis: eventEpochMillis,
                walEventIndex,
                bidLevelsBestFirst,
                askLevelsBestFirst,
            });
        }
    }

    snapshotsInWindow
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::fs;

    /// Every test gets its own WAL file under the OS temp dir, named after
    /// the test, so parallel `cargo test` runs never collide with each
    /// other or with a real WAL a live `cargo run` might have written.
    fn freshTempWalFilePathForTest(testName: &str) -> std::path::PathBuf {
        let mut walFilePath = std::env::temp_dir();
        walFilePath.push(format!(
            "matchingEngineWalTest-{testName}-{}.jsonl",
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
    fn appendedEventRoundTripsThroughReadAllEventRecords() {
        let walFilePath = freshTempWalFilePathForTest("appendReadRoundTrip");

        let mut walWriter = WriteAheadLogWriter::openForAppending(&walFilePath)
            .expect("should open a fresh WAL file");
        let orderAcceptedRecord = WalEventRecord::OrderAccepted {
            assignedOrderSequenceNumber: 1,
            clientAccountId: "acct-1".into(),
            orderSide: OrderSide::Buy,
            orderType: OrderType::Limit,
            limitPriceInMinorUnits: 100,
            stopTriggerPriceInMinorUnits: None,
            orderQuantity: 5,
            epochMillis: 1_000,
        };
        walWriter
            .appendEvent(&orderAcceptedRecord)
            .expect("append should succeed");
        walWriter
            .appendEvent(&WalEventRecord::OrderCancelled {
                orderSequenceNumberToCancel: 1,
                wasOrderCancelled: true,
                epochMillis: 2_000,
            })
            .expect("append should succeed");

        let readBackRecords =
            readAllEventRecordsFromWalFile(&walFilePath).expect("read should succeed");
        assert_eq!(readBackRecords.len(), 2);
        assert_eq!(readBackRecords[0], orderAcceptedRecord);
        assert_eq!(
            readBackRecords[1],
            WalEventRecord::OrderCancelled {
                orderSequenceNumberToCancel: 1,
                wasOrderCancelled: true,
                epochMillis: 2_000,
            }
        );

        fs::remove_file(&walFilePath).ok();
    }

    #[test]
    fn replayOfASimpleCrossReproducesIdenticalFinalBookState() {
        let walFilePath = freshTempWalFilePathForTest("replaySimpleCross");
        let mut walWriter = WriteAheadLogWriter::openForAppending(&walFilePath).expect("open");

        let mut liveOrderBook = OrderBookCore::newEmptyOrderBook("TEST");

        let sellOrder = sampleLimitOrder("seller", OrderSide::Sell, 100, 10);
        let sellOutcome = liveOrderBook.submitIncomingOrder(sellOrder.clone());
        walWriter
            .appendEvent(&WalEventRecord::orderAcceptedFrom(
                sellOutcome.assignedOrderSequenceNumber,
                &sellOrder,
                1_000,
            ))
            .unwrap();

        let buyOrder = sampleLimitOrder("buyer", OrderSide::Buy, 100, 4);
        let buyOutcome = liveOrderBook.submitIncomingOrder(buyOrder.clone());
        walWriter
            .appendEvent(&WalEventRecord::orderAcceptedFrom(
                buyOutcome.assignedOrderSequenceNumber,
                &buyOrder,
                1_000,
            ))
            .unwrap();
        for tradeEvent in &buyOutcome.tradeExecutionEvents {
            walWriter
                .appendEvent(&WalEventRecord::tradeExecutedFrom(tradeEvent, 1_000))
                .unwrap();
        }

        let liveFinalState = liveOrderBook.fullBookStateSnapshotForTesting();

        let eventRecords = readAllEventRecordsFromWalFile(&walFilePath).expect("read");
        let (replayedOrderBook, replayedTrades) =
            replayWalEventRecordsIntoFreshOrderBook("TEST", &eventRecords);
        let replayedFinalState = replayedOrderBook.fullBookStateSnapshotForTesting();

        assert_eq!(liveFinalState, replayedFinalState);
        assert_eq!(replayedTrades.len(), 1);
        assert_eq!(replayedTrades[0].executedQuantity, 4);
        assert_eq!(replayedTrades[0].executedPriceInMinorUnits, 100);

        fs::remove_file(&walFilePath).ok();
    }

    #[test]
    fn replayAfterAPartialFillReconstructsTheCorrectRemainingQuantity() {
        let walFilePath = freshTempWalFilePathForTest("replayPartialFill");
        let mut walWriter = WriteAheadLogWriter::openForAppending(&walFilePath).expect("open");
        let mut liveOrderBook = OrderBookCore::newEmptyOrderBook("TEST");

        let sellOrder = sampleLimitOrder("seller", OrderSide::Sell, 100, 10);
        let sellOutcome = liveOrderBook.submitIncomingOrder(sellOrder.clone());
        walWriter
            .appendEvent(&WalEventRecord::orderAcceptedFrom(
                sellOutcome.assignedOrderSequenceNumber,
                &sellOrder,
                1_000,
            ))
            .unwrap();

        // Only 4 of the resting 10 get filled — 6 must remain resting,
        // both live and after replay.
        let buyOrder = sampleLimitOrder("buyer", OrderSide::Buy, 100, 4);
        let buyOutcome = liveOrderBook.submitIncomingOrder(buyOrder.clone());
        walWriter
            .appendEvent(&WalEventRecord::orderAcceptedFrom(
                buyOutcome.assignedOrderSequenceNumber,
                &buyOrder,
                1_000,
            ))
            .unwrap();

        let liveFinalState = liveOrderBook.fullBookStateSnapshotForTesting();
        assert_eq!(
            liveFinalState.restingSellOrdersByPriceAscending[0].1[0].remainingQuantity,
            6
        );

        let eventRecords = readAllEventRecordsFromWalFile(&walFilePath).unwrap();
        let (replayedOrderBook, _) = replayWalEventRecordsIntoFreshOrderBook("TEST", &eventRecords);
        assert_eq!(
            replayedOrderBook.fullBookStateSnapshotForTesting(),
            liveFinalState
        );

        fs::remove_file(&walFilePath).ok();
    }

    #[test]
    fn replayAfterACancellationLeavesTheCancelledOrderGoneOnBothSides() {
        let walFilePath = freshTempWalFilePathForTest("replayAfterCancel");
        let mut walWriter = WriteAheadLogWriter::openForAppending(&walFilePath).expect("open");
        let mut liveOrderBook = OrderBookCore::newEmptyOrderBook("TEST");

        let restingOrder = sampleLimitOrder("buyer", OrderSide::Buy, 100, 5);
        let restingOutcome = liveOrderBook.submitIncomingOrder(restingOrder.clone());
        walWriter
            .appendEvent(&WalEventRecord::orderAcceptedFrom(
                restingOutcome.assignedOrderSequenceNumber,
                &restingOrder,
                1_000,
            ))
            .unwrap();

        let wasCancelled = liveOrderBook.cancelOrder(restingOutcome.assignedOrderSequenceNumber);
        assert!(wasCancelled);
        walWriter
            .appendEvent(&WalEventRecord::OrderCancelled {
                orderSequenceNumberToCancel: restingOutcome.assignedOrderSequenceNumber,
                wasOrderCancelled: wasCancelled,
                epochMillis: 2_000,
            })
            .unwrap();

        let liveFinalState = liveOrderBook.fullBookStateSnapshotForTesting();
        assert!(liveFinalState.restingBuyOrdersByPriceDescending.is_empty());

        let eventRecords = readAllEventRecordsFromWalFile(&walFilePath).unwrap();
        let (replayedOrderBook, _) = replayWalEventRecordsIntoFreshOrderBook("TEST", &eventRecords);
        assert_eq!(
            replayedOrderBook.fullBookStateSnapshotForTesting(),
            liveFinalState
        );

        fs::remove_file(&walFilePath).ok();
    }

    #[test]
    fn replayReconstructsMultiplePriceLevelsOnBothSidesInOrder() {
        let walFilePath = freshTempWalFilePathForTest("replayMultiLevel");
        let mut walWriter = WriteAheadLogWriter::openForAppending(&walFilePath).expect("open");
        let mut liveOrderBook = OrderBookCore::newEmptyOrderBook("TEST");

        let restingOrders = [
            sampleLimitOrder("buyer1", OrderSide::Buy, 90, 5),
            sampleLimitOrder("buyer2", OrderSide::Buy, 95, 3),
            sampleLimitOrder("buyer3", OrderSide::Buy, 90, 2), // second order at the SAME price 90 — FIFO within level
            sampleLimitOrder("seller1", OrderSide::Sell, 110, 4),
            sampleLimitOrder("seller2", OrderSide::Sell, 105, 6),
        ];

        for order in &restingOrders {
            let outcome = liveOrderBook.submitIncomingOrder(order.clone());
            walWriter
                .appendEvent(&WalEventRecord::orderAcceptedFrom(
                    outcome.assignedOrderSequenceNumber,
                    order,
                    1_000,
                ))
                .unwrap();
        }

        let liveFinalState = liveOrderBook.fullBookStateSnapshotForTesting();
        // Sanity: 2 distinct buy price levels, 2 distinct sell price
        // levels, and the 90-level has both buyer1 and buyer3 in FIFO
        // order (buyer1 first, since it was submitted first).
        assert_eq!(liveFinalState.restingBuyOrdersByPriceDescending.len(), 2);
        assert_eq!(liveFinalState.restingSellOrdersByPriceAscending.len(), 2);
        let level90 = liveFinalState
            .restingBuyOrdersByPriceDescending
            .iter()
            .find(|(price, _)| *price == 90)
            .unwrap();
        assert_eq!(level90.1.len(), 2);
        assert_eq!(level90.1[0].clientAccountId, "buyer1");
        assert_eq!(level90.1[1].clientAccountId, "buyer3");

        let eventRecords = readAllEventRecordsFromWalFile(&walFilePath).unwrap();
        let (replayedOrderBook, _) = replayWalEventRecordsIntoFreshOrderBook("TEST", &eventRecords);
        assert_eq!(
            replayedOrderBook.fullBookStateSnapshotForTesting(),
            liveFinalState
        );

        fs::remove_file(&walFilePath).ok();
    }

    #[test]
    fn replayReconstructsAStillPendingStopOrderIdentically() {
        let walFilePath = freshTempWalFilePathForTest("replayPendingStop");
        let mut walWriter = WriteAheadLogWriter::openForAppending(&walFilePath).expect("open");
        let mut liveOrderBook = OrderBookCore::newEmptyOrderBook("TEST");

        let stopOrder = IncomingOrderRequest {
            clientAccountId: "longHolder".into(),
            orderSide: OrderSide::Sell,
            orderType: OrderType::StopLossMarket,
            limitPriceInMinorUnits: 0,
            stopTriggerPriceInMinorUnits: Some(90),
            orderQuantity: 5,
            orderSequenceNumber: 0,
        };
        let outcome = liveOrderBook.submitIncomingOrder(stopOrder.clone());
        walWriter
            .appendEvent(&WalEventRecord::orderAcceptedFrom(
                outcome.assignedOrderSequenceNumber,
                &stopOrder,
                1_000,
            ))
            .unwrap();

        let liveFinalState = liveOrderBook.fullBookStateSnapshotForTesting();
        assert_eq!(liveFinalState.pendingStopOrders.len(), 1);

        let eventRecords = readAllEventRecordsFromWalFile(&walFilePath).unwrap();
        let (replayedOrderBook, _) = replayWalEventRecordsIntoFreshOrderBook("TEST", &eventRecords);
        assert_eq!(
            replayedOrderBook.fullBookStateSnapshotForTesting(),
            liveFinalState
        );

        fs::remove_file(&walFilePath).ok();
    }

    #[test]
    fn aTruncatedTrailingLineFromASimulatedMidAppendCrashIsDiscardedNotFatal() {
        let walFilePath = freshTempWalFilePathForTest("truncatedTail");
        let mut walWriter = WriteAheadLogWriter::openForAppending(&walFilePath).expect("open");

        let goodRecord = WalEventRecord::OrderAccepted {
            assignedOrderSequenceNumber: 1,
            clientAccountId: "acct-1".into(),
            orderSide: OrderSide::Buy,
            orderType: OrderType::Limit,
            limitPriceInMinorUnits: 100,
            stopTriggerPriceInMinorUnits: None,
            orderQuantity: 5,
            epochMillis: 1_000,
        };
        walWriter.appendEvent(&goodRecord).unwrap();

        // Simulate a crash mid-write of the NEXT line: append a torn,
        // syntactically-invalid partial JSON fragment with no trailing
        // newline, exactly what an interrupted `write_all` could leave
        // behind on disk.
        {
            let mut rawFile = OpenOptions::new().append(true).open(&walFilePath).unwrap();
            rawFile
                .write_all(b"{\"eventType\":\"OrderAccepted\",\"assignedOrderSeq")
                .unwrap();
            rawFile.sync_all().unwrap();
        }

        let recoveredRecords = readAllEventRecordsFromWalFile(&walFilePath)
            .expect("should recover despite the torn tail");
        assert_eq!(recoveredRecords, vec![goodRecord]);

        fs::remove_file(&walFilePath).ok();
    }

    #[test]
    fn corruptionInTheMiddleOfTheFileIsARealErrorNotSilentlySkipped() {
        let walFilePath = freshTempWalFilePathForTest("midFileCorruption");
        fs::write(&walFilePath, "not valid json at all\n{\"eventType\":\"OrderCancelled\",\"orderSequenceNumberToCancel\":1,\"wasOrderCancelled\":true}\n").unwrap();

        let result = readAllEventRecordsFromWalFile(&walFilePath);
        assert!(
            result.is_err(),
            "corruption that isn't at the very tail must not be silently swallowed"
        );

        fs::remove_file(&walFilePath).ok();
    }

    // --- replayWalEventRecordsCollectingDepthSnapshotsInWindow (FEATURES.md
    // §20 "Historical DOM replay for a chosen instrument/time window") ---
    // These construct `WalEventRecord`s directly in memory (no WAL file
    // needed — the function under test operates purely on the event slice)
    // and hand-verify the exact resulting depth snapshots.

    fn orderAcceptedRecordAt(
        assignedOrderSequenceNumber: u64,
        clientAccountId: &str,
        orderSide: OrderSide,
        price: i64,
        quantity: u64,
        epochMillis: u64,
    ) -> WalEventRecord {
        WalEventRecord::OrderAccepted {
            assignedOrderSequenceNumber,
            clientAccountId: clientAccountId.to_string(),
            orderSide,
            orderType: OrderType::Limit,
            limitPriceInMinorUnits: price,
            stopTriggerPriceInMinorUnits: None,
            orderQuantity: quantity,
            epochMillis,
        }
    }

    #[test]
    fn emptyEventListReturnsNoSnapshots() {
        let snapshots =
            replayWalEventRecordsCollectingDepthSnapshotsInWindow("TEST", &[], 0, u64::MAX);
        assert!(snapshots.is_empty());
    }

    #[test]
    fn singleOrderAcceptedInsideWindowProducesOneSnapshotWithThatLevel() {
        let events = vec![orderAcceptedRecordAt(
            1,
            "buyer",
            OrderSide::Buy,
            100,
            5,
            1_000,
        )];
        let snapshots =
            replayWalEventRecordsCollectingDepthSnapshotsInWindow("TEST", &events, 500, 1_500);

        assert_eq!(snapshots.len(), 1);
        assert_eq!(snapshots[0].epochMillis, 1_000);
        assert_eq!(snapshots[0].walEventIndex, 0);
        assert_eq!(snapshots[0].bidLevelsBestFirst, vec![(100, 5)]);
        assert!(snapshots[0].askLevelsBestFirst.is_empty());
    }

    #[test]
    fn eventOutsideTheRequestedWindowProducesNoSnapshot() {
        let events = vec![orderAcceptedRecordAt(
            1,
            "buyer",
            OrderSide::Buy,
            100,
            5,
            5_000,
        )];
        let snapshots =
            replayWalEventRecordsCollectingDepthSnapshotsInWindow("TEST", &events, 0, 1_000);
        assert!(snapshots.is_empty());
    }

    #[test]
    fn windowBoundsAreInclusiveOnBothEnds() {
        let events = vec![
            orderAcceptedRecordAt(1, "buyer1", OrderSide::Buy, 100, 5, 1_000),
            orderAcceptedRecordAt(2, "buyer2", OrderSide::Buy, 101, 5, 2_000),
        ];
        let exactBoundarySnapshots =
            replayWalEventRecordsCollectingDepthSnapshotsInWindow("TEST", &events, 1_000, 2_000);
        assert_eq!(exactBoundarySnapshots.len(), 2);

        let strictlyInsideSnapshots =
            replayWalEventRecordsCollectingDepthSnapshotsInWindow("TEST", &events, 1_001, 1_999);
        assert!(strictlyInsideSnapshots.is_empty());
    }

    #[test]
    fn bookStateBeforeTheWindowStillAffectsInWindowSnapshotsFullReplayNotJustTheWindow() {
        // A resting order placed BEFORE the window (epochMillis=500) must
        // still show up in a snapshot taken from an event INSIDE the
        // window (epochMillis=1_500) — genuine full-history replay, not a
        // truncated replay that starts only at the window boundary.
        let events = vec![
            orderAcceptedRecordAt(1, "buyer1", OrderSide::Buy, 100, 5, 500),
            orderAcceptedRecordAt(2, "buyer2", OrderSide::Buy, 101, 3, 1_500),
        ];
        let snapshots =
            replayWalEventRecordsCollectingDepthSnapshotsInWindow("TEST", &events, 1_000, 2_000);

        assert_eq!(snapshots.len(), 1);
        // Both the pre-window resting order (100,5) AND the in-window one
        // (101,3) are present — best bid (101) first.
        assert_eq!(snapshots[0].bidLevelsBestFirst, vec![(101, 3), (100, 5)]);
    }

    #[test]
    fn multipleEventsInsideWindowEachProduceASnapshotInChronologicalOrder() {
        let events = vec![
            orderAcceptedRecordAt(1, "buyer1", OrderSide::Buy, 100, 5, 1_000),
            orderAcceptedRecordAt(2, "buyer2", OrderSide::Buy, 101, 2, 1_100),
            orderAcceptedRecordAt(3, "buyer3", OrderSide::Buy, 102, 1, 1_200),
        ];
        let snapshots =
            replayWalEventRecordsCollectingDepthSnapshotsInWindow("TEST", &events, 0, u64::MAX);

        assert_eq!(snapshots.len(), 3);
        assert_eq!(
            snapshots.iter().map(|s| s.epochMillis).collect::<Vec<_>>(),
            vec![1_000, 1_100, 1_200]
        );
        assert_eq!(snapshots[2].bidLevelsBestFirst.len(), 3);
    }

    #[test]
    fn cancelledOrderDisappearsFromTheNextSnapshotsDepth() {
        let events = vec![
            orderAcceptedRecordAt(1, "buyer", OrderSide::Buy, 100, 5, 1_000),
            WalEventRecord::OrderCancelled {
                orderSequenceNumberToCancel: 1,
                wasOrderCancelled: true,
                epochMillis: 2_000,
            },
        ];
        let snapshots =
            replayWalEventRecordsCollectingDepthSnapshotsInWindow("TEST", &events, 0, u64::MAX);

        assert_eq!(snapshots.len(), 2);
        assert_eq!(snapshots[0].bidLevelsBestFirst, vec![(100, 5)]);
        assert!(snapshots[1].bidLevelsBestFirst.is_empty());
    }

    #[test]
    fn tradeExecutedEventsNeverProduceASnapshotOfTheirOwn() {
        let events = vec![
            orderAcceptedRecordAt(1, "seller", OrderSide::Sell, 100, 10, 1_000),
            orderAcceptedRecordAt(2, "buyer", OrderSide::Buy, 100, 4, 2_000),
            WalEventRecord::TradeExecuted {
                buyingClientAccountId: "buyer".into(),
                sellingClientAccountId: "seller".into(),
                executedPriceInMinorUnits: 100,
                executedQuantity: 4,
                isBuyAggressor: true,
                epochMillis: 2_000,
            },
        ];
        let snapshots =
            replayWalEventRecordsCollectingDepthSnapshotsInWindow("TEST", &events, 0, u64::MAX);

        // Two OrderAccepted events => exactly two snapshots, even though
        // three events total were replayed.
        assert_eq!(snapshots.len(), 2);
        // After the cross, only the remaining 6 on the sell side is left.
        assert_eq!(snapshots[1].askLevelsBestFirst, vec![(100, 6)]);
    }

    #[test]
    fn bidLevelsAreSortedHighestFirstAndAskLevelsLowestFirst() {
        let events = vec![
            orderAcceptedRecordAt(1, "buyer1", OrderSide::Buy, 99, 1, 1_000),
            orderAcceptedRecordAt(2, "buyer2", OrderSide::Buy, 101, 1, 1_000),
            orderAcceptedRecordAt(3, "buyer3", OrderSide::Buy, 100, 1, 1_000),
            orderAcceptedRecordAt(4, "seller1", OrderSide::Sell, 205, 1, 1_000),
            orderAcceptedRecordAt(5, "seller2", OrderSide::Sell, 203, 1, 1_000),
            orderAcceptedRecordAt(6, "seller3", OrderSide::Sell, 204, 1, 1_000),
        ];
        let snapshots =
            replayWalEventRecordsCollectingDepthSnapshotsInWindow("TEST", &events, 1_000, 1_000);

        let lastSnapshot = snapshots.last().unwrap();
        assert_eq!(
            lastSnapshot.bidLevelsBestFirst,
            vec![(101, 1), (100, 1), (99, 1)]
        );
        assert_eq!(
            lastSnapshot.askLevelsBestFirst,
            vec![(203, 1), (204, 1), (205, 1)]
        );
    }

    #[test]
    fn zeroEpochMillisRecordsAreExcludedUnlessTheWindowExplicitlyStartsAtZero() {
        // epochMillis=0 is the documented "unknown time" sentinel for a
        // record logged before this field existed — it should NOT show up
        // in an open-ended window that doesn't explicitly ask for time 0.
        let events = vec![orderAcceptedRecordAt(1, "buyer", OrderSide::Buy, 100, 5, 0)];

        let windowStartingAtOne =
            replayWalEventRecordsCollectingDepthSnapshotsInWindow("TEST", &events, 1, u64::MAX);
        assert!(windowStartingAtOne.is_empty());

        let windowStartingAtZero =
            replayWalEventRecordsCollectingDepthSnapshotsInWindow("TEST", &events, 0, u64::MAX);
        assert_eq!(windowStartingAtZero.len(), 1);
    }

    #[test]
    fn walEventIndexReflectsPositionInTheFullEventListNotJustInWindowEvents() {
        let events = vec![
            orderAcceptedRecordAt(1, "buyer1", OrderSide::Buy, 100, 5, 500), // index 0, outside window
            orderAcceptedRecordAt(2, "buyer2", OrderSide::Buy, 101, 5, 1_500), // index 1, inside window
        ];
        let snapshots =
            replayWalEventRecordsCollectingDepthSnapshotsInWindow("TEST", &events, 1_000, 2_000);

        assert_eq!(snapshots.len(), 1);
        assert_eq!(snapshots[0].walEventIndex, 1);
    }
}
