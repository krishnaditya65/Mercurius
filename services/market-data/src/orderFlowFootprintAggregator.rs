// FEATURES.md §20 "[P3] Order-flow footprint charts (bid/ask volume per
// price per candle)" — given the real trade tape (with real aggressor-
// side information, see `TickRecord::isBuyAggressor` and the additive
// wire-protocol extension documented in `columnarTickStore.rs`/
// `ingestionWireProtocol.rs`), computes real buy-volume vs. sell-volume
// per price level within each fixed-width candle interval — exactly what
// a real footprint chart renders as its per-cell "buy x sell" numbers.
//
// Pure, stateless, same posture as `volumeProfileAggregator.rs`: operates
// on whatever `TickRecord`s the caller already pulled from
// `ColumnarTickStore::rangeQuery`, no new mutable state of its own.
#![allow(non_snake_case)]

use std::collections::BTreeMap;

use crate::columnarTickStore::TickRecord;

/// One price level's buy-vs-sell split within one candle — the actual
/// "cell" a footprint chart renders.
#[derive(Debug, Clone, PartialEq, serde::Serialize)]
pub struct FootprintPriceLevel {
    pub priceBucketStart: i64,
    pub buyVolume: u64,
    pub sellVolume: u64,
}

/// One candle's full footprint: every price level touched during that
/// candle, plus the level with the largest total (buy+sell) volume — the
/// "point of control" for that single candle, analogous to (but distinct
/// from) the Volume Profile's POC over an entire window.
#[derive(Debug, Clone, PartialEq, serde::Serialize)]
pub struct CandleFootprint {
    pub candleStartEpochSeconds: u64,
    pub levels: Vec<FootprintPriceLevel>,
    pub totalBuyVolume: u64,
    pub totalSellVolume: u64,
}

/// Buckets `ticks` into fixed `candleIntervalSeconds`-wide candles
/// (candle start = `timestamp - timestamp % candleIntervalSeconds`, an
/// absolute-wall-clock bucketing — deliberately the SAME convention
/// `candleAggregator::CandleAggregator` already uses for its OHLCV
/// candles, so a footprint candle and an OHLCV candle for the same
/// `candleIntervalSeconds` line up on the same x-axis), and within each
/// candle, into `priceBucketSizeInMinorUnits`-wide price levels — same
/// fixed-grid bucketing function as `volumeProfileAggregator::
/// computeVolumeProfile` uses, so a price level here means the same thing
/// it means there. Each tick's quantity is added to `buyVolume` when
/// `isBuyAggressor` is `true`, `sellVolume` otherwise — genuinely reusing
/// the real aggressor flag captured all the way from matching-engine's
/// `TradeExecutionEvent`, not inferred or guessed.
///
/// Both size parameters must be `> 0`; either being non-positive (or an
/// empty tick list) produces an empty result rather than panicking/
/// dividing by zero, same posture as `volumeProfileAggregator.rs`.
pub fn computeOrderFlowFootprint(
    ticks: &[TickRecord],
    priceBucketSizeInMinorUnits: i64,
    candleIntervalSeconds: u64,
) -> Vec<CandleFootprint> {
    if priceBucketSizeInMinorUnits <= 0 || candleIntervalSeconds == 0 {
        return Vec::new();
    }

    // candleStart -> priceBucket -> (buyVolume, sellVolume)
    let mut footprintsByCandle: BTreeMap<u64, BTreeMap<i64, (u64, u64)>> = BTreeMap::new();

    for tick in ticks {
        let candleStart = (tick.executedAtEpochSeconds / candleIntervalSeconds) * candleIntervalSeconds;
        let priceBucket = bucketStartForPrice(tick.priceInMinorUnits, priceBucketSizeInMinorUnits);
        let levelEntry = footprintsByCandle
            .entry(candleStart)
            .or_default()
            .entry(priceBucket)
            .or_insert((0, 0));
        if tick.isBuyAggressor {
            levelEntry.0 += tick.quantity;
        } else {
            levelEntry.1 += tick.quantity;
        }
    }

    footprintsByCandle
        .into_iter()
        .map(|(candleStartEpochSeconds, levelsByPrice)| {
            let levels: Vec<FootprintPriceLevel> = levelsByPrice
                .into_iter()
                .map(|(priceBucketStart, (buyVolume, sellVolume))| FootprintPriceLevel {
                    priceBucketStart,
                    buyVolume,
                    sellVolume,
                })
                .collect();
            let totalBuyVolume = levels.iter().map(|level| level.buyVolume).sum();
            let totalSellVolume = levels.iter().map(|level| level.sellVolume).sum();
            CandleFootprint {
                candleStartEpochSeconds,
                levels,
                totalBuyVolume,
                totalSellVolume,
            }
        })
        .collect()
}

fn bucketStartForPrice(priceInMinorUnits: i64, bucketSize: i64) -> i64 {
    priceInMinorUnits - priceInMinorUnits.rem_euclid(bucketSize)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn tick(priceInMinorUnits: i64, quantity: u64, executedAtEpochSeconds: u64, isBuyAggressor: bool) -> TickRecord {
        TickRecord {
            instrumentSymbol: "DEMO-EQ".to_string(),
            executedAtEpochSeconds,
            priceInMinorUnits,
            quantity,
            isBuyAggressor,
        }
    }

    // HAND-WORKED FIXTURE used by several tests: candle width 60s, price
    // bucket 10. Ticks, all within candle [960, 1020):
    //   (t=1000, price=100, qty=5, BUY)
    //   (t=1005, price=100, qty=3, SELL)
    //   (t=1010, price=110, qty=2, BUY)
    //   (t=1015, price=110, qty=7, SELL)
    // -> one candle, candleStartEpochSeconds=960:
    //      level 100: buy=5, sell=3
    //      level 110: buy=2, sell=7
    //      totalBuy = 7, totalSell = 10

    fn handWorkedFixtureTicks() -> Vec<TickRecord> {
        vec![
            tick(100, 5, 1_000, true),
            tick(100, 3, 1_005, false),
            tick(110, 2, 1_010, true),
            tick(110, 7, 1_015, false),
        ]
    }

    #[test]
    fn emptyTickListProducesNoCandles() {
        assert!(computeOrderFlowFootprint(&[], 10, 60).is_empty());
    }

    #[test]
    fn nonPositiveBucketSizeProducesEmptyResultRatherThanPanicking() {
        let ticks = handWorkedFixtureTicks();
        assert!(computeOrderFlowFootprint(&ticks, 0, 60).is_empty());
        assert!(computeOrderFlowFootprint(&ticks, -5, 60).is_empty());
    }

    #[test]
    fn zeroCandleIntervalProducesEmptyResultRatherThanDividingByZero() {
        let ticks = handWorkedFixtureTicks();
        assert!(computeOrderFlowFootprint(&ticks, 10, 0).is_empty());
    }

    #[test]
    fn handWorkedFixtureProducesExactlyOneCandleWithTheRightBucketStart() {
        let footprints = computeOrderFlowFootprint(&handWorkedFixtureTicks(), 10, 60);
        assert_eq!(footprints.len(), 1);
        assert_eq!(footprints[0].candleStartEpochSeconds, 960);
    }

    #[test]
    fn handWorkedFixtureLevelsMatchManualCalculation() {
        let footprints = computeOrderFlowFootprint(&handWorkedFixtureTicks(), 10, 60);
        let levels = &footprints[0].levels;
        assert_eq!(levels.len(), 2);
        assert_eq!(
            levels[0],
            FootprintPriceLevel {
                priceBucketStart: 100,
                buyVolume: 5,
                sellVolume: 3
            }
        );
        assert_eq!(
            levels[1],
            FootprintPriceLevel {
                priceBucketStart: 110,
                buyVolume: 2,
                sellVolume: 7
            }
        );
    }

    #[test]
    fn handWorkedFixtureCandleTotalsMatchManualCalculation() {
        let footprints = computeOrderFlowFootprint(&handWorkedFixtureTicks(), 10, 60);
        assert_eq!(footprints[0].totalBuyVolume, 7);
        assert_eq!(footprints[0].totalSellVolume, 10);
    }

    #[test]
    fn ticksInDifferentCandlesProduceSeparateCandleFootprints() {
        let ticks = vec![
            tick(100, 5, 1_000, true), // candle 960
            tick(100, 5, 1_100, true), // candle 1080 — new candle
        ];
        let footprints = computeOrderFlowFootprint(&ticks, 10, 60);
        assert_eq!(footprints.len(), 2);
        assert_eq!(footprints[0].candleStartEpochSeconds, 960);
        assert_eq!(footprints[1].candleStartEpochSeconds, 1_080);
    }

    #[test]
    fn candlesAreSortedAscendingByStartTime() {
        let ticks = vec![
            tick(100, 1, 1_200, true),
            tick(100, 1, 1_000, true),
            tick(100, 1, 1_100, true),
        ];
        let footprints = computeOrderFlowFootprint(&ticks, 10, 60);
        let starts: Vec<u64> = footprints.iter().map(|f| f.candleStartEpochSeconds).collect();
        assert_eq!(starts, vec![960, 1_080, 1_200]);
    }

    #[test]
    fn levelsWithinACandleAreSortedAscendingByPrice() {
        let ticks = vec![
            tick(300, 1, 1_000, true),
            tick(100, 1, 1_005, true),
            tick(200, 1, 1_010, true),
        ];
        let footprints = computeOrderFlowFootprint(&ticks, 100, 60);
        let prices: Vec<i64> = footprints[0]
            .levels
            .iter()
            .map(|level| level.priceBucketStart)
            .collect();
        assert_eq!(prices, vec![100, 200, 300]);
    }

    #[test]
    fn allBuyVolumeAtALevelLeavesSellVolumeAtZero() {
        let ticks = vec![tick(100, 5, 1_000, true), tick(100, 3, 1_005, true)];
        let footprints = computeOrderFlowFootprint(&ticks, 10, 60);
        assert_eq!(
            footprints[0].levels[0],
            FootprintPriceLevel {
                priceBucketStart: 100,
                buyVolume: 8,
                sellVolume: 0
            }
        );
    }

    #[test]
    fn allSellVolumeAtALevelLeavesBuyVolumeAtZero() {
        let ticks = vec![tick(100, 5, 1_000, false), tick(100, 3, 1_005, false)];
        let footprints = computeOrderFlowFootprint(&ticks, 10, 60);
        assert_eq!(
            footprints[0].levels[0],
            FootprintPriceLevel {
                priceBucketStart: 100,
                buyVolume: 0,
                sellVolume: 8
            }
        );
    }

    #[test]
    fn ticksAtExactPriceBucketBoundaryBelongToTheLowerBucket() {
        let ticks = vec![tick(100, 1, 1_000, true), tick(109, 1, 1_005, false)];
        let footprints = computeOrderFlowFootprint(&ticks, 10, 60);
        assert_eq!(footprints[0].levels.len(), 1);
        assert_eq!(
            footprints[0].levels[0],
            FootprintPriceLevel {
                priceBucketStart: 100,
                buyVolume: 1,
                sellVolume: 1
            }
        );
    }
}
