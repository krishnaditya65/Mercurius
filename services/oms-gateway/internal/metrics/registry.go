package metrics

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"sync"
)

// routeLabel identifies one HTTP route for per-route histograms — method
// + path, the two dimensions that actually matter for spotting a slow
// endpoint (e.g. matching-engine hand-off inside /orders/submit) versus
// a generally slow service.
type routeLabel struct {
	method string
	path   string
}

// Registry owns one Histogram per (method, path) pair, created lazily on
// first observation. Safe for concurrent use.
type Registry struct {
	mutexGuardingHistograms sync.Mutex
	histogramsByRoute       map[routeLabel]*Histogram
}

func NewRegistry() *Registry {
	return &Registry{histogramsByRoute: make(map[routeLabel]*Histogram)}
}

// ObserveRequestDuration records one request's latency for the given
// method+path — the single entry point request-timing middleware calls.
func (registry *Registry) ObserveRequestDuration(method string, path string, durationMs float64) {
	registry.histogramFor(method, path).Observe(durationMs)
}

func (registry *Registry) histogramFor(method string, path string) *Histogram {
	label := routeLabel{method: method, path: path}

	registry.mutexGuardingHistograms.Lock()
	defer registry.mutexGuardingHistograms.Unlock()

	histogram, wasFound := registry.histogramsByRoute[label]
	if !wasFound {
		histogram = NewHistogramWithDefaultBuckets()
		registry.histogramsByRoute[label] = histogram
	}
	return histogram
}

// WritePrometheusText formats every registered histogram in real
// Prometheus text exposition format
// (https://prometheus.io/docs/instrumenting/exposition_formats/) — a
// real Prometheus server could scrape this directly. Routes are
// written in a stable sorted order so output is deterministic (useful
// for tests and for diffing scrapes), not because Prometheus itself
// requires ordering.
func (registry *Registry) WritePrometheusText(writer io.Writer) error {
	registry.mutexGuardingHistograms.Lock()
	labelsSnapshot := make([]routeLabel, 0, len(registry.histogramsByRoute))
	histogramsSnapshot := make(map[routeLabel]HistogramSnapshot, len(registry.histogramsByRoute))
	for label, histogram := range registry.histogramsByRoute {
		labelsSnapshot = append(labelsSnapshot, label)
		histogramsSnapshot[label] = histogram.Snapshot()
	}
	registry.mutexGuardingHistograms.Unlock()

	sort.Slice(labelsSnapshot, func(i, j int) bool {
		if labelsSnapshot[i].path != labelsSnapshot[j].path {
			return labelsSnapshot[i].path < labelsSnapshot[j].path
		}
		return labelsSnapshot[i].method < labelsSnapshot[j].method
	})

	if _, writeError := fmt.Fprintln(writer, "# HELP http_request_duration_milliseconds HTTP request latency in milliseconds"); writeError != nil {
		return writeError
	}
	if _, writeError := fmt.Fprintln(writer, "# TYPE http_request_duration_milliseconds histogram"); writeError != nil {
		return writeError
	}

	for _, label := range labelsSnapshot {
		snapshot := histogramsSnapshot[label]
		labelPrefix := fmt.Sprintf(`method=%q,path=%q`, label.method, label.path)

		for bucketIndex, upperBound := range snapshot.BucketUpperBoundsMs {
			if _, writeError := fmt.Fprintf(
				writer,
				"http_request_duration_milliseconds_bucket{%s,le=%q} %d\n",
				labelPrefix, formatBucketBound(upperBound), snapshot.CumulativeCounts[bucketIndex],
			); writeError != nil {
				return writeError
			}
		}
		// The +Inf bucket is always the full observation count — every
		// finite bucket is a subset of it, per Prometheus convention.
		if _, writeError := fmt.Fprintf(
			writer,
			"http_request_duration_milliseconds_bucket{%s,le=\"+Inf\"} %d\n",
			labelPrefix, snapshot.ObservationCount,
		); writeError != nil {
			return writeError
		}
		if _, writeError := fmt.Fprintf(writer, "http_request_duration_milliseconds_sum{%s} %v\n", labelPrefix, snapshot.ObservationSumMs); writeError != nil {
			return writeError
		}
		if _, writeError := fmt.Fprintf(writer, "http_request_duration_milliseconds_count{%s} %d\n", labelPrefix, snapshot.ObservationCount); writeError != nil {
			return writeError
		}
	}

	return nil
}

func formatBucketBound(upperBoundMs float64) string {
	return strconv.FormatFloat(upperBoundMs, 'g', -1, 64)
}
