// Package executionalgos implements FEATURES.md §15's "Execution algos
// for institutional/large orders: VWAP, TWAP, POV" — given one large
// parent order, real algorithmic slicing into a sequence of smaller
// child orders released over time, using one of three strategies:
//
//   - TWAP (Time-Weighted Average Price): the parent quantity is sliced
//     into N equal-sized child orders (remainder distributed by the
//     largest-remainder method so the slices sum EXACTLY to the parent
//     quantity, never losing or fabricating shares to rounding),
//     released at N equally-spaced points in time between the parent's
//     StartTime and EndTime inclusive.
//   - VWAP (Volume-Weighted Average Price): the parent quantity is
//     sliced according to a caller-supplied historical/assumed intraday
//     volume curve — buckets with more historical volume get
//     proportionally larger slices — again using the largest-remainder
//     method for an exact sum.
//   - POV (Percentage of Volume): instead of a pre-built time schedule,
//     a PovScheduler consumes real-time observed cumulative market
//     volume ticks and, for each observation, releases a child slice
//     sized at a configured participation rate of the volume traded
//     since the last observation — capped at both a configured maximum
//     clip size and the parent order's remaining unfilled quantity.
//
// Every clock decision in this package takes `now` (or an observed
// cumulative volume reading) as an explicit parameter — nothing here
// ever sleeps or reads the wall clock internally — so every scheduling
// boundary is exactly reproducible in tests, the same discipline
// internal/algolimits' token bucket and internal/exposurelimits use.
//
// Known gap, stated loudly: this package computes WHAT child orders to
// release and WHEN/under what real-time condition — it does not itself
// submit them. Wiring PollDueSlices/OnVolumeObservation's returned
// ChildOrderSlice values into orders.OrderSubmissionRequest and POSTing
// them through the existing order-submission path is the caller's (or a
// future scheduler goroutine's) job; see cmd/server/main.go's
// buildExecutionAlgosHandler for the illustrative HTTP-level wiring this
// build ships, which computes/polls a schedule but does not itself run
// a background submission loop against a live matching engine.
package executionalgos

import (
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

var (
	// ErrZeroTotalQuantity is returned when a ParentOrder's TotalQuantity
	// is zero — there is nothing to slice.
	ErrZeroTotalQuantity = errors.New("parent order totalQuantity must be greater than zero")

	// ErrEndTimeNotAfterStartTime is returned when EndTime is not
	// strictly after StartTime (TWAP/VWAP need a real time window to
	// spread slices across).
	ErrEndTimeNotAfterStartTime = errors.New("parent order endTime must be after startTime")

	// ErrInvalidNumberOfSlices is returned when BuildTwapSchedule is
	// asked for zero or a negative number of slices.
	ErrInvalidNumberOfSlices = errors.New("numberOfSlices must be greater than zero")

	// ErrEmptyVolumeCurve is returned when BuildVwapSchedule is given no
	// volume curve buckets.
	ErrEmptyVolumeCurve = errors.New("volumeCurve must contain at least one bucket")

	// ErrNonPositiveVolumeCurveWeightSum is returned when every bucket in
	// a supplied volume curve has a zero or negative weight — there is
	// no valid proportional split.
	ErrNonPositiveVolumeCurveWeightSum = errors.New("volumeCurve weights must sum to a positive value")

	// ErrNegativeVolumeCurveWeight is returned when any individual bucket
	// weight is negative — a negative share of volume is nonsensical.
	ErrNegativeVolumeCurveWeight = errors.New("volumeCurve bucket weights must be non-negative")

	// ErrNonPositiveParticipationRate is returned when a PovConfig's
	// ParticipationRate is <= 0.
	ErrNonPositiveParticipationRate = errors.New("pov participationRate must be greater than zero")

	// ErrZeroMaxClipSize is returned when a PovConfig's
	// MaxClipSizeQuantity is zero — POV needs a real cap to bound burst
	// slice size.
	ErrZeroMaxClipSize = errors.New("pov maxClipSizeQuantity must be greater than zero")

	// ErrCumulativeVolumeWentBackwards is returned by
	// PovScheduler.OnVolumeObservation when the supplied cumulative
	// market volume is lower than a previously observed reading —
	// cumulative volume must be monotonically non-decreasing.
	ErrCumulativeVolumeWentBackwards = errors.New("observed cumulative market volume went backwards")
)

// AlgoType names which strategy produced a schedule — carried on
// ChildOrderSlice purely for observability/logging.
type AlgoType string

const (
	AlgoTypeTwap AlgoType = "TWAP"
	AlgoTypeVwap AlgoType = "VWAP"
	AlgoTypePov  AlgoType = "POV"
)

// ParentOrder is the large institutional order being sliced.
type ParentOrder struct {
	InstrumentSymbol      string
	OrderSideIsBuyNotSell bool
	TotalQuantity         uint64

	// StartTime/EndTime bound the execution window for TWAP/VWAP
	// scheduling. Unused by POV, which reacts to real-time volume
	// instead of a pre-built time grid.
	StartTime time.Time
	EndTime   time.Time
}

// ChildOrderSlice is one slice of the parent order this package computed
// — everything a caller needs to build an orders.OrderSubmissionRequest
// for it.
type ChildOrderSlice struct {
	AlgoType             AlgoType  `json:"algoType"`
	SliceIndex           int       `json:"sliceIndex"`
	ScheduledReleaseTime time.Time `json:"scheduledReleaseTime"`
	Quantity             uint64    `json:"quantity"`
}

func validateParentOrder(parent ParentOrder) error {
	if parent.TotalQuantity == 0 {
		return ErrZeroTotalQuantity
	}
	return nil
}

// distributeByLargestRemainder splits totalQuantity across len(weights)
// buckets proportionally to weights (which need not sum to 1 — they are
// normalized here), using the largest-remainder (Hamilton) apportionment
// method: each bucket first gets floor(its exact proportional share),
// then the leftover units (always < len(weights), by construction) go
// one each to the buckets whose fractional remainder was largest,
// breaking ties by lowest index for determinism. This guarantees the
// returned quantities sum EXACTLY to totalQuantity — never off by
// rounding — which matters in a financial system slicing real share
// counts.
func distributeByLargestRemainder(totalQuantity uint64, weights []float64) []uint64 {
	n := len(weights)
	weightSum := 0.0
	for _, w := range weights {
		weightSum += w
	}

	quantities := make([]uint64, n)
	type remainder struct {
		index int
		frac  float64
	}
	remainders := make([]remainder, n)

	allocated := uint64(0)
	for i, w := range weights {
		exactShare := (w / weightSum) * float64(totalQuantity)
		base := uint64(exactShare)
		quantities[i] = base
		remainders[i] = remainder{index: i, frac: exactShare - float64(base)}
		allocated += base
	}

	leftover := totalQuantity - allocated
	sort.SliceStable(remainders, func(a, b int) bool {
		if remainders[a].frac != remainders[b].frac {
			return remainders[a].frac > remainders[b].frac
		}
		return remainders[a].index < remainders[b].index
	})
	for i := uint64(0); i < leftover; i++ {
		quantities[remainders[i].index]++
	}

	return quantities
}

// BuildTwapSchedule slices parent.TotalQuantity into numberOfSlices
// equal (as equal as integer division allows) child orders, released at
// numberOfSlices equally-spaced points in time from parent.StartTime
// (the first slice) to parent.EndTime (the last slice) inclusive. For
// numberOfSlices == 1 the single slice releases at StartTime.
func BuildTwapSchedule(parent ParentOrder, numberOfSlices int) ([]ChildOrderSlice, error) {
	if err := validateParentOrder(parent); err != nil {
		return nil, err
	}
	if numberOfSlices <= 0 {
		return nil, ErrInvalidNumberOfSlices
	}
	if !parent.EndTime.After(parent.StartTime) {
		return nil, ErrEndTimeNotAfterStartTime
	}

	weights := make([]float64, numberOfSlices)
	for i := range weights {
		weights[i] = 1.0
	}
	quantities := distributeByLargestRemainder(parent.TotalQuantity, weights)

	slices := make([]ChildOrderSlice, numberOfSlices)
	if numberOfSlices == 1 {
		slices[0] = ChildOrderSlice{AlgoType: AlgoTypeTwap, SliceIndex: 0, ScheduledReleaseTime: parent.StartTime, Quantity: quantities[0]}
		return slices, nil
	}

	totalDuration := parent.EndTime.Sub(parent.StartTime)
	interval := totalDuration / time.Duration(numberOfSlices-1)
	for i := 0; i < numberOfSlices; i++ {
		releaseTime := parent.StartTime.Add(time.Duration(i) * interval)
		if i == numberOfSlices-1 {
			releaseTime = parent.EndTime // exact, avoids any integer-division drift
		}
		slices[i] = ChildOrderSlice{AlgoType: AlgoTypeTwap, SliceIndex: i, ScheduledReleaseTime: releaseTime, Quantity: quantities[i]}
	}
	return slices, nil
}

// VolumeCurvePoint is one bucket of a caller-supplied historical/assumed
// intraday volume curve: HistoricalVolumeWeight is that bucket's
// relative share of the day's expected volume — weights need not sum to
// 1 (they are normalized internally), so callers can pass raw
// historical share counts directly.
type VolumeCurvePoint struct {
	BucketReleaseTime      time.Time `json:"bucketReleaseTime"`
	HistoricalVolumeWeight float64   `json:"historicalVolumeWeight"`
}

// BuildVwapSchedule slices parent.TotalQuantity across volumeCurve's
// buckets proportionally to each bucket's HistoricalVolumeWeight — a
// bucket historically carrying 40% of the day's volume gets ~40% of the
// parent order's quantity — releasing each slice at its bucket's
// BucketReleaseTime. Buckets are NOT required to be pre-sorted; the
// returned schedule is sorted by BucketReleaseTime ascending.
func BuildVwapSchedule(parent ParentOrder, volumeCurve []VolumeCurvePoint) ([]ChildOrderSlice, error) {
	if err := validateParentOrder(parent); err != nil {
		return nil, err
	}
	if len(volumeCurve) == 0 {
		return nil, ErrEmptyVolumeCurve
	}

	weights := make([]float64, len(volumeCurve))
	weightSum := 0.0
	for i, point := range volumeCurve {
		if point.HistoricalVolumeWeight < 0 {
			return nil, ErrNegativeVolumeCurveWeight
		}
		weights[i] = point.HistoricalVolumeWeight
		weightSum += point.HistoricalVolumeWeight
	}
	if weightSum <= 0 {
		return nil, ErrNonPositiveVolumeCurveWeightSum
	}

	quantities := distributeByLargestRemainder(parent.TotalQuantity, weights)

	slices := make([]ChildOrderSlice, len(volumeCurve))
	for i, point := range volumeCurve {
		slices[i] = ChildOrderSlice{AlgoType: AlgoTypeVwap, SliceIndex: i, ScheduledReleaseTime: point.BucketReleaseTime, Quantity: quantities[i]}
	}
	sort.SliceStable(slices, func(a, b int) bool {
		return slices[a].ScheduledReleaseTime.Before(slices[b].ScheduledReleaseTime)
	})
	for i := range slices {
		slices[i].SliceIndex = i
	}
	return slices, nil
}

// Scheduler wraps a pre-built TWAP/VWAP schedule (from BuildTwapSchedule
// or BuildVwapSchedule) with real, mutex-guarded release-tracking state:
// repeated calls to PollDueSlices(now) — e.g. from a periodic ticker —
// return only NEWLY due slices each time, never re-returning a slice
// already handed back by an earlier call, so a caller can poll on any
// cadence without double-submitting a child order.
type Scheduler struct {
	mutexGuardingState sync.Mutex

	parent               ParentOrder
	slices               []ChildOrderSlice
	releasedSliceIndices map[int]bool
}

// NewScheduler builds a Scheduler around an already-computed slice plan.
func NewScheduler(parent ParentOrder, slices []ChildOrderSlice) *Scheduler {
	return &Scheduler{
		parent:               parent,
		slices:               slices,
		releasedSliceIndices: make(map[int]bool),
	}
}

// PollDueSlices returns every slice whose ScheduledReleaseTime is <= now
// that has not already been returned by a previous call, in schedule
// order, marking them released. Calling this repeatedly with a
// monotonically advancing `now` (as tests do) — or even with the same
// `now` twice — never returns the same slice twice.
func (s *Scheduler) PollDueSlices(now time.Time) []ChildOrderSlice {
	s.mutexGuardingState.Lock()
	defer s.mutexGuardingState.Unlock()

	var due []ChildOrderSlice
	for _, slice := range s.slices {
		if s.releasedSliceIndices[slice.SliceIndex] {
			continue
		}
		if slice.ScheduledReleaseTime.After(now) {
			continue
		}
		s.releasedSliceIndices[slice.SliceIndex] = true
		due = append(due, slice)
	}
	return due
}

// IsComplete reports whether every slice in the schedule has been
// released via PollDueSlices.
func (s *Scheduler) IsComplete() bool {
	s.mutexGuardingState.Lock()
	defer s.mutexGuardingState.Unlock()

	return len(s.releasedSliceIndices) == len(s.slices)
}

// RemainingQuantity sums the Quantity of every slice not yet released.
func (s *Scheduler) RemainingQuantity() uint64 {
	s.mutexGuardingState.Lock()
	defer s.mutexGuardingState.Unlock()

	var remaining uint64
	for _, slice := range s.slices {
		if !s.releasedSliceIndices[slice.SliceIndex] {
			remaining += slice.Quantity
		}
	}
	return remaining
}

// PovConfig configures a PovScheduler's participation behavior.
type PovConfig struct {
	// ParticipationRate is the fraction (0, 1] of observed real-time
	// market volume this algo tries to capture per observation, e.g.
	// 0.1 == "trade 10% of whatever volume prints between observations".
	ParticipationRate float64

	// MaxClipSizeQuantity hard-caps any single released child slice,
	// regardless of how much volume printed — preventing one abnormally
	// large print from producing one abnormally large, market-moving
	// child order.
	MaxClipSizeQuantity uint64
}

// PovScheduler implements the Percentage-of-Volume execution algo: it
// has no pre-built time schedule — instead, a caller feeds it real-time
// observed cumulative market volume readings via OnVolumeObservation,
// and it releases a right-sized child slice for each observation based
// on how much volume printed since the previous reading.
type PovScheduler struct {
	mutexGuardingState sync.Mutex

	parent ParentOrder
	config PovConfig

	remainingQuantity            uint64
	lastObservedCumulativeVolume uint64
	haveObservedVolumeYet        bool
	nextSliceIndex               int
}

// NewPovScheduler builds a PovScheduler for parent, validating config.
func NewPovScheduler(parent ParentOrder, config PovConfig) (*PovScheduler, error) {
	if err := validateParentOrder(parent); err != nil {
		return nil, err
	}
	if config.ParticipationRate <= 0 {
		return nil, ErrNonPositiveParticipationRate
	}
	if config.MaxClipSizeQuantity == 0 {
		return nil, ErrZeroMaxClipSize
	}
	return &PovScheduler{
		parent:            parent,
		config:            config,
		remainingQuantity: parent.TotalQuantity,
	}, nil
}

// OnVolumeObservation is called with the real-time cumulative traded
// market volume for parent.InstrumentSymbol observed at `now`
// (cumulative for the trading session — e.g. "14,532 shares have traded
// today as of now", not a delta). It computes
// participationRate * (volume traded since the last observation),
// caps that at MaxClipSizeQuantity and at whatever of the parent order
// remains unfilled, and — if the capped amount is > 0 — returns a
// ChildOrderSlice for it (nil, nil if the volume delta was too small to
// produce even one unit, or if the parent order is already fully
// sliced). The very first observation establishes the baseline and
// never itself produces a slice (there is no "volume since last
// observation" yet).
func (p *PovScheduler) OnVolumeObservation(now time.Time, cumulativeMarketVolume uint64) (*ChildOrderSlice, error) {
	p.mutexGuardingState.Lock()
	defer p.mutexGuardingState.Unlock()

	if !p.haveObservedVolumeYet {
		p.haveObservedVolumeYet = true
		p.lastObservedCumulativeVolume = cumulativeMarketVolume
		return nil, nil
	}

	if cumulativeMarketVolume < p.lastObservedCumulativeVolume {
		return nil, fmt.Errorf("%w: last observed %d, got %d", ErrCumulativeVolumeWentBackwards, p.lastObservedCumulativeVolume, cumulativeMarketVolume)
	}

	volumeDelta := cumulativeMarketVolume - p.lastObservedCumulativeVolume
	p.lastObservedCumulativeVolume = cumulativeMarketVolume

	if p.remainingQuantity == 0 {
		return nil, nil
	}

	rawSliceQuantity := uint64(float64(volumeDelta) * p.config.ParticipationRate)
	sliceQuantity := rawSliceQuantity
	if sliceQuantity > p.config.MaxClipSizeQuantity {
		sliceQuantity = p.config.MaxClipSizeQuantity
	}
	if sliceQuantity > p.remainingQuantity {
		sliceQuantity = p.remainingQuantity
	}
	if sliceQuantity == 0 {
		return nil, nil
	}

	p.remainingQuantity -= sliceQuantity
	slice := ChildOrderSlice{
		AlgoType:             AlgoTypePov,
		SliceIndex:           p.nextSliceIndex,
		ScheduledReleaseTime: now,
		Quantity:             sliceQuantity,
	}
	p.nextSliceIndex++
	return &slice, nil
}

// RemainingQuantity returns how much of the parent order POV has not yet
// sliced off.
func (p *PovScheduler) RemainingQuantity() uint64 {
	p.mutexGuardingState.Lock()
	defer p.mutexGuardingState.Unlock()
	return p.remainingQuantity
}

// IsComplete reports whether POV has sliced off the entire parent order.
func (p *PovScheduler) IsComplete() bool {
	p.mutexGuardingState.Lock()
	defer p.mutexGuardingState.Unlock()
	return p.remainingQuantity == 0
}
