# Core sleeve (the mandate's rule-based index share)

Written: 2026-10-14 16:50 CEST
Status: design for review. Nothing is built, enabled or armed by this
record. It precedes the independent safety review the owner asked for and
the code that follows it; buying stays off until the owner arms Fully
armed on a device.

Sources read, read-only: Canary main `761a1c31` (mandate increment 1, Desk
authority and execution groundwork), Canary `integration-2` `e4df5eeb`
(forced reduction and flatten to hedges, not yet on main), Desk `77c3966`
(`docs/automatic-trading.md`, `docs/mandate.md`, `automatic_controller.go`),
lab `1ef744c` (`specs/s0_core_sleeve.yaml`, `lab/strategies.py`,
`reports/calibration.md`) and the alpha review of 2026-10-14 in Desk's
private output. This record follows `.agents/docs/risk-policy-contract.md`:
it records owner decisions and proposes numbers the owner writes; it
creates no policy.

## Owner decisions this design implements

From the owner's chat of 2026-10-14; the mandate as recorded in
`mandate-risk-budget.md`, the rest as relayed in this design request:

- Mandate: "max loss 30% of equity minus a tax reserve, vol target 15%,
  sleeves owner 7 / core 6 / overlay 4 (% vol of the risk base), forced
  reduce-only to budget with a 30-minute veto; at the max loss flatten to
  hedges" (the last part: "Yes, flatten to hedges", ~16:10 CEST).
- Core instrument: "IBCF (iShares S&P 500 EUR Hedged UCITS ETF Acc, Xetra,
  ISIN IE00B3ZW0K18)". FX hedge rule: "keep USD exposure under 20% of NAV".
- ~16:10 CEST: "Start with the core sleeve": design first, independent
  safety review, then code; buying stays off until the owner arms Fully
  armed on their device.
- 06:26 CEST: "The system should run by itself within risk boundaries."

Earlier decisions that bind this design: Disarmed, Protection and Fully
armed with device-confirmed arming; AUTO alone never creates authority;
additions use a midpoint limit, IBKR Adaptive and DAY, with no plain-limit
or market fallback (Desk `automatic-trading.md`, 2026-10-09); each drawdown
brake engagement holds a full mandate at protection until it is confirmed
again on a device (2026-10-10 07:51 CEST). Target-allocation strategies
were deferred on 2026-10-09; the 16:10 instruction starts the first one,
for one instrument.

## Recommendation in brief

1. The core holds IBCF while IBCF's month-end close is above its 10-month
   average, sized so its own volatility is at most its 6% share of the
   budget base × room and its value at most 40% of the budget base ×
   room. It rebalances monthly on the Xetra calendar with a 10% no-trade
   band. Trend off sells to zero, and the cash returns to the sweep.
2. Canary computes the target as a daemon-owned verdict (`core.target`,
   parity-tested against the lab's Python) and serves each order as one
   proposal row (bucket `core_rebalance`). Desk decides nothing and sizes
   nothing.
3. Ship option A now: the owner confirms that row once on the device
   through the existing prepared-proposal path, one tap in a month that
   trades (about three a year). Ship option B next: the Desk controller
   names the same row under the Fully armed mandate. One row, two
   authorisations, as `desk-execution.md` already separates them.
4. The whole-book mandate gate stays binding for every core buy, and a buy
   is sized to leave the book at most at 0.95 of the budget, the forced
   reduction's own target, so a core buy can never set up a forced sale by
   itself.
5. Three owner decisions remain (section 7): whether the sleeve shares
   bind each sleeve, a plain-limit fallback if IBKR refuses Adaptive for
   IBCF, and what happens when USD securities alone exceed 20%.

## 1. The rule

### Instrument and series

- Instrument: IBCF, ISIN IE00B3ZW0K18, Xetra (IBKR exchange `IBIS`), EUR,
  security type STK, accumulating. The policy pins IBKR's contract id and
  the ISIN; every preview and a daily check compare IBKR's contract
  details (symbol, ISIN, currency, primary exchange, security type) with
  the pin.
- Signal series: IBCF's own daily Xetra closes from IBKR historical data,
  kept in the mandate's decision-grade history store (`mandate_history.go`).
  The store keeps held names, SPY and FX series today; it is extended to
  keep the core instrument whether held or not. The fund accumulates, so
  its close is a total-return series of the EUR-hedged S&P 500 with the
  hedge carry and fees inside: the rule judges what the sleeve earns.
- No live fallback series. IBCF has traded since 2010, more than every
  live input needs (the store keeps 320 closes; the rule needs 253). A
  missing, stale or suspect IBCF series holds the month. The rule never
  switches to the index, because a silent series switch is a data error
  of its own. The S&P 500 serves elsewhere: SPY, already in the store as
  the index proxy, is the live cross-check for IBCF price errors, and SPY
  or the S&P 500 total return index is the lab's proxy before 2010.

### Decision date

The last Xetra session of each month, from `marketcal` `de_xetra` (the
official calendar, covered to 2028-12-31; for example 30 December when the
31st is closed). The target is computed once that session's close is in
the store, from 18:00 Europe/Berlin, on closes up to and including it,
and is then frozen for the month. A running month is never decided.

### Trend

On when P_m > SMA10. P_m is the decision close; SMA10 is the mean of the
last ten month-end closes including P_m (the lab's `core_sleeve` with
`trend = "sma10m"`, strict inequality). Not live before ten month-ends.

The 200-day average is not used. It is evaluated daily, so it flips more
often and off the monthly cadence; s0 pre-registered the 10-month rule,
and choosing between the two now would be a search.

### Volatility and size

- σ_E: the EWMA of IBCF's daily simple returns, span 63 (α = 2/64), bias
  corrected, at least 63 observations, × √252. This is the lab's
  `_scale_to_vol` (pandas `ewm(span=63, adjust=True)`, `bias=False`).
- σ_252: the sample standard deviation of the last 252 daily simple
  returns × √252, the mandate's window, so the sleeve is never over its
  share by the measure that judges the book.
- σ = max(σ_E, σ_252, 15%).
- Sleeve budget S = core_vol_pct / 100 × budget_base × room, from the
  mandate verdict's capital at the decision (`budget_base`, `room`;
  `capital.known` required). Equivalently core_vol_pct / vol_target_pct ×
  `budget_annual_sd_base`. With the owner's numbers S = 0.06 × budget
  base × room: the request's "6% × risk-base high-water mark × room".
- Target notional N = S / σ when the trend is on, 0 when it is off.
- Target shares Q = floor(N × scale_pct / 100 / P_m): whole shares,
  rounded down. `scale_pct` is the commissioning ramp (25, 50, 100).

Caps. Each follows from the formula or an existing gate; none is a new
number:

- The 15% floor is s0's no-leverage gross cap restated. With one
  instrument and sleeve capital K = S / 0.15, s0's w = min(0.15 / σ_E, 1)
  gives N = w × K = S / max(σ_E, 15%). Hence N ≤ 0.4 × budget base × room
  with the owner's 6.
- Ex ante N × σ_E ≤ S and N × σ_252 ≤ S: the sleeve's own volatility is
  within its share on both estimators at every rebalance.
- Funding: a buy is funded as an Add of that exact quantity would be
  (`position-add.md`): settled EUR after the account reserve, commitments
  and a fee bound when the row is sized, the exact WhatIf at preview,
  never a loan or a conversion, and `no_buy_while_borrowed` applies. It
  waits where an Add waits (an unacknowledged buy, uncertain broker
  inventory, a pending non-stock order).
- Mandate headroom (section 3), and the target recomputed with the
  current verdict's capital: the frozen target is a ceiling for buys,
  never a floor.

### Rebalance and no-trade band

The order for a session is D = Q − H, where H is the IBCF shares the
broker's current position projection confirms. A row is served only when:

- the trend is off and the sleeve holds shares (sell all), or the sleeve
  holds none and Q is positive (first buy), or |D| > band_pct / 100 × Q
  (proposed band 10%);
- |D| × mid ≥ `min_order_base` (proposed 2,500 EUR, so IBKR's per-order
  minimum commission stays near 0.1% of the order; an assumption from
  IBKR's published Xetra rates, to calibrate from fills), except the
  sell-all, which always sells every share;
- no core order is working, unacknowledged or of unknown outcome.

A sale is H − Q, never more than H; the core never shorts. A buy is capped
as above and waits when what is left is below the minimum.

### Window and order terms

- Window: the first five Xetra sessions after the decision day. At most
  one core order per session, with the episode
  `core:<account>:<mode>:<YYYY-MM>:<session date>`. A remainder after a
  partial fill or an expired DAY order becomes the next session's D,
  computed again from confirmed holdings; the frozen target is never
  topped up. What is left when the window ends waits for next month.
- Due time: 15:45 Europe/Berlin (`send_after`), after the US cash open,
  when Xetra's S&P 500 ETF quotes are tightest. The row is visible from
  the open. Five sessions also give a bill redemption time to settle
  before a buy that needs its cash.
- Terms: LMT, DAY, regular session; the limit is the live mid snapped to
  the tick grid without crossing the mid against the owner (a buy rounds
  down, a sale up); IBKR Adaptive at the owner's priority where Canary
  admits it; SMART with primary exchange `IBIS`. No market order, no
  repricing, no chasing. A limit more than 10% from P_m holds the row as a
  probable data or contract error.
- Canary's Adaptive shape is US-only today: `reduceAdaptiveSupported`
  requires USD, and `validateAdaptiveOrder` in `pkg/ibkr` requires SMART.
  Whether IBKR accepts Adaptive for IBCF routed SMART is assumption A1, to
  verify by WhatIf in paper; owner decision D2 covers a refusal.

### Trend off

Sell to zero in the window. The proceeds are ordinary EUR cash under the
cash sweep's own band (bills while the sweep is on), which is s0's
"weight to T-bills". When the trend turns on again, the frozen buy amount
becomes the sweep's planned need through the typed seam
`cashSweepPlannedNeeds` (always 0 today), so the sweep stops investing
that cash and its redemption rule frees it; the buy waits for settled EUR
inside the window.

### Turnover and cost

Estimated with the lab's own `core_sleeve` code on SPY as the proxy,
January 1995 to December 2015, before s0's holdout. Only turnover was
computed: no return, Sharpe or drawdown. One-way turnover is in units of
sleeve capital K = S / 0.15:

| Sizing | Band | Trades a year | Turnover a year | Cost a year at 10 bp a side |
|---|---|---:|---:|---:|
| s0 (σ_E, 15% floor) | none | 9.6 | 1.19 K | 12 bp of K |
| s0 | 10% | 3.0 | 1.09 K | 11 bp of K |
| this rule (max of σ_E, σ_252, 15%) | none | 9.6 | 1.06 K | 11 bp of K |
| this rule | 10% | 2.9 | 0.96 K | 10 bp of K |
| this rule | 20% | 1.8 | 0.89 K | 9 bp of K |

The trend flipped about once a year (0.5 round trips) and was on at 76%
of month-ends; most turnover is those flips. At the proposed 10% band,
expect about three orders and about 10 bp of K a year, at most 4 bp of
the budget base, at the lab's placeholder UCITS costs (5 bp commission
and 5 bp half spread). Not included: tax on gains that trend-off sales
realise, and the fund's own fees and hedging carry, which are inside its
price. This run is prior exploration of the `equity_timing` family and
should be logged with `lab prior`; this design session could not write
the lab's registry.

### Relation to s0 and pre-registration

The rule is s0 (`s0_core_sleeve`, hash `7c4dd5b5…`, R0, never run) with
five changes:

1. One instrument, IBCF, instead of SPY, QQQ and EFA: the owner's choice;
   s0 left the real instrument mapping to the owner.
2. The volatility target is the mandate's share S instead of a
   free-standing 15%; s0's 15% target and gross cap 1.0 become the 15%
   floor.
3. σ_252 joins σ_E, so the sleeve is within its share on the mandate's
   own window too. Cost: slower re-risking for up to a year after a
   volatility spike.
4. A 10% no-trade band and a minimum order.
5. Orders go out in the next session at a midpoint limit, not at the next
   close.

Before any lab run, register these as a new version of s0 with variants
v1 (this rule), v2 (s0 sizing: σ_E and the floor only) and v3 (no trend
filter). The lab harness needs EUR-priced instruments (it refuses
non-USD datasets today) and the no-trade band. The compiled rule
`core-ibcf-v1` carries the spec hash it implements, the policy names the
rule, the verdict prints both, and a mismatch holds.

Parity: one synthetic fixture (about 800 sessions of generated closes on
a Xetra-shaped calendar, with a crash, a volatility spike, a holiday
month-end and a missing bar) and its expected values per decision (trend,
SMA10, σ_E, σ_252, σ and Q at a fixed synthetic S and price), produced by
the lab's builder and committed to both repositories. Canary's Go test
matches the trend and Q exactly and every float to 1e-9 relative; the
lab's test proves its builder still produces the fixture. A change on
either side fails a test; the fixture changes only with a new rule
version. No market data or account figure enters either repository.

Not reused: Canary's `target-band-plan-v1` (`portfolio-plan.md`). Its
targets are static owner numbers, it never sells, and it grants advisory
selection only.

## 2. Where it lives

| Concern | Owner | How |
|---|---|---|
| Rule, monthly target, evidence | Canary verdict `core.target` | Pure evaluator `internal/core`; a frozen monthly state document with its input digest; `canary core` and `canary_core`, read-only; listed in `TestOnlyTheDaemonEvaluates` |
| The exact order | Canary proposal engine, bucket `core_rebalance` | Sizes D; applies the band, minimum, funding and headroom; nets and merges with other rows; Xetra readiness |
| The decision to send | A: the owner on the device. B: the Desk controller under the Fully armed mandate | Desk never computes a size or a price |
| Execution | Canary's existing order path | Every gate, the order journal, the wire guard |
| Durable progress (B only) | Torok | The existing controller records |
| Evidence for the rule | Lab R2 shadow and s0's kill rule; scorecard sleeve `core` | The core's fills score as `core`, never as the owner's; reported, not IC-gated |

### The two options

| | A: monthly row, the owner confirms | B: Fully armed, automatic |
|---|---|---|
| Authority | One device confirmation of the exact previewed order | The standing mandate, device-confirmed once, and Canary's wire fence |
| Exists today | The prepared-proposal path (`trade.proposals.prepare`, device confirmation, `submit` with `prepared_ref`), every gate, Desk's proposal confirmation | Authority methods (Go client only), the execution journal and lookup, the Desk controller in tests |
| Missing | The verdict, the row and one typed buy exception | All of A and the phase 3 list below |
| Owner effort | One tap in a month that trades, about three a year | None; pause, downgrade or an Auto lock stops it |
| Blast radius of a defect | One reviewed order | Every rebalance until a control stops it |
| Ships | After phases 1 and 2 (S and M items) | After the authority path is served (an L item) |

Recommendation: A now, B next, on the same row. Under A the new buying
code meets live money with the owner as the last line, as the cash
sweep's first buys did (`cash-sweep.md`, P1); B then changes only who
presses.

Rejected:

- A pre-authorised bucket with the 30-minute veto, sent by the daemon's
  scheduler (`proposal_automatic.go`). It would arm risk-adding buys with
  a policy-file line instead of the device-confirmed Fully armed mandate
  the owner named. `core_rebalance` stays out of the pre-authorisable
  vocabulary (`validPreAuthorisedBucket`), with a test.
- Desk's manual Add with a quantity Desk computes. Desk would size an
  order, the fill would score as the owner's, and B could not reuse it.

### The one typed exception

Core buys need a third typed exception to `authority.close_reduce_only`,
after the sweep's (`cashSweepOpenException`) and currency leveling's:
only the pinned contract id, only BUY, only up to Q − H of the frozen
target, checked at preview, prepare and submit. Every other buy under
`close_reduce_only` is still refused. Core sales are ordinary reductions.

### Shipping sequence

Phase 0, no code: the owner's answers to D1–D3; the independent safety
review of this record; in the lab, the spec version, EUR instruments and
the band in the harness, and the parity fixture (S to M).

Phase 1, read-only, no change to the write path:

| # | Item | Size |
|---|---|---|
| 1 | `[mandate.core]` schema: inactive when absent; a missing key holds and names it; fingerprinted | S |
| 2 | The history store keeps the core instrument's series, held or not; Xetra month-ends | S |
| 3 | `internal/core` evaluator and the parity fixture test | M |
| 4 | `core.target` verdict, frozen monthly state, RPC, CLI, MCP, golden render | M |
| 5 | Scorecard resolver: the core instrument's fills and `proposal:core_rebalance` go to sleeve `core` | S |
| 6 | Desk: the core's line on Risk › Mandate (target, holding, trend, next decision) | S |

Then witness W0 (section 6).

Phase 2, option A:

| # | Item | Size |
|---|---|---|
| 7 | `core_rebalance` rows: window, band, minimum, funding, headroom, netting and merge, readiness | M |
| 8 | The typed buy exception at preview, prepare and submit | M |
| 9 | Core buys require a known incremental mandate evaluation (section 3) | S |
| 10 | The sweep's planned need from the core's pending buy | S |
| 11 | Desk: the row's notice and its review through the existing prepared-proposal confirmation; the matcher accepts the row | S |

Then witnesses W1 (paper) and W2 (live ramp).

Phase 3, option B: what the implementation sequence in Desk's
`automatic-trading.md` still lacks against Canary main, numbered by its
steps, plus the core's own items:

| # | Item (step) | State today | Size |
|---|---|---|---|
| 12 | Serve `desk.execution.submit` with the automatic authorisation and construct the wire fence on that path (1, 2) | Capabilities list only `capabilities` and `lookup`; no production dispatcher sets `binding.deskAuthority` | L |
| 13 | One admission for funds and commitments across manual, automatic and pre-authorised work, enforcing AUTO's priority there; overlap with the protection scheduler and queued reductions (2) | Not built | M |
| 14 | Pin lookup's ID-only `absent` receipt in handler tests (the contract edge Desk recorded on 2026-10-10) | Open | S |
| 15 | Contract: a proposal item may carry class `add` only for `core_rebalance`; fence action `entry` (full scope) for its buys, `reduction` (Protection) for its sales | `Validate` refuses proposal additions | S |
| 16 | Desk snapshot and executor adapters over the Go client with the private capability (3) | Interfaces and tests only | M |
| 17 | Torok lifecycle: the controller in the operating coordinator, reconciliation at startup and reconnect, controls through the daemon generation, the 15:45 due time (4) | Not registered | M |
| 18 | Device-confirmed arming in Desk (prepare, confirm) (4) | Arming refused | M |
| 19 | Adaptive for IBCF proven and admitted, or D2 | US-only | S |
| 20 | Contract and recovery witnesses with a simulated daemon (5) | Not built | M |

Then witness W3.

## 3. The mandate

### What the gate does today

`mandateBudgetGate` (`mandate_verdict.go`) runs at order preview, strategy
preview, admission and the final wire guard. A risk-adding order (anything
but a close or reduce, or a buy of a put on a hedge symbol) is refused
unless the kept verdict for the policy in force is at most five minutes
old and reads `within`, and the book with the order added stays within the
whole budget. The gate has no sleeves: a core buy competes with the
owner's book for one budget.

One gap matters here. When the order's leg cannot be evaluated, the
incremental check returns no blocker and the standing verdict alone
decides. A name without a decision-grade series makes the incremental
evaluation unknown, and the store keeps series only for held names, so
the core's first buy would pass the incremental check unchecked. The core
therefore keeps IBCF's series always (item 2) and requires a known
incremental evaluation before any buy (item 9). The same gap for other
first-time stock additions is outside this design and is reported
separately.

### The core's own share

By the formula, the sleeve's own volatility is at most S on σ_E and on
σ_252. The verdict reports the core's standalone risk (the mandate's
evaluator on the core leg alone) and its Euler contribution, which cannot
exceed the standalone figure. A holding over its share at a decision is
sold down by the rule; between decisions it is not.

### Headroom: the core never pushes the book over budget

A buy is the largest whole quantity, up to the funded D, for which the
book with the buy measures at most 0.95 × budget, found by bisection on
the daemon's evaluator. 0.95 is the forced reduction's own
`mandateReductionTargetFactor`, not a new number. The forced reduction
starts above 1.0 × budget on two scheduled readings and sizes back to
0.95, so a core buy can never start a forced sale by itself; only the
market can.

### When the owner's sleeve fills the budget

Today the whole-book gate decides. While the verdict reads over, reduce
or unknown, no core buy is admitted; while it reads within, the core gets
only the room between the book and 0.95 × budget. In a book whose owner
sleeve fills the budget, the core holds nothing. The core's sales
(rebalance down, trend off) always pass.

Recommendation: keep the whole-book gate binding for every core buy; it
is the mandate's hard line and must not move. Whether the shares bind
each sleeve, so that the owner's sleeve makes room for the core, is owner
decision D1.

### With the forced reduction (integration-2, not yet on main)

The reduction ranks lines by Euler contribution and treats IBCF as any
other line. To coexist:

- One row per contract and side: a reduction sale of IBCF and a core sale
  merge into one row sized by the larger need (the existing same-contract
  merge; the mandate row takes precedence).
- No core buy while a breach episode is open, while any reduction row
  stands, or while a sale of IBCF works.
- No buy-back in the month of a forced cut: the core buys again only in
  the next window, after `within`, under the 0.95 headroom.
- At the maximum loss the room is 0, so S and the target are 0, and the
  reduction flattens to hedges. IBCF is not a hedge and is sold, which is
  also what the rule wants.

The ranking stays "largest risk contributor first" (06:26 CEST) unless D1
changes it.

### Drawdown brake and controls

The brake refuses risk-adding orders, so core buys stop and core sales
continue. Under B, each engagement holds a full mandate at protection
until the owner confirms full again on a device; core sales are
reductions and keep running under Protection. Freeze, MANUAL, Disarm and
an Auto lock on IBCF stop new core buys at the final gate and cancel
nothing.

## 4. The FX rule

### Measure

USD share u = IBKR's net liquidation in USD (the ledger's per-currency
`NetLiquidation`: USD cash plus USD securities and options at market
value) at the ledger rate, divided by the account's net liquidation. The
owner said NAV, so the base is net liquidation, not the risk base. IBCF
is EUR and hedged at fund level, so it adds nothing; its unhedged
intra-month gains are a negligible residual.

Two facts shape enforcement:

- A trade inside USD (a USD stock bought with USD cash or a USD loan)
  leaves u unchanged. u moves with USD gains and losses, with flows, and
  with conversions.
- Currency leveling's repayment of a USD loan converts EUR into USD and
  raises u; a USD loan lowers it.

### Rule

| State | Action | Who sends |
|---|---|---|
| u ≤ 20% | Nothing | — |
| u > 20% on two consecutive daily readings, with USD cash above its keep-cash and commitments | One conversion row, BUY EUR.USD, sized to the lesser of the free USD cash and (u − 15%) × net liquidation; new USD bill buys hold while u is over the cap | The owner, one tap on the leveling card |
| u > 20% from USD securities alone | Reported on Risk and in the brief; nothing automatic | D3 |

The conversion uses leveling's order path: IDEALPRO, LMT DAY within
`max_slippage_bp` of a live two-sided mid, a typed exception for
converting a positive balance down to the target, held while any CASH
order works (which holds the sweep, as today). It never takes USD cash
below its keep-cash, so it never creates a loan for leveling to repay, and
the two cannot oscillate. Leveling itself is unchanged. The 15% target and
the two-reading debounce are proposals the owner writes. Cadence: one
reading a day after the US close (22:30 Europe/Berlin), at most one
conversion row a day, for the next IDEALPRO session.

Cost: an IDEALPRO commission of about 0.2 bp (USD 2 minimum) plus at most
the slippage bound of 2 bp, a few times a year, with no carry. The
measurement is automatic; the conversion is confirmed, following
leveling's "not pre-authorisable" decision, and B never sends one (the
execution contract has no class for it). Policy home:
`[cash.currency_cap]` under the `[cash]` authority. Rulebook rule 14
stays watch-only.

## 5. Safety invariants and failure modes

Invariants, each with a synthetic test in section 6:

1. Only Canary computes the target, the quantity and the limit. Desk and
   Torok never supply a size or a price.
2. One frozen target per month, persisted with the digest of its inputs.
   A recomputation that differs holds the month (`core_target_drift`).
3. At most one core order per Xetra session, and none while a core order
   is working, unacknowledged or of unknown outcome.
4. Every quantity is the target minus the confirmed holding; a partial
   fill never makes a new target or tops up past it.
5. A core buy passes every existing gate, including the mandate gate with
   a known incremental evaluation, fits under 0.95 × budget and is funded
   from settled EUR without a loan or a conversion; otherwise it waits.
6. The core never sells more than it holds and never shorts.
7. `core_rebalance` is never pre-authorisable. It reaches the broker only
   through the owner's device confirmation (A) or the Fully armed
   mandate's wire fence (B).
8. Missing, stale or suspect data holds. Nothing is guessed and no series
   is substituted.
9. Restrictive controls stop new core buys at the final gate and cancel
   nothing.

| Failure | Guard |
|---|---|
| Duplicate order | Frozen target; one episode per session (a one-use prepared reference under A; the journal's request and episode keys under B); netting against working, unacknowledged and unknown core orders; quantities from confirmed holdings |
| Partial fill | The DAY order expires; the next session's D comes from confirmed holdings, only once the position projection is newer than the last fill; the window's end drops the rest |
| Restart | Target and digest persisted; the order journal resolves unknown sends before any new core row; under B the controller reconciles before it runs |
| Stale data | A decision needs the decision day's bar; a buy needs a verdict at most five minutes old, equity within `capital.max_equity_age_minutes`, current positions and a live two-sided quote; the frozen target expires with its window |
| Whipsaw | Monthly evaluation only, no flip within a month, the band; about one flip a year at about 10 bp of K. A buffer around the average would be a new lab variant, never a live tweak |
| Market closed, holidays | The Xetra calendar sets the decision day, the window and readiness; half days follow it; past its coverage (2028-12-31) the core holds |
| Bad ETF price | A daily move beyond ±8% that SPY's close on the same date does not show at half the size, a missing session bar, σ outside 5–100%, or a limit more than 10% from P_m holds the month for review |
| Wrong contract | Contract id and ISIN pinned; IBKR's details must match symbol, ISIN, EUR, `IBIS` and STK; a second held line with the same ISIN under another contract id holds |
| Currency mismatch | The order currency is EUR, the pin's and the account base's; funding only from settled EUR; a core order never creates a conversion or a debit; a non-EUR base holds |
| Mandate unknown at the decision | The target needs only the verdict's capital; a buy still needs a fresh `within` when it is sent; sales go |
| The owner trades IBCF by hand | The instrument is reserved to the core: its fills count as the core's, and the next decision corrects the holding |
| Cash sits in bills | The planned need starts at the decision; the buy waits for settled EUR inside the five-session window |

## 6. Acceptance tests and live witness

Synthetic tests that must pass before A is offered (1–14) and before
Fully armed may include the core (15–20):

1. Parity fixture, Go against the lab: trend and Q exact, floats to
   1e-9; cases where each of σ_E, σ_252 and the floor binds, a holiday
   month-end, and no target before ten month-ends.
2. A decision waits for the decision day's bar; a running month is never
   decided; a missing bar holds.
3. The frozen target survives a restart; a changed input holds with
   `core_target_drift`.
4. Unknown capital gives no target; room 0 gives Q = 0; `scale_pct` 25,
   50 and 100 scale Q.
5. Band and minimum: inside the band, no row; the sell-all and the first
   buy pass the band; a buy below the minimum waits; the sell-all ignores
   the minimum.
6. Buy sizing is the least of D, funding, the 0.95 headroom and the
   current-capital target. An unknown incremental evaluation, or a verdict
   that reads over, reduce or unknown or is stale, gives no buy; sales
   still go.
7. One order per session; a working, unacknowledged or unknown core order
   holds the next; after a partial fill the next session buys only the
   remainder; never past Q; the window's end drops the rest.
8. Merge with a mandate reduction of IBCF (one row, the larger need); no
   buy during a breach episode, beside a reduction row or a working IBCF
   sale; no buy-back in the month of a cut.
9. Contract guards: a mismatched contract id, ISIN, currency, exchange or
   type, a second line with the ISIN, and a non-EUR base each hold.
10. Data guards: the price jump, the missing bar, the σ bounds and the
    price distance each hold.
11. Calendar: a month-end on a holiday, a closed session, a date past the
    calendar's coverage.
12. Funding: short settled EUR waits; the planned need reaches the sweep;
    `no_buy_while_borrowed` holds the buy; no conversion is ever produced.
13. The typed exception admits only the pinned buy up to Q − H at
    preview, prepare and submit; every other buy under
    `close_reduce_only` is still refused; `core_rebalance` is refused in
    both pre-authorised lists, and the scheduler never creates a record
    for it.
14. Scorecard: core fills land in sleeve `core`, in no owner stream, and
    are not gated.
15. B: a core buy needs fence action `entry` under full scope; a core sale
    runs under Protection; a brake hold refuses the buy at the wire.
16. B: pause, downgrade, disarm and an Auto lock racing a dispatch are
    refused at the final gate; an order already sent survives.
17. B: episode consumption across a restart; a lost reply resolves by
    lookup and never sends a second order.
18. B: unsupported Adaptive gives a structured refusal and no fallback,
    unless D2 allows the plain-limit shape, which then has its own test.
19. B: a core buy and an entry-rule addition competing for the same cash
    are admitted once each in the controller's order, after protection and
    reductions.
20. Property: on synthetic books, a core buy sized under the 0.95
    headroom never by itself yields a verdict over the budget.

FX: u from ledger rows; the two-reading debounce; the conversion sized to
the target and to free USD cash; a working CASH order holds; USD bill buys
hold while over the cap; no row when USD securities alone cause the
excess.

Live witness, each step only after the one before:

- **W0, shadow.** At least one month-end, two preferred, with the verdict
  served read-only in the live account and compared with the lab's
  recomputation on independent closes (Yahoo's IBCF.DE): the same trend,
  σ within 0.5 points, Q within 1%. The lab's R2 shadow log starts here.
- **W1, paper.** One buy and one sale through A in the paper account:
  contract resolution by ISIN, Xetra readiness and the 15:45 due time,
  IBKR's answer on Adaptive (A1), tick snapping, settlement (A3), the
  sweep's planned need, the scorecard sleeve and the receipts.
- **W2, live under A.** `scale_pct` 25, then 50, then 100, one monthly
  window each (the lab's R3 and R4 fractions). In each window: fills within
  10 bp of the mid at sending, the holding equal to Q within one share, no
  duplicate, the standalone risk within the share.
- **W3, B.** After tests 15–20 and the authority path's own witnesses, the
  owner arms Fully armed on the device, and the first automatic window is
  watched end to end.

Suspension, by the owner setting `scale_pct` to 0: s0's kill rule (the
sleeve's drawdown beyond 1.5 times its backtest maximum at the same
volatility, or a rolling 24-month net Sharpe below 0 while the static
mix's is above 0.5), or realised costs above twice the cost model.

Assumptions to verify before the step that needs them: A1, IBKR accepts
IBCF with Adaptive routed SMART (W1); A2, IBKR's daily IBCF bars equal the
Xetra closing auction price (W0, against the exchange's close); A3, IBCF
settles T+2 and a Bubill redemption settles inside the window (W1).

## 7. Owner decisions

- **D1. Do the sleeve shares bind each sleeve, or only the whole book?**
  Today only the whole book: the core uses the room the owner's sleeve
  leaves, and the forced reduction cuts the largest contributor whatever
  its sleeve. Binding each sleeve means an owner addition is refused
  while the owner's own risk would exceed 7% of the budget base × room
  (the core is already held to 6% by its rule), and the forced reduction
  cuts the sleeve furthest over its share first, the largest contributor
  within it; that amends the 06:26 "largest risk contributor first".
  Recommendation: bind each sleeve, built before B; A ships under today's
  semantics. Without it the core holds nothing whenever the owner's
  sleeve fills the budget.
- **D2. If IBKR refuses Adaptive for IBCF, may core orders use a plain
  DAY limit at the mid?** Tick-snapped, never crossing the mid against
  the owner, never repriced. Recommendation: yes, for the core instrument
  only; the 2026-10-09 "no fallback" rule stays for every other automatic
  order. If IBKR accepts Adaptive, there is nothing to decide: the
  predicate widens, with a test.
- **D3. When USD securities alone exceed 20% of net liquidation, what
  then?** (a) Report only. (b) Hedge with a USD loan, which leveling must
  then leave in place and `no_buy_while_borrowed` must exempt, or the core
  and the sweep stop buying; it costs the USD debit rate minus the EUR
  credit rate, about 1.5 to 2 points a year above a forward hedge at
  IBKR's published spreads (to verify against the statement rates
  leveling reads). (c) Hedge with micro EUR/USD futures, which Canary's
  mandate and order paths cannot handle yet (L). Recommendation: (a) now,
  with the cash conversion; (c) as the hedge once futures are supported;
  not (b).

## Proposed policy

The owner writes these; the code carries no default that acts. A missing
key holds the core and names the key.

```toml
[mandate.core]                  # absent: the core is inactive
rule            = "core-ibcf-v1" # compiled; carries the lab spec hash
# con_id        = IBKR contract id, written after resolving the ISIN
isin            = "IE00B3ZW0K18"
exchange        = "IBIS"         # Xetra
currency        = "EUR"
scale_pct       = 25             # ramp: 25, then 50, then 100
band_pct        = 10
min_order_base  = 2500
window_sessions = 5
send_after      = "15:45"        # Europe/Berlin

[cash.currency_cap.USD]
max_share_pct    = 20
target_share_pct = 15
```

The sleeve's share is the existing `[mandate.sleeves] core_vol_pct`. The
rule's own parameters (ten months, span 63, 252 days, the 15% floor) are
compiled with the rule version and are not policy keys; changing one is a
new lab spec version and a new rule.

## Planned files

```
internal/core/                     pure evaluator; parity fixture test
internal/core/testdata/            synthetic fixture shared with the lab
internal/rpc/core.go               core.target method and result types
internal/daemon/core_target.go     verdict, frozen monthly state, cadence
internal/daemon/core_rebalance.go  rows, sizing, the typed buy exception
internal/daemon/mandate_history.go keeps the core instrument's series
internal/risk/mandate.go           [mandate.core] schema
internal/cli/core.go               canary core [--json]
internal/mcp/tools.go              canary_core, read-only
```
