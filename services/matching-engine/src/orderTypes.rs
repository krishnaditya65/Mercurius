// Wire/domain types for the matching engine.
//
// TODO(real build): per ARCHITECTURE.md §3.5, the real Tier 0 wire format
// must be a fixed-layout binary encoding (SBE or hand-rolled) with
// zero-copy decode — these `String`/`Vec` based types are fine for the
// skeleton but must NOT survive into the allocation-free hot path.

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum OrderSide {
    Buy,
    Sell,
}

/// FEATURES.md §3's "Order entry: Market, Limit, SL, SL-M" — all four are
/// real as of this build. `StopLossLimit`/`StopLossMarket` don't match
/// immediately: they sit in `OrderBookCore`'s pending-stop pool, invisible
/// to the resting book and to depth snapshots, until the last traded price
/// crosses the order's trigger price — at which point they convert to a
/// live `Limit`/`Market` order and enter normal matching, exactly like an
/// order a client just submitted fresh.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum OrderType {
    Limit,
    Market,
    /// Stop-loss, limit-priced once triggered: rests invisibly until the
    /// last traded price crosses `stopTriggerPriceInMinorUnits`, then
    /// becomes a `Limit` order at `limitPriceInMinorUnits`.
    StopLossLimit,
    /// Stop-loss, market-priced once triggered: same trigger behavior as
    /// `StopLossLimit`, but becomes a `Market` order (crosses at whatever
    /// price is available) instead of a `Limit` order.
    StopLossMarket,
}

/// An order arriving from the OMS/risk gateway, already risk-checked and
/// already assigned a global sequence number upstream (see
/// ARCHITECTURE.md §4 — sequencing happens at the OMS, not here — that's
/// a separate, OMS-owned sequence space from `orderSequenceNumber` below).
#[derive(Debug, Clone)]
pub struct IncomingOrderRequest {
    pub clientAccountId: String,
    pub orderSide: OrderSide,
    pub orderType: OrderType,

    /// Integer minor-unit pricing (e.g. paise, cents) — never floating
    /// point for money. Floats introduce rounding drift that is
    /// unacceptable in a matching core. Ignored for `OrderType::Market`
    /// and `OrderType::StopLossMarket` (both cross at whatever price is
    /// available once live).
    pub limitPriceInMinorUnits: i64,

    /// Only meaningful for `OrderType::StopLossLimit` /
    /// `OrderType::StopLossMarket` — the last-traded-price level that
    /// arms this order. `None` for `Limit`/`Market` orders.
    pub stopTriggerPriceInMinorUnits: Option<i64>,

    pub orderQuantity: u64,

    /// This engine's own local order identifier — the id a client can
    /// later pass to `OrderBookCore::cancelOrder` to pull this order off
    /// the book (whether it's resting as a `Limit` or still armed as a
    /// pending stop order). Caller-supplied value is ignored: set to
    /// anything (`0` by convention) on the way in — `submitIncomingOrder`
    /// overwrites it with a freshly allocated id the moment it's called,
    /// same sequence space `RestingLimitOrder::restingOrderSequenceNumber`
    /// draws from.
    pub orderSequenceNumber: u64,
}

/// An order resting on the book after partial or zero fill.
#[derive(Debug, Clone)]
pub struct RestingLimitOrder {
    pub restingOrderSequenceNumber: u64,
    pub clientAccountId: String,
    pub limitPriceInMinorUnits: i64,
    pub remainingQuantity: u64,
}

/// One executed trade, produced by matching an incoming order against one
/// or more resting orders. A single incoming order can produce many of
/// these if it crosses multiple price levels / resting orders.
#[derive(Debug, Clone)]
pub struct TradeExecutionEvent {
    pub buyingClientAccountId: String,
    pub sellingClientAccountId: String,
    pub executedPriceInMinorUnits: i64,
    pub executedQuantity: u64,
}

/// What `OrderBookCore::submitIncomingOrder` returns: every trade produced
/// (possibly none), plus the id this order was assigned so the caller can
/// cancel it later if any of it is still resting or pending.
#[derive(Debug, Clone)]
pub struct OrderSubmissionOutcome {
    pub tradeExecutionEvents: Vec<TradeExecutionEvent>,
    pub assignedOrderSequenceNumber: u64,
}

/// What `OrderBookCore::queryOrderStatus` returns — lets a caller ask
/// "what's happening with the order I got back id N for?" without
/// needing to track fills/cancels itself. Read-only, no side effects.
#[derive(Debug, Clone, PartialEq)]
pub enum OrderStatusQueryResult {
    /// Still (partially or fully) resting as a live `Limit` order —
    /// includes a triggered stop order that itself rested afterward,
    /// found under the same id it was armed with.
    RestingLimit {
        orderSide: OrderSide,
        limitPriceInMinorUnits: i64,
        remainingQuantity: u64,
    },
    /// Armed but not yet triggered — still in the pending-stop pool.
    PendingStop {
        orderSide: OrderSide,
        stopTriggerPriceInMinorUnits: i64,
        orderQuantity: u64,
    },
    /// No order with this id is currently resting or pending — either
    /// the id never existed, the order fully filled, it was cancelled,
    /// or (for a stop order) it triggered and its resulting order also
    /// fully filled with nothing left resting. This engine keeps no
    /// history, so these cases are indistinguishable from here.
    NotFound,
}
