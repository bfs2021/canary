# Strategy evaluator (one evaluator for every register entry)

Written: 2026-10-18 18:10 CEST. Status: built on Canary branch
`strategy-evaluator`, 2026-10-18 (section 8). This record follows
`.agents/docs/risk-policy-contract.md`: it proposes the keys the owner may
write and creates no policy. It carries out plan item 4 of the review of
2026-10-18 (Desk `output/secret-sauce-20261018/review.md`, finding 2: "a
strategy is a project, not a register entry") against `strategy-register.md`
and `core-sleeve.md`. Revised 2026-10-19 09:30 CEST: every live entry serves
its rows (build 2; §5), the kill rule is measured on a live entry's own fills
(§3), and §7 records the owner's decisions on the second entry as taken
(owner instruction 2026-10-19 07:07 CEST, "task build 2 and then 6 as well").

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
  a proposal or places an order: every entry rule sent nothing, so every
  entry ran in shadow at most in this first build. That was the build's reach, not a step the owner wants: on 2026-10-18 21:05 CEST the owner decided "we NEVER want shadow, it's useless", so an entry goes live on the owner's word. Since 2026-10-19 (build 2) every live entry serves its month's row through the one order path (`strategy-register.md`, "Rows for every live entry").
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
recorded as a hold naming it, and the owner suspends.

Live (built 2026-10-19, `strategy_kill_live.go`): a live entry keeps no
shadow record, so the drawdown is measured on its own fills. At each frozen
decision the background refresh keeps one mark (scope `strategy/<id>`,
never read by an order path): the decision close, the units and the cash
its own fills (the executions of its rows' orders) left, and S at that
decision; the sleeve's equity is cash plus units times the close, its peak
starts at zero as the paper position's does, and the drawdown in K is
measured from the running peak, this reading included. A hand trade of the
line is not an own fill, so it moves the measure nothing. The spec's other
tests are the scorecard's and the cost report's, not this. The core keeps
its measure on its shadow record (it writes no bound).

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
  An entry's holding is what the placement puts in its sleeve: the units
  its own fills left, never more than the broker holds (by lineage,
  `mandate-risk-budget.md`, "Placement", as built 2026-10-19); while the
  owner holds units of its line it holds, naming them; in shadow the paper
  position. In the hypothetical book an entry's paper
  position is a leg of its own (`sleeve:<sleeve>:con:<id>`), placed in its
  sleeve beside the owner's real units of the same fund, and the fund is
  measured on the entry's own closes where the book keeps none; the core's
  real units are replaced, as today.
- Protection's automatic sales leave out the core's contract (D5) and, since
  2026-10-19, every line the verdict places whole in another entry's sleeve
  (the entry holds it alone; carried out under the owner's instruction of
  07:07 CEST, the brief naming the exclusion "as the core's");
  `mandateReservedIDs`. A line the owner holds units of keeps its automatic
  protection. The forced reduction still covers them all.
- Rows (built 2026-10-19): `timed-holding-v1` sends, so a live entry serves
  its month's `strategy_rebalance` row with the core's terms and gates, its
  units placed by lineage (`strategy-register.md`, "Rows for every live
  entry"); shadow is the technical state of an entry not yet written live,
  never a required step (owner, 2026-10-18 21:05 CEST).

## 6. Surfaces

`strategy.target {id, refresh}` serves any entry's verdict in the
`core.target` shape (`rpc.StrategyTargetResult`; `CoreTargetResult` is its
alias) with `strategy`, `sleeve`, `signal` and `kill` added; `core.target`
is unchanged. `canary strategy ID [--refresh] [--json]` and
`canary_strategy` render it as `canary core` does, headed by the id;
`canary core` is byte for byte as before (goldens). `canary mandate`'s
register lines name each entry's fund and signal. Shadow records of an
entry carry its id, rule, cited hash and stage under `shadow/<id>`.

## 7. The second entry: the decisions as taken (2026-10-19)

The owner's instruction of 2026-10-19 07:07 CEST, "task build 2 and then 6
as well", with every authorisation given, delegated these to the
integrating session; each is recorded with its reason.

1. **The fund and its line.** iMGP DBi Managed Futures, in its class hedged
   to EUR (R EUR HP, ISIN LU3359622902), on Xetra (IBKR primary IBIS2,
   symbol MFEH), in EUR. The rule chose it: a EUR line on a venue Canary
   serves comes before the London line DBMG (the USD class quoted in GBP),
   which would have needed FX sizing and funding; the Paris EUR class has
   no calendar Canary embeds. §5's currency hold therefore still stands for
   any other currency.
2. **The signal series** (integrating session on the owner's delegated
   judgement, 2026-10-19 07:31 CEST, verbatim in substance: "The trend
   signal is the fund's, not the share class's, so read the ten-month
   month-end series from the fund's oldest share class with enough history
   ... A seven-month data hold on a live entry is a shadow by another name,
   and the owner ruled shadow out"). The EUR class listed on 18 May 2026, so
   its own closes fill the 253-session window only about mid-May 2027. The
   entry names a signal series separately from the traded instrument
   (`signal_isin`, `signal_currency`, `signal_in`): the fund's R USD class
   (LU2951555585) on its London line (DBMG, GBP, closes since April 2025),
   measured in USD, converted at each session's GBP.USD IDEALPRO close,
   since the class's own currency is the dollar and a GBP quote of it would
   mix sterling into the fund's trend. Until the EUR class's own closes
   fill the window the month is measured on that series, on London's
   calendar; the target is sized, the row's limit checked and the kill
   rule marked on MFEH's own close; the mandate measures MFEH on the
   series' returns, as it measures the core's fund on SPY's. From the first
   decision whose window MFEH's closes fill, they take over. A signal line
   that cannot be resolved, a series or a rate missing or stale, holds the
   entry, named; nothing falls back. "Same fund" is the owner's citation in
   the policy and the lab spec, checked for shape (another ISIN than the
   traded line's), not by Canary.
3. **The lab spec**: `s6_trend_fund`, registered (no edge claim; the same
   ten-month filter on month-end closes; the kill rule a drawdown bound of
   0.35 K, set a priori at about twice the replicated CTA index's recent
   worst drawdown, wide on the owner's "aggressive side"). Its hash is the
   entry's `spec_hash`.
4. **The entry**: `[mandate.strategies.s6-trend]`, live, its own sleeve
   `trend` with `risk_allocation_pct` at the budget's room left beside the
   core's and the overlay's allocations (the file refuses more), scale 25
   (the ladder's first live step, as the core's), band 10%, minimum 2,500,
   cap 40%, `kill_drawdown_k = 0.35`; risk policy v16, written after the
   daemon that reads it was installed.
5. **Protection's automatic sales** leave its line out while the entry
   holds it alone, as the core's (D5, extended); a hand buy of its line is
   refused while it is live.

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
