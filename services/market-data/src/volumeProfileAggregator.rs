// FEATURES.md §20 "[P3] Volume Profile / Market Profile (TPO) charts" —
// given the real trade tape held in `columnarTickStore.rs`, computes a
// real Volume Profile (volume traded per price bucket over a time window,
// with a real Point of Control and a real Value Area) and a real,
// simplified-but-genuinely-correct TPO (Time Price Opportunity) profile —
// the fixed-time-interval "letters" a real Market Profile chart is built
// from.
//
// This is a pure, stateless module: it operates on whatever
// `TickRecord`s the caller already pulled out of `ColumnarTickStore::
// rangeQuery` (see `httpQueryServer.rs`'s `GET /volumeProfile` route) —
// no new storage, no new mutable state, just real arithmetic over real
// trade ticks. `main.rs`'s ingestion path is untouched by this module.
#![allow(non_snake_case)]

use std::collections::BTreeMap;

use crate::columnarTickStore::TickRecord;

/// One price bucket's aggregated volume — the horizontal bar a real
/// Volume Profile chart renders at this price level.
#[derive(Debug, Clone, PartialEq, serde::Serialize)]
pub struct VolumeProfileLevel {
    pub priceBucketStart: i64,
    pub totalVolume: u64,
}

/// The full result of `computeVolumeProfile`: every touched price bucket
/// (ascending), the real Point of Control, and the real Value Area.
#[derive(Debug, Clone, PartialEq, serde::Serialize)]
pub struct VolumeProfileResult {
    pub levels: Vec<VolumeProfileLevel>,
    /// The price bucket with the single largest traded volume. `None`
    /// only when `levels` is empty (no ticks in range).
    pub pointOfControlPriceBucketStart: Option<i64>,
    /// Lower/upper bound (inclusive, bucket-start values) of the smallest
    /// contiguous-around-POC price range containing at least
    /// `valueAreaVolumeFraction` of total volume. `None` when `levels` is
    /// empty.
    pub valueAreaLowPriceBucketStart: Option<i64>,
    pub valueAreaHighPriceBucketStart: Option<i64>,
    pub totalVolume: u64,
}

/// Buckets every tick's price into `priceBucketSizeInMinorUnits`-wide
/// buckets (bucket start = `price - price.rem_euclid(bucketSize)`, so
/// buckets always land on multiples of the bucket size regardless of
/// where individual prices fall — the same "fixed grid" a real Volume
/// Profile chart uses, not a grid that shifts per-query), sums volume per
/// bucket, then derives:
///
///  - **Point of Control (POC)**: the bucket with the single largest
///    summed volume. Ties are broken by picking the LOWEST such price —
///    an arbitrary but deterministic and documented tie-break (real
///    charting platforms vary here too).
///  - **Value Area**: starting from the POC bucket, greedily grows the
///    contiguous price range outward — at each step adding whichever of
///    the next bucket immediately below the current low or immediately
///    above the current high has the larger volume (a tie again prefers
///    the lower price) — until the accumulated volume reaches at least
///    `valueAreaVolumeFraction` (typically `0.70`, i.e. 70%) of
///    `totalVolume`. This is the standard Market Profile Value Area
///    construction: it must be a single contiguous range containing the
///    POC, not just "the N most-traded buckets" (which could be
///    disjoint).
///
/// `priceBucketSizeInMinorUnits` must be `> 0`; a non-positive value
/// makes bucketing meaningless, so it's treated the same as "no ticks" —
/// an empty result — rather than panicking or dividing by zero.
pub fn computeVolumeProfile(
    ticks: &[TickRecord],
    priceBucketSizeInMinorUnits: i64,
    valueAreaVolumeFraction: f64,
) -> VolumeProfileResult {
    if priceBucketSizeInMinorUnits <= 0 {
        return emptyVolumeProfileResult();
    }

    let mut volumeByBucket: BTreeMap<i64, u64> = BTreeMap::new();
    for tick in ticks {
        let bucketStart = bucketStartForPrice(tick.priceInMinorUnits, priceBucketSizeInMinorUnits);
        *volumeByBucket.entry(bucketStart).or_insert(0) += tick.quantity;
    }

    if volumeByBucket.is_empty() {
        return emptyVolumeProfileResult();
    }

    let totalVolume: u64 = volumeByBucket.values().sum();

    // POC: largest volume, ties broken by lowest price (BTreeMap iterates
    // ascending, so the first max found when scanning in order is
    // naturally the lowest-priced one among ties).
    let pointOfControlPriceBucketStart = *volumeByBucket
        .iter()
        .max_by(|(priceA, volumeA), (priceB, volumeB)| volumeA.cmp(volumeB).then(priceB.cmp(priceA)))
        .map(|(price, _)| price)
        .expect("volumeByBucket is non-empty here");

    let (valueAreaLow, valueAreaHigh) = computeValueAreaRange(
        &volumeByBucket,
        pointOfControlPriceBucketStart,
        totalVolume,
        valueAreaVolumeFraction,
        priceBucketSizeInMinorUnits,
    );

    let levels = volumeByBucket
        .into_iter()
        .map(|(priceBucketStart, totalVolume)| VolumeProfileLevel {
            priceBucketStart,
            totalVolume,
        })
        .collect();

    VolumeProfileResult {
        levels,
        pointOfControlPriceBucketStart: Some(pointOfControlPriceBucketStart),
        valueAreaLowPriceBucketStart: Some(valueAreaLow),
        valueAreaHighPriceBucketStart: Some(valueAreaHigh),
        totalVolume,
    }
}

fn emptyVolumeProfileResult() -> VolumeProfileResult {
    VolumeProfileResult {
        levels: Vec::new(),
        pointOfControlPriceBucketStart: None,
        valueAreaLowPriceBucketStart: None,
        valueAreaHighPriceBucketStart: None,
        totalVolume: 0,
    }
}

fn bucketStartForPrice(priceInMinorUnits: i64, bucketSize: i64) -> i64 {
    priceInMinorUnits - priceInMinorUnits.rem_euclid(bucketSize)
}

/// Greedily grows a contiguous price range outward from `pocPrice` until
/// accumulated volume reaches `valueAreaVolumeFraction * totalVolume`
/// (rounded up — reaching "at least" the target, matching the standard
/// definition). Returns `(low, high)`, both inclusive bucket-start prices.
///
/// `bucketStep` MUST be the actual `priceBucketSizeInMinorUnits` the ticks
/// were bucketed with (threaded through from `computeVolumeProfile`'s own
/// parameter), NOT inferred from the minimum gap between occupied buckets:
/// when a bucket between the POC and the next occupied bucket carries zero
/// volume (so it's simply absent from `volumeByBucket`), the gap between
/// two occupied keys can be a multiple of the real bucket size, and
/// inferring the step from that gap silently understates it — walking the
/// wrong-sized step from then on skips over real, occupied buckets
/// entirely and can end the Value Area (or start it) at the wrong price.
fn computeValueAreaRange(
    volumeByBucket: &BTreeMap<i64, u64>,
    pocPrice: i64,
    totalVolume: u64,
    valueAreaVolumeFraction: f64,
    bucketStep: i64,
) -> (i64, i64) {
    let targetVolume = (totalVolume as f64 * valueAreaVolumeFraction).ceil() as u64;

    let mut lowPrice = pocPrice;
    let mut highPrice = pocPrice;
    let mut accumulatedVolume = *volumeByBucket.get(&pocPrice).unwrap_or(&0);

    while accumulatedVolume < targetVolume {
        // Walk outward in real `bucketStep` increments past any
        // intervening EMPTY (zero-volume, hence absent-from-the-map)
        // buckets until the next OCCUPIED bucket is found on that side,
        // or the map's own known extent is exceeded — rather than giving
        // up on a side after a single empty immediate neighbor, which
        // would wrongly treat "the next bucket over is empty" as "nothing
        // left on this side" even when a farther occupied bucket with
        // real volume exists.
        let belowCandidate = nextOccupiedBucketOutward(volumeByBucket, lowPrice, bucketStep, Direction::Down);
        let aboveCandidate = nextOccupiedBucketOutward(volumeByBucket, highPrice, bucketStep, Direction::Up);

        match (belowCandidate, aboveCandidate) {
            (None, None) => break, // nothing left to add on either side
            (Some((belowPrice, belowVolume)), None) => {
                lowPrice = belowPrice;
                accumulatedVolume += belowVolume;
            }
            (None, Some((abovePrice, aboveVolume))) => {
                highPrice = abovePrice;
                accumulatedVolume += aboveVolume;
            }
            (Some((belowPrice, belowVolume)), Some((abovePrice, aboveVolume))) => {
                // Larger volume wins; a tie prefers extending downward
                // (an arbitrary, documented, deterministic tie-break,
                // same posture as the POC tie-break above).
                if belowVolume >= aboveVolume {
                    lowPrice = belowPrice;
                    accumulatedVolume += belowVolume;
                } else {
                    highPrice = abovePrice;
                    accumulatedVolume += aboveVolume;
                }
            }
        }
    }

    (lowPrice, highPrice)
}

#[derive(Clone, Copy, PartialEq)]
enum Direction {
    Down,
    Up,
}

/// Steps outward from `currentEdgePrice` by `bucketStep` increments,
/// looking for the nearest OCCUPIED bucket strictly beyond it in the
/// given direction — skipping over any empty (absent-from-the-map)
/// buckets in between rather than stopping at the first one. Returns
/// `None` once stepping goes past the map's own lowest/highest occupied
/// key (there is nothing farther out to find).
fn nextOccupiedBucketOutward(
    volumeByBucket: &BTreeMap<i64, u64>,
    currentEdgePrice: i64,
    bucketStep: i64,
    direction: Direction,
) -> Option<(i64, u64)> {
    let boundaryKey = match direction {
        Direction::Down => *volumeByBucket.keys().next()?,
        Direction::Up => *volumeByBucket.keys().next_back()?,
    };

    let mut candidatePrice = match direction {
        Direction::Down => currentEdgePrice - bucketStep,
        Direction::Up => currentEdgePrice + bucketStep,
    };

    loop {
        let pastBoundary = match direction {
            Direction::Down => candidatePrice < boundaryKey,
            Direction::Up => candidatePrice > boundaryKey,
        };
        if pastBoundary {
            return None;
        }
        if let Some(&volume) = volumeByBucket.get(&candidatePrice) {
            return Some((candidatePrice, volume));
        }
        candidatePrice = match direction {
            Direction::Down => candidatePrice - bucketStep,
            Direction::Up => candidatePrice + bucketStep,
        };
    }
}

/// One "letter" (fixed time interval) of a TPO (Time Price Opportunity)
/// profile — a real Market Profile chart's building block. Each letter
/// represents one fixed-width time slice; `pricesTouchedThisInterval`
/// lists every price BUCKET that had at least one trade during that
/// slice (not volume — TPO is about time spent at a price, not volume
/// traded there, which is exactly what distinguishes it from a Volume
/// Profile).
#[derive(Debug, Clone, PartialEq, serde::Serialize)]
pub struct TpoLetter {
    pub letter: String,
    pub intervalStartEpochSeconds: u64,
    pub pricesTouchedThisInterval: Vec<i64>,
}

/// The full TPO profile: one letter per occupied time interval (in
/// chronological order, so `letters[0].letter == "A"`,
/// `letters[1].letter == "B"`, ... `letters[25].letter == "Z"`,
/// `letters[26].letter == "AA"`, and so on, mirroring the lettering
/// convention real Market Profile charts use), plus a per-price-bucket
/// TPO COUNT (how many distinct letters touched that price — this is
/// what a real TPO chart's horizontal histogram/POC is built from,
/// analogous to but distinct from the Volume Profile's POC).
#[derive(Debug, Clone, PartialEq, serde::Serialize)]
pub struct TpoProfileResult {
    pub letters: Vec<TpoLetter>,
    /// Ascending by price bucket.
    pub tpoCountsByPriceBucket: Vec<(i64, u32)>,
    /// The price bucket touched by the most distinct letters — the TPO
    /// profile's own Point of Control analogue (time-based, not
    /// volume-based). `None` when there are no ticks.
    pub tpoPointOfControlPriceBucketStart: Option<i64>,
}

/// Buckets ticks into fixed `tpoIntervalSeconds`-wide time intervals
/// (interval index = `executedAtEpochSeconds / tpoIntervalSeconds`,
/// relative to the FIRST tick's interval — i.e. letter "A" is always the
/// earliest occupied interval in `ticks`, not an absolute wall-clock
/// interval, matching how a real trading-session TPO chart's first
/// letter starts at session open, not at Unix epoch 0) and price into
/// `priceBucketSizeInMinorUnits`-wide price buckets, then, for every
/// (interval, price bucket) pair that had at least one trade, marks that
/// price as "touched" during that letter.
///
/// Both size parameters must be `> 0`; either being non-positive produces
/// an empty result rather than panicking, same posture as
/// `computeVolumeProfile`.
pub fn computeTpoProfile(
    ticks: &[TickRecord],
    priceBucketSizeInMinorUnits: i64,
    tpoIntervalSeconds: u64,
) -> TpoProfileResult {
    if priceBucketSizeInMinorUnits <= 0 || tpoIntervalSeconds == 0 || ticks.is_empty() {
        return TpoProfileResult {
            letters: Vec::new(),
            tpoCountsByPriceBucket: Vec::new(),
            tpoPointOfControlPriceBucketStart: None,
        };
    }

    let earliestEpochSeconds = ticks.iter().map(|tick| tick.executedAtEpochSeconds).min().unwrap();

    // intervalIndex -> set of price buckets touched, using a BTreeMap of
    // BTreeSet so both intervals and, within each, prices come out in a
    // deterministic, sorted order without a separate sort pass.
    let mut pricesTouchedByIntervalIndex: BTreeMap<u64, std::collections::BTreeSet<i64>> = BTreeMap::new();
    for tick in ticks {
        let intervalIndex = (tick.executedAtEpochSeconds - earliestEpochSeconds) / tpoIntervalSeconds;
        let priceBucket = bucketStartForPrice(tick.priceInMinorUnits, priceBucketSizeInMinorUnits);
        pricesTouchedByIntervalIndex
            .entry(intervalIndex)
            .or_default()
            .insert(priceBucket);
    }

    let letters: Vec<TpoLetter> = pricesTouchedByIntervalIndex
        .iter()
        .enumerate()
        .map(|(ordinal, (&intervalIndex, prices))| TpoLetter {
            letter: tpoLetterForOrdinal(ordinal),
            intervalStartEpochSeconds: earliestEpochSeconds + intervalIndex * tpoIntervalSeconds,
            pricesTouchedThisInterval: prices.iter().copied().collect(),
        })
        .collect();

    let mut tpoCountByPriceBucket: BTreeMap<i64, u32> = BTreeMap::new();
    for prices in pricesTouchedByIntervalIndex.values() {
        for &price in prices {
            *tpoCountByPriceBucket.entry(price).or_insert(0) += 1;
        }
    }

    let tpoPointOfControlPriceBucketStart = tpoCountByPriceBucket
        .iter()
        .max_by(|(priceA, countA), (priceB, countB)| countA.cmp(countB).then(priceB.cmp(priceA)))
        .map(|(&price, _)| price);

    TpoProfileResult {
        letters,
        tpoCountsByPriceBucket: tpoCountByPriceBucket.into_iter().collect(),
        tpoPointOfControlPriceBucketStart,
    }
}

/// Spreadsheet-column-style lettering: 0->"A", 1->"B", ..., 25->"Z",
/// 26->"AA", 27->"AB", ... — the same scheme real Market Profile charts
/// use once a session runs past 26 intervals.
fn tpoLetterForOrdinal(ordinal: usize) -> String {
    let mut letters = Vec::new();
    let mut remaining = ordinal;
    loop {
        letters.push((b'A' + (remaining % 26) as u8) as char);
        if remaining < 26 {
            break;
        }
        remaining = remaining / 26 - 1;
    }
    letters.iter().rev().collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    fn tick(priceInMinorUnits: i64, quantity: u64, executedAtEpochSeconds: u64) -> TickRecord {
        TickRecord {
            instrumentSymbol: "DEMO-EQ".to_string(),
            executedAtEpochSeconds,
            priceInMinorUnits,
            quantity,
            isBuyAggressor: true,
        }
    }

    // --- computeVolumeProfile ---
    //
    // HAND-WORKED FIXTURE (used by several tests below): 6 ticks, bucket
    // size 10, prices 100/100/110/110/110/120, quantities 5/5/3/3/4/2:
    //   bucket 100: 5+5 = 10
    //   bucket 110: 3+3+4 = 10
    //   bucket 120: 2
    //   total volume = 22
    // POC tie between 100 and 110 (both 10) -> lowest price wins -> 100.
    // Value area @ 70%: target = ceil(22*0.7) = ceil(15.4) = 16.
    //   Start at POC=100, accumulated=10. Compare below(90, absent) vs
    //   above(110, vol 10): only above exists -> take it. accumulated=20
    //   >= 16 -> stop. Value area = [100, 110].

    #[test]
    fn emptyTickListProducesAnEmptyResult() {
        let result = computeVolumeProfile(&[], 10, 0.7);
        assert!(result.levels.is_empty());
        assert_eq!(result.pointOfControlPriceBucketStart, None);
        assert_eq!(result.valueAreaLowPriceBucketStart, None);
        assert_eq!(result.totalVolume, 0);
    }

    #[test]
    fn nonPositiveBucketSizeProducesAnEmptyResultRatherThanPanicking() {
        let ticks = vec![tick(100, 5, 1_000)];
        assert!(computeVolumeProfile(&ticks, 0, 0.7).levels.is_empty());
        assert!(computeVolumeProfile(&ticks, -5, 0.7).levels.is_empty());
    }

    #[test]
    fn singleTickIsItsOwnPocAndValueArea() {
        let ticks = vec![tick(105, 7, 1_000)];
        let result = computeVolumeProfile(&ticks, 10, 0.7);
        assert_eq!(result.totalVolume, 7);
        assert_eq!(
            result.levels,
            vec![VolumeProfileLevel {
                priceBucketStart: 100,
                totalVolume: 7
            }]
        );
        assert_eq!(result.pointOfControlPriceBucketStart, Some(100));
        assert_eq!(result.valueAreaLowPriceBucketStart, Some(100));
        assert_eq!(result.valueAreaHighPriceBucketStart, Some(100));
    }

    #[test]
    fn handWorkedFixturePocIsTheLowestPriceAmongTiedMaxVolumeBuckets() {
        let ticks = vec![
            tick(100, 5, 1_000),
            tick(100, 5, 1_010),
            tick(110, 3, 1_020),
            tick(110, 3, 1_030),
            tick(110, 4, 1_040),
            tick(120, 2, 1_050),
        ];
        let result = computeVolumeProfile(&ticks, 10, 0.7);
        assert_eq!(result.totalVolume, 22);
        assert_eq!(result.pointOfControlPriceBucketStart, Some(100));
    }

    #[test]
    fn handWorkedFixtureValueAreaMatchesManualCalculation() {
        let ticks = vec![
            tick(100, 5, 1_000),
            tick(100, 5, 1_010),
            tick(110, 3, 1_020),
            tick(110, 3, 1_030),
            tick(110, 4, 1_040),
            tick(120, 2, 1_050),
        ];
        let result = computeVolumeProfile(&ticks, 10, 0.7);
        assert_eq!(result.valueAreaLowPriceBucketStart, Some(100));
        assert_eq!(result.valueAreaHighPriceBucketStart, Some(110));
    }

    #[test]
    fn valueAreaExpansionSkipsOverEmptyBucketsToReachAFartherOccupiedBucketRatherThanStoppingShort() {
        // Exact audit repro: ticks at 100 (qty 10), 130 (qty 100, POC), 140
        // (qty 40), bucket size 10, valueAreaVolumeFraction = 1.0 (target =
        // 150 = all the volume). Buckets 110 and 120 are EMPTY (no ticks
        // landed there), so they're simply absent from volumeByBucket —
        // two consecutive empty buckets separate the POC (130) from the
        // next occupied bucket below it (100).
        //
        // The buggy version's inferred bucketStep (min gap between
        // occupied keys: min(130-100=30, 140-130=10) = 10) happens to
        // match the real bucket size here, so that specific inference
        // isn't what fails — it's the `(None, None) => break` early exit:
        // after taking 140 (immediate neighbor, step 10, occupied), the
        // NEXT below-candidate at a single step (130-10=120) is empty AND
        // the next above-candidate (140+10=150) is empty too, so the old
        // code gives up right there at accumulated=140 (<150), leaving
        // valueAreaLowPriceBucketStart stuck at the POC (130) — never
        // walking two steps further down to the real, occupied,
        // volume-10 bucket at 100 that would complete the target.
        let ticks = vec![tick(100, 10, 1_000), tick(130, 100, 1_010), tick(140, 40, 1_020)];
        let result = computeVolumeProfile(&ticks, 10, 1.0);

        assert_eq!(result.totalVolume, 150);
        assert_eq!(result.pointOfControlPriceBucketStart, Some(130));
        assert_eq!(result.valueAreaLowPriceBucketStart, Some(100));
        assert_eq!(result.valueAreaHighPriceBucketStart, Some(140));
    }

    #[test]
    fn valueAreaCoveringAllVolumeSpansTheFullPriceRange() {
        let ticks = vec![tick(100, 1, 1_000), tick(110, 1, 1_010), tick(120, 1, 1_020)];
        let result = computeVolumeProfile(&ticks, 10, 1.0);
        assert_eq!(result.valueAreaLowPriceBucketStart, Some(100));
        assert_eq!(result.valueAreaHighPriceBucketStart, Some(120));
    }

    #[test]
    fn levelsAreSortedAscendingByPriceBucket() {
        let ticks = vec![tick(300, 1, 1_000), tick(100, 1, 1_010), tick(200, 1, 1_020)];
        let result = computeVolumeProfile(&ticks, 100, 0.7);
        let prices: Vec<i64> = result.levels.iter().map(|level| level.priceBucketStart).collect();
        assert_eq!(prices, vec![100, 200, 300]);
    }

    #[test]
    fn negativePricesBucketCorrectlyViaRemEuclid() {
        // rem_euclid, unlike plain `%`, keeps the bucket-start math correct
        // for negative prices too (not expected in this domain, but the
        // arithmetic should still not silently misbucket).
        assert_eq!(bucketStartForPrice(-5, 10), -10);
        assert_eq!(bucketStartForPrice(-10, 10), -10);
        assert_eq!(bucketStartForPrice(5, 10), 0);
    }

    #[test]
    fn ticksAtExactBucketBoundaryBelongToTheLowerBucket() {
        let ticks = vec![tick(100, 1, 1_000), tick(109, 1, 1_010)];
        let result = computeVolumeProfile(&ticks, 10, 0.7);
        assert_eq!(result.levels.len(), 1);
        assert_eq!(
            result.levels[0],
            VolumeProfileLevel {
                priceBucketStart: 100,
                totalVolume: 2
            }
        );
    }

    // --- computeTpoProfile ---

    #[test]
    fn emptyTickListProducesAnEmptyTpoProfile() {
        let result = computeTpoProfile(&[], 10, 60);
        assert!(result.letters.is_empty());
        assert_eq!(result.tpoPointOfControlPriceBucketStart, None);
    }

    #[test]
    fn singleIntervalSingleTickProducesLetterAWithOnePrice() {
        let ticks = vec![tick(105, 1, 1_000)];
        let result = computeTpoProfile(&ticks, 10, 60);
        assert_eq!(result.letters.len(), 1);
        assert_eq!(result.letters[0].letter, "A");
        assert_eq!(result.letters[0].pricesTouchedThisInterval, vec![100]);
    }

    #[test]
    fn handWorkedFixtureTwoIntervalsProduceLettersAThenB() {
        // Interval width 60s, first tick at t=1000 defines letter A's
        // start; a tick at t=1065 is 65s later -> falls into interval
        // index 1 -> letter B.
        let ticks = vec![tick(100, 1, 1_000), tick(110, 1, 1_065)];
        let result = computeTpoProfile(&ticks, 10, 60);
        assert_eq!(result.letters.len(), 2);
        assert_eq!(result.letters[0].letter, "A");
        assert_eq!(result.letters[0].intervalStartEpochSeconds, 1_000);
        assert_eq!(result.letters[1].letter, "B");
        assert_eq!(result.letters[1].intervalStartEpochSeconds, 1_060);
    }

    #[test]
    fn samePriceTouchedInMultipleIntervalsIncreasesItsTpoCount() {
        let ticks = vec![
            tick(100, 1, 1_000), // interval A, price 100
            tick(100, 1, 1_065), // interval B, price 100 again
            tick(110, 1, 1_130), // interval C, price 110
        ];
        let result = computeTpoProfile(&ticks, 10, 60);
        assert_eq!(result.tpoCountsByPriceBucket, vec![(100, 2), (110, 1)]);
        assert_eq!(result.tpoPointOfControlPriceBucketStart, Some(100));
    }

    #[test]
    fn twentySevenIntervalsRolloverLetteringPastZToAa() {
        let ticks: Vec<TickRecord> = (0..27).map(|i| tick(100, 1, 1_000 + i * 60)).collect();
        let result = computeTpoProfile(&ticks, 10, 60);
        assert_eq!(result.letters.len(), 27);
        assert_eq!(result.letters[25].letter, "Z");
        assert_eq!(result.letters[26].letter, "AA");
    }

    #[test]
    fn zeroIntervalWidthProducesAnEmptyResultRatherThanDividingByZero() {
        let ticks = vec![tick(100, 1, 1_000)];
        assert!(computeTpoProfile(&ticks, 10, 0).letters.is_empty());
    }

    #[test]
    fn multiplePricesInTheSameIntervalAreAllRecordedForThatLetter() {
        let ticks = vec![tick(100, 1, 1_000), tick(200, 1, 1_010), tick(150, 1, 1_020)];
        let result = computeTpoProfile(&ticks, 10, 60);
        assert_eq!(result.letters.len(), 1);
        assert_eq!(result.letters[0].pricesTouchedThisInterval, vec![100, 150, 200]);
    }
}
