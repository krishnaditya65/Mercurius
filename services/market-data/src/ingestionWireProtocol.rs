// The TCP JSON bridge FROM matching-engine INTO market-data. See
// ARCHITECTURE.md §5 — this replaces the in-process demo driver in
// main.rs as market-data's actual ingress. Same caveats as
// matching-engine's own wireProtocol.rs: a synchronous TCP+JSON
// connection-per-message bridge, not the real Kafka/Redpanda backbone.

use serde::Deserialize;

use crate::marketDataEventTypes::PriceLevelDeltaUpdate;

#[derive(Debug, Deserialize)]
pub struct IncomingPriceLevelDeltaWireUpdate {
    pub isBidSide: bool,
    pub priceInMinorUnits: i64,
    pub newTotalQuantityAtPrice: u64,
}

/// One executed trade as published by matching-engine. No timestamp field
/// — matching-engine doesn't stamp one (there's no shared clock/NTP
/// discipline set up in this skeleton), so market-data stamps
/// `executedAtEpochSeconds` itself on receipt in `main.rs`. TODO(real
/// build): the matching engine, not the downstream consumer, should be
/// the source of truth for execution time.
#[derive(Debug, Deserialize)]
pub struct IncomingTradeTickWireEvent {
    pub executedPriceInMinorUnits: i64,
    pub executedQuantity: u64,
}

/// One full depth publish from matching-engine, covering one instrument.
/// TODO(real build): matching-engine currently sends its FULL book depth
/// on every order, not an actual diff — see the TODO on
/// `OrderBookCore::currentBookDepthSnapshot`. market-data re-derives
/// per-instrument sequence numbers on receipt regardless (via
/// `DeltaPublisher`), so this is at least internally consistent, just not
/// bandwidth-efficient yet.
#[derive(Debug, Deserialize)]
pub struct IncomingDepthPublishWireMessage {
    pub instrumentSymbol: String,
    pub deltas: Vec<IncomingPriceLevelDeltaWireUpdate>,
    /// `#[serde(default)]` so this stays backward-compatible with any
    /// matching-engine build that predates trade-tick publishing —
    /// omitting the key just means "no trades this order produced."
    #[serde(default)]
    pub tradeTicks: Vec<IncomingTradeTickWireEvent>,
}

impl IncomingDepthPublishWireMessage {
    pub fn intoInternalDeltaUpdates(&self) -> Vec<PriceLevelDeltaUpdate> {
        self.deltas
            .iter()
            .map(|wireDelta| PriceLevelDeltaUpdate {
                instrumentSymbol: self.instrumentSymbol.clone(),
                isBidSide: wireDelta.isBidSide,
                priceInMinorUnits: wireDelta.priceInMinorUnits,
                newTotalQuantityAtPrice: wireDelta.newTotalQuantityAtPrice,
            })
            .collect()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn depthPublishWireMessageDeserializesFromMatchingEngineShapedJson() {
        // This exact shape is what matching-engine's main.rs sends — see
        // its publishBookDepthToMarketData function. If this test breaks,
        // the two sides' wire contracts likely drifted apart.
        let jsonLine = r#"{"instrumentSymbol":"DEMO-EQ","deltas":[{"isBidSide":true,"priceInMinorUnits":10000,"newTotalQuantityAtPrice":6}]}"#;

        let parsed: IncomingDepthPublishWireMessage =
            serde_json::from_str(jsonLine).expect("should parse matching-engine-shaped JSON");
        let internalUpdates = parsed.intoInternalDeltaUpdates();

        assert_eq!(internalUpdates.len(), 1);
        assert_eq!(internalUpdates[0].instrumentSymbol, "DEMO-EQ");
        assert_eq!(internalUpdates[0].priceInMinorUnits, 10000);
    }
}
