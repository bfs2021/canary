# Decision ledger and scorecard

Implementation record: 2026-10-14. Branch `decision-scorecard`.

The scorecard measures trading skill per decision, for every strategy
("sleeve"), on one scale, starting with the owner's own trades. It is a
daemon-owned verdict (`scorecard.read`) with uncertainty bands, never a single
badge. Edge stays as it is: Edge explains price consequences and grades
nothing; the scorecard is a separate read that says how much evidence there is
that a sleeve's decisions carry information, and how far that evidence is from
a decision either way.

## Owner decisions

- 2026-10-14 06:26 CEST: per-decision scoring approved. "Fine to score me, but
  we need to score an algo (or several), I'm just one trader out of many. The
  system should run by itself within risk boundaries." This is a bounded
  exception to the owner's earlier refusals of a skill score / hit rate in Edge
  (2026-09-21, reaffirmed 2026-10-13): Edge itself stays as is; the scorecard
  is a separate verdict with uncertainty bands, never a single badge.
- 2026-10-14 ~06:55 CEST: "Goal is to identify strategies, review mine, having
  a scoring etc." Every strategy, including the owner's, is scored on one
  scale.
- Never ask the owner to declare a purpose or thesis: owner decisions are
  derived from broker fills.

## Ledger

One decision record per position change (open, add, trim, exit) per contract
and sleeve, derived from the same broker statement trades Edge reads
(`edge.PositionChanges`, the exported form of Edge's execution grouping,
position anchoring and change classification, without a review window). The
ledger is append-only and daemon-owned: each record is one immutable
observation in daemon.db (`scorecard/ledger`, kind `scorecard.decision.v1`,
never decision-eligible), appended once by its stable id. Back-fill starts at
the earliest statement trade the retained projection holds.

Fields on the persisted row: decision id (hash of sleeve, contract, execution
time, side and the execution record ids), sleeve, underlying (symbol and
contract id), instrument (stock or option with expiry, strike, right and
multiplier), action, side, underlying-direction sign (+1 long exposure, −1
short: a long put or a short call is −1, a short put or a long call +1),
quantity, fill price (execution VWAP), notional in base currency, execution
FX, execution time, cluster key = sleeve × ISO week of entry, and the lineage
field `strategy_ref` (empty for hand and TWS trades; a Canary order_ref
lineage fills it later). Sleeve is `owner` for every trade today.

Entry facts that need market history (underlying close of the execution
session, the implied volatility inverted from the fill price by Black-Scholes,
and delta at entry) are derived at evaluation time from the cached history and
shown on the decision; they are regenerable and not part of the append-only
row, so a row never has to be rewritten when history arrives late. Black-
Scholes uses r = 4% a year (a constant USD policy-rate proxy; the broker's
cash-rate rows are deposit tiers, not a risk-free curve) and q = 0. When the
inversion has no solution (price below intrinsic, no convergence, vega
collapsed) the IV and delta stay unknown and the option scores that need them
say so.

Broker identifiers (order, execution and record ids) stay inside the daemon
row; the served result carries only the opaque decision id.

## History

Daily closes for each decision's underlying and for SPY, covering 252
sessions before the decision through 21 sessions after. The daemon fetches
them on the background lane (`ibkr.PriorityBackground`) inside the IBKR
history budget (60 requests per 10 minutes, `pkg/ibkr/ratelimiter.go`): at
most 20 fetches per pass, at most one fetch per name per session, names no
longer held included. A name is fetched again only while it has a decision
whose 21st session after entry is not yet in the cache. Series are kept in one
state document (`scorecard_bars`, version 1, exact contract id, up to 800 bars each).
Missing history makes a score unknown; it is counted and never imputed.

## Scores per decision

Horizons h ∈ {5, 10, 21} sessions after the execution session; the primary
horizon is 10. A decision matures at a horizon once the cache holds the close
h sessions after entry.

- Direction: z = sign × (r_u − β·r_SPY) / (σ_idio·√h), where r_u and r_SPY
  are log returns from the execution-session close to the close h sessions
  later, and β and σ_idio come from an OLS of daily log returns on SPY over
  the 252 sessions ending the session before the decision. Nothing after the
  entry enters β or σ. At least 60 overlapping sessions are required.
- Realised-vs-implied (options): |r_u| / (IV·√(h/252)).
- Wrapper (options): option P&L over the horizon, or to exit when earlier,
  minus the P&L of a static delta-equivalent stock position (delta at entry ×
  multiplier × signed quantity × (S_h − S_0)), in base currency at execution
  FX. The option's value at the horizon comes from a statement mark on that
  day, from the closing fill when the contract was flat by then, or from the
  option event (expiration is zero, exercise and assignment are intrinsic at
  the underlying close); otherwise the wrapper is unknown. Retained
  statements carry a position snapshot per statement, so marks are daily only
  where the daily acquisition ran and sparse across the quarterly back-fill
  chunks.
- Slippage: unknown. Canary retains no quote at the fill (markouts are read
  30 minutes and one close after it, and the journal keeps no preview quote
  on the fill), so the field is served as unknown with that reason until a
  fill-time quote capture exists.

## Evidence per sleeve

At the primary horizon, decisions are grouped into clusters by sleeve × ISO
week of entry and each cluster contributes its mean z once. With n clusters
and running sum S_n:

- Posterior on IC with prior N(0, 0.1²) and unit variance per cluster mean:
  mean S_n/(100+n), variance 1/(100+n); P(IC > 0.05) and P(IC > 0.1).
- Mixture-SPRT e-value E_n = (1+nτ²)^(−1/2) · exp(τ² S_n² / (2(1+nτ²))),
  τ = 0.2, reported only while S_n > 0, else 0 ("against").
- Gates: `established` when E ≥ 100, n ≥ 100, P(IC > 0.05) ≥ 0.95, the mean
  z has the same sign in both chronological halves and in up- and down-market
  clusters (the sign of SPY's mean horizon return within the cluster);
  `futile` when n ≥ 60 and P(IC > 0.1) < 0.05; otherwise `accruing`.
- Also the wrapper mean with a 95% bootstrap interval (1,000 resamples,
  seeded) and the realised-vs-implied median.
- Holm correction across sleeves when more than one is live
  (`scorecard.Holm`); only `owner` exists today, so it is a pass-through.

With a true IC of 0.3 per cluster the closed form passes E ≥ 100 at n = 145
clusters; a sleeve of one decision a week needs about three years, a sleeve
with several decisions a week proportionally less. Zero skill keeps E small
(a test martingale: P(E ever ≥ 100) ≤ 1%) and reaches `futile` once the
posterior has narrowed, from n ≈ 100 onward depending on the draw.

## Surfaces

`scorecard.read` (daemon RPC, 10 s, dispatched through `verdict_reads.go`)
serves the publication the worker saved after its last pass; readers never
compute a score. `canary scorecard [--sleeve S] [--horizon 5|10|21] [--json]`
and the MCP tool `canary_scorecard` fetch it through `scorecard.Fetch`. The
CLI says in plain words what the numbers mean and how large the sample is; a
sleeve is never called skilled before `established`, and `accruing` reads as
"not enough evidence yet".

The worker runs after start, every two hours, and when Edge's statement
evidence changes. The publication (`scorecard_publication`, version 1) carries the
evidence per sleeve, the matured decisions with their scores and the coverage
counts (unknown by reason).

## Tests

`internal/scorecard`: zero-skill random z keeps E small and reaches futility;
IC 0.3 is established at the closed-form cluster count and in a seeded
simulation; β and σ use only pre-decision sessions (a look-ahead sentinel in
later bars changes nothing); Black-Scholes IV round-trips; direction sign for
long and short puts and calls; a week counts once however many decisions it
holds; missing history is unknown, not zero. `internal/edge`: position changes
match Edge's own classification. `internal/daemon`: the ledger appends a
decision once and reloads it. `internal/cli`: golden render. `internal/stress`:
`TestOnlyTheDaemonEvaluates` names the scorecard evaluators.
