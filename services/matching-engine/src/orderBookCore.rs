use std::collections::BTreeMap;
use std::collections::VecDeque;

use crate::orderTypes::{
    IncomingOrderRequest, OrderSide, OrderStatusQueryResult, OrderSubmissionOutcome, OrderType,
    RestingLimitOrder, TradeExecutionEvent,
};

/// Single-instrument, single-threaded, price-time-priority limit order book.
///
/// Per ARCHITECTURE.md §3.1 (single-writer principle): exactly one thread
/// should ever own an instance of this struct. `main.rs` now enforces this
/// at the type level — a `WalBackedOrderBook` (which wraps one of these)
/// is moved into the dedicated matching-core thread's closure and never
/// named again on the thread that spawned it, with a lock-free SPSC ring
/// buffer (`src/lockFreeSpscRingBuffer.rs`) as the only channel in or out
/// (see the README's "Lock-free ring buffer ingress/egress" section). The
/// internal data structures here are, independently, already lock-free BY
/// DESIGN: there is no interior mutability or locking in this struct at
/// all. Correctness comes from single-threaded ownership, not from
/// synchronization primitives.
pub struct OrderBookCore {
    instrumentSymbol: String,
    nextOrderSequenceNumber: u64,

    // Price -> FIFO queue of resting orders at that price (time priority
    // within a price level). BTreeMap keeps price levels sorted, which is
    // fine for this skeleton; the real Tier 0 build should replace this
    // with a tick-indexed array per ARCHITECTURE.md §3.3 (cache-friendly,
    // no pointer-chasing through a generic tree implementation).
    restingBuyOrdersByPriceDescending: BTreeMap<i64, VecDeque<RestingLimitOrder>>,
    restingSellOrdersByPriceAscending: BTreeMap<i64, VecDeque<RestingLimitOrder>>,

    /// Stop-loss orders (`StopLossLimit`/`StopLossMarket`) that have not
    /// yet triggered. Deliberately NOT part of either resting-order map
    /// above: an armed-but-untriggered stop order must be invisible to
    /// `currentBookDepthSnapshot` and to matching — it isn't a live bid or
    /// ask, it's a standing instruction to create one later. Checked, in
    /// submission order, every time a trade updates
    /// `lastTradedPriceInMinorUnits`.
    pendingStopOrders: Vec<IncomingOrderRequest>,

    /// The price of the most recent trade on this instrument. `None`
    /// until the first trade ever executes. This is what arms stop
    /// orders — real venues sometimes trigger off a separate reference/
    /// index price instead of last-traded; last-traded is the simpler,
    /// more common retail convention and what this skeleton implements.
    lastTradedPriceInMinorUnits: Option<i64>,
}

/// See `OrderBookCore::fullBookStateSnapshotForTesting`. Test-only
/// equality-comparable dump of the whole book, id-for-id and
/// price-level-for-price-level.
#[derive(Debug, Clone, PartialEq)]
pub struct FullOrderBookStateSnapshotForTesting {
    pub restingBuyOrdersByPriceDescending: Vec<(i64, Vec<RestingLimitOrder>)>,
    pub restingSellOrdersByPriceAscending: Vec<(i64, Vec<RestingLimitOrder>)>,
    pub pendingStopOrders: Vec<IncomingOrderRequest>,
    pub lastTradedPriceInMinorUnits: Option<i64>,
    pub nextOrderSequenceNumber: u64,
}

impl OrderBookCore {
    pub fn newEmptyOrderBook(instrumentSymbol: &str) -> Self {
        OrderBookCore {
            instrumentSymbol: instrumentSymbol.to_string(),
            nextOrderSequenceNumber: 1,
            restingBuyOrdersByPriceDescending: BTreeMap::new(),
            restingSellOrdersByPriceAscending: BTreeMap::new(),
            pendingStopOrders: Vec::new(),
            lastTradedPriceInMinorUnits: None,
        }
    }

    /// Submits one incoming order. `Limit`/`Market` orders match against
    /// the resting book immediately, as far as price allows, and rest any
    /// unfilled remainder. `StopLossLimit`/`StopLossMarket` orders never
    /// match immediately — they're parked in `pendingStopOrders` until a
    /// later trade's price crosses their trigger. Either way, after the
    /// order is handled, every pending stop order is re-checked against
    /// the (possibly just-updated) last traded price, and any that now
    /// qualify are triggered — which can itself produce trades that arm
    /// further stops, so this cascades until nothing new triggers.
    ///
    /// Returns every trade produced by this call, across the original
    /// order and any cascade of triggered stop orders, in the order they
    /// executed, plus the local order id this order was assigned —
    /// callers should hold onto that id if they might want to
    /// `cancelOrder` it later (a resting `Limit` remainder, or a
    /// still-armed stop order).
    ///
    /// TODO(real build): this must become allocation-free on the hot path
    /// (no String cloning, no Vec growth per call) per ARCHITECTURE.md §1
    /// tenet 2. The skeleton prioritizes readability of the matching
    /// algorithm over the hot-path constraints it will eventually need.
    pub fn submitIncomingOrder(
        &mut self,
        mut incomingOrderRequest: IncomingOrderRequest,
    ) -> OrderSubmissionOutcome {
        let assignedOrderSequenceNumber = self.allocateNextOrderSequenceNumber();
        incomingOrderRequest.orderSequenceNumber = assignedOrderSequenceNumber;

        let mut allTradeExecutionEvents = match incomingOrderRequest.orderType {
            OrderType::StopLossLimit | OrderType::StopLossMarket => {
                self.pendingStopOrders.push(incomingOrderRequest);
                Vec::new()
            }
            OrderType::Limit | OrderType::Market => {
                self.matchAndRecordLastPrice(incomingOrderRequest)
            }
        };

        allTradeExecutionEvents.extend(self.triggerAnyEligiblePendingStopOrders());

        OrderSubmissionOutcome {
            tradeExecutionEvents: allTradeExecutionEvents,
            assignedOrderSequenceNumber,
        }
    }

    /// Removes a resting `Limit` order or a still-armed (not yet
    /// triggered) stop order from the book by the id
    /// `submitIncomingOrder` returned when it was first submitted.
    /// Returns `true` if something was actually removed, `false` if no
    /// order with that id exists — either it never existed, it already
    /// fully filled, or (for a stop order) it already triggered and is
    /// now either filled or resting under the SAME id as a plain `Limit`
    /// order (still cancellable, just found in the resting maps instead
    /// of the pending pool).
    ///
    /// Linear scan over every price level on both sides, then over the
    /// pending-stop pool — fine for this skeleton's single-instrument
    /// scale; a real build would index resting orders by id directly
    /// (e.g. `HashMap<u64, (i64, OrderSide)>`) instead of searching.
    pub fn cancelOrder(&mut self, orderSequenceNumberToCancel: u64) -> bool {
        if Self::removeFromRestingMap(
            &mut self.restingBuyOrdersByPriceDescending,
            orderSequenceNumberToCancel,
        ) {
            return true;
        }
        if Self::removeFromRestingMap(
            &mut self.restingSellOrdersByPriceAscending,
            orderSequenceNumberToCancel,
        ) {
            return true;
        }

        let pendingIndex = self.pendingStopOrders.iter().position(|pendingOrder| {
            pendingOrder.orderSequenceNumber == orderSequenceNumberToCancel
        });
        if let Some(pendingIndex) = pendingIndex {
            self.pendingStopOrders.remove(pendingIndex);
            return true;
        }

        false
    }

    /// Read-only lookup of where an order currently stands: still resting
    /// (partially or fully) as a live `Limit` order, still armed as a
    /// pending stop order, or `NotFound` (never existed, fully filled,
    /// cancelled, or — for a stop order — triggered and then itself
    /// fully filled). Same linear-scan approach as `cancelOrder`, same
    /// "index by id in a real build" caveat.
    pub fn queryOrderStatus(&self, orderSequenceNumberToQuery: u64) -> OrderStatusQueryResult {
        if let Some((priceInMinorUnits, remainingQuantity)) = Self::findInRestingMap(
            &self.restingBuyOrdersByPriceDescending,
            orderSequenceNumberToQuery,
        ) {
            return OrderStatusQueryResult::RestingLimit {
                orderSide: OrderSide::Buy,
                limitPriceInMinorUnits: priceInMinorUnits,
                remainingQuantity,
            };
        }
        if let Some((priceInMinorUnits, remainingQuantity)) = Self::findInRestingMap(
            &self.restingSellOrdersByPriceAscending,
            orderSequenceNumberToQuery,
        ) {
            return OrderStatusQueryResult::RestingLimit {
                orderSide: OrderSide::Sell,
                limitPriceInMinorUnits: priceInMinorUnits,
                remainingQuantity,
            };
        }
        if let Some(pendingOrder) = self
            .pendingStopOrders
            .iter()
            .find(|pendingOrder| pendingOrder.orderSequenceNumber == orderSequenceNumberToQuery)
        {
            return OrderStatusQueryResult::PendingStop {
                orderSide: pendingOrder.orderSide,
                stopTriggerPriceInMinorUnits: pendingOrder
                    .stopTriggerPriceInMinorUnits
                    .expect("a pending stop order always carries a trigger price"),
                orderQuantity: pendingOrder.orderQuantity,
            };
        }

        OrderStatusQueryResult::NotFound
    }

    fn findInRestingMap(
        restingOrdersByPrice: &BTreeMap<i64, VecDeque<RestingLimitOrder>>,
        orderSequenceNumberToFind: u64,
    ) -> Option<(i64, u64)> {
        for (priceInMinorUnits, restingOrdersAtPrice) in restingOrdersByPrice {
            if let Some(foundOrder) = restingOrdersAtPrice
                .iter()
                .find(|order| order.restingOrderSequenceNumber == orderSequenceNumberToFind)
            {
                return Some((*priceInMinorUnits, foundOrder.remainingQuantity));
            }
        }
        None
    }

    fn removeFromRestingMap(
        restingOrdersByPrice: &mut BTreeMap<i64, VecDeque<RestingLimitOrder>>,
        orderSequenceNumberToCancel: u64,
    ) -> bool {
        let mut emptiedPriceLevel: Option<i64> = None;
        let mut wasRemoved = false;

        for (priceInMinorUnits, restingOrdersAtPrice) in restingOrdersByPrice.iter_mut() {
            let beforeLen = restingOrdersAtPrice.len();
            restingOrdersAtPrice
                .retain(|order| order.restingOrderSequenceNumber != orderSequenceNumberToCancel);
            if restingOrdersAtPrice.len() != beforeLen {
                wasRemoved = true;
                if restingOrdersAtPrice.is_empty() {
                    emptiedPriceLevel = Some(*priceInMinorUnits);
                }
                break;
            }
        }

        if let Some(emptiedPriceLevel) = emptiedPriceLevel {
            restingOrdersByPrice.remove(&emptiedPriceLevel);
        }

        wasRemoved
    }

    fn matchAndRecordLastPrice(&mut self, order: IncomingOrderRequest) -> Vec<TradeExecutionEvent> {
        let tradeExecutionEvents = match order.orderSide {
            OrderSide::Buy => self.matchIncomingBuyOrderAgainstRestingSellOrders(order),
            OrderSide::Sell => self.matchIncomingSellOrderAgainstRestingBuyOrders(order),
        };
        if let Some(mostRecentTrade) = tradeExecutionEvents.last() {
            self.lastTradedPriceInMinorUnits = Some(mostRecentTrade.executedPriceInMinorUnits);
        }
        tradeExecutionEvents
    }

    /// Repeatedly scans `pendingStopOrders` for the first one whose
    /// trigger condition is met at the current last traded price, converts
    /// it to a live `Limit`/`Market` order, and runs it through normal
    /// matching — looping until a full scan finds nothing left to trigger.
    /// Looping (rather than a single pass) is what makes cascades work: a
    /// triggered stop order's own trades can move the last price far
    /// enough to arm another stop order behind it.
    fn triggerAnyEligiblePendingStopOrders(&mut self) -> Vec<TradeExecutionEvent> {
        let mut allTradeExecutionEvents = Vec::new();

        loop {
            let Some(lastTradedPrice) = self.lastTradedPriceInMinorUnits else {
                break;
            };
            let triggeredIndex = self.pendingStopOrders.iter().position(|pendingOrder| {
                isStopOrderTriggeredAtPrice(pendingOrder, lastTradedPrice)
            });

            let Some(triggeredIndex) = triggeredIndex else {
                break;
            };

            let mut triggeredOrder = self.pendingStopOrders.remove(triggeredIndex);
            triggeredOrder.orderType = match triggeredOrder.orderType {
                OrderType::StopLossMarket => OrderType::Market,
                OrderType::StopLossLimit => OrderType::Limit,
                alreadyLive => alreadyLive,
            };

            allTradeExecutionEvents.extend(self.matchAndRecordLastPrice(triggeredOrder));
        }

        allTradeExecutionEvents
    }

    /// Every stop order currently armed but not yet triggered, exposed for
    /// `main.rs`/tests to inspect without giving out mutable access to the
    /// pool itself.
    pub fn pendingStopOrderCount(&self) -> usize {
        self.pendingStopOrders.len()
    }

    fn matchIncomingBuyOrderAgainstRestingSellOrders(
        &mut self,
        mut incomingBuyOrder: IncomingOrderRequest,
    ) -> Vec<TradeExecutionEvent> {
        let mut tradeExecutionEventsProduced = Vec::new();

        loop {
            let bestRestingSellPriceOpt = self
                .restingSellOrdersByPriceAscending
                .keys()
                .next()
                .copied();

            let bestRestingSellPrice = match bestRestingSellPriceOpt {
                Some(price)
                    if (incomingBuyOrder.orderType == OrderType::Market
                        || price <= incomingBuyOrder.limitPriceInMinorUnits)
                        && incomingBuyOrder.orderQuantity > 0 =>
                {
                    price
                }
                _ => break,
            };

            let restingOrdersAtBestPrice = self
                .restingSellOrdersByPriceAscending
                .get_mut(&bestRestingSellPrice)
                .expect("price key was just read from this same map");

            while incomingBuyOrder.orderQuantity > 0 {
                let Some(oldestRestingSellOrder) = restingOrdersAtBestPrice.front_mut() else {
                    break;
                };

                let executedQuantity = incomingBuyOrder
                    .orderQuantity
                    .min(oldestRestingSellOrder.remainingQuantity);

                tradeExecutionEventsProduced.push(TradeExecutionEvent {
                    buyingClientAccountId: incomingBuyOrder.clientAccountId.clone(),
                    sellingClientAccountId: oldestRestingSellOrder.clientAccountId.clone(),
                    executedPriceInMinorUnits: bestRestingSellPrice,
                    executedQuantity,
                });

                incomingBuyOrder.orderQuantity -= executedQuantity;
                oldestRestingSellOrder.remainingQuantity -= executedQuantity;

                if oldestRestingSellOrder.remainingQuantity == 0 {
                    restingOrdersAtBestPrice.pop_front();
                }
            }

            if restingOrdersAtBestPrice.is_empty() {
                self.restingSellOrdersByPriceAscending
                    .remove(&bestRestingSellPrice);
            }
        }

        // Market orders never rest: an unfilled remainder is simply
        // dropped (IOC-like behavior), never left sitting on the book at
        // no price. Real exchanges vary here (some reject the whole
        // order instead of partial-filling); this is a documented
        // simplification, not the final word on market-order semantics.
        if incomingBuyOrder.orderQuantity > 0 && incomingBuyOrder.orderType == OrderType::Limit {
            self.restRemainingBuyOrderOnBook(incomingBuyOrder);
        }

        tradeExecutionEventsProduced
    }

    fn matchIncomingSellOrderAgainstRestingBuyOrders(
        &mut self,
        mut incomingSellOrder: IncomingOrderRequest,
    ) -> Vec<TradeExecutionEvent> {
        let mut tradeExecutionEventsProduced = Vec::new();

        loop {
            let bestRestingBuyPriceOpt = self
                .restingBuyOrdersByPriceDescending
                .keys()
                .next_back()
                .copied();

            let bestRestingBuyPrice = match bestRestingBuyPriceOpt {
                Some(price)
                    if (incomingSellOrder.orderType == OrderType::Market
                        || price >= incomingSellOrder.limitPriceInMinorUnits)
                        && incomingSellOrder.orderQuantity > 0 =>
                {
                    price
                }
                _ => break,
            };

            let restingOrdersAtBestPrice = self
                .restingBuyOrdersByPriceDescending
                .get_mut(&bestRestingBuyPrice)
                .expect("price key was just read from this same map");

            while incomingSellOrder.orderQuantity > 0 {
                let Some(oldestRestingBuyOrder) = restingOrdersAtBestPrice.front_mut() else {
                    break;
                };

                let executedQuantity = incomingSellOrder
                    .orderQuantity
                    .min(oldestRestingBuyOrder.remainingQuantity);

                tradeExecutionEventsProduced.push(TradeExecutionEvent {
                    buyingClientAccountId: oldestRestingBuyOrder.clientAccountId.clone(),
                    sellingClientAccountId: incomingSellOrder.clientAccountId.clone(),
                    executedPriceInMinorUnits: bestRestingBuyPrice,
                    executedQuantity,
                });

                incomingSellOrder.orderQuantity -= executedQuantity;
                oldestRestingBuyOrder.remainingQuantity -= executedQuantity;

                if oldestRestingBuyOrder.remainingQuantity == 0 {
                    restingOrdersAtBestPrice.pop_front();
                }
            }

            if restingOrdersAtBestPrice.is_empty() {
                self.restingBuyOrdersByPriceDescending
                    .remove(&bestRestingBuyPrice);
            }
        }

        if incomingSellOrder.orderQuantity > 0 && incomingSellOrder.orderType == OrderType::Limit {
            self.restRemainingSellOrderOnBook(incomingSellOrder);
        }

        tradeExecutionEventsProduced
    }

    fn restRemainingBuyOrderOnBook(&mut self, remainingBuyOrder: IncomingOrderRequest) {
        // Reuse the id `submitIncomingOrder` already assigned at intake
        // (rather than allocating a new one here) so a client's
        // cancel-by-id request works whether or not the order rested,
        // and a triggered stop order keeps the same id it was armed
        // under.
        let restingLimitOrder = RestingLimitOrder {
            restingOrderSequenceNumber: remainingBuyOrder.orderSequenceNumber,
            clientAccountId: remainingBuyOrder.clientAccountId,
            limitPriceInMinorUnits: remainingBuyOrder.limitPriceInMinorUnits,
            remainingQuantity: remainingBuyOrder.orderQuantity,
        };
        self.restingBuyOrdersByPriceDescending
            .entry(remainingBuyOrder.limitPriceInMinorUnits)
            .or_default()
            .push_back(restingLimitOrder);
    }

    fn restRemainingSellOrderOnBook(&mut self, remainingSellOrder: IncomingOrderRequest) {
        let restingLimitOrder = RestingLimitOrder {
            restingOrderSequenceNumber: remainingSellOrder.orderSequenceNumber,
            clientAccountId: remainingSellOrder.clientAccountId,
            limitPriceInMinorUnits: remainingSellOrder.limitPriceInMinorUnits,
            remainingQuantity: remainingSellOrder.orderQuantity,
        };
        self.restingSellOrdersByPriceAscending
            .entry(remainingSellOrder.limitPriceInMinorUnits)
            .or_default()
            .push_back(restingLimitOrder);
    }

    fn allocateNextOrderSequenceNumber(&mut self) -> u64 {
        let allocatedSequenceNumber = self.nextOrderSequenceNumber;
        self.nextOrderSequenceNumber += 1;
        allocatedSequenceNumber
    }

    /// Returns every price level currently on the book, both sides, as
    /// (isBidSide, priceInMinorUnits, totalQuantityAtPrice). Used by
    /// main.rs to publish a depth update to market-data after each order
    /// is processed.
    ///
    /// TODO(real build): this is a full-depth snapshot on every call, not
    /// an actual diff against the previously published state — publishing
    /// it as if it were "the deltas" is a simplification. A real
    /// implementation tracks what changed and only sends that, per the
    /// delta-compression requirement in ARCHITECTURE.md §5.
    pub fn currentBookDepthSnapshot(&self) -> Vec<(bool, i64, u64)> {
        let mut depthSnapshot = Vec::new();

        for (priceInMinorUnits, restingOrdersAtPrice) in &self.restingBuyOrdersByPriceDescending {
            let totalQuantityAtPrice: u64 = restingOrdersAtPrice
                .iter()
                .map(|o| o.remainingQuantity)
                .sum();
            depthSnapshot.push((true, *priceInMinorUnits, totalQuantityAtPrice));
        }
        for (priceInMinorUnits, restingOrdersAtPrice) in &self.restingSellOrdersByPriceAscending {
            let totalQuantityAtPrice: u64 = restingOrdersAtPrice
                .iter()
                .map(|o| o.remainingQuantity)
                .sum();
            depthSnapshot.push((false, *priceInMinorUnits, totalQuantityAtPrice));
        }

        depthSnapshot
    }

    /// A full, order-for-order, price-level-for-price-level dump of every
    /// bit of book state this struct holds — every resting order on both
    /// sides (in FIFO order within each price level), every still-armed
    /// pending stop order, the last traded price, and the next id the
    /// allocator would hand out. Deliberately much richer than
    /// `currentBookDepthSnapshot` (which only aggregates quantity per
    /// price level for depth publishing) — this exists purely so tests
    /// (in particular `writeAheadLog.rs`'s replay round-trip tests and
    /// `deterministicReplayHarness.rs`) can assert two books are IDENTICAL,
    /// not just "have the same visible depth."
    pub fn fullBookStateSnapshotForTesting(&self) -> FullOrderBookStateSnapshotForTesting {
        FullOrderBookStateSnapshotForTesting {
            restingBuyOrdersByPriceDescending: self
                .restingBuyOrdersByPriceDescending
                .iter()
                .map(|(price, ordersAtPrice)| (*price, ordersAtPrice.iter().cloned().collect()))
                .collect(),
            restingSellOrdersByPriceAscending: self
                .restingSellOrdersByPriceAscending
                .iter()
                .map(|(price, ordersAtPrice)| (*price, ordersAtPrice.iter().cloned().collect()))
                .collect(),
            pendingStopOrders: self.pendingStopOrders.clone(),
            lastTradedPriceInMinorUnits: self.lastTradedPriceInMinorUnits,
            nextOrderSequenceNumber: self.nextOrderSequenceNumber,
        }
    }

    pub fn printCurrentBookDepth(&self) {
        println!("--- order book depth for {} ---", self.instrumentSymbol);
        println!("BIDS (best first):");
        for (priceInMinorUnits, restingOrdersAtPrice) in
            self.restingBuyOrdersByPriceDescending.iter().rev()
        {
            let totalQuantityAtPrice: u64 = restingOrdersAtPrice
                .iter()
                .map(|o| o.remainingQuantity)
                .sum();
            println!("  {:>8} x {}", priceInMinorUnits, totalQuantityAtPrice);
        }
        println!("ASKS (best first):");
        for (priceInMinorUnits, restingOrdersAtPrice) in
            self.restingSellOrdersByPriceAscending.iter()
        {
            let totalQuantityAtPrice: u64 = restingOrdersAtPrice
                .iter()
                .map(|o| o.remainingQuantity)
                .sum();
            println!("  {:>8} x {}", priceInMinorUnits, totalQuantityAtPrice);
        }
    }
}

/// Standard stop-loss trigger convention: a BUY stop (typically arming a
/// breakout entry, or covering a short) fires once the last traded price
/// rises to or through the trigger; a SELL stop (typically protecting a
/// long position) fires once the last traded price falls to or through
/// the trigger. Free function rather than a method so
/// `triggerAnyEligiblePendingStopOrders` can call it from inside an
/// `iter().position()` closure without a `self` borrow conflict.
fn isStopOrderTriggeredAtPrice(
    pendingOrder: &IncomingOrderRequest,
    lastTradedPriceInMinorUnits: i64,
) -> bool {
    let triggerPrice = pendingOrder
        .stopTriggerPriceInMinorUnits
        .expect("a StopLossLimit/StopLossMarket order must always carry a trigger price");

    match pendingOrder.orderSide {
        OrderSide::Buy => lastTradedPriceInMinorUnits >= triggerPrice,
        OrderSide::Sell => lastTradedPriceInMinorUnits <= triggerPrice,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn crossingSellOrderMatchesRestingBuyOrderAtRestingPrice() {
        let mut orderBookUnderTest = OrderBookCore::newEmptyOrderBook("TEST");

        orderBookUnderTest.submitIncomingOrder(IncomingOrderRequest {
            clientAccountId: "buyer".into(),
            orderSide: OrderSide::Buy,
            orderType: OrderType::Limit,
            limitPriceInMinorUnits: 100,
            stopTriggerPriceInMinorUnits: None,
            orderSequenceNumber: 0,
            orderQuantity: 5,
        });

        let tradeExecutionEvents = orderBookUnderTest.submitIncomingOrder(IncomingOrderRequest {
            clientAccountId: "seller".into(),
            orderSide: OrderSide::Sell,
            orderType: OrderType::Limit,
            limitPriceInMinorUnits: 95,
            stopTriggerPriceInMinorUnits: None,
            orderSequenceNumber: 0,
            orderQuantity: 5,
        });

        assert_eq!(tradeExecutionEvents.tradeExecutionEvents.len(), 1);
        // Price-time priority: trade executes at the RESTING order's price,
        // not the aggressor's — this is the standard convention and the
        // one every downstream P&L/margin calc must assume.
        assert_eq!(
            tradeExecutionEvents.tradeExecutionEvents[0].executedPriceInMinorUnits,
            100
        );
        assert_eq!(
            tradeExecutionEvents.tradeExecutionEvents[0].executedQuantity,
            5
        );
    }

    #[test]
    fn partialFillLeavesRemainderRestingOnBook() {
        let mut orderBookUnderTest = OrderBookCore::newEmptyOrderBook("TEST");

        orderBookUnderTest.submitIncomingOrder(IncomingOrderRequest {
            clientAccountId: "seller".into(),
            orderSide: OrderSide::Sell,
            orderType: OrderType::Limit,
            limitPriceInMinorUnits: 100,
            stopTriggerPriceInMinorUnits: None,
            orderSequenceNumber: 0,
            orderQuantity: 10,
        });

        let tradeExecutionEvents = orderBookUnderTest.submitIncomingOrder(IncomingOrderRequest {
            clientAccountId: "buyer".into(),
            orderSide: OrderSide::Buy,
            orderType: OrderType::Limit,
            limitPriceInMinorUnits: 100,
            stopTriggerPriceInMinorUnits: None,
            orderSequenceNumber: 0,
            orderQuantity: 4,
        });

        assert_eq!(tradeExecutionEvents.tradeExecutionEvents.len(), 1);
        assert_eq!(
            tradeExecutionEvents.tradeExecutionEvents[0].executedQuantity,
            4
        );

        let remainingRestingSellOrders = orderBookUnderTest
            .restingSellOrdersByPriceAscending
            .get(&100)
            .expect("6 units should still be resting at price 100");
        assert_eq!(
            remainingRestingSellOrders
                .front()
                .unwrap()
                .remainingQuantity,
            6
        );
    }

    #[test]
    fn nonCrossingOrdersBothRestWithoutTrading() {
        let mut orderBookUnderTest = OrderBookCore::newEmptyOrderBook("TEST");

        let firstTradeBatch = orderBookUnderTest.submitIncomingOrder(IncomingOrderRequest {
            clientAccountId: "buyer".into(),
            orderSide: OrderSide::Buy,
            orderType: OrderType::Limit,
            limitPriceInMinorUnits: 90,
            stopTriggerPriceInMinorUnits: None,
            orderSequenceNumber: 0,
            orderQuantity: 5,
        });
        let secondTradeBatch = orderBookUnderTest.submitIncomingOrder(IncomingOrderRequest {
            clientAccountId: "seller".into(),
            orderSide: OrderSide::Sell,
            orderType: OrderType::Limit,
            limitPriceInMinorUnits: 100,
            stopTriggerPriceInMinorUnits: None,
            orderSequenceNumber: 0,
            orderQuantity: 5,
        });

        assert!(firstTradeBatch.tradeExecutionEvents.is_empty());
        assert!(secondTradeBatch.tradeExecutionEvents.is_empty());
    }

    #[test]
    fn currentBookDepthSnapshotReflectsRestingOrdersOnBothSides() {
        let mut orderBookUnderTest = OrderBookCore::newEmptyOrderBook("TEST");

        orderBookUnderTest.submitIncomingOrder(IncomingOrderRequest {
            clientAccountId: "buyer".into(),
            orderSide: OrderSide::Buy,
            orderType: OrderType::Limit,
            limitPriceInMinorUnits: 90,
            stopTriggerPriceInMinorUnits: None,
            orderSequenceNumber: 0,
            orderQuantity: 5,
        });
        orderBookUnderTest.submitIncomingOrder(IncomingOrderRequest {
            clientAccountId: "seller".into(),
            orderSide: OrderSide::Sell,
            orderType: OrderType::Limit,
            limitPriceInMinorUnits: 100,
            stopTriggerPriceInMinorUnits: None,
            orderSequenceNumber: 0,
            orderQuantity: 7,
        });

        let depthSnapshot = orderBookUnderTest.currentBookDepthSnapshot();

        assert!(depthSnapshot.contains(&(true, 90, 5)));
        assert!(depthSnapshot.contains(&(false, 100, 7)));
        assert_eq!(depthSnapshot.len(), 2);
    }

    #[test]
    fn marketOrderCrossesRegardlessOfRestingPrice() {
        let mut orderBookUnderTest = OrderBookCore::newEmptyOrderBook("TEST");

        orderBookUnderTest.submitIncomingOrder(IncomingOrderRequest {
            clientAccountId: "seller".into(),
            orderSide: OrderSide::Sell,
            orderType: OrderType::Limit,
            limitPriceInMinorUnits: 10_000, // an aggressively high resting ask
            stopTriggerPriceInMinorUnits: None,
            orderSequenceNumber: 0,
            orderQuantity: 5,
        });

        let tradeExecutionEvents = orderBookUnderTest.submitIncomingOrder(IncomingOrderRequest {
            clientAccountId: "buyer".into(),
            orderSide: OrderSide::Buy,
            orderType: OrderType::Market,
            limitPriceInMinorUnits: 0, // ignored for a market order
            stopTriggerPriceInMinorUnits: None,
            orderSequenceNumber: 0,
            orderQuantity: 5,
        });

        assert_eq!(tradeExecutionEvents.tradeExecutionEvents.len(), 1);
        assert_eq!(
            tradeExecutionEvents.tradeExecutionEvents[0].executedPriceInMinorUnits,
            10_000
        );
        assert_eq!(
            tradeExecutionEvents.tradeExecutionEvents[0].executedQuantity,
            5
        );
    }

    #[test]
    fn unfilledMarketOrderRemainderIsDroppedNotRested() {
        let mut orderBookUnderTest = OrderBookCore::newEmptyOrderBook("TEST");

        // No resting sell orders at all — this market buy can't fill any of it.
        let tradeExecutionEvents = orderBookUnderTest.submitIncomingOrder(IncomingOrderRequest {
            clientAccountId: "buyer".into(),
            orderSide: OrderSide::Buy,
            orderType: OrderType::Market,
            limitPriceInMinorUnits: 0,
            stopTriggerPriceInMinorUnits: None,
            orderSequenceNumber: 0,
            orderQuantity: 5,
        });

        assert!(tradeExecutionEvents.tradeExecutionEvents.is_empty());

        let depthSnapshot = orderBookUnderTest.currentBookDepthSnapshot();
        assert!(
            depthSnapshot.is_empty(),
            "an unfilled market order must never rest on the book"
        );
    }

    #[test]
    fn stopLossMarketSellOrderIsInvisibleToDepthUntilTriggered() {
        let mut orderBookUnderTest = OrderBookCore::newEmptyOrderBook("TEST");

        // Arm a sell stop at 90 — protects a long position if the price
        // falls to/through 90. No trade has happened yet, so it can't be
        // triggered yet no matter what its trigger price is.
        let armingTradeEvents = orderBookUnderTest.submitIncomingOrder(IncomingOrderRequest {
            clientAccountId: "longHolder".into(),
            orderSide: OrderSide::Sell,
            orderType: OrderType::StopLossMarket,
            limitPriceInMinorUnits: 0,
            stopTriggerPriceInMinorUnits: Some(90),
            orderSequenceNumber: 0,
            orderQuantity: 5,
        });

        assert!(armingTradeEvents.tradeExecutionEvents.is_empty());
        assert_eq!(orderBookUnderTest.pendingStopOrderCount(), 1);
        assert!(
            orderBookUnderTest.currentBookDepthSnapshot().is_empty(),
            "an armed-but-untriggered stop order must not appear in depth"
        );
    }

    #[test]
    fn stopLossMarketSellOrderTriggersAndFillsOnceLastPriceFalls() {
        let mut orderBookUnderTest = OrderBookCore::newEmptyOrderBook("TEST");

        // Rest a cheap sell at 85 that a marketable buy will cross into,
        // printing a trade at 85.
        orderBookUnderTest.submitIncomingOrder(IncomingOrderRequest {
            clientAccountId: "cheapSeller".into(),
            orderSide: OrderSide::Sell,
            orderType: OrderType::Limit,
            limitPriceInMinorUnits: 85,
            stopTriggerPriceInMinorUnits: None,
            orderSequenceNumber: 0,
            orderQuantity: 3,
        });

        // Arm a sell stop that fires once the last trade is <= 90. No
        // trade has printed yet, so it stays pending for now.
        orderBookUnderTest.submitIncomingOrder(IncomingOrderRequest {
            clientAccountId: "longHolder".into(),
            orderSide: OrderSide::Sell,
            orderType: OrderType::StopLossMarket,
            limitPriceInMinorUnits: 0,
            stopTriggerPriceInMinorUnits: Some(90),
            orderSequenceNumber: 0,
            orderQuantity: 5,
        });
        assert_eq!(orderBookUnderTest.pendingStopOrderCount(), 1);

        // A buy crosses the resting 85 sell, printing a trade at 85 — at
        // or below the trigger — which must cascade-trigger the stop
        // above. That triggered order becomes a Market sell with no
        // resting buys left to fill into, so it produces no further
        // trades but must still leave the pending pool (IOC-like: it
        // doesn't go back to being "pending," it's just done).
        let tradeExecutionEvents = orderBookUnderTest.submitIncomingOrder(IncomingOrderRequest {
            clientAccountId: "dipBuyer".into(),
            orderSide: OrderSide::Buy,
            orderType: OrderType::Limit,
            limitPriceInMinorUnits: 85,
            stopTriggerPriceInMinorUnits: None,
            orderSequenceNumber: 0,
            orderQuantity: 3,
        });

        assert_eq!(
            tradeExecutionEvents.tradeExecutionEvents.len(),
            1,
            "only the initiating trade at 85 — the triggered stop found no resting buys to fill into"
        );
        assert_eq!(
            tradeExecutionEvents.tradeExecutionEvents[0].executedPriceInMinorUnits,
            85
        );
        assert_eq!(
            orderBookUnderTest.pendingStopOrderCount(),
            0,
            "the stop order must have triggered and left the pending pool"
        );
        assert!(
            orderBookUnderTest.currentBookDepthSnapshot().is_empty(),
            "the cheap sell was fully consumed and the triggered market \
             sell never rests"
        );
    }

    #[test]
    fn stopLossLimitOrderDoesNotTriggerWhileLastPriceStaysOnTheFavorableSide() {
        let mut orderBookUnderTest = OrderBookCore::newEmptyOrderBook("TEST");

        // Trade prints at 100.
        orderBookUnderTest.submitIncomingOrder(IncomingOrderRequest {
            clientAccountId: "buyer".into(),
            orderSide: OrderSide::Buy,
            orderType: OrderType::Limit,
            limitPriceInMinorUnits: 100,
            stopTriggerPriceInMinorUnits: None,
            orderSequenceNumber: 0,
            orderQuantity: 5,
        });
        orderBookUnderTest.submitIncomingOrder(IncomingOrderRequest {
            clientAccountId: "seller".into(),
            orderSide: OrderSide::Sell,
            orderType: OrderType::Limit,
            limitPriceInMinorUnits: 100,
            stopTriggerPriceInMinorUnits: None,
            orderSequenceNumber: 0,
            orderQuantity: 5,
        });

        // Sell stop armed at 90 — last price (100) is well above it, so it
        // must stay pending.
        orderBookUnderTest.submitIncomingOrder(IncomingOrderRequest {
            clientAccountId: "longHolder".into(),
            orderSide: OrderSide::Sell,
            orderType: OrderType::StopLossLimit,
            limitPriceInMinorUnits: 85,
            stopTriggerPriceInMinorUnits: Some(90),
            orderSequenceNumber: 0,
            orderQuantity: 5,
        });

        assert_eq!(orderBookUnderTest.pendingStopOrderCount(), 1);
    }

    #[test]
    fn cancelOrderRemovesARestingLimitOrderAndFreesItFromFutureMatching() {
        let mut orderBookUnderTest = OrderBookCore::newEmptyOrderBook("TEST");

        let restingOrderOutcome = orderBookUnderTest.submitIncomingOrder(IncomingOrderRequest {
            clientAccountId: "buyer".into(),
            orderSide: OrderSide::Buy,
            orderType: OrderType::Limit,
            limitPriceInMinorUnits: 100,
            stopTriggerPriceInMinorUnits: None,
            orderSequenceNumber: 0,
            orderQuantity: 5,
        });
        assert!(!orderBookUnderTest.currentBookDepthSnapshot().is_empty());

        let wasCancelled =
            orderBookUnderTest.cancelOrder(restingOrderOutcome.assignedOrderSequenceNumber);
        assert!(wasCancelled);
        assert!(
            orderBookUnderTest.currentBookDepthSnapshot().is_empty(),
            "the cancelled order's price level must be gone entirely"
        );

        // A sell that would have crossed the cancelled buy must now find
        // nothing to match against.
        let tradeExecutionEvents = orderBookUnderTest.submitIncomingOrder(IncomingOrderRequest {
            clientAccountId: "seller".into(),
            orderSide: OrderSide::Sell,
            orderType: OrderType::Limit,
            limitPriceInMinorUnits: 100,
            stopTriggerPriceInMinorUnits: None,
            orderSequenceNumber: 0,
            orderQuantity: 5,
        });
        assert!(tradeExecutionEvents.tradeExecutionEvents.is_empty());
    }

    #[test]
    fn cancelOrderRemovesAPendingStopOrderBeforeItEverTriggers() {
        let mut orderBookUnderTest = OrderBookCore::newEmptyOrderBook("TEST");

        let stopOrderOutcome = orderBookUnderTest.submitIncomingOrder(IncomingOrderRequest {
            clientAccountId: "longHolder".into(),
            orderSide: OrderSide::Sell,
            orderType: OrderType::StopLossMarket,
            limitPriceInMinorUnits: 0,
            stopTriggerPriceInMinorUnits: Some(90),
            orderSequenceNumber: 0,
            orderQuantity: 5,
        });
        assert_eq!(orderBookUnderTest.pendingStopOrderCount(), 1);

        let wasCancelled =
            orderBookUnderTest.cancelOrder(stopOrderOutcome.assignedOrderSequenceNumber);
        assert!(wasCancelled);
        assert_eq!(orderBookUnderTest.pendingStopOrderCount(), 0);

        // A trade printing well below the (now-cancelled) trigger must not
        // resurrect it.
        orderBookUnderTest.submitIncomingOrder(IncomingOrderRequest {
            clientAccountId: "seller".into(),
            orderSide: OrderSide::Sell,
            orderType: OrderType::Limit,
            limitPriceInMinorUnits: 50,
            stopTriggerPriceInMinorUnits: None,
            orderSequenceNumber: 0,
            orderQuantity: 5,
        });
        orderBookUnderTest.submitIncomingOrder(IncomingOrderRequest {
            clientAccountId: "buyer".into(),
            orderSide: OrderSide::Buy,
            orderType: OrderType::Limit,
            limitPriceInMinorUnits: 50,
            stopTriggerPriceInMinorUnits: None,
            orderSequenceNumber: 0,
            orderQuantity: 5,
        });
        assert_eq!(
            orderBookUnderTest.pendingStopOrderCount(),
            0,
            "still zero — the cancelled stop must not reappear"
        );
    }

    #[test]
    fn cancelOrderReturnsFalseForAnUnknownOrUnrecognizedId() {
        let mut orderBookUnderTest = OrderBookCore::newEmptyOrderBook("TEST");
        assert!(!orderBookUnderTest.cancelOrder(999));
    }

    #[test]
    fn queryOrderStatusReturnsNotFoundForAnUnknownId() {
        let orderBookUnderTest = OrderBookCore::newEmptyOrderBook("TEST");
        assert_eq!(
            orderBookUnderTest.queryOrderStatus(999),
            OrderStatusQueryResult::NotFound
        );
    }

    #[test]
    fn queryOrderStatusReportsARestingLimitOrderWithItsRemainingQuantity() {
        let mut orderBookUnderTest = OrderBookCore::newEmptyOrderBook("TEST");

        let restingOutcome = orderBookUnderTest.submitIncomingOrder(IncomingOrderRequest {
            clientAccountId: "buyer".into(),
            orderSide: OrderSide::Buy,
            orderType: OrderType::Limit,
            limitPriceInMinorUnits: 100,
            stopTriggerPriceInMinorUnits: None,
            orderSequenceNumber: 0,
            orderQuantity: 5,
        });
        // Partially fill it so remainingQuantity in the status differs
        // from the original orderQuantity — proves the status reflects
        // live book state, not just the original submission.
        orderBookUnderTest.submitIncomingOrder(IncomingOrderRequest {
            clientAccountId: "seller".into(),
            orderSide: OrderSide::Sell,
            orderType: OrderType::Limit,
            limitPriceInMinorUnits: 100,
            stopTriggerPriceInMinorUnits: None,
            orderSequenceNumber: 0,
            orderQuantity: 2,
        });

        let status =
            orderBookUnderTest.queryOrderStatus(restingOutcome.assignedOrderSequenceNumber);
        assert_eq!(
            status,
            OrderStatusQueryResult::RestingLimit {
                orderSide: OrderSide::Buy,
                limitPriceInMinorUnits: 100,
                remainingQuantity: 3,
            }
        );
    }

    #[test]
    fn queryOrderStatusReportsAPendingStopOrder() {
        let mut orderBookUnderTest = OrderBookCore::newEmptyOrderBook("TEST");

        let stopOutcome = orderBookUnderTest.submitIncomingOrder(IncomingOrderRequest {
            clientAccountId: "longHolder".into(),
            orderSide: OrderSide::Sell,
            orderType: OrderType::StopLossMarket,
            limitPriceInMinorUnits: 0,
            stopTriggerPriceInMinorUnits: Some(90),
            orderSequenceNumber: 0,
            orderQuantity: 5,
        });

        let status = orderBookUnderTest.queryOrderStatus(stopOutcome.assignedOrderSequenceNumber);
        assert_eq!(
            status,
            OrderStatusQueryResult::PendingStop {
                orderSide: OrderSide::Sell,
                stopTriggerPriceInMinorUnits: 90,
                orderQuantity: 5,
            }
        );
    }

    #[test]
    fn queryOrderStatusReturnsNotFoundAfterCancellation() {
        let mut orderBookUnderTest = OrderBookCore::newEmptyOrderBook("TEST");

        let restingOutcome = orderBookUnderTest.submitIncomingOrder(IncomingOrderRequest {
            clientAccountId: "buyer".into(),
            orderSide: OrderSide::Buy,
            orderType: OrderType::Limit,
            limitPriceInMinorUnits: 100,
            stopTriggerPriceInMinorUnits: None,
            orderSequenceNumber: 0,
            orderQuantity: 5,
        });
        orderBookUnderTest.cancelOrder(restingOutcome.assignedOrderSequenceNumber);

        assert_eq!(
            orderBookUnderTest.queryOrderStatus(restingOutcome.assignedOrderSequenceNumber),
            OrderStatusQueryResult::NotFound
        );
    }
}
