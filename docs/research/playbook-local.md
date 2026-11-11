# Local playbook evidence

The canonical learning contract is in [spec section 7](../spec.md#7-learning).
This implementation uses synthetic observations and an injected clock; it does
not establish live Threads engagement, elapsed live qualification or causal effect.

The fixed policy lives in `internal/playbook/gate.go`; the actual revision-bound
collector lives in `internal/playbook/processor.go`. Schema, source IDs, counters,
consumed looks and terminal decisions persist through migration 006. The guard
captures the revision and freezes an eligible prior-only baseline while reserving
the action intent. Counters advance only when a completed 24-hour metric target
references an eligible own confirmed non-dry-run sample.

`TestFrozenBaselineCountsOnceAndEstablishesWithMetadataOnlyDecision` exercises
real guarded reservations, the metric job store, rollback after a forced DB
failure, recovery, isolation and redacted decision logs. It deliberately uses a
large synthetic positive effect with 200 same-day controls; those controls are
not an estimate of an account's real posting volume.

`TestCommonShiftsUseExactlyDayMatchedControlIDs` checks account-wide start/up,
mid-up and mid-down changes, exact source IDs and missing, fractional, foreign,
wrong-kind, stale and future control support. It verifies that late control
arrivals cannot reopen a consumed look. The sampling policy requires exact
posting-day proportions and therefore sacrifices power when support is sparse.

The original frozen-reference-only simulation falsely established all 200 runs
under a common account-wide rise. It was rejected before release; day-matched
controls are part of the shipped policy. Remaining limitations include within-day
time effects, adaptive assignment and nonrandomized exposure. Local simulation
results must always identify their policy, seed, horizon, action allocation,
censoring and paid-call estimate explicitly.
