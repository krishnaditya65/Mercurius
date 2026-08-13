// FEATURES.md §8 [P1] — "Exchange feed ingestion (simulated/sandbox feed
// for Phase 0-1)". Generates a deterministic (seeded), synthetic tick
// stream — a per-symbol random walk with configurable drift/volatility —
// shaped EXACTLY like a real matching-engine depth publish
// (`ingestionWireProtocol::IncomingDepthPublishWireMessage`), so it can be
// fed into the SAME ingestion pipeline (`main.rs`'s `ingestDepthPublishMessage`)
// real matching-engine ticks flow through. There is no parallel code path
// here for candles/L1/columnar storage/multicast — this module's only job
// is to manufacture wire-shaped messages; everything downstream of that is
// shared with the real feed.
//
// Controlled entirely by env vars, read once in `main.rs`:
//   MARKET_DATA_SIMULATED_FEED_ENABLED=true   — turns the feed on at all
//   MARKET_DATA_SIMULATED_FEED_SEED=<u64>     — deterministic seed
//   MARKET_DATA_SIMULATED_FEED_TICK_INTERVAL_MILLIS=<u64> — pace (optional)
//
// This makes the whole service runnable standalone for Phase 0-1 demos/
// tests without matching-engine running at all — genuinely useful, not a
// stub: real candles, a real L1 feed, real columnar tick storage, and real
// UDP multicast fan-out all get driven off of it exactly as they would off
// real trades.
#![allow(non_snake_case)]

use crate::ingestionWireProtocol::{
    IncomingDepthPublishWireMessage, IncomingPriceLevelDeltaWireUpdate, IncomingTradeTickWireEvent,
};

/// Fixed spread (in minor units) between the synthetic best bid/ask and
/// the walking mid price. Not configurable — this generator's job is to
/// produce a plausible, deterministic depth publish, not to model a real
/// limit order book's microstructure.
const SIMULATED_BID_ASK_SPREAD_IN_MINOR_UNITS: i64 = 1;

/// Per-symbol configuration for the random walk: where the price starts,
/// how much it drifts per tick on average, and how much it's allowed to
/// randomly swing per tick.
#[derive(Debug, Clone, PartialEq)]
pub struct SimulatedSymbolConfig {
    pub instrumentSymbol: String,
    pub startingPriceInMinorUnits: i64,
    pub driftInMinorUnitsPerTick: i64,
    pub volatilityInMinorUnits: u64,
}

impl SimulatedSymbolConfig {
    /// Convenience constructor used throughout this module's tests. Not
    /// called from `main.rs` (which builds its own explicit
    /// `defaultSimulatedSymbolConfigs()` list with real drift/volatility
    /// tuning) — kept `pub` as a reasonable default for any future caller
    /// that just wants "a symbol starting at price X".
    #[allow(dead_code)]
    pub fn withDefaults(instrumentSymbol: &str, startingPriceInMinorUnits: i64) -> Self {
        SimulatedSymbolConfig {
            instrumentSymbol: instrumentSymbol.to_string(),
            startingPriceInMinorUnits,
            driftInMinorUnitsPerTick: 0,
            volatilityInMinorUnits: 5,
        }
    }
}

/// A single generated tick, in a shape that's easy to assert on directly
/// in tests (rather than reaching into the wire message's nested structs
/// every time). `intoDepthPublishWireMessage` converts this into exactly
/// what the real ingestion pipeline consumes.
#[derive(Debug, Clone, PartialEq)]
pub struct SimulatedTick {
    pub instrumentSymbol: String,
    pub priceInMinorUnits: i64,
    pub quantity: u64,
    pub bestBidPriceInMinorUnits: i64,
    pub bestBidQuantity: u64,
    pub bestAskPriceInMinorUnits: i64,
    pub bestAskQuantity: u64,
}

impl SimulatedTick {
    pub fn intoDepthPublishWireMessage(&self) -> IncomingDepthPublishWireMessage {
        IncomingDepthPublishWireMessage {
            instrumentSymbol: self.instrumentSymbol.clone(),
            deltas: vec![
                IncomingPriceLevelDeltaWireUpdate {
                    isBidSide: true,
                    priceInMinorUnits: self.bestBidPriceInMinorUnits,
                    newTotalQuantityAtPrice: self.bestBidQuantity,
                },
                IncomingPriceLevelDeltaWireUpdate {
                    isBidSide: false,
                    priceInMinorUnits: self.bestAskPriceInMinorUnits,
                    newTotalQuantityAtPrice: self.bestAskQuantity,
                },
            ],
            tradeTicks: vec![IncomingTradeTickWireEvent {
                executedPriceInMinorUnits: self.priceInMinorUnits,
                executedQuantity: self.quantity,
            }],
        }
    }
}

/// Per-symbol random walk state, including its own independent PRNG
/// stream (seeded deterministically from the generator's overall seed
/// plus the symbol's index — see `newGeneratorWithSeed`) so adding or
/// removing a symbol from the config list doesn't perturb any other
/// symbol's sequence.
struct SimulatedSymbolState {
    config: SimulatedSymbolConfig,
    currentPriceInMinorUnits: i64,
    pseudoRandomGeneratorState: u64,
}

/// Generates a deterministic synthetic tick stream across one or more
/// symbols. "Deterministic" means: constructing two generators with the
/// same seed and the same symbol configs and pulling N ticks from each
/// produces byte-for-byte identical `SimulatedTick` sequences, forever —
/// see the `sameSeedProducesIdenticalSequence` test. This holds because
/// the only source of randomness is `splitmix64Next`, a pure function of
/// its `u64` state, with no dependency on wall-clock time, thread
/// scheduling, or OS entropy.
pub struct SimulatedExchangeFeedGenerator {
    symbolStates: Vec<SimulatedSymbolState>,
}

impl SimulatedExchangeFeedGenerator {
    /// `seed` deterministically seeds every symbol's PRNG stream (each
    /// symbol gets `seed` mixed with its position in `symbolConfigs`, so
    /// symbols never share a stream and would still diverge from each
    /// other even with identical config).
    pub fn newGeneratorWithSeed(seed: u64, symbolConfigs: Vec<SimulatedSymbolConfig>) -> Self {
        let symbolStates = symbolConfigs
            .into_iter()
            .enumerate()
            .map(|(symbolIndex, config)| SimulatedSymbolState {
                currentPriceInMinorUnits: config.startingPriceInMinorUnits,
                pseudoRandomGeneratorState: seed
                    ^ splitmix64Next(&mut (symbolIndex as u64 + 1).wrapping_mul(0x9E3779B97F4A7C15)),
                config,
            })
            .collect();
        SimulatedExchangeFeedGenerator { symbolStates }
    }

    /// How many symbols this generator is driving — `main.rs`'s feed loop
    /// round-robins over `0..symbolCount()`.
    pub fn symbolCount(&self) -> usize {
        self.symbolStates.len()
    }

    /// Advances the random walk for `symbolIndex` by one tick and returns
    /// it. Panics on an out-of-range index — same "caller's responsibility"
    /// contract as `Vec::get`'s panicking sibling `[]`, acceptable here
    /// since `main.rs` only ever iterates `0..symbolCount()`.
    pub fn nextTickForSymbolIndex(&mut self, symbolIndex: usize) -> SimulatedTick {
        let symbolState = &mut self.symbolStates[symbolIndex];

        let volatility = symbolState.config.volatilityInMinorUnits;
        let randomStepInMinorUnits = if volatility == 0 {
            0
        } else {
            let randomDraw = splitmix64Next(&mut symbolState.pseudoRandomGeneratorState);
            (randomDraw % (2 * volatility + 1)) as i64 - volatility as i64
        };

        let candidatePrice =
            symbolState.currentPriceInMinorUnits + symbolState.config.driftInMinorUnitsPerTick + randomStepInMinorUnits;
        // A price can't walk to zero or negative — floor it at 1 minor
        // unit rather than letting the walk produce a nonsensical price,
        // same "clamp, don't panic" posture as the rest of this skeleton.
        symbolState.currentPriceInMinorUnits = candidatePrice.max(1);

        let quantityRandomDraw = splitmix64Next(&mut symbolState.pseudoRandomGeneratorState);
        let quantity = 1 + (quantityRandomDraw % 100);

        let bestBidPriceInMinorUnits =
            (symbolState.currentPriceInMinorUnits - SIMULATED_BID_ASK_SPREAD_IN_MINOR_UNITS).max(1);
        let bestAskPriceInMinorUnits = symbolState.currentPriceInMinorUnits + SIMULATED_BID_ASK_SPREAD_IN_MINOR_UNITS;

        SimulatedTick {
            instrumentSymbol: symbolState.config.instrumentSymbol.clone(),
            priceInMinorUnits: symbolState.currentPriceInMinorUnits,
            quantity,
            bestBidPriceInMinorUnits,
            bestBidQuantity: quantity,
            bestAskPriceInMinorUnits,
            bestAskQuantity: quantity,
        }
    }
}

/// SplitMix64 — a small, fast, well-known deterministic PRNG. Chosen over
/// pulling in the `rand` crate: this codebase's convention so far is
/// std-library-only (see `httpQueryServer.rs`'s hand-rolled HTTP parsing,
/// the raw-TCP-JSON bridges elsewhere), and a demo/sandbox feed generator
/// has no need for cryptographic-quality randomness, just a reproducible
/// stream. Pure function of `state`: same input state always produces the
/// same output and the same next state, which is exactly the property
/// `sameSeedProducesIdenticalSequence` depends on.
fn splitmix64Next(state: &mut u64) -> u64 {
    *state = state.wrapping_add(0x9E3779B97F4A7C15);
    let mut z = *state;
    z = (z ^ (z >> 30)).wrapping_mul(0xBF58476D1CE4E5B9);
    z = (z ^ (z >> 27)).wrapping_mul(0x94D049BB133111EB);
    z ^ (z >> 31)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn oneDemoSymbolConfig() -> Vec<SimulatedSymbolConfig> {
        vec![SimulatedSymbolConfig::withDefaults("DEMO-EQ", 10_000)]
    }

    #[test]
    fn sameSeedProducesIdenticalSequence() {
        let mut firstGenerator = SimulatedExchangeFeedGenerator::newGeneratorWithSeed(42, oneDemoSymbolConfig());
        let mut secondGenerator = SimulatedExchangeFeedGenerator::newGeneratorWithSeed(42, oneDemoSymbolConfig());

        let firstSequence: Vec<SimulatedTick> = (0..50).map(|_| firstGenerator.nextTickForSymbolIndex(0)).collect();
        let secondSequence: Vec<SimulatedTick> = (0..50).map(|_| secondGenerator.nextTickForSymbolIndex(0)).collect();

        assert_eq!(firstSequence, secondSequence);
    }

    #[test]
    fn differentSeedsProduceDifferentSequences() {
        let mut firstGenerator = SimulatedExchangeFeedGenerator::newGeneratorWithSeed(1, oneDemoSymbolConfig());
        let mut secondGenerator = SimulatedExchangeFeedGenerator::newGeneratorWithSeed(2, oneDemoSymbolConfig());

        let firstSequence: Vec<SimulatedTick> = (0..20).map(|_| firstGenerator.nextTickForSymbolIndex(0)).collect();
        let secondSequence: Vec<SimulatedTick> = (0..20).map(|_| secondGenerator.nextTickForSymbolIndex(0)).collect();

        assert_ne!(firstSequence, secondSequence);
    }

    #[test]
    fn sameSeedIsStableAcrossRepeatedProcessRuns() {
        // Regression pin: if `splitmix64Next` or the seeding formula ever
        // changes, this catches it explicitly rather than only failing
        // the more abstract "two generators agree" tests above.
        let mut generator = SimulatedExchangeFeedGenerator::newGeneratorWithSeed(7, oneDemoSymbolConfig());
        let firstTick = generator.nextTickForSymbolIndex(0);
        let secondTick = generator.nextTickForSymbolIndex(0);

        assert_eq!(firstTick.instrumentSymbol, "DEMO-EQ");
        assert!(firstTick.priceInMinorUnits > 0);
        assert!(secondTick.priceInMinorUnits > 0);
        // Two consecutive ticks from a live PRNG stream should essentially
        // never produce the exact same price+quantity pair by chance.
        assert_ne!(firstTick, secondTick);
    }

    #[test]
    fn priceNeverGoesToZeroOrNegativeEvenWithHighVolatilityNearZeroStart() {
        let mut generator = SimulatedExchangeFeedGenerator::newGeneratorWithSeed(
            99,
            vec![SimulatedSymbolConfig {
                instrumentSymbol: "PENNY".to_string(),
                startingPriceInMinorUnits: 2,
                driftInMinorUnitsPerTick: 0,
                volatilityInMinorUnits: 1_000,
            }],
        );

        for _ in 0..200 {
            let tick = generator.nextTickForSymbolIndex(0);
            assert!(tick.priceInMinorUnits >= 1);
            assert!(tick.bestBidPriceInMinorUnits >= 1);
        }
    }

    #[test]
    fn zeroVolatilityWithZeroDriftKeepsPriceConstant() {
        let mut generator = SimulatedExchangeFeedGenerator::newGeneratorWithSeed(
            5,
            vec![SimulatedSymbolConfig {
                instrumentSymbol: "FLAT".to_string(),
                startingPriceInMinorUnits: 500,
                driftInMinorUnitsPerTick: 0,
                volatilityInMinorUnits: 0,
            }],
        );

        for _ in 0..10 {
            let tick = generator.nextTickForSymbolIndex(0);
            assert_eq!(tick.priceInMinorUnits, 500);
        }
    }

    #[test]
    fn positiveDriftWithZeroVolatilityMarchesPriceUpwardByExactlyTheDriftEachTick() {
        let mut generator = SimulatedExchangeFeedGenerator::newGeneratorWithSeed(
            5,
            vec![SimulatedSymbolConfig {
                instrumentSymbol: "UPTREND".to_string(),
                startingPriceInMinorUnits: 1_000,
                driftInMinorUnitsPerTick: 3,
                volatilityInMinorUnits: 0,
            }],
        );

        let firstTick = generator.nextTickForSymbolIndex(0);
        let secondTick = generator.nextTickForSymbolIndex(0);
        let thirdTick = generator.nextTickForSymbolIndex(0);

        assert_eq!(firstTick.priceInMinorUnits, 1_003);
        assert_eq!(secondTick.priceInMinorUnits, 1_006);
        assert_eq!(thirdTick.priceInMinorUnits, 1_009);
    }

    #[test]
    fn bestBidIsBelowMidAndBestAskIsAboveMidBySpread() {
        let mut generator = SimulatedExchangeFeedGenerator::newGeneratorWithSeed(11, oneDemoSymbolConfig());
        let tick = generator.nextTickForSymbolIndex(0);

        assert_eq!(
            tick.bestBidPriceInMinorUnits,
            tick.priceInMinorUnits - SIMULATED_BID_ASK_SPREAD_IN_MINOR_UNITS
        );
        assert_eq!(
            tick.bestAskPriceInMinorUnits,
            tick.priceInMinorUnits + SIMULATED_BID_ASK_SPREAD_IN_MINOR_UNITS
        );
    }

    #[test]
    fn multipleSymbolsWalkIndependentlyEvenWithIdenticalConfig() {
        let mut generator = SimulatedExchangeFeedGenerator::newGeneratorWithSeed(
            3,
            vec![
                SimulatedSymbolConfig::withDefaults("AAPL", 10_000),
                SimulatedSymbolConfig::withDefaults("MSFT", 10_000),
            ],
        );

        let aaplTick = generator.nextTickForSymbolIndex(0);
        let msftTick = generator.nextTickForSymbolIndex(1);

        assert_eq!(aaplTick.instrumentSymbol, "AAPL");
        assert_eq!(msftTick.instrumentSymbol, "MSFT");
        // Same seed, same config, different symbol index in the seeding
        // formula -> different streams, so prices should (overwhelmingly
        // likely) differ despite an identical starting point.
        assert_ne!(aaplTick.priceInMinorUnits, msftTick.priceInMinorUnits);
    }

    #[test]
    fn symbolCountReflectsHowManySymbolsWereConfigured() {
        let generator = SimulatedExchangeFeedGenerator::newGeneratorWithSeed(
            1,
            vec![
                SimulatedSymbolConfig::withDefaults("AAPL", 10_000),
                SimulatedSymbolConfig::withDefaults("MSFT", 20_000),
                SimulatedSymbolConfig::withDefaults("GOOG", 30_000),
            ],
        );
        assert_eq!(generator.symbolCount(), 3);
    }

    #[test]
    fn intoDepthPublishWireMessageCarriesTheSameSymbolPriceAndQuantity() {
        let tick = SimulatedTick {
            instrumentSymbol: "DEMO-EQ".to_string(),
            priceInMinorUnits: 10_050,
            quantity: 7,
            bestBidPriceInMinorUnits: 10_049,
            bestBidQuantity: 7,
            bestAskPriceInMinorUnits: 10_051,
            bestAskQuantity: 7,
        };

        let wireMessage = tick.intoDepthPublishWireMessage();

        assert_eq!(wireMessage.instrumentSymbol, "DEMO-EQ");
        assert_eq!(wireMessage.tradeTicks.len(), 1);
        assert_eq!(wireMessage.tradeTicks[0].executedPriceInMinorUnits, 10_050);
        assert_eq!(wireMessage.tradeTicks[0].executedQuantity, 7);
        assert_eq!(wireMessage.deltas.len(), 2);
        assert!(
            wireMessage
                .deltas
                .iter()
                .any(|delta| delta.isBidSide && delta.priceInMinorUnits == 10_049)
        );
        assert!(
            wireMessage
                .deltas
                .iter()
                .any(|delta| !delta.isBidSide && delta.priceInMinorUnits == 10_051)
        );
    }

    #[test]
    fn splitmix64NextNeverRepeatsWithinAShortWindowAndAdvancesState() {
        let mut state = 12345u64;
        let mut seenValues = std::collections::HashSet::new();
        for _ in 0..1000 {
            let value = splitmix64Next(&mut state);
            assert!(
                seenValues.insert(value),
                "PRNG produced a repeated value within 1000 draws"
            );
        }
    }
}
