// The TCP JSON bridge between oms-gateway and matching-engine.
//
// TODO(real build): this is a pragmatic bridge to prove the service
// boundary end-to-end — a synchronous TCP+JSON round-trip per order. The
// types in this file describe the wire SHAPE only; what actually carries
// requests/responses between the network thread and the matching-core
// thread inside this process IS a real lock-free SPSC ring buffer now
// (`src/lockFreeSpscRingBuffer.rs`, wired up in `main.rs` — see the
// README's "Lock-free ring buffer ingress/egress" section). What's still
// explicitly a placeholder here is the wire encoding itself: JSON, not the
// zero-copy binary (SBE) encoding described in ARCHITECTURE.md §3.5, and
// one connection per request rather than a persistent multiplexed
// session. Field names are deliberately identical to oms-gateway's
// `orders.OrderSubmissionRequest` JSON shape (see
// services/oms-gateway/internal/orders/orderTypes.go) so the wire
// contract reads the same on both sides of the boundary.

use serde::{Deserialize, Serialize};

use crate::orderTypes::{
    IncomingOrderRequest, OrderSide, OrderStatusQueryResult, OrderType, TradeExecutionEvent,
};

#[derive(Debug, Deserialize)]
pub struct IncomingOrderWireRequest {
    pub clientAccountIdentifier: String,
    pub instrumentSymbol: String,
    pub orderSideIsBuyNotSell: bool,

    /// Defaults to `false` (Limit) if the sender omits this field —
    /// keeps this wire contract backward-compatible with any client that
    /// predates Market order support instead of hard-failing to parse.
    #[serde(default)]
    pub orderIsMarketOrderNotLimit: bool,

    /// Combines with `orderIsMarketOrderNotLimit` to select one of the
    /// four `OrderType`s: `(false, false)` = Limit, `(false, true)` =
    /// Market, `(true, false)` = StopLossLimit, `(true, true)` =
    /// StopLossMarket. Defaults to `false` for the same backward-
    /// compatibility reason as above — omitting it means "not a stop
    /// order."
    #[serde(default)]
    pub orderIsStopLossVariant: bool,

    /// Required when `orderIsStopLossVariant` is true; ignored otherwise.
    /// `#[serde(default)]` so older/non-stop clients that never send this
    /// key still parse.
    #[serde(default)]
    pub stopTriggerPriceInMinorUnits: Option<i64>,

    /// If set, this line is a CANCEL request, not an order submission:
    /// every other field is ignored except `instrumentSymbol` (still
    /// used to route to the right book). `#[serde(default)]` so this
    /// stays backward-compatible — omitting it means "this is a normal
    /// order submission," exactly as before this field existed. A
    /// dedicated message-type enum would be cleaner, but this skeleton's
    /// wire format is a flat JSON object already, so a single optional
    /// discriminant field matches the existing extension pattern
    /// (`orderIsMarketOrderNotLimit`, `orderIsStopLossVariant`) instead
    /// of introducing a new shape.
    #[serde(default)]
    pub cancelOrderSequenceNumber: Option<u64>,

    /// If set, this line is a STATUS QUERY, not an order submission or a
    /// cancel — every other field but `instrumentSymbol` is ignored.
    /// Same flat-JSON extension pattern as `cancelOrderSequenceNumber`.
    /// If BOTH this and `cancelOrderSequenceNumber` are somehow set on
    /// the same line, `main.rs` checks cancel first — an odd client
    /// sending both gets cancel semantics, not a documented contract to
    /// rely on.
    #[serde(default)]
    pub queryOrderStatusSequenceNumber: Option<u64>,

    pub limitPriceInMinorUnits: i64,
    pub orderQuantity: u64,
}

impl IncomingOrderWireRequest {
    pub fn intoInternalOrderRequest(self) -> IncomingOrderRequest {
        IncomingOrderRequest {
            clientAccountId: self.clientAccountIdentifier,
            orderSide: if self.orderSideIsBuyNotSell {
                OrderSide::Buy
            } else {
                OrderSide::Sell
            },
            orderType: match (self.orderIsStopLossVariant, self.orderIsMarketOrderNotLimit) {
                (true, true) => OrderType::StopLossMarket,
                (true, false) => OrderType::StopLossLimit,
                (false, true) => OrderType::Market,
                (false, false) => OrderType::Limit,
            },
            limitPriceInMinorUnits: self.limitPriceInMinorUnits,
            stopTriggerPriceInMinorUnits: self.stopTriggerPriceInMinorUnits,
            orderQuantity: self.orderQuantity,
            // Overwritten by OrderBookCore::submitIncomingOrder the
            // instant it's submitted — this placeholder value is never
            // observed by a caller.
            orderSequenceNumber: 0,
        }
    }
}

#[derive(Debug, Serialize, PartialEq)]
pub struct TradeExecutionWireEvent {
    pub buyingClientAccountId: String,
    pub sellingClientAccountId: String,
    pub executedPriceInMinorUnits: i64,
    pub executedQuantity: u64,
}

impl From<&TradeExecutionEvent> for TradeExecutionWireEvent {
    fn from(internalEvent: &TradeExecutionEvent) -> Self {
        TradeExecutionWireEvent {
            buyingClientAccountId: internalEvent.buyingClientAccountId.clone(),
            sellingClientAccountId: internalEvent.sellingClientAccountId.clone(),
            executedPriceInMinorUnits: internalEvent.executedPriceInMinorUnits,
            executedQuantity: internalEvent.executedQuantity,
        }
    }
}

/// Every response is one line of JSON. Either `errorMessage` is set (and
/// `tradeExecutionEvents` is empty), or the order was accepted by the book
/// and `tradeExecutionEvents` holds whatever trades it produced (possibly
/// none, if it fully rested without crossing, or if it armed as a pending
/// stop order). `assignedOrderSequenceNumber` is set on every successful
/// order SUBMISSION (not on cancel responses) — hold onto it to cancel
/// this order later. `wasOrderCancelled` is set only on responses to a
/// cancel request.
#[derive(Debug, Serialize)]
pub struct OrderSubmissionWireResponse {
    pub tradeExecutionEvents: Vec<TradeExecutionWireEvent>,
    pub errorMessage: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub assignedOrderSequenceNumber: Option<u64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub wasOrderCancelled: Option<bool>,

    /// Set only on responses to a status query — one of `"RESTING_LIMIT"`,
    /// `"PENDING_STOP"`, or `"NOT_FOUND"`. The remaining `orderStatus*`
    /// fields are only meaningful when this is `"RESTING_LIMIT"` or
    /// `"PENDING_STOP"`.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub orderStatus: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub orderStatusSideIsBuyNotSell: Option<bool>,
    /// The resting limit price for `RESTING_LIMIT`, or the trigger price
    /// for `PENDING_STOP`.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub orderStatusPriceInMinorUnits: Option<i64>,
    /// Remaining quantity for `RESTING_LIMIT`, original quantity for
    /// `PENDING_STOP` (a pending stop order hasn't partially filled
    /// anything yet — it isn't live on the book at all).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub orderStatusQuantity: Option<u64>,
}

impl OrderSubmissionWireResponse {
    pub fn successResponse(
        tradeExecutionEvents: Vec<TradeExecutionWireEvent>,
        assignedOrderSequenceNumber: u64,
    ) -> Self {
        OrderSubmissionWireResponse {
            tradeExecutionEvents,
            errorMessage: None,
            assignedOrderSequenceNumber: Some(assignedOrderSequenceNumber),
            wasOrderCancelled: None,
            orderStatus: None,
            orderStatusSideIsBuyNotSell: None,
            orderStatusPriceInMinorUnits: None,
            orderStatusQuantity: None,
        }
    }

    pub fn errorResponse(errorMessage: String) -> Self {
        OrderSubmissionWireResponse {
            tradeExecutionEvents: Vec::new(),
            errorMessage: Some(errorMessage),
            assignedOrderSequenceNumber: None,
            wasOrderCancelled: None,
            orderStatus: None,
            orderStatusSideIsBuyNotSell: None,
            orderStatusPriceInMinorUnits: None,
            orderStatusQuantity: None,
        }
    }

    pub fn cancelResponse(wasOrderCancelled: bool) -> Self {
        OrderSubmissionWireResponse {
            tradeExecutionEvents: Vec::new(),
            errorMessage: None,
            assignedOrderSequenceNumber: None,
            wasOrderCancelled: Some(wasOrderCancelled),
            orderStatus: None,
            orderStatusSideIsBuyNotSell: None,
            orderStatusPriceInMinorUnits: None,
            orderStatusQuantity: None,
        }
    }

    pub fn statusResponse(queryResult: OrderStatusQueryResult) -> Self {
        let mut response = OrderSubmissionWireResponse {
            tradeExecutionEvents: Vec::new(),
            errorMessage: None,
            assignedOrderSequenceNumber: None,
            wasOrderCancelled: None,
            orderStatus: None,
            orderStatusSideIsBuyNotSell: None,
            orderStatusPriceInMinorUnits: None,
            orderStatusQuantity: None,
        };

        match queryResult {
            OrderStatusQueryResult::RestingLimit {
                orderSide,
                limitPriceInMinorUnits,
                remainingQuantity,
            } => {
                response.orderStatus = Some("RESTING_LIMIT".to_string());
                response.orderStatusSideIsBuyNotSell = Some(orderSide == OrderSide::Buy);
                response.orderStatusPriceInMinorUnits = Some(limitPriceInMinorUnits);
                response.orderStatusQuantity = Some(remainingQuantity);
            }
            OrderStatusQueryResult::PendingStop {
                orderSide,
                stopTriggerPriceInMinorUnits,
                orderQuantity,
            } => {
                response.orderStatus = Some("PENDING_STOP".to_string());
                response.orderStatusSideIsBuyNotSell = Some(orderSide == OrderSide::Buy);
                response.orderStatusPriceInMinorUnits = Some(stopTriggerPriceInMinorUnits);
                response.orderStatusQuantity = Some(orderQuantity);
            }
            OrderStatusQueryResult::NotFound => {
                response.orderStatus = Some("NOT_FOUND".to_string());
            }
        }

        response
    }
}

/// Outgoing message TO market-data after processing an order — mirrors
/// market-data's `IncomingDepthPublishWireMessage`/
/// `IncomingPriceLevelDeltaWireUpdate` field-for-field. See
/// `publishBookDepthToMarketData` in main.rs.
#[derive(Debug, Serialize)]
pub struct OutgoingPriceLevelDeltaWireUpdate {
    pub isBidSide: bool,
    pub priceInMinorUnits: i64,
    pub newTotalQuantityAtPrice: u64,
}

/// One executed trade, published alongside the book depth so market-data
/// can build a trade tape / OHLCV candles — see market-data's
/// `candleAggregator.rs`. Deliberately a much narrower shape than the
/// internal `TradeExecutionEvent`: market-data has no business need to
/// know either counterparty's account id, only price and size.
#[derive(Debug, Serialize)]
pub struct OutgoingTradeTickWireEvent {
    pub executedPriceInMinorUnits: i64,
    pub executedQuantity: u64,
}

#[derive(Debug, Serialize)]
pub struct OutgoingDepthPublishWireMessage {
    pub instrumentSymbol: String,
    pub deltas: Vec<OutgoingPriceLevelDeltaWireUpdate>,
    pub tradeTicks: Vec<OutgoingTradeTickWireEvent>,
}

impl OutgoingDepthPublishWireMessage {
    pub fn fromDepthSnapshotAndTrades(
        instrumentSymbol: &str,
        depthSnapshot: Vec<(bool, i64, u64)>,
        tradeExecutionEvents: &[TradeExecutionEvent],
    ) -> Self {
        OutgoingDepthPublishWireMessage {
            instrumentSymbol: instrumentSymbol.to_string(),
            deltas: depthSnapshot
                .into_iter()
                .map(|(isBidSide, priceInMinorUnits, newTotalQuantityAtPrice)| {
                    OutgoingPriceLevelDeltaWireUpdate {
                        isBidSide,
                        priceInMinorUnits,
                        newTotalQuantityAtPrice,
                    }
                })
                .collect(),
            tradeTicks: tradeExecutionEvents
                .iter()
                .map(|tradeEvent| OutgoingTradeTickWireEvent {
                    executedPriceInMinorUnits: tradeEvent.executedPriceInMinorUnits,
                    executedQuantity: tradeEvent.executedQuantity,
                })
                .collect(),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn buySideWireFlagConvertsToOrderSideBuy() {
        let wireRequest = IncomingOrderWireRequest {
            clientAccountIdentifier: "acct-001".into(),
            instrumentSymbol: "DEMO-EQ".into(),
            orderSideIsBuyNotSell: true,
            orderIsMarketOrderNotLimit: false,
            orderIsStopLossVariant: false,
            stopTriggerPriceInMinorUnits: None,
            cancelOrderSequenceNumber: None,
            queryOrderStatusSequenceNumber: None,
            limitPriceInMinorUnits: 100,
            orderQuantity: 5,
        };

        let internalRequest = wireRequest.intoInternalOrderRequest();
        assert_eq!(internalRequest.orderSide, OrderSide::Buy);
    }

    #[test]
    fn sellSideWireFlagConvertsToOrderSideSell() {
        let wireRequest = IncomingOrderWireRequest {
            clientAccountIdentifier: "acct-001".into(),
            instrumentSymbol: "DEMO-EQ".into(),
            orderSideIsBuyNotSell: false,
            orderIsMarketOrderNotLimit: false,
            orderIsStopLossVariant: false,
            stopTriggerPriceInMinorUnits: None,
            cancelOrderSequenceNumber: None,
            queryOrderStatusSequenceNumber: None,
            limitPriceInMinorUnits: 100,
            orderQuantity: 5,
        };

        let internalRequest = wireRequest.intoInternalOrderRequest();
        assert_eq!(internalRequest.orderSide, OrderSide::Sell);
    }

    #[test]
    fn marketOrderWireFlagConvertsToOrderTypeMarket() {
        let wireRequest = IncomingOrderWireRequest {
            clientAccountIdentifier: "acct-001".into(),
            instrumentSymbol: "DEMO-EQ".into(),
            orderSideIsBuyNotSell: true,
            orderIsMarketOrderNotLimit: true,
            orderIsStopLossVariant: false,
            stopTriggerPriceInMinorUnits: None,
            cancelOrderSequenceNumber: None,
            queryOrderStatusSequenceNumber: None,
            limitPriceInMinorUnits: 0,
            orderQuantity: 5,
        };

        let internalRequest = wireRequest.intoInternalOrderRequest();
        assert_eq!(internalRequest.orderType, OrderType::Market);
    }

    #[test]
    fn omittedMarketFlagDefaultsToLimitForBackwardCompatibility() {
        // No "orderIsMarketOrderNotLimit" key at all — must still parse,
        // defaulting to Limit, so any client written before Market order
        // support keeps working without a hard parse failure.
        let jsonLine = r#"{"clientAccountIdentifier":"acct-001","instrumentSymbol":"DEMO-EQ","orderSideIsBuyNotSell":true,"limitPriceInMinorUnits":10000,"orderQuantity":5}"#;

        let parsed: IncomingOrderWireRequest =
            serde_json::from_str(jsonLine).expect("should parse even without the newer field");
        assert_eq!(
            parsed.intoInternalOrderRequest().orderType,
            OrderType::Limit
        );
    }

    #[test]
    fn incomingOrderWireRequestDeserializesFromOmsGatewayShapedJson() {
        // This exact JSON shape is what oms-gateway's matchingengineclient
        // package sends — see its OrderSubmissionWireRequest. If this test
        // breaks, that Go struct's json tags likely drifted out of sync.
        let jsonLine = r#"{"clientAccountIdentifier":"acct-001","instrumentSymbol":"DEMO-EQ","orderSideIsBuyNotSell":true,"limitPriceInMinorUnits":10000,"orderQuantity":5}"#;

        let parsed: IncomingOrderWireRequest =
            serde_json::from_str(jsonLine).expect("should parse oms-gateway-shaped JSON");
        assert_eq!(parsed.clientAccountIdentifier, "acct-001");
        assert_eq!(parsed.orderQuantity, 5);
    }

    #[test]
    fn stopLossVariantFlagsConvertToStopOrderTypesWithTriggerPrice() {
        let stopLimitWireRequest = IncomingOrderWireRequest {
            clientAccountIdentifier: "acct-001".into(),
            instrumentSymbol: "DEMO-EQ".into(),
            orderSideIsBuyNotSell: false,
            orderIsMarketOrderNotLimit: false,
            orderIsStopLossVariant: true,
            stopTriggerPriceInMinorUnits: Some(90),
            cancelOrderSequenceNumber: None,
            queryOrderStatusSequenceNumber: None,
            limitPriceInMinorUnits: 85,
            orderQuantity: 5,
        };
        let internalStopLimit = stopLimitWireRequest.intoInternalOrderRequest();
        assert_eq!(internalStopLimit.orderType, OrderType::StopLossLimit);
        assert_eq!(internalStopLimit.stopTriggerPriceInMinorUnits, Some(90));

        let stopMarketWireRequest = IncomingOrderWireRequest {
            clientAccountIdentifier: "acct-001".into(),
            instrumentSymbol: "DEMO-EQ".into(),
            orderSideIsBuyNotSell: false,
            orderIsMarketOrderNotLimit: true,
            orderIsStopLossVariant: true,
            stopTriggerPriceInMinorUnits: Some(90),
            cancelOrderSequenceNumber: None,
            queryOrderStatusSequenceNumber: None,
            limitPriceInMinorUnits: 0,
            orderQuantity: 5,
        };
        let internalStopMarket = stopMarketWireRequest.intoInternalOrderRequest();
        assert_eq!(internalStopMarket.orderType, OrderType::StopLossMarket);
    }

    #[test]
    fn omittedStopLossFlagsDefaultToNonStopForBackwardCompatibility() {
        // No "orderIsStopLossVariant"/"stopTriggerPriceInMinorUnits" keys
        // at all — must still parse cleanly for any client that predates
        // stop-order support.
        let jsonLine = r#"{"clientAccountIdentifier":"acct-001","instrumentSymbol":"DEMO-EQ","orderSideIsBuyNotSell":true,"limitPriceInMinorUnits":10000,"orderQuantity":5}"#;

        let parsed: IncomingOrderWireRequest =
            serde_json::from_str(jsonLine).expect("should parse even without the newer fields");
        let internalRequest = parsed.intoInternalOrderRequest();
        assert_eq!(internalRequest.orderType, OrderType::Limit);
        assert_eq!(internalRequest.stopTriggerPriceInMinorUnits, None);
    }

    #[test]
    fn cancelOrderSequenceNumberDeserializesFromWireJson() {
        let jsonLine = r#"{"clientAccountIdentifier":"acct-001","instrumentSymbol":"DEMO-EQ","orderSideIsBuyNotSell":true,"cancelOrderSequenceNumber":7,"limitPriceInMinorUnits":0,"orderQuantity":0}"#;

        let parsed: IncomingOrderWireRequest =
            serde_json::from_str(jsonLine).expect("should parse a cancel request");
        assert_eq!(parsed.cancelOrderSequenceNumber, Some(7));
    }

    #[test]
    fn omittedCancelFieldDefaultsToNoneMeaningNotACancelRequest() {
        let jsonLine = r#"{"clientAccountIdentifier":"acct-001","instrumentSymbol":"DEMO-EQ","orderSideIsBuyNotSell":true,"limitPriceInMinorUnits":10000,"orderQuantity":5}"#;

        let parsed: IncomingOrderWireRequest =
            serde_json::from_str(jsonLine).expect("should parse without the cancel field");
        assert_eq!(parsed.cancelOrderSequenceNumber, None);
    }

    #[test]
    fn successResponseCarriesTheAssignedOrderSequenceNumberCancelResponseDoesNot() {
        let successWireResponse = OrderSubmissionWireResponse::successResponse(Vec::new(), 42);
        assert_eq!(successWireResponse.assignedOrderSequenceNumber, Some(42));
        assert_eq!(successWireResponse.wasOrderCancelled, None);

        let cancelWireResponse = OrderSubmissionWireResponse::cancelResponse(true);
        assert_eq!(cancelWireResponse.wasOrderCancelled, Some(true));
        assert_eq!(cancelWireResponse.assignedOrderSequenceNumber, None);
    }

    #[test]
    fn queryOrderStatusSequenceNumberDeserializesFromWireJson() {
        let jsonLine = r#"{"clientAccountIdentifier":"acct-001","instrumentSymbol":"DEMO-EQ","orderSideIsBuyNotSell":true,"queryOrderStatusSequenceNumber":9,"limitPriceInMinorUnits":0,"orderQuantity":0}"#;

        let parsed: IncomingOrderWireRequest =
            serde_json::from_str(jsonLine).expect("should parse a status query");
        assert_eq!(parsed.queryOrderStatusSequenceNumber, Some(9));
    }

    #[test]
    fn omittedQueryFieldDefaultsToNoneForBackwardCompatibility() {
        let jsonLine = r#"{"clientAccountIdentifier":"acct-001","instrumentSymbol":"DEMO-EQ","orderSideIsBuyNotSell":true,"limitPriceInMinorUnits":10000,"orderQuantity":5}"#;

        let parsed: IncomingOrderWireRequest =
            serde_json::from_str(jsonLine).expect("should parse without the status-query field");
        assert_eq!(parsed.queryOrderStatusSequenceNumber, None);
    }

    #[test]
    fn statusResponseEncodesEachQueryResultVariantCorrectly() {
        use crate::orderTypes::{OrderSide, OrderStatusQueryResult};

        let restingResponse =
            OrderSubmissionWireResponse::statusResponse(OrderStatusQueryResult::RestingLimit {
                orderSide: OrderSide::Buy,
                limitPriceInMinorUnits: 100,
                remainingQuantity: 3,
            });
        assert_eq!(
            restingResponse.orderStatus,
            Some("RESTING_LIMIT".to_string())
        );
        assert_eq!(restingResponse.orderStatusSideIsBuyNotSell, Some(true));
        assert_eq!(restingResponse.orderStatusPriceInMinorUnits, Some(100));
        assert_eq!(restingResponse.orderStatusQuantity, Some(3));

        let pendingResponse =
            OrderSubmissionWireResponse::statusResponse(OrderStatusQueryResult::PendingStop {
                orderSide: OrderSide::Sell,
                stopTriggerPriceInMinorUnits: 90,
                orderQuantity: 5,
            });
        assert_eq!(
            pendingResponse.orderStatus,
            Some("PENDING_STOP".to_string())
        );
        assert_eq!(pendingResponse.orderStatusSideIsBuyNotSell, Some(false));

        let notFoundResponse =
            OrderSubmissionWireResponse::statusResponse(OrderStatusQueryResult::NotFound);
        assert_eq!(notFoundResponse.orderStatus, Some("NOT_FOUND".to_string()));
        assert_eq!(notFoundResponse.orderStatusQuantity, None);
    }
}
