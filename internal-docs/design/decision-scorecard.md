# Decision ledger and scorecard

Implementation record: 2026-10-14. Branch `decision-scorecard`. Gate
redesign, sector adjustment and fill-quote slippage: 2026-10-19, branch
`scorecard-gate` (the review of 2026-10-18, strategy engine map builds 3 and
7). The thresholds in "Gates" are proposed; the owner decides them.

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

Fields on the persisted row: decision id (hash of contract, execution time,
side, action, quantity and the execution record ids; the sleeve is an
attribute, so a fill keeps its id when later lineage reclassifies it),
sleeve, stream, origin, underlying (symbol and contract id), instrument (stock
or option with expiry, strike, right and multiplier), action, side,
underlying-direction sign (+1 long exposure, −1 short: a long put or a short
call is −1, a short put or a long call +1), quantity, fill price (execution
VWAP), notional in base currency, execution FX, execution time, the week key
(`cluster_key` = sleeve × ISO week of entry), the unit key (`unit_key` = the
week key × underlying contract id: the name-week the decision's z counts in;
derived at evaluation for rows persisted before 2026-10-19), and the lineage
field `strategy_ref` (empty for hand and TWS trades; a Canary order_ref
lineage fills it later).

### Streams: entries and exits

Pooling every position change into one stream would mix two skills: entry
selection (does the name move with the decision after an open or add?) and
exit timing (does it move against the position after a trim or exit?). Many
exits are not discretionary at all: protection stops, expiry closes, budget
reductions, exercise and assignment. Pooling would dilute the entry score that is
the owner's candidate edge and make the gate meaningless. So each decision
carries a stream: `entries` (open, add) is the primary stream and the only
one with gates, the Holm correction and the CLI headline; `exits` (trim,
exit) is reported per sleeve with the same statistics and never gates. A
flip (long to short in one fill) is Edge's exit part plus its open part, each
in its own stream.

### Index streams

Added 2026-10-14 09:45 CEST (branch `scorecard-index`). A decision on an
underlying that is the market proxy, or effectively the market, is market
timing or a hedge: a different skill from single-name selection. Regressed on
SPY such an underlying leaves no idiosyncratic noise (SPY on itself fits
exactly), so the single-name z was undefined (`idio_vol_zero`) or, for a near
fit such as SPX, a ratio over residual noise close to zero. A decision's
underlying is an index when either holds:

- its symbol is on the fixed list of broad index products: SPY, SPX, SPXW,
  XSP, QQQ, NDX, IWM, RUT, DIA (`scorecard.IndexUnderlying`);
- its pre-entry regression on SPY (the window and the at-least-60-sessions
  rule of the direction score below) has R² ≥ 0.90 (`IndexRSquared`), which
  catches the broad-market ETFs off the list.

An index decision is scored as z = sign × r_u / (σ_total·√h), where σ_total
is the sample standard deviation of the underlying's own daily log returns
over the same pre-entry window as β; nothing after entry enters σ_total, R²
or the class. It is published in `index_entries` (open, add) or `index_exits`
(trim, exit) of its sleeve, reported with the same statistics as exits and
never gated, so the gated single-name `entries` stream holds selection
decisions only. The split mirrors entries and exits: whether the index moved
the way an opened hedge or market bet positioned for is a different question
from whether it moved against a position after it was cut, and many index
exits are hedge rolls and expiry closes that would dilute the entry timing
read. `index_entries` also reports the realised-vs-implied median of its bought
puts (`long_put_realised_vs_implied`): did the hedges realise the move they
paid for?

The class needs history (R²), so it is derived at evaluation time like the
other entry facts: the ledger row keeps its action stream, the published
decision carries the index stream with `entry.index_basis` (`list` or
`r_squared`) and `entry.market_r_squared`. A name off the list reads as a
single name until its pre-entry history is cached.

### The rule sleeve

Fills that Canary's own rules or the broker's lifecycle produced are the
`rule` sleeve, with an `origin`, and count for no owner stream. They stay in
the ledger and the decision list and are counted under
`coverage.rule_originated` by origin. The daemon's resolver
(`scorecardSleeveResolver`) reads two local sources:

- Canary's order journal, joined to the statement fill by execution id (the
  same join Edge's protection context uses), and the proposal records joined
  by order reference. A fill whose submitted proposal carries one of the
  buckets `trailing_stop`, `option_loss_exit`, `option_profit_trail`,
  `budget_reduction`, `mandate_reduction`, `option_expiry_close`,
  `theta_hygiene`, `risk_reduction`, `strategy_exit`, `option_exit_review`, `cash_sweep` or
  `currency_leveling`, or whose journal origin is `daemon-preauthorised`,
  `daemon-protective-guard` or the daemon's internal transmit origin, is
  `rule` with origin `proposal:<bucket>` or `origin:<origin>`.
- The broker's option lifecycle rows (`OptionEAE`: exercise, assignment,
  expiration and their underlying deliveries) share a trade id with the
  trade rows they produced; such a fill is `rule` with origin `lifecycle`,
  whatever the journal says.

Open since 2026-10-18 (strategy-register.md, section 5): the journal path
needs a round-trip test before a classification rule changes. Lifecycle and
fill events are journaled without an origin, which the journal stores as
`daemon_internal`, so by the last clause above a journaled fill would read as
`rule`, the owner's included, and an owner order queued for the open would
too; yet the live publication tags only lifecycle fills, which suggests the
journal join matches no fill at all. The fix is to classify a fill by its
order's staged attempt, joined by order reference, never by a lifecycle
event's transmit origin. When Desk's automatic submission is served, its
writes journal their own origin (`desk-automatic`) with the item's source,
and an entry-rule addition counts as the owner's (the owner configures and
arms it) until the rule is registered as a strategy with its own sleeve.

What remains unclassifiable: the Flex trade row keeps no transaction type or
notes, so a lifecycle delivery without an `OptionEAE` row, a stop that rested
at the broker outside Canary's journal, a Canary order older than the
journal's 400-day read, and any fill while the bounded journal or proposal
read is withheld (10,000-record cap) all read as the owner's. The coverage
counts say how many rows the resolver could tag; they never say the owner
stream is clean of rule fills.

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

Daily closes for each decision's underlying, for SPY and for each sector
fund the ledger's names belong to, covering 252 sessions before the decision
through 21 sessions after. The daemon fetches them on the background lane
(`ibkr.PriorityBackground`) inside the IBKR history budget (60 requests per
10 minutes, `pkg/ibkr/ratelimiter.go`): at most 20 fetches per pass, at most
one successful fetch per series per session, names no longer held included.
The order of a pass is the market proxy, then the sector funds (at most one
series per sector, eleven in all, by symbol: `cache.Sectors`, read by the
symbol route like SPY and stale on the same rule), then the names by earliest
decision. A sector fund is needed only while a name of its sector has a
decision; its lookback runs from that sector's earliest decision. A name is fetched again only while it has a
decision whose 21st session after entry is not yet in the cache. Series are
kept in one state document (`scorecard_bars`, version 1, exact contract id,
up to 800 bars each). Missing history makes a score unknown; it is counted
and never imputed. A lookback above a year is asked for in years
(`formatHistoricalDuration`: 400 days is "2 Y", three years "3 Y"), the form
IBKR accepts for daily bars.

One read waits up to four minutes behind the pacing queue
(`scorecardFetchTimeout`, the market history refresh's window): the
historical bucket admits one request per 10 s and Edge's fan-out and the
market and mandate history readers share it, so a read sent right after a
restart can wait minutes before the broker sees it. A failed read is not
written off for the day (fixed 2026-10-18; until then a failure was stamped
as fetched today and replaced the series, so one timeout cost the name its
day and its bars): the closes of the last successful read stay behind the
scores, the series records the broker's answer, when it came and how many
reads in a row failed, and the name is asked again after one pass (2 h),
doubling per consecutive failure up to a day. A gateway that is away ends the
pass without a mark against any name; the names it did not reach are pending
for the next pass. The worker warns once per name per day in the daemon log;
the result lists every current failure (`history.failures`: symbol, contract
id, closes kept, the broker's answer, when it failed and when it is retried),
the market proxy first.

## Scores per decision

Horizons h ∈ {5, 10, 21} sessions after the execution session; the primary
horizon is 10. A decision matures at a horizon once the cache holds the close
h sessions after entry.

- Direction: z = sign × (r_u − β·r_SPY − β_s·r_sector) / (σ_idio·√h) for a
  single name and z = sign × r_u / (σ_total·√h) for an index (above), where
  r_u, r_SPY and r_sector are log returns from the execution-session close to
  the close h sessions later. β, β_s and σ_idio come from an OLS of the
  name's daily log returns on SPY and its sector fund together; σ_total, R²
  and the index class from the OLS on SPY alone; both over the 252 sessions
  ending the session before the decision. Nothing after the entry enters β
  or σ. At least 60 overlapping sessions are required. The sector fund is
  the S&P 500 sector fund of the name's GICS sector as the S&P 500 sector
  registry (`spx.SectorOf`, refreshed with the membership) lists it: XLK,
  XLF, XLV, XLY, XLP, XLE, XLI, XLB, XLU, XLRE, XLC
  (`scorecard.SectorFund`). The decision says which adjustment it got
  (`entry.market_adjustment`: `spy_sector`, or `spy` with
  `entry.sector_reason`: `sector_unknown` for a name off the S&P 500,
  `sector_history_unavailable` while the fund's series is short or
  collinear with SPY); the fund and sector are shown on the entry, β_s and
  r_sector on the score. Added 2026-10-19 because the SPY-only residual
  counted sector beta as skill (the review of 2026-10-18). Entry
  is measured from the close of the execution session, not from the fill:
  the intraday move after the fill is excluded, which is conservative (a
  fill that was right within the day earns nothing for it) and avoids any
  intraday look-ahead, since the daemon holds no intraday history for the
  fill's session.
- Degenerate volatility: a perfect fit is now an index, so `idio_vol_zero`
  remains only for a single name whose closes did not move across the
  window (a halted or stale series, where R² is undefined) or, as a guard,
  when SPY itself did not move; `total_vol_zero` is an index whose own closes
  did not move.
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
- Slippage (2026-10-19): the half-spread the decision paid, from the quote
  the daemon retained at its fill (fill-quotes.md): (fill − mid) × side ×
  quantity × multiplier, summed over the decision's executions, in the
  contract currency and in base at each fill's FX, with the quantity-weighted
  basis points of the mid and the longest quote delay. The daemon matches by
  execution id first (`FillQuoteByExecID` for every id in the row's lineage);
  when none is retained, the retained fill on the same contract nearest in
  time within five minutes stands in (`FillQuoteNear`; `slippage.match` =
  `time`). Five minutes because the broker's execution time is kept to the
  second and a receipt stands in for it only within a minute, one order's
  partial fills on one contract rarely spread further, and two distinct
  decisions on one contract minutes apart are rare in the statements. The
  pure core only sums and reports (`Input.Costs`). `slippage_reason` names
  the real cause when nothing was measured: `no_fill_quote` only for a fill
  before 2026-10-18 (`scorecard.FillQuoteCaptureSince`, when capture began:
  such a fill has no quote and never will); `execution_not_observed` for a
  later fill the daemon never saw (TWS fills invisible to a non-master
  client, or the daemon down); otherwise the retained record's own reason
  (`timeout`, `quote_not_two_sided`, `quote_crossed`, `quote_not_live`, …);
  `fx_unavailable` when the half-spread is known but not its base sum. The
  decision carries it once (`slippage`); the per-horizon `slippage_bp` and
  `slippage_reason` repeat it for one release (below, "Deprecations").

## Evidence per sleeve and stream

Redesigned 2026-10-19. The review of 2026-10-18 found that the gate of
2026-10-14 needed 13 to 21 years to read `established` for a good selector,
killed a true edge a third of the time, counted sector beta as skill, called
the z-mean an "IC" and let overlapping horizons inflate the false-positive
rate (the statistician's memo, Desk `output/secret-sauce-20261018/`).

### Units and overlap

One unit of evidence is a name-week: the decisions of one underlying in one
sleeve, stream and ISO entry week count once, through their mean z. Several
names in one week are several units (the week-cluster of 2026-10-14 averaged
them into one unit of assumed unit variance and threw the breadth away:
"several decisions a week proportionally less" was false). Units whose
horizons overlap share returns, so under no information the covariance of
two units' z is the overlapping share of the horizon, 1 − gap/h with the gap
in calendar days against a horizon of 7h/5 days: fully for the same name,
scaled by `CrossNameResidualCorrelation` = 0.1 across names (the residual
correlation left after the market and sector adjustment; conservative for
US large caps). Only entry days and h enter, never anything after entry.
The null covariance V of the sum of n units (V ≥ n) gives the effective
sample n_eff = n²/V and S_eff = S·n/V (`scorecard.EffectiveSample`); every
sequential statistic reads the effective sample, and S_eff/n_eff is the
score per unit S/n. The sum's variance is exact under the null for the
stated covariance; the e-value is then a valid e-value at every read, and
the placebo below guards the practical calibration.

### The directional score

The measure is the directional score: the mean z per unit, how far in units
of its own daily noise the name moved the decision's way net of the market
and its sector. It is not an information coefficient (no correlation is
estimated), so "IC" is gone from the CLI, the JSON, the MCP description and
this record; the JSON keeps the old keys one release (below).

### Statistics

With the effective sample (n_eff, S_eff):

- Posterior on the score with prior N(0, 0.1²) and unit variance per
  effective unit: mean S_eff/(100+n_eff), variance 1/(100+n_eff); P(score >
  0), P(score > 0.05) and P(score > 0.10).
- Mixture-SPRT e-value E = (1+n_eff·τ²)^(−1/2) · exp(τ²·S_eff² /
  (2(1+n_eff·τ²))), τ = 0.2, reported only while S_eff > 0, else 0
  ("against"). P(E ever ≥ 20) ≤ 5% under no information however often it
  is read.
- Futility e-value exp(−δ·S_eff + δ²·n_eff/2) with δ = `FutilityScore` =
  0.05: the likelihood of no information against a score of 0.05, the
  smallest worth a risk allocation (a good selector in Grinold and Kahn's
  terms; the statistician's memo puts the owner's allocation at 0.02 to
  0.05). It is a test martingale under any true score ≥ 0.05, so such a
  sleeve is called futile with probability at most 1/20 whenever the reading
  stops; the posterior rule of 2026-10-14 (P(IC > 0.10) < 0.05 at n ≥ 60)
  killed a true 0.10 a third of the time within five years.
- Placebo: the units' signs are redrawn at random 200 times (seeded by the
  unit count, so the same evidence yields the same placebo) and the
  statistical bar (E, n_eff, posterior) re-read on the same effective
  sample. Under no information the sign choice is a coin, so the bar must
  stay out of reach: the share of shuffles reaching it must not exceed 5%
  (`PlaceboMaxRate`), else the evidence comes from the magnitudes, not the
  directions, and the sleeve is not established. The standardised sd of the
  shuffled sums is reported (about 1 when the overlap covariance is
  calibrated, below it when conservative).
- Also the wrapper mean with a 95% bootstrap interval (1,000 resamples,
  seeded), the realised-vs-implied median, the bought-put median of
  index_entries, the sum of the half-spread paid with the decisions without
  one by reason, and how many scored decisions were net of the sector fund.
- Holm correction across sleeves' entries streams when more than one sleeve
  is live (`scorecard.Holm`); only `owner` exists today, so it is a
  pass-through. The rule sleeve is never a live sleeve.

### Gates (proposed 2026-10-19, for the owner's decision)

- `established`: E ≥ 20 (`EstablishedEValue`), n_eff ≥ 60
  (`EstablishedUnits`) over at least 26 entry weeks (`EstablishedWeeks`, so
  one lucky quarter never establishes), P(score > 0) ≥ 0.975
  (`EstablishedPosterior`), the placebo within 5%, and the same sign of mean
  z in both chronological halves of the units and in up- and down-market
  units (the sign of SPY's mean horizon return within the unit). Every
  component is disclosed (`gates`).
- `futile`: n_eff ≥ 60 (`FutileUnits`) and futility e-value ≥ 20
  (`FutileEValue`).
- Otherwise `accruing`. The words are unchanged, so Desk's wording survives.

What changed against 2026-10-14 and why: E ≥ 100 → 20 (an anytime-valid 5%
instead of 1%; with one live sleeve the Holm correction adds nothing, and
the e-value's optional-stopping guarantee is stricter than a fixed-sample
5%); n ≥ 100 week-clusters → 60 effective name-weeks over 26 weeks; P(IC >
0.05) ≥ 0.95 → P(score > 0) ≥ 0.975, because the 0.05 bar at 95% needs a
posterior sd of 0.03, about 1,000 effective units, and was the binding gate
at a true 0.10 while never reachable at a true 0.05; futility as an e-test of
the 0.05 bar instead of a posterior whose prior already put 84% below 0.10.
The projection `units_to_establish` is the effective count at which E passes
if every further unit adds the current score; `weeks_to_establish` converts
it at the sleeve's own pace of effective units per entry week.

### Power

Synthetic, `internal/scorecard/power_test.go` (`SCORECARD_POWER_PATHS=200`,
run 2026-10-19). Selectors with a true directional score of 0, 0.05 and 0.10
trade k names a week from a rotating pool; each unit's z is the standardised
sum of the h = 10 daily residual returns after its entry, so the same name a
week later shares half its path and names in one week share a common part
at correlation 0.1 — the covariance the proposed rule assumes. Each path
reads both gates' statistical bars weekly over 20 years; the consistency
gates (halves, markets, placebo) are left out, so the times are lower bounds
for `established` and the kill rates are exact for the statistical rule.
"Established first" and "futile first" are shares of 200 paths; the years
are the median and 80th percentile over all paths (— when fewer than half,
or a fifth, ever got there).

| True score | Names a week (pool) | Today's gate: established, median / 80th | established | futile (true edge killed) | Proposed gate: established, median / 80th | established | futile |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 0 | 1 (1) | — | 3% | 95% | — | 1% | 3% |
| 0 | 6 (30) | — | 0% | 100% | — | 2% | 71% |
| 0.05 | 1 (1) | — | 12% | 81% | — | 6% | 1% |
| 0.05 | 6 (30) | — | 0% | 80% | — | 40% | 3% |
| 0.05 | 12 (60) | — | 0% | 73% | 16.3 y / — | 59% | 3% |
| 0.10 | 1 (1) | — | 37% | 55% | — | 29% | 0% |
| 0.10 | 6 (6) | — | 31% | 24% | 7.2 y / 12.9 y | 94% | 0% |
| 0.10 | 6 (30) | — | 25% | 15% | 5.3 y / 9.7 y | 98% | 0% |
| 0.10 | 12 (60) | — | 26% | 3% | 3.9 y / 7.0 y | 99% | 0% |

Reading: today's gate establishes a true 0.10 within 20 years on a quarter
of the paths and kills it on up to a quarter (the review's third was at a
0.10 futility bar over five years); it also lets zero skill through on 3% of
one-name paths because the week mean's variance is understated under
overlap. The proposed gate establishes a true 0.10 at six rotating names a
week in a median 5.3 years (3.9 at twelve) and kills a true 0.05 or 0.10 on
at most 3% of paths, with zero skill established on at most 2%. A true 0.05
is a long road at any pace (half the paths within 20 years at twelve names a
week): distinguishing 0.05 from 0 at 5% both ways needs about 4,000
effective units, and no gate changes that; only breadth does. The one-name
sleeve at 0.10 (`6 (6)` and `1 (1)` rows) shows the overlap cost: the same
names every week share their paths, and a sleeve that trades six fixed names
reads about a third slower than one rotating through thirty. Today's
reading of the owner's sleeve (about 1,078 scored decisions, mean z per week
about +0.10, "1,260 weeks" to the old bar) will restate itself in name-weeks
and effective units on the first pass; the directional score per unit is
expected near the old weekly mean, and the distance line now says how many
weeks at the sleeve's own pace.

### Deprecations (one release, from v3.24)

The JSON keeps the keys of 2026-10-14 beside the new ones and drops them one
release later: `clusters` (= `units`), `mean_z` (= `directional_score`),
`p_ic_above_0_05` and `p_ic_above_0_10` (= `p_score_above_0_05`,
`p_score_above_0_10`), `clusters_to_establish` (= `units_to_establish`),
`gates.clusters`, `gates.up_clusters`, `gates.down_clusters` (= `units`,
`up_units`, `down_units`), `method.clusters` (= `method.units`), and the
per-horizon `slippage_bp` and `slippage_reason` (= the decision's
`slippage`). `schema_version` is `canary-scorecard-v2`; the publication
version is 3 (an older publication is not served until the first pass).

### Desk

Desk reads `canary_scorecard` through its pinned client and shows the gate
word. New fields worth showing: `units`, `effective_units` and `weeks` beside
the sample; `directional_score` in place of "mean z per week"; `p_score_above_0`;
`futility_e_value`; `weeks_to_establish`; `placebo` (`established` of
`replicates`); `gates.units`, `gates.weeks`, `gates.placebo`; `slippage`
per stream (`half_spread_base`, `quoted` of `decisions`, `mean_bp`,
`unquoted` by reason) and per decision; `entry.market_adjustment`,
`entry.sector`, `entry.sector_fund`; `history.sector_bars`. Desk's reference
distance (τ = 0.2, "E ≥ 100", "n ≥ 100" in the method strings) no longer
matches the served method and will hide itself; the served
`weeks_to_establish` replaces it.

## Surfaces

`scorecard.read` (daemon RPC, 10 s, dispatched through `verdict_reads.go`)
serves the publication the worker saved after its last pass; readers never
compute a score. `canary scorecard [--sleeve S] [--horizon 5|10|21] [--json]`
and the MCP tool `canary_scorecard` fetch it through `scorecard.Fetch`. The
CLI says in plain words what the numbers mean and how large the sample is; a
sleeve is never called skilled before `established`, and `accruing` reads as
"not enough evidence yet". It shows each sleeve's entries, then exits, then
its index streams once the sleeve has an index decision, each with a
one-line meaning ("market timing and hedges: did the index move the way the
decision positioned for?"). After the history line, a "History failures"
block names each name whose last read failed, with the broker's answer, when
it failed, when the daemon asks again and how many closes it kept.

The worker runs after start, every two hours, and when Edge's statement
evidence changes. The publication (`scorecard_publication`, version 3 since
the name-week gate; an older publication is not served) carries the
evidence per sleeve and stream, the decisions with their scores and
slippage, and the coverage counts (entries, exits, index entries, index
exits, rule-originated by origin, unknown by reason, decisions with a fill
quote and without by reason).

## Tests

`internal/scorecard`: an exit-only sleeve, however loud, never moves the
entries gate and is never gated itself; rule-originated and lifecycle fills
never count for the owner and keep their id when reclassified; zero-skill
random z keeps E under 20 and reaches futility; a score of 0.10 passes the
closed-form unit count and, on twelve rotating names a week in a seeded
simulation, the whole gate within ten years and never before 26 weeks; the
power table (`power_test.go`) kills a true 0.05 or 0.10 on at most an eighth
of its sample paths and never establishes zero skill on more than a tenth;
a name counts once a week however many decisions it holds, two names in a
week are two units, and the effective sample follows the overlap covariance
exactly; weekly units of one name at 21 sessions keep about a quarter of
their count while units a horizon apart keep it all; the placebo passes
loud, consistent signs and is a gate component; a name that loads on its
sector fund is scored net of both betas with less residual noise than SPY
alone leaves, says `spy_sector`, and falls back to `spy` with the reason
when the fund is unknown or its history short; slippage names the real
cause by date and record and the stream sums the measured half-spread; β
and σ use only pre-decision sessions (a look-ahead sentinel in later bars
changes nothing); Black-Scholes IV round-trips; direction sign for long and
short puts and calls; missing history is unknown, not zero; a SPY option is scored on total
volatility in index_entries with the bought-put median; a broad ETF off the
list with R² ≥ 0.90 is routed to the index streams while a list member is
routed whatever its fit; a single name keeps the market-adjusted z exactly;
post-entry bars change neither σ_total, R² nor the class; only flat series
leave `idio_vol_zero` and `total_vol_zero`; each unknown reason names the
series that is missing (`market_history_unavailable` when the name has
history and SPY has none, `underlying_unknown` when the statement named no
underlying contract). `internal/edge`: position changes match Edge's own
classification. `internal/daemon`: the ledger appends a decision once and
reloads it; the resolver tags proposal buckets and daemon origins as rule and
leaves hand and TWS fills to the owner; the history plan reads one sector
fund per sector after the market and before the names, once per session;
the sector map names one fund per S&P 500 name and none for a name off the
list or an index; fill costs match by execution id, then by contract and
time within five minutes and not beyond, and keep an unavailable quote's
reason; a failed read keeps the last closes,
leaves the session stamp alone and is retried at its retry time with the
doubling wait, a gateway that is away ends the pass unmarked, the result
lists the failures and the log warns once per name per day. `pkg/ibkr`: a
lookback above a year is asked for in years. `internal/cli`: golden render.
`internal/stress`: `TestOnlyTheDaemonEvaluates` names the scorecard
evaluators.
