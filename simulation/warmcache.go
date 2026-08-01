// warmcache.go: the simulator's warm-cache execution-time discount (see
// docs/adr/0006-warm-cache-execution-discount.md). This models the real
// payoff of cache affinity — a job dispatched to a worker already warm on
// its cache keys runs faster — which the fixed-duration model omitted.
package simulation

import "github.com/amirmarcel/switchyard/api"

// MaxWarmDiscount caps how much a full cache-key match can reduce a job's
// base duration. A fully warm job never runs faster than half its cold
// duration — see ADR-0006 for why 50% was chosen over a smaller or larger
// cap or an uncapped discount.
const MaxWarmDiscount = 0.5

// DiscountedDuration reduces base in proportion to warmOverlap — the
// fraction (0..1) of a job's CacheKeys the worker was already warm on
// before this dispatch (scheduler.enactDispatch computes and logs this as
// Decision.Factors["warm_overlap"]). A full match (1.0) gets the maximum
// discount; a partial match a proportional one; no overlap (0.0) none. Pure
// function of its inputs — no time or randomness — so replaying the same
// decision log always yields the same durations.
func DiscountedDuration(base api.Time, warmOverlap float64) api.Time {
	if warmOverlap <= 0 {
		return base
	}
	if warmOverlap > 1 {
		warmOverlap = 1
	}
	reduction := warmOverlap * MaxWarmDiscount
	return api.Time(float64(base) * (1 - reduction))
}
