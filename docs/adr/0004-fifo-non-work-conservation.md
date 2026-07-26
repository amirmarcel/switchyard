# ADR-0004: FIFO is deliberately non-work-conserving (head-of-line reservation)

- Status: accepted
- Date: 2026-07-25

## Context

CLAUDE.md's conditional work-conservation invariant says a policy may `Hold`
while a runnable job and free capacity exist *only if it explicitly reserves
capacity*, and that "an unexplained hold is a bug."

`docs/reviews/2026-07-25-opus-code-review.md` (finding F3) confirmed FIFO
breaches this by the letter of the invariant on the shipped burst scenario:
46% of its holds (164 of 354) occurred while free capacity existed on some
worker, just not enough for the head-of-line job. FIFO's `Hold` was labeled
`Factors: {"no_capacity": 1}` in every case, which is factually wrong
whenever capacity exists but is merely too small for the head. Nothing
checked work-conservation at all — the one invariant CLAUDE.md calls
non-negotiable was the one the harness couldn't see, the same shape as the
starvation bug ADR-0001 fixed and the capacity-checker bug fixed in the
bench slice.

ADR-0001 already settled that FIFO stays strict head-of-line (no backfill)
for this slice, and reconciled that with the no-starvation invariant. It
never addressed work-conservation, which is a distinct invariant strict
FIFO breaches as literally written the moment job sizes are non-uniform.

## Decision

FIFO is declared non-work-conserving by design. Holding the head-of-line job
while capacity exists elsewhere in the pool *is* FIFO's explicit capacity
reservation: FIFO reserves whatever free capacity remains for the job at the
head of the queue rather than letting a smaller job behind it jump ahead.
That is the reservation the conditional invariant requires — it does not
require work-conservation itself, only that a hold be either genuinely
unavoidable (no capacity anywhere) or an explained reservation.

Two changes make this checkable rather than merely asserted:

- **Honest hold labels.** `FIFO.Schedule`'s `Hold` `Factors` now distinguish
  two cases (`scheduler/fifo.go`, `holdFactor`):
  - `no_capacity` — no worker has any free room at all.
  - `head_of_line_reserved` — some worker has free room, but not enough for
    the head job; FIFO is reserving what exists for it.
- **A checker that can fail.** `bench/invariants.go`'s `checkWorkConservation`
  walks every `Hold` in a decision log and flags it unless it's either
  genuinely no-capacity, or its `Factors` declare a reservation (any key
  other than `no_capacity`). It reconstructs free capacity per worker at the
  hold's timestamp from prior `Dispatch` decisions plus `JobDuration`, and
  checks whether any still-pending job would fit — the same reconstruction
  approach `CheckCapacityInvariant` already uses.

Backfill (letting a smaller job behind the head dispatch first) remains a
deliberate non-goal here, unchanged from ADR-0001 — this ADR documents an
exemption for the policy that exists, it doesn't propose changing it.

## Consequences

- FIFO's holds are legal under `checkWorkConservation` by construction: every
  hold it produces is either `no_capacity` (nothing anywhere fits) or
  `head_of_line_reserved` (a declared reservation).
- A future policy that holds without declaring a reason — a real bug, not a
  design choice — is caught by the same checker. `bench/invariants_test.go`
  pins this with a deliberately-bad test policy that mislabels a hold as
  `no_capacity` while capacity actually exists.
- Once a second policy exists, "policy B holds capacity less often than
  FIFO" becomes a reportable, checked comparison rather than an unaudited
  claim, because the checker and the labeling predate the second policy.
- This does not change FIFO's dispatch behavior at all — only what its
  `Hold` decisions say about themselves, and what the harness verifies
  against that claim.

## Alternatives considered

- **Add backfill to FIFO instead of declaring the exemption.** Rejected:
  backfill is a real policy behavior with its own fairness trade-offs
  (ADR-0001 already deferred it deliberately), not a side effect of making
  the invariant checkable.
- **Leave `Factors` as a single undifferentiated `no_capacity` and only add
  the checker.** Rejected: the checker would then have no way to distinguish
  FIFO's legal head-of-line holds from an actual bug, since both would carry
  the same label — the label and the checker have to land together.
- **Make the checker FIFO-specific (hardcode head-of-line semantics).**
  Rejected: a checker that only understands one policy's reservation shape
  won't catch or clear a future policy's holds. Treating any non-`no_capacity`
  `Factors` key as a declared reservation generalizes without naming FIFO.
