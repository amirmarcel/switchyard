package bench

import (
	"math"
	"sort"

	"github.com/amirmarcel/switchyard/api"
)

// percentile computes the exact p-th percentile (0..100) of sorted using
// the nearest-rank method: rank = ceil(p/100 * n), 1-indexed. No
// interpolation, no bucketing — the input must already be fully sorted
// ascending. See ADR-0002 for why this is the benchmark's source of truth
// for percentiles rather than a bucketed histogram.
func percentile(sorted []api.Time, p float64) api.Time {
	if len(sorted) == 0 {
		return 0
	}
	rank := int(math.Ceil(p / 100 * float64(len(sorted))))
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}

// queueDelaySamples extracts the raw queue-delay sample for every
// dispatched job from the decision log. Queue delay is submit-to-dispatch
// latency — the metric the scheduler actually controls — so only Dispatch
// decisions contribute a sample; Hold and Reject decisions are not
// dispatch latency.
func queueDelaySamples(log []api.Decision) []api.Time {
	samples := make([]api.Time, 0, len(log))
	for _, d := range log {
		if d.Outcome == api.Dispatch {
			samples = append(samples, d.QueueDelay)
		}
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	return samples
}
