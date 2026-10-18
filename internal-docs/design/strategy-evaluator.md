# Strategy evaluator (one evaluator for every register entry)

Written: 2026-10-18 18:10 CEST. Status: built on Canary branch
`strategy-evaluator`, 2026-10-18 (section 8). This record follows
`.agents/docs/risk-policy-contract.md`: it proposes the keys the owner may
write and creates no policy. It carries out plan item 4 of the review of
2026-10-18 (Desk `output/secret-sauce-20261018/review.md`, finding 2: "a
strategy is a project, not a register entry") against `strategy-register.md`
and `core-sleeve.md`.

Sources read: Canary main `73577985` (`internal/core`, `internal/register`,
`internal/risk`, the daemon's core files), the lab `0870f3b` (`core_rule.py`,
`core_fixture.py`, `s0_core_sleeve.yaml`), Desk `9f0d0ff`.

## Decision

- Goal: one evaluator in Canary runs any entry of the strategy register. An
  entry names its instrument (ISIN and currency), a signal from a small
  library, the owner's numbers (band, minimum, value cap) and its kill rule;
  the sizing is against its sleeve's risk allocation at the register's stage
  and scale. The evaluator serves the verdict shape `core.target` serves, so
  the gate, the forced reduction, the shadow record and Desk's controller
  read one number per strategy.
- Protected behaviour: the core's verdict is unchanged (its goldens and the
  parity fixture prove it); no entry adds risk, reserves allocation, creates
  a proposal or places an order: every entry rule sends nothing, so every
  entry runs in shadow at most. The one order path is untouched.
- Policy owner: the desk owner, who writes every entry, promotion and number;
  this build proposes keys only (§2). Status: built; the second entry (the
  trend fund) waits on the owner's decisions (§7).

## 1. What the core's code became

`internal/core` (pure) was already instrument-free: `Measure`, `SizeTarget`,
`Guard`, `Headroom` and `Decide` take a calendar, closes, a decision day and
the owner's numbers. It is now the evaluator's arithmetic:

| Before | After |
|---|---|
| `Measure` with the 10-month average compiled in | `MeasureWith(trend, label, …)`: the filter is a library entry, the digest label the strategy's; `Measure` is the core's call, byte for byte |
| `Sizing.CoreVolPct` | `Sizing.AllocationPct` (JSON `allocation_pct`): the sleeve's risk allocation, whichever sleeve |
| `Digest(w)` names `core-ibcf-v1|<spec>` | `DigestFor(label, w)`; the core's label is unchanged, so no frozen month drifts |
| `Guard` corroborates on SPY | `GuardWith(…, proxy)`: the instrument's returns proxy, or none (section 5) |
| `Decide` names IBCF and "the core" | `DecideInput.Symbol` and `.Name`; the core passes its own, so its copy is unchanged |

The compiled rule catalogue (`internal/register`) carries two rules:

| Rule | Form | Instrument | Signal | Numbers | Spec hash |
|---|---|---|---|---|---|
| `core-ibcf-v1` | `[mandate.core]` | compiled: IE00B3ZW0K18 EUR | compiled: `trend-sma10m` | the core's keys, as today | compiled and pinned; a cited hash must match |
| `timed-holding-v1` | `[mandate.strategies.<id>]` | the entry's `isin`, `currency` (`con_id`, `exchange` optional) | the entry's `signal` | the entry's `band_pct`, `min_order_base`, `max_value_pct` | the entry's citation (section 4) |

Both run through one daemon evaluator (`strategy_target.go`): the core is
its first instance, built from `[mandate.core]` with the compiled
constants, and `evaluateCoreTarget` wraps it. One contract resolver per
instrument (pins keyed by ISIN, currency and pinned id), one shadow recorder
and one frozen-month store serve every entry; the core's keys, scopes and
wording are kept exactly. `register.Resolve` gives each entry what the
evaluator runs: `Instrument`, `Trend`, `Terms`, `SpecHash`, `Data` and the
kill bound; for `timed-holding-v1` an unwritten key or a signal the library
lacks holds the entry and names it. The register's refusals are unchanged.

## 2. The entry (proposed; the owner writes it)

```toml
[mandate.strategies.trend-fund]     # id: the owner's, lower-case
rule                = "timed-holding-v1"
spec_hash           = "…"           # the lab registry's hash of the spec it implements
sleeve              = "overlay"     # its allocation is [mandate.sleeves] overlay_vol_pct
stage               = "shadow"
scale_pct           = 25
isin                = "…"           # the fund; IBKR's record names the line, venue and calendar
currency            = "EUR"         # the account's base currency, in this build (section 5)
signal              = "trend-sma10m"
band_pct            = 10
min_order_base      = 2500
max_value_pct       = 40
# con_id, exchange: optional pins, as in [mandate.core]
# kill_drawdown_k   = …             # optional, section 3; unwritten, the kill rule cannot fire
```

`risk_allocation_pct` stays as the register defines it. `[mandate.core]`
keeps its form and takes `kill_drawdown_k` as an optional key (fingerprint kept).

## 3. The signal library and the kill rule

A signal is a trend filter on month-end closes, `core.Trend{ID, Months}`,
read on the fixed window of `WindowCloses` sessions to the decision day (the
last session of the month on the listing's calendar). `trend-sma10m` is the
only entry: on when the decision close is strictly above the mean of the
last ten month-end closes, the decision's included; not live before ten
month-ends. A second filter is a new library entry with its own lab fixture
(section 4); the window stays the evaluator's, and a test refuses a filter
that needs more month-ends than the window holds. The volatility estimates
(EWMA span 63, the 252-day sample, the 15% floor) and the sizing
(allocation over σ, capped at `max_value_pct`, scaled by `scale_pct`, whole
shares) are the evaluator's, not the signal's: `core-sleeve.md` §1.

The kill rule is the lab spec's; the owner's lever is `stage = "suspended"`.
What Canary measures is the spec's drawdown test (a): the sleeve's
peak-to-trough loss in units of K = S / 0.15, marked at each decision. The
entry may write `kill_drawdown_k`, the bound the lab's design run writes into
its registry; unwritten, the verdict says the kill rule cannot fire, as the
spec says. Written, the evaluator marks the paper sleeve (cash and the paper
position) at each decision close on the shadow record and at the current
reading, and past the bound holds the strategy before the month can be
frozen: buys stop, a sale the rule wants goes to the owner, the month is
recorded as a hold naming it, and the owner suspends. The live measurement
(the sleeve's own fills) comes with the first rule that sends; the spec's
(b) and (c) are the scorecard's and the cost report's, not this.

## 4. Parity: how the lab's fixture tests any entry

The lab's fixture (`core_ibcf_v1_parity.json`) names no instrument and no
account figure: synthetic closes on a synthetic calendar, and the rule's
reading per decision at three scales for a fixed synthetic allocation. It
therefore proves the evaluator, not the core: the trend filter and the
sizing. Every entry that declares `trend-sma10m` is tested by it, so no
fixture is written per entry; `TestParityWithTheLab` runs it through the
library entry, `TestTrendLibraryCarriesTheCoreFilter` pins `Measure` to
`MeasureWith(TrendSMA10)`, and `TestParityFixturePinnedToTheSpec` pins
fixture, rule and spec together as before.

What `spec_hash` means changes for a parameterised rule. For `core-ibcf-v1`
the hash is compiled and a cited hash must match (as today). For
`timed-holding-v1` Canary carries no hash to match: the hash is the owner's
citation of the lab spec the entry implements, checked for shape, shown and
recorded on every shadow record as cited. The parity guarantee moves to the
signal: the lab spec names its signal and numbers in `signal.params` (as
`s0_core_sleeve` does), the fixture pinned to that signal proves Canary
reads it as the lab does, and the owner cites the spec whose params they
copied into the entry (`strategy-register.md`, invariant 1). A second filter
needs the lab to extend `lab core-fixture` with its months and to commit one
fixture per filter; Canary pins each.

## 5. Where the evaluator stops, and why

- Currency: an entry trades in the account's base currency only; another
  holds it. Sizing a base-money target against a price in another currency
  needs an FX rate at the decision and funding by conversion, both outside
  the core's invariants; they come with the first entry that needs them.
- Data guard: a move beyond 8% is corroborated by the instrument's returns
  proxy where the mandate has one (`mandate.ReturnsProxies`: SPY for IBCF);
  an instrument without one holds the month on such a move, for review.
- Series: the core's stays in the mandate's history pass. Every other
  entry's is read after the verdict, one per entry per refresh, under
  `strategy:<id>:con:<contract id>` (register review S2), so an entry never
  ages the book's verdict; a test pins that no such key enters its evidence.
- Holding and placement: every unit of the core's contract is the core's.
  An entry's holding is what the allocation plan places in its sleeve;
  nothing in this build, so its live holding reads zero and its shadow
  holding is the paper position. In the hypothetical book an entry's paper
  position is a leg of its own (`sleeve:<sleeve>:con:<id>`), placed in its
  sleeve beside the owner's real units of the same fund, and the fund is
  measured on the entry's own closes where the book keeps none; the core's
  real units are replaced, as today.
- Protection's automatic sales leave out the core's contract (D5). Whether a
  second entry's contract leaves them too is the owner's; not extended here.
- No rows, no proposal, no exception: `timed-holding-v1` sends nothing, so
  every entry runs as shadow at most and the shadow record is all it writes.

## 6. Surfaces

`strategy.target {id, refresh}` serves any entry's verdict in the
`core.target` shape (`rpc.StrategyTargetResult`; `CoreTargetResult` is its
alias) with `strategy`, `sleeve`, `signal` and `kill` added; `core.target`
is unchanged. `canary strategy ID [--refresh] [--json]` and
`canary_strategy` render it as `canary core` does, headed by the id;
`canary core` is byte for byte as before (goldens). `canary mandate`'s
register lines name each entry's fund and signal. Shadow records of an
entry carry its id, rule, cited hash and stage under `shadow/<id>`.

## 7. What the owner decides before the second entry runs

1. The trend fund and its line: iMGP DBi (London line DBMG) yes or no; the
   ISIN and the currency of the line. A line not in EUR cannot run yet (§5).
2. Its lab spec (registered, with its kill rule and no edge claim, as
   `s0_core_sleeve`), whose hash the entry cites and whose params it copies.
3. The entry in `risk-policy.toml` with a higher `policy_version`: the empty
   overlay as its sleeve, `stage = "shadow"`, the scale, the three numbers.
4. Whether its contract leaves protection's automatic sales, as the core's.

## 8. Build and verification

Built 2026-10-18 on branch `strategy-evaluator`, synthetic data only.
Tests: the fixture unchanged and pinned, `Measure` pinned to
`MeasureWith(TrendSMA10)`; the two rules and the parameterised resolve
(each unwritten key and an unknown signal hold; a complete entry resolves
with its instrument, filter, terms and series); the keys' shapes, old files
keeping both fingerprints; the core's daemon tests unchanged; a second entry
on a synthetic ISIN on a London line in shadow decides its month, writes
decision, intent and fill once beside the core's, keeps its paper leg in its
own sleeve beside the owner's real units, never reaches the kept verdict,
its key never in the verdict's evidence; an unwritten key, another
currency, an unknown signal and a missing listing hold; the kill bound holds
past the drawdown and cannot fire unwritten; CLI goldens (`core*`
unchanged, `strategy_shadow`, `strategy_hold`); the order-path scan and
`TestOnlyTheDaemonEvaluates` widened. Files: `internal/daemon/strategy_target.go`
(was `core_target.go`), `core_resolve.go`, `shadow_record.go`,
`internal/rpc/core.go`, `internal/cli/core.go`, `internal/mcp/tools.go`.
