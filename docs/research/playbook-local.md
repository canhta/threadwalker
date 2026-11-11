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

Measured gate simulations use the predeclared v2 seeds 101/211/307 and the same
score, split baseline and Fisher engine as production. Parameters and all 26
scenario summaries are canonical in
[`gate-v2.json`](../../internal/playbook/testdata/gate-v2.json) and
[`gate-v2-results.json`](../../internal/playbook/testdata/gate-v2-results.json).
The original failed policy/result is preserved separately in
[`gate-v1-results.json`](../../internal/playbook/testdata/gate-v1-results.json),
with its immutable source commit for reproduction. V1 is not the shipped engine.

| Synthetic scenario | Horizon from first tagged action | Measured result |
|---|---:|---|
| Stationary null, 1,500 runs with five hypotheses each | 430 days | At least one promotion in 2/1,500 runs (0.1333%); seeds individually 1/500, 1/500, 0/500 |
| 2x rate, 1,500 runs / 7,500 hypotheses | 180 days | 7,019 detected; 481 pending/undetected; median 51 days each seed, p90 102/101/101 days |
| 4x rate, 1,000 hypotheses | 180 days | 1,000 detected, median 27 days, p90 51 days |
| 1.25x rate, 1,000 hypotheses | 430 days | 24 detected, 976 undetected |
| 2x with 25% stale/censored observations, 1,000 hypotheses | 180 days | 528 detected, 472 undetected, median 68 days |
| Common start rise, mid-rise, mid-fall, reversal, 600 runs each | 430 days | Promotions in 5/600, 1/600, 4/600, 1/600 runs respectively; every seed below 5% |

Detection quantiles are conditional on detection; the undetected denominator
includes rejected and pending entries. The effect multiplies synthetic engagement
rates, not the binary success probability: 2x produced candidate success rates
0.7101–0.7131 against frozen reference rates 0.4996–0.5041. Views use a Pareto tail
(shape 1.3, cap 1e9); rates use a capped lognormal (median 1%, log SD 1.25), integer
likes, and zero replies/reposts. The synthetic history is 300 untagged controls at
30/day; cold-start estimates add 11.25 days including observation lag. Candidate
operation uses 30 total actions/day, 20 tagged plus 10 untagged controls, with fixed
round-robin allocation across five hypotheses. Observations arrive after 24h plus
uniform 0..6h lateness; stale cases arrive at 32h and are excluded.

The paired-control availability cost is material. Across the primary 2x cases,
N=50 had 7,500/7,500 looks unavailable, N=100 had 2,184/7,500, N=200 had 7/5,296 and
N=400 had 1/1,815. The 180-day horizon did not reach N=800 or 1600. In the 430-day
stationary null cases, N=400 had 3/7,497 unavailable, N=800 had 5/7,496, and N=1600
had 7,496/7,496 unavailable because the exact day allocations were fractional.
There is no rounding or retrospective look retry to recover that power. The
`look_counts` arrays retain attempted and unavailable counts for all six looks.

Every run makes zero provider calls and spends $0; synthetic action totals are
explicit per scenario. For example the three 2x cases generated 1,234,284,
1,255,778 and 1,245,786 synthetic actions across their 500 runs, including each
run's baseline and controls, stopping when all hypotheses were decided. These
are simulated operational counts, not billed tokens or real account activity.

Reproduce the shared report and redacted decision trace using [docs/evals.md](../evals.md#playbook-mutation-outcomes-and-seeded-gate-simulations).
`TestBaselineFreezeExcludesInvalidOrFutureControls` verifies prior-only source
eligibility. `TestFractionalDayAllocationConsumesLookWithoutRounding` exercises
a real N=400 collector look with abundant controls but fractional daily
requirements. `TestGateLookUsesActualObservationOrder` ensures the look timestamp
tracks the newest counted observation even when publication order differs.

The evidence files retain the original producer commits from the worker checkouts.
Their source is preserved in the main history: v1 gate/helpers are at `a1d2e1a`,
v1 simulation/fixture at `99e4de2`, and v2 simulation/fixture at `148d139` with
matched gate `ecc53b1`. Per-look reporting was added at `8236e61` without changing
the policy, RNG sequence or measured outcomes; the recorded summaries landed at
`771deb8`. These main-history commits can be resolved from a fresh clone, while
the producer hashes are provenance labels for the original measurements.

To rerun the superseded v1 policy without changing a checkout or creating a branch:

```sh
playbook_repro_dir=$(mktemp -d)
git archive a1d2e1a | tar -x -C "$playbook_repro_dir"
mkdir -p "$playbook_repro_dir/internal/playbook/testdata"
for playbook_path in internal/playbook/simulation.go internal/playbook/simulation_test.go internal/playbook/testdata/gate-v1.json; do
  git show "99e4de2:$playbook_path" > "$playbook_repro_dir/$playbook_path"
done
(cd "$playbook_repro_dir" && go test ./internal/playbook -run TestPredeclaredSimulationSuite -count=1 -v)
```

The current main checkout and shared eval command reproduce the shipped v2 policy.
