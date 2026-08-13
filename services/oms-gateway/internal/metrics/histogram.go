// Package metrics provides real latency histograms — FEATURES.md §13:
// "Metrics/tracing (latency histograms on the execution path
// especially)". Stdlib only, no Prometheus client library dependency
// (same "hand-roll it" convention as every other cross-cutting piece in
// this repo) — but the OUTPUT format (`WritePrometheusText`) is real
// Prometheus text exposition format, so a real Prometheus server could
// scrape `GET /metrics` on this service today without any adapter.
//
// TODO(real build): only oms-gateway has this wired up. The actual
// hottest path this FEATURES.md item calls out — matching-engine — has
// no metrics at all yet; adding them there means giving matching-engine
// its own HTTP listener the way market-data grew one for its query API,
// which is real, separate work not done in this increment.
package metrics

import "sync"

// defaultLatencyBucketUpperBoundsMs are Prometheus-style CUMULATIVE
// bucket upper bounds in milliseconds — each bucket counts every
// observation <= its bound, not just observations that fall in its own
// slice, per the Prometheus histogram convention. Chosen to span
// "sub-millisecond-ish" HTTP handling up through multi-second outliers
// (e.g. a slow downstream service), not tuned against real production
// latency data (there isn't any — this is a skeleton).
var defaultLatencyBucketUpperBoundsMs = []float64{1, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000}

// Histogram is safe for concurrent use. Bucket counts are CUMULATIVE
// (bucket i counts every observation <= bucketUpperBoundsMs[i]), matching
// Prometheus's own histogram semantics, so WritePrometheusText's output
// needs no transformation before being scraped.
type Histogram struct {
	mutexGuardingCounts sync.Mutex
	bucketUpperBoundsMs []float64
	cumulativeCounts    []uint64 // same length as bucketUpperBoundsMs
	observationCount    uint64
	observationSumMs    float64
}

func NewHistogramWithDefaultBuckets() *Histogram {
	return NewHistogram(defaultLatencyBucketUpperBoundsMs)
}

func NewHistogram(bucketUpperBoundsMs []float64) *Histogram {
	return &Histogram{
		bucketUpperBoundsMs: bucketUpperBoundsMs,
		cumulativeCounts:    make([]uint64, len(bucketUpperBoundsMs)),
	}
}

// Observe records one latency observation.
func (histogram *Histogram) Observe(valueMs float64) {
	histogram.mutexGuardingCounts.Lock()
	defer histogram.mutexGuardingCounts.Unlock()

	histogram.observationCount++
	histogram.observationSumMs += valueMs

	for bucketIndex, upperBound := range histogram.bucketUpperBoundsMs {
		if valueMs <= upperBound {
			histogram.cumulativeCounts[bucketIndex]++
		}
	}
}

// HistogramSnapshot is a point-in-time, immutable copy of a Histogram's
// state — safe to read without holding any lock, and what
// WritePrometheusText actually formats.
type HistogramSnapshot struct {
	BucketUpperBoundsMs []float64
	CumulativeCounts    []uint64
	ObservationCount    uint64
	ObservationSumMs    float64
}

func (histogram *Histogram) Snapshot() HistogramSnapshot {
	histogram.mutexGuardingCounts.Lock()
	defer histogram.mutexGuardingCounts.Unlock()

	boundsCopy := make([]float64, len(histogram.bucketUpperBoundsMs))
	copy(boundsCopy, histogram.bucketUpperBoundsMs)
	countsCopy := make([]uint64, len(histogram.cumulativeCounts))
	copy(countsCopy, histogram.cumulativeCounts)

	return HistogramSnapshot{
		BucketUpperBoundsMs: boundsCopy,
		CumulativeCounts:    countsCopy,
		ObservationCount:    histogram.observationCount,
		ObservationSumMs:    histogram.observationSumMs,
	}
}
