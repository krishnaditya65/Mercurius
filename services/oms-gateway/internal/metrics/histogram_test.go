package metrics

import "testing"

func TestFreshHistogramHasZeroCounts(t *testing.T) {
	histogram := NewHistogramWithDefaultBuckets()
	snapshot := histogram.Snapshot()

	if snapshot.ObservationCount != 0 {
		t.Fatalf("expected 0 observations, got %d", snapshot.ObservationCount)
	}
	for bucketIndex, count := range snapshot.CumulativeCounts {
		if count != 0 {
			t.Fatalf("expected bucket %d to start at 0, got %d", bucketIndex, count)
		}
	}
}

func TestObserveIncrementsEveryBucketAtOrAboveTheValue(t *testing.T) {
	histogram := NewHistogram([]float64{10, 50, 100})
	histogram.Observe(30) // falls in the 50 and 100 buckets, not the 10 bucket

	snapshot := histogram.Snapshot()
	if snapshot.CumulativeCounts[0] != 0 {
		t.Fatalf("expected the 10ms bucket to NOT count a 30ms observation, got %d", snapshot.CumulativeCounts[0])
	}
	if snapshot.CumulativeCounts[1] != 1 {
		t.Fatalf("expected the 50ms bucket to count a 30ms observation, got %d", snapshot.CumulativeCounts[1])
	}
	if snapshot.CumulativeCounts[2] != 1 {
		t.Fatalf("expected the 100ms bucket to count a 30ms observation (cumulative), got %d", snapshot.CumulativeCounts[2])
	}
}

func TestObservationCountAndSumAccumulate(t *testing.T) {
	histogram := NewHistogramWithDefaultBuckets()
	histogram.Observe(5)
	histogram.Observe(15)
	histogram.Observe(2)

	snapshot := histogram.Snapshot()
	if snapshot.ObservationCount != 3 {
		t.Fatalf("expected 3 observations, got %d", snapshot.ObservationCount)
	}
	expectedSum := 5.0 + 15.0 + 2.0
	if snapshot.ObservationSumMs != expectedSum {
		t.Fatalf("expected sum %v, got %v", expectedSum, snapshot.ObservationSumMs)
	}
}

func TestAValueLargerThanEveryBucketOnlyAffectsCountAndSum(t *testing.T) {
	histogram := NewHistogram([]float64{10, 50})
	histogram.Observe(1000)

	snapshot := histogram.Snapshot()
	if snapshot.CumulativeCounts[0] != 0 || snapshot.CumulativeCounts[1] != 0 {
		t.Fatalf("expected no finite bucket to count a 1000ms observation against a max bound of 50ms, got %v", snapshot.CumulativeCounts)
	}
	if snapshot.ObservationCount != 1 {
		t.Fatalf("expected the observation to still count toward the total, got %d", snapshot.ObservationCount)
	}
}

func TestSnapshotIsAnIndependentCopyNotAffectedByLaterObservations(t *testing.T) {
	histogram := NewHistogramWithDefaultBuckets()
	histogram.Observe(5)
	snapshot := histogram.Snapshot()

	histogram.Observe(2000)

	if snapshot.ObservationCount != 1 {
		t.Fatalf("expected the earlier snapshot to stay frozen at 1 observation, got %d", snapshot.ObservationCount)
	}
}
