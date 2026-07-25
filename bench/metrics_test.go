package bench

import (
	"testing"

	"github.com/amirmarcel/switchyard/api"
)

// TestPercentileKnownSample checks the nearest-rank percentile math
// against a known 1..100 sample set, where the exact ranks are easy to
// hand-verify: p50 -> rank 50 -> value 50, p95 -> rank 95 -> value 95,
// p99 -> rank 99 -> value 99.
func TestPercentileKnownSample(t *testing.T) {
	samples := make([]api.Time, 100)
	for i := range samples {
		samples[i] = api.Time(i + 1) // 1..100, already sorted
	}

	cases := []struct {
		p    float64
		want api.Time
	}{
		{50, 50},
		{95, 95},
		{99, 99},
		{100, 100},
	}
	for _, c := range cases {
		if got := percentile(samples, c.p); got != c.want {
			t.Errorf("percentile(1..100, %v) = %d, want %d", c.p, got, c.want)
		}
	}
}

// TestPercentileSmallSample checks the nearest-rank method on a sample too
// small for percentile boundaries to land on round numbers, where
// ceil-based rank selection matters most.
func TestPercentileSmallSample(t *testing.T) {
	samples := []api.Time{10, 20, 30, 40, 50} // n=5

	cases := []struct {
		p    float64
		want api.Time
	}{
		{50, 30}, // ceil(0.5*5)=3 -> index 2 -> 30
		{95, 50}, // ceil(0.95*5)=5 -> index 4 -> 50
		{99, 50}, // ceil(0.99*5)=5 -> index 4 -> 50
	}
	for _, c := range cases {
		if got := percentile(samples, c.p); got != c.want {
			t.Errorf("percentile(small, %v) = %d, want %d", c.p, got, c.want)
		}
	}
}

// TestPercentileEmpty guards the zero-samples edge case (e.g. a run where
// every job was rejected) so it returns 0 rather than panicking.
func TestPercentileEmpty(t *testing.T) {
	if got := percentile(nil, 50); got != 0 {
		t.Errorf("percentile(nil, 50) = %d, want 0", got)
	}
}
