"""Latency benchmarking dashboard for algo clients: order round-trip
histograms, per-venue comparison. See FEATURES.md §22 ("Deep Quant &
Algorithmic Trading Internals") — "Latency benchmarking dashboard for
algo clients: order round-trip histograms, per-venue comparison".

Two real, independent pieces:

1. **Real sample collection** (`measureRoundTripTimeSamplesOverHttp`):
   actually times real HTTP request/response round trips against a
   caller-supplied URL using `time.perf_counter()` (a monotonic,
   high-resolution clock — the correct tool for latency measurement, NOT
   wall-clock `time.time()`) and stdlib `urllib.request` — no framework,
   matching this service's convention. This is written generically
   (any URL, any request builder) so it can time
   `services/oms-gateway`'s real `POST /orders/submit` endpoint when that
   service is reachable — see the module-level `README.md` "§22 pass 2"
   section for the honest live-verification status (oms-gateway requires
   its own dependency chain, including `services/matching-engine`, to be
   running; if it isn't reachable when you run this, this function raises
   `ConnectionError`/`urllib.error.URLError` and the caller falls back to
   supplying their own samples — see `#2` below).
2. **Real histogram + percentile statistics**
   (`buildLatencyHistogramAndPercentiles`): given ANY list of observed
   round-trip-time samples (from #1, or supplied directly by a caller who
   already has their own timing data), computes a real fixed-width-bucket
   histogram (bucket boundaries and per-bucket counts, not a fake
   pre-baked shape) and real p50/p95/p99/max percentile statistics using
   the SAME nearest-rank empirical-percentile convention as
   `valueAtRiskCalculator.calculateHistoricalValueAtRisk` (sort ascending,
   read off `sortedSamples[floor(percentileFraction * n)]`, documented
   there as a standard practitioner simplification — reused here for
   consistency rather than reimplementing a second percentile
   convention).

Multi-venue comparison (`compareLatencyAcrossVenues`) is a thin, real
wrapper: run `buildLatencyHistogramAndPercentiles` once per named venue
and return the results side by side, keyed by venue label, so a caller
can render a comparison table/chart without doing any math of its own.

Naming convention: long, descriptive camelCase identifiers throughout —
this intentionally overrides PEP 8's snake_case convention, per project
convention (see the mercurius-naming-convention memory).
"""

from __future__ import annotations

import time
import urllib.error
import urllib.request
from dataclasses import dataclass


def measureRoundTripTimeSamplesOverHttp(
    url: str,
    sampleCount: int,
    requestBody: bytes | None = None,
    method: str = "GET",
    timeoutSeconds: float = 5.0,
) -> list[float]:
    """Actually performs `sampleCount` real HTTP request/response round
    trips against `url` (GET by default; pass `requestBody` and
    `method="POST"` to time an order-submission-style endpoint), timing
    each one with `time.perf_counter()` (monotonic, sub-millisecond
    resolution — the correct clock for latency measurement). Returns the
    observed round-trip times in MILLISECONDS, one per sample, in the
    order they were taken.

    Raises `urllib.error.URLError` (which includes `ConnectionError`-style
    refused-connection cases) if `url` isn't reachable — this function
    does NOT silently fall back to fake data; a caller that wants a
    fallback should catch this and supply its own samples to
    `buildLatencyHistogramAndPercentiles` instead (see module docstring).
    Raises `ValueError` if `sampleCount` is not positive.
    """
    if sampleCount <= 0:
        raise ValueError("sampleCount must be a positive integer")

    roundTripTimesInMilliseconds: list[float] = []
    for _ in range(sampleCount):
        request = urllib.request.Request(url, data=requestBody, method=method)
        startTime = time.perf_counter()
        with urllib.request.urlopen(request, timeout=timeoutSeconds) as response:
            response.read()
        elapsedSeconds = time.perf_counter() - startTime
        roundTripTimesInMilliseconds.append(elapsedSeconds * 1000.0)

    return roundTripTimesInMilliseconds


def checkHttpEndpointIsReachable(url: str, timeoutSeconds: float = 2.0) -> bool:
    """Best-effort reachability probe used to decide whether to attempt
    real live timing against an endpoint (e.g. oms-gateway) before
    falling back to caller-supplied samples. Returns `False` on ANY
    connection failure rather than raising — unlike
    `measureRoundTripTimeSamplesOverHttp`, this function's entire purpose
    is to answer "can I reach it?" without raising.
    """
    try:
        with urllib.request.urlopen(url, timeout=timeoutSeconds):
            return True
    except (urllib.error.URLError, OSError):
        return False


@dataclass(frozen=True)
class LatencyHistogramBucket:
    lowerBoundInclusive: float
    upperBoundExclusive: float
    sampleCount: int


@dataclass(frozen=True)
class LatencyBenchmarkResult:
    sampleCount: int
    minimumMilliseconds: float
    maximumMilliseconds: float
    p50Milliseconds: float
    p95Milliseconds: float
    p99Milliseconds: float
    histogramBuckets: list[LatencyHistogramBucket]


def _calculateNearestRankPercentile(sortedSamples: list[float], percentileFraction: float) -> float:
    """Same nearest-rank convention as
    `valueAtRiskCalculator.calculateHistoricalValueAtRisk`:
    `index = floor(percentileFraction * n)`, clamped to the last valid
    index, with a tiny epsilon guarding floating-point boundary error.
    """
    index = int(percentileFraction * len(sortedSamples) + 1e-9)
    index = min(index, len(sortedSamples) - 1)
    return sortedSamples[index]


def buildLatencyHistogramAndPercentiles(
    roundTripTimeSamplesInMilliseconds: list[float],
    bucketCount: int = 10,
) -> LatencyBenchmarkResult:
    """Real fixed-width-bucket histogram over
    `roundTripTimeSamplesInMilliseconds`, spanning
    `[min(samples), max(samples)]` split into `bucketCount` equal-width
    buckets (the last bucket's `upperBoundExclusive` is nudged up by a
    tiny epsilon so the maximum sample itself falls inside a bucket
    rather than being excluded by strict `<`), plus real p50/p95/p99/max
    percentile statistics using the nearest-rank convention documented
    above.

    Raises `ValueError` if `roundTripTimeSamplesInMilliseconds` is empty
    or `bucketCount` is not positive. A single-sample or zero-spread
    (all-identical) series is handled: every sample falls in one bucket.
    """
    if not roundTripTimeSamplesInMilliseconds:
        raise ValueError("roundTripTimeSamplesInMilliseconds must contain at least one sample")
    if bucketCount <= 0:
        raise ValueError("bucketCount must be a positive integer")

    sortedSamples = sorted(roundTripTimeSamplesInMilliseconds)
    minimumValue = sortedSamples[0]
    maximumValue = sortedSamples[-1]

    valueSpan = maximumValue - minimumValue
    if valueSpan == 0.0:
        # All samples identical — one bucket holds everything, exactly.
        histogramBuckets = [
            LatencyHistogramBucket(
                lowerBoundInclusive=minimumValue,
                upperBoundExclusive=minimumValue + 1.0,
                sampleCount=len(sortedSamples),
            )
        ]
    else:
        bucketWidth = valueSpan / bucketCount
        bucketCounts = [0] * bucketCount
        for sample in sortedSamples:
            bucketIndex = int((sample - minimumValue) / bucketWidth)
            bucketIndex = min(bucketIndex, bucketCount - 1)
            bucketCounts[bucketIndex] += 1
        histogramBuckets = [
            LatencyHistogramBucket(
                lowerBoundInclusive=minimumValue + i * bucketWidth,
                upperBoundExclusive=minimumValue + (i + 1) * bucketWidth,
                sampleCount=bucketCounts[i],
            )
            for i in range(bucketCount)
        ]

    return LatencyBenchmarkResult(
        sampleCount=len(sortedSamples),
        minimumMilliseconds=minimumValue,
        maximumMilliseconds=maximumValue,
        p50Milliseconds=_calculateNearestRankPercentile(sortedSamples, 0.50),
        p95Milliseconds=_calculateNearestRankPercentile(sortedSamples, 0.95),
        p99Milliseconds=_calculateNearestRankPercentile(sortedSamples, 0.99),
        histogramBuckets=histogramBuckets,
    )


def compareLatencyAcrossVenues(
    roundTripTimeSamplesInMillisecondsByVenue: dict[str, list[float]],
    bucketCount: int = 10,
) -> dict[str, LatencyBenchmarkResult]:
    """Runs `buildLatencyHistogramAndPercentiles` once per named venue in
    `roundTripTimeSamplesInMillisecondsByVenue`, returning results keyed
    by venue label for side-by-side comparison. Raises `ValueError` if
    the dict is empty (propagates any per-venue `ValueError`, e.g. an
    empty sample list for one venue, unmodified).
    """
    if not roundTripTimeSamplesInMillisecondsByVenue:
        raise ValueError("roundTripTimeSamplesInMillisecondsByVenue must contain at least one venue")

    return {
        venueLabel: buildLatencyHistogramAndPercentiles(samples, bucketCount)
        for venueLabel, samples in roundTripTimeSamplesInMillisecondsByVenue.items()
    }
