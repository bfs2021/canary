# Core sleeve (the mandate's rule-based index share)

Written: 2026-10-14 16:50 CEST. Revised 2026-10-14 18:30 CEST: the owner's
answers to D1 to D4, the independent design review folded in (section
"Design review"), and phase 1 built.
Status: phase 1 built (read-only: the target is served, nothing is
proposed or sent) with the two mandate-gate fixes the review requires;
phases 2 and 3 are design. Buying stays off until the owner arms Fully
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

Answers of 2026-10-14 (relayed with the build request, verbatim where
quoted):

- **D1** "No, whole book only": the sleeve shares do not bind each sleeve;
  the whole-book mandate gate decides. The core gets the room the owner's
  sleeve leaves, and the forced reduction keeps "largest risk contributor
  first".
- **D2** "Yes, for the core only": if IBKR refuses Adaptive for IBCF, core
  orders may use a plain DAY limit at the mid, never crossing it. The
  2026-10-09 "no fallback" rule stays for every other automatic order.
- **D3** "Report now, futures hedge later": excess USD cash is converted
  (the cash rule in section 4); USD securities above 20% of NAV are
  reported; micro EUR/USD futures are the hedge later; never a USD loan.
- **D4** "12% for broad index funds": broad index funds take a 12% gap
  shock; single names keep 25%.
- Earlier the same day: at the maximum loss the book flattens to hedges;
  the core instrument is IBCF (Xetra, ISIN IE00B3ZW0K18).

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
4. The whole-book mandate gate stays binding for every core buy (D1), and
   a buy is sized to leave the book at most at 0.95 of the budget and the
   core's gap loss at most at 0.95 of its cap, the forced reduction's own
   target, so a core buy can never set up a forced sale by itself.
5. This is a beta allocation the owner chose, with a drawdown filter. It
   claims no alpha: the lab supplies the parity fixture and the kill rule,
   nothing else, and no Sharpe, IC or e-value is a test of it.
6. The owner answered D1 to D4 on 2026-10-14 (above). Phase 1 is built:
   the target is served read-only (`canary core`), with the two mandate
   fixes the review found necessary (the index gap and the gap-cap check
   on additions).

## Design review (2026-10-14) and how it is folded in

The independent review found one blocker and eight serious items. Each is
answered here; "built" means phase 1 carries it, with a test.

| # | Finding | Resolution | Where |
|---|---|---|---|
| B1 | At its target (40% of the budget base) the core's one-name 25% gap loses 10% of the base, at the 9.99% gap cap: the core breaches it, the forced reduction sells, the next month buys back: ping-pong | D4: broad index funds take a 12% gap (4.8% of the base, under half the cap). The incremental additions gate refuses an addition that creates or deepens a gap-cap breach (it checked volatility only). The core's sizing lands inside every gap cap with margin: its gap at most 0.95 of the cap. The core is not ready until `index_gap_shock_pct` is written: at 25% its target would sit at the cap's margin and a rise of about 5% would breach it | Built: `index_gap_shock_pct`, `mandate.BroadIndexInstruments`, `mandateGapBreachAdded`, `coreHeadroomFits`, `CoreUnapprovedKeys` |
| S1 | Honesty | This is a beta allocation the owner chose with a drawdown filter, no alpha claim. The lab supplies parity and the kill rule only; s0's Sharpe hypothesis is dropped as a success test; the kill rule's reference is concrete (the new spec); Desk shows no IC, e-value or "established" for the core | Lab spec `2408d09e6189`; Desk in phase 2 |
| S2 | Lab accounting | The design's 1995-2015 turnover run (five configurations) is logged with `lab prior`; the 10% band was picked on that turnover; "S&P 500 only" is recorded as hindsight-prone; a new s0 version matching the rule is registered | Built in the lab: registry seq 29-31 |
| S3 | Costs beyond commission and spread | Whipsaw, tax on realised gains, Vorabpauschale and hedge carry, each with a number or "unknown" | "Turnover and cost" below |
| S4 | The typed buy exception | Bound like `cashSweepOpenException`: to the `core_rebalance` row, its sized quantity and a cost at most the funded EUR; netted against every working IBCF order, the owner's included; 0.95 and the gap headroom re-checked at submit; held on `book_changed` | Phase 2 item 8 (design only) |
| S5 | IBCF closes at 17:30 CET, the US at 22:00: on its own closes the mandate understates its correlation with US names | The mandate moves IBCF with SPY's local-currency returns, no FX leg (EUR-hedged class); the ±8% price cross-check uses SPY's move over the two US sessions spanning IBCF's day | Built: `mandate.ReturnsProxies`, `core.Guard` |
| S6 | Sales | A trend-off remainder does not wait a month: it continues in the next sessions until sold. A data hold stops buys and sends any sale the rule wants to the owner | Phase 2 item 7; phase 1 serves the hold |
| S7 | Series | Requested on IBIS, not SMART, keyed by contract id; an unheld core series never makes the book's verdict stale; freshness on Xetra's calendar; a fixed input window | Built: key `core:con:<id>`, `mandateSeriesMarket`, `core.WindowCloses` |
| S8 | Calendar | The order window skips sessions the US is closed (wide spreads on Xetra's S&P 500 funds); the send time is clamped before an early close | Phase 2 item 7 |

Notes the review raised, with their rules:

- **Adopting an existing IBCF holding.** Every share of the pinned
  contract counts as the core's holding, whoever bought it: the instrument
  is reserved to the core. An existing holding is adopted at the first
  decision; above the target beyond the band, the rule sells the excess
  (under A, the owner confirms that row).
- **The owner's hand IBCF orders.** They are the owner's call and change
  the holding the next decision corrects. While any IBCF order works, the
  owner's included, no core row is served, and every core row is netted
  against working IBCF orders (phase 2).
- **Option B's armed terms** name the rule, the spec hash, the scale and
  core_vol_pct, so a policy edit cannot raise automatic buying under a
  standing arming. Phase 1 already freezes each month's target as a
  ceiling: an edit within a month can only lower it.
- **Protection's scope would sell the core.** Protection proposals cover
  every held line, IBCF included; a protection sale lowers the holding and
  the next decision may buy back under the headroom. Whether the pinned
  contract leaves protection's automatic scope (the forced reduction still
  covering it) is for the owner before phase 2 (proposed D5).
- **An Auto lock on IBCF stops core sales too**, not only buys: it locks
  the instrument. A trend-off sale waits while it stands (corrected in
  section 3).
- **Property test:** the target is non-increasing in drawdown (and in
  volatility): built, `TestTargetNonIncreasingInDrawdownAndVolatility`.

## 1. The rule

### Instrument and series

- Instrument: IBCF, ISIN IE00B3ZW0K18, Xetra (IBKR exchange `IBIS`), EUR,
  security type STK, accumulating. The policy pins IBKR's contract id and
  the ISIN; every preview and a daily check compare IBKR's contract
  details (symbol, ISIN, currency, primary exchange, security type) with
  the pin.
- Signal series: IBCF's own daily Xetra closes from IBKR historical data,
  kept in the mandate's decision-grade history store (`mandate_history.go`)
  under its own key `core:con:<contract id>`, requested on `IBIS` by
  contract id (never SMART), whether held or not, and judged fresh on
  Xetra's calendar (review S7). The book's verdict never reads it, so it
  can never make that verdict stale: the mandate measures a held IBCF line
  on SPY's returns (review S5, section 3). A series whose echoed contract
  id is not the pin holds. The fund accumulates, so its close is a
  total-return series of the EUR-hedged S&P 500 with the hedge carry and
  fees inside: the rule judges what the sleeve earns.
- No live fallback series. IBCF has traded since 2010, more than every
  live input needs (the store keeps 320 closes; the rule needs 253). A
  missing, stale or suspect IBCF series holds the month. The rule never
  switches to the index, because a silent series switch is a data error
  of its own. The S&P 500 serves elsewhere: SPY, already in the store as
  the index proxy, is the live cross-check for IBCF price errors, and SPY
  or the S&P 500 total return index is the lab's proxy before 2010.

### Decision date

The last Xetra session of each month, from `marketcal` `de_xetra` (the
official calendar, covered 2026-01-01 to 2028-12-31; for example 30
December when the 31st is closed). The target is computed once that
session's close is in the store, from 18:00 Europe/Berlin, on closes up to
and including it, and is then frozen for the month with the digest of its
window (`core_target_drift` holds a later reading that differs). A running
month is never decided.

Fixed input window (review S7): the last 253 calendar sessions to the
decision day, 252 daily returns, for both volatilities and the month-ends.
Where the embedded calendar covers a day, it decides which days are
sessions; before its coverage the series' own dates stand. A session
without a close holds the month; it is never filled, and a bar on a day
Xetra was closed holds too. The store re-reads the whole series each
session, so a broker gap repairs itself on the next read; a persistent one
holds every month whose window contains it.

### Trend

On when P_m > SMA10. P_m is the decision close; SMA10 is the mean of the
last ten month-end closes including P_m (the lab's `core_sleeve` with
`trend = "sma10m"`, strict inequality). Not live before ten month-ends.

The 200-day average is not used. It is evaluated daily, so it flips more
often and off the monthly cadence; s0 pre-registered the 10-month rule,
and choosing between the two now would be a search.

### Volatility and size

- σ_E: the EWMA of IBCF's daily simple returns over the window, span 63
  (α = 2/64), weights (1 − α)^(n−1−i), the variance around the weighted
  mean with the bias correction (Σw)² / ((Σw)² − Σw²), × √252. This is
  the lab's `_scale_to_vol` machinery (pandas `ewm(span=63,
  adjust=True)`, `bias=False`), on the fixed window.
- σ_252: the sample standard deviation of the window's 252 daily simple
  returns × √252, the mandate's window length, so the sleeve is never over
  its share by that measure.
- σ = max(σ_E, σ_252, 15%).
- Sleeve budget S = core_vol_pct / 100 × budget_base × room, from the
  mandate verdict's capital at the decision (`budget_base`, `room`;
  `capital.known` required). Equivalently core_vol_pct / vol_target_pct ×
  `budget_annual_sd_base`. With the owner's numbers S = 0.06 × budget
  base × room: the request's "6% × risk-base high-water mark × room".
- Target notional N = S / σ when the trend is on, 0 when it is off.
- Cap: N at most `max_value_pct` / 100 × budget base × room (owner's 40).
- Target shares Q = floor(N × scale_pct / 100 / P_m): whole shares,
  rounded down. `scale_pct` is the commissioning ramp (25, 50, 100); 0
  suspends the core (the kill rule's lever).

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
  never a floor. Built: the served target is the lower of the frozen one
  and today's.

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
  topped up. What is left of a buy when the window ends waits for next
  month; what is left of a sale to zero does not wait (review S6): it
  continues in the next sessions until the holding is zero.
- US-closed sessions are skipped (review S8): on a US holiday Xetra's S&P
  500 funds quote wide, so no core order is sent that day, and the window
  counts only sessions both markets trade.
- Due time: 15:45 Europe/Berlin (`send_after`), after the US cash open,
  when Xetra's S&P 500 ETF quotes are tightest; on an early close it is
  clamped to an hour before that close (review S8). The row is visible
  from the open. Five sessions also give a bill redemption time to settle
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
  verify by WhatIf in paper. A refusal is decided (D2, "Yes, for the core
  only"): core orders then use a plain DAY limit at the mid, tick-snapped,
  never crossing it, never repriced; every other automatic order keeps
  the 2026-10-09 "no fallback" rule.

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
and 5 bp half spread). The 10% band was picked on this turnover alone
(review S2): about three orders a year against 9.6 without a band; no
return was looked at. The run is logged as prior exploration of the
`equity_timing` family (five configurations, `lab prior`, 2026-10-14).

Costs beyond commission and spread (review S3), each a number or
"unknown":

| Cost | Size | Note |
|---|---|---|
| Whipsaw | Unknown | The turnover run computed no returns. A flip sells after a fall and buys after a rise; that loss is the price of the filter, and the kill rule's (b) watches it against buy-and-hold at the same notional |
| Tax on realised gains | If German-taxed: 26.375% (plus church tax where due) on 70% of the gain, the 30% partial exemption of an equity fund, so about 18.5% of each realised gain | Every trend-off sale realises the gain since purchase and ends its deferral; the size depends on the gain, unknown until the first sale. The tax reserve carries it. To confirm with the owner's tax adviser |
| Vorabpauschale | If German-taxed: the January value × the base rate × 0.7, taxed after the same exemption: about base rate × 0.13 of the value a year (2025's base rate of 2.53% gives about 0.33%) | IBCF accumulates, so the advance lump sum applies to January holdings, pro rata in the year of purchase, at most the year's gain, credited against the sale's gain later: a timing cost. To confirm with the owner's tax adviser |
| Hedge carry | Unknown here; roughly the USD-EUR one-month rate gap a year while USD rates are higher | Inside IBCF's price, so the rule's own series and the sleeve's returns carry it; not in the SPY proxy the lab uses before 2010 |
| Fund costs | TER inside the price | Not a separate charge |

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

Registered (2026-10-14 18:05 CEST) as s0's new version
`2408d09e618904c84219cacf6bc0f599e1a00ee3c4b04b0effbcc4521f45a441`, with
variants v1 (this rule), v2 (s0 sizing: σ_E and the floor only) and v3
(no trend filter); the earlier version `7c4dd5b5…` is superseded before
any run (review S1, S2). The new version states the honesty plainly: a
beta allocation the owner chose with a drawdown filter, no alpha claim,
no Sharpe hypothesis and no R1 look; the lab supplies the parity fixture
and the kill rule only. It records "S&P 500 only" as hindsight-prone and
the band as picked on turnover. Its kill rule names its references
concretely: (a) drawdown in units of K beyond 1.5 times v1's design-period
maximum, a number the first design run writes into the registry before R2
(until then it cannot fire and the mandate's brake and maximum loss bind
alone); (b) a rolling 24-month net Sharpe below 0 while IBCF held
throughout at the sleeve's mean notional, on its own closes, with no
costs, is above 0.5; (c) realised costs above twice the cost model. Design
runs stay blocked until the lab harness carries EUR-priced instruments (it
refuses non-USD datasets today) and the no-trade band. The compiled rule
`core-ibcf-v1` carries the spec hash it implements, the policy names the
rule, the verdict prints both, and a mismatch holds (built).

Parity (built): one synthetic fixture (880 sessions of generated closes
on a Xetra-shaped calendar, with a calm stretch where the floor binds, a
crash and a volatility spike where the EWMA and then the 252-day window
bind, a holiday month-end and a missing bar) and its expected values per
decision (status, month-ends, trend, SMA10, σ_E, σ_252, σ, binding, and
N and Q at scales 25, 50 and 100 for a fixed synthetic budget), produced
by the lab (`lab core-fixture`, `lab/core_rule.py`) and committed byte for
byte to both repositories (`internal/core/testdata/`). Canary's Go test
matches the status, trend, binding and Q exactly and every float to 1e-9
relative; the lab's test proves its builder still produces the fixture.
A change on either side fails a test; the fixture changes only with a new
rule version. No market data or account figure enters either
repository.

Not reused: Canary's `target-band-plan-v1` (`portfolio-plan.md`). Its
targets are static owner numbers, it never sells, and it grants advisory
selection only.

## 2. Where it lives

| Concern | Owner | How |
|---|---|---|
| Rule, monthly target, evidence | Canary verdict `core.target` (built) | Pure evaluator `internal/core`; a frozen monthly state document with its input digest; `canary core` and `canary_core`, read-only; listed in `TestOnlyTheDaemonEvaluates` |
| The exact order | Canary proposal engine, bucket `core_rebalance` | Sizes D; applies the band, minimum, funding and headroom; nets and merges with other rows; Xetra readiness |
| The decision to send | A: the owner on the device. B: the Desk controller under the Fully armed mandate | Desk never computes a size or a price |
| Execution | Canary's existing order path | Every gate, the order journal, the wire guard |
| Durable progress (B only) | Torok | The existing controller records |
| Evidence for the rule | The parity fixture and s0's kill rule (lab); scorecard sleeve `core` | The core's fills score as `core`, never as the owner's; reported, never IC- or e-value-gated, and Desk shows no IC, e-value or "established" for it (review S1) |

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
after the sweep's (`cashSweepOpenException`) and currency leveling's,
bound as tightly (review S4): to the `core_rebalance` row it was sized
for, only the pinned contract id, only BUY, only that row's sized
quantity (never more than Q − H of the frozen target), at a cost at most
the funded settled EUR. It is netted against every working IBCF order,
the owner's included; at submit it re-checks the 0.95 budget headroom and
the gap headroom on the current verdict; and it holds on `book_changed`
(the book moved since the row was sized). Checked at preview, prepare and
submit. Every other buy under `close_reduce_only` is still refused. Core
sales are ordinary reductions.

### Shipping sequence

Phase 0, done 2026-10-14: the owner's answers to D1 to D4; the
independent safety review of this record (folded in above); in the lab,
the spec version and the parity fixture. Still open in the lab: EUR
instruments and the band in the harness, which the kill rule's (a) needs.

Phase 1, read-only, no change to the write path, built on Canary branch
`core-sleeve` (2026-10-14):

| # | Item | State |
|---|---|---|
| 0a | Mandate: `index_gap_shock_pct` (D4) over a compiled broad index list; the evaluator, the gap caps and the forced reduction's gap cut read each underlying's gap | Built, `627af51e` |
| 0b | Mandate: the incremental additions gate refuses an addition that creates or deepens a gap-cap breach (B1) | Built, `cdbf8ef8` |
| 0c | Mandate: IBCF measured on SPY's local-currency returns (S5) | Built, `342582ee` |
| 1 | `[mandate.core]` schema: inactive when absent; a missing key holds the core and names it, never in the constitution's approval list; fingerprinted | Built, `e0421bb1` |
| 2 | The history store keeps the core instrument's series, held or not, on IBIS by contract id, fresh on Xetra's calendar (S7) | Built, `e0421bb1` |
| 3 | `internal/core` evaluator and the parity fixture test; data guards; the headroom bisection; the band and minimum | Built, `293fb222` |
| 4 | `core.target` verdict, frozen monthly state with `core_target_drift`, RPC, `canary core` (goldens), `canary_core`, `TestOnlyTheDaemonEvaluates` | Built, `e0421bb1` |
| 5 | Scorecard resolver: the core instrument's fills and `proposal:core_rebalance` go to sleeve `core`, reported, never gated (S1) | Moved to phase 2 |
| 6 | Desk: the core's line on Risk › Mandate (target, holding, trend, next decision), with no IC, e-value or "established" (S1) | Moved to phase 2 (Desk) |

Phase 1 knows its limits: the ISIN is checked against the compiled rule
and the contract id against the series' echo, not yet against IBKR's
contract details (W1); the decision needs the decision day's own bar and
waits for it; before the embedded Xetra calendar's coverage (2026) the
series' own dates stand.

Then witness W0 (section 6).

Phase 2, option A:

| # | Item | Size |
|---|---|---|
| 7 | `core_rebalance` rows: window (skipping US-closed sessions, S8), send time clamped before an early close (S8), band, minimum, funding, headroom, netting against every working IBCF order and merge, readiness; a trend-off remainder continues past the window (S6); a data hold stops buys and sends a sale to the owner (S6) | M |
| 8 | The typed buy exception at preview, prepare and submit, bound to the row, its quantity and its funded cost, re-checking both headrooms and holding on `book_changed` (S4) | M |
| 9 | Core buys require a known incremental mandate evaluation (section 3) | S |
| 10 | The sweep's planned need from the core's pending buy | S |
| 11 | Desk: the row's notice and its review through the existing prepared-proposal confirmation; the matcher accepts the row | S |
| 12a | Items 5 and 6 above; the contract-details check of the pin (symbol, ISIN, EUR, IBIS, STK) daily | S |
| 12b | Owner decision D5 (proposed): whether the pinned contract leaves protection's automatic scope | — |

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
| 19 | Adaptive for IBCF proven and admitted, or the D2 plain limit at the mid for the core only | US-only | S |
| 20 | Contract and recovery witnesses with a simulated daemon (5) | Not built | M |

Then witness W3.

## 3. The mandate

### What the gate does today

`mandateBudgetGate` (`mandate_verdict.go`) runs at order preview, strategy
preview, admission and the final wire guard. A risk-adding order (anything
but a close or reduce, or a buy of a put on a hedge symbol) is refused
unless the kept verdict for the policy in force is at most five minutes
old and reads `within`, and the book with the order added stays within the
whole budget and creates or deepens no gap-cap breach. The gate has no
sleeves: a core buy competes with the
owner's book for one budget.

Two gaps mattered here; both are closed on this branch. A name without a
decision-grade series now refuses the addition and fetches the series
(main `56a3c94a`), and IBCF needs none: the mandate measures it on SPY's
returns and prices an unheld IBCF order at its limit, refusing one without
a limit (review S5). And the incremental check measured volatility only,
so an addition could pass with a gap-cap breach and be sold back by the
forced reduction; it now refuses an addition that creates or deepens one
(review B1). Item 9 still requires a known incremental evaluation for a
core buy.

Gap stress (D4): a broad index fund or index on the compiled list takes
`index_gap_shock_pct` (the owner's 12) instead of `gap_shock_pct` (25).
At its 40% cap the core's gap loss is 4.8% of the budget base, under half
the 9.99% cap at full room, and both scale with room.

### The core's own share

By the formula, the sleeve's own volatility is at most S on σ_E and on
σ_252, both measured on IBCF's own Xetra closes, which carry what the
sleeve earns. The mandate measures the same line on SPY's returns, on the
US clock of the rest of the book (review S5); the headroom uses the
mandate's measure. Phase 2 adds the core's standalone risk (the mandate's
evaluator on the core leg alone) and its Euler contribution, which cannot
exceed the standalone figure, to the target. A holding over its share at
a decision is sold down by the rule; between decisions it is not.

### Headroom: the core never pushes the book over budget

A buy is the largest whole quantity, up to the funded D, for which the
book with the buy measures at most 0.95 × budget, carries no gap-cap
breach, and keeps the core's own gap loss at most at 0.95 × its cap,
found by bisection on the daemon's evaluator (built: `core.Headroom`,
`coreHeadroomFits`). 0.95 is the forced reduction's own
`mandateReductionTargetFactor`, not a new number. The forced reduction
starts above 1.0 × budget on two scheduled readings, or on a gap-cap
breach, and sizes back to 0.95, so a core buy can never start a forced
sale by itself; only the market can.

### When the owner's sleeve fills the budget

Today the whole-book gate decides. While the verdict reads over, reduce
or unknown, no core buy is admitted; while it reads within, the core gets
only the room between the book and 0.95 × budget. In a book whose owner
sleeve fills the budget, the core holds nothing. The core's sales
(rebalance down, trend off) always pass.

The whole-book gate stays binding for every core buy; it is the mandate's
hard line and must not move. The shares do not bind each sleeve (D1, "No,
whole book only"): the core gets the room the owner's sleeve leaves.

### With the forced reduction (on main since `9b10b9ca`)

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

The ranking stays "largest risk contributor first" (06:26 CEST); D1 kept
it.

### Drawdown brake and controls

The brake refuses risk-adding orders, so core buys stop and core sales
continue. Under B, each engagement holds a full mandate at protection
until the owner confirms full again on a device; core sales are
reductions and keep running under Protection. Freeze, MANUAL and Disarm
stop new core buys at the final gate and cancel nothing. An Auto lock on
IBCF locks the instrument, so it stops core sales as well as buys; a
trend-off sale waits while it stands (review note).

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
| u > 20% from USD securities alone | Reported on Risk and in the brief; nothing automatic | D3: "Report now, futures hedge later": micro EUR/USD futures once supported; never a USD loan |

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
   nothing; an Auto lock on IBCF stops core sales too.
10. A core buy never by itself takes the book over 0.95 of the budget or
    any gap loss over 0.95 of its cap (property test, built).

| Failure | Guard |
|---|---|
| Duplicate order | Frozen target; one episode per session (a one-use prepared reference under A; the journal's request and episode keys under B); netting against working, unacknowledged and unknown core orders; quantities from confirmed holdings |
| Partial fill | The DAY order expires; the next session's D comes from confirmed holdings, only once the position projection is newer than the last fill; the window's end drops the rest |
| Restart | Target and digest persisted; the order journal resolves unknown sends before any new core row; under B the controller reconciles before it runs |
| Stale data | A decision needs the decision day's bar; a buy needs a verdict at most five minutes old, equity within `capital.max_equity_age_minutes`, current positions and a live two-sided quote; the frozen target expires with its window |
| Whipsaw | Monthly evaluation only, no flip within a month, the band; about one flip a year at about 10 bp of K. A buffer around the average would be a new lab variant, never a live tweak |
| Market closed, holidays | The Xetra calendar sets the decision day, the window and readiness; half days follow it; past its coverage (2028-12-31) the core holds |
| Bad ETF price | A daily move beyond ±8% that SPY's move over the two US sessions spanning IBCF's day (from the US close before its previous session to the first US close on or after its own day, review S5) does not show at half the size and the same sign, a missing session bar, a bar on a closed day, σ outside 5–100%, or a limit more than 10% from P_m holds the month for review (built but the limit check, which is phase 2) |
| Wrong contract | Contract id and ISIN pinned; IBKR's details must match symbol, ISIN, EUR, `IBIS` and STK; a second held line with the same ISIN under another contract id holds |
| Currency mismatch | The order currency is EUR, the pin's and the account base's; funding only from settled EUR; a core order never creates a conversion or a debit; a non-EUR base holds |
| Mandate unknown at the decision | The target needs only the verdict's capital; a buy still needs a fresh `within` when it is sent; sales go |
| The owner trades IBCF by hand | The instrument is reserved to the core: its fills count as the core's, the next decision corrects the holding, and while an owner IBCF order works no core row is served (phase 2 nets every core row against it) |
| An IBCF holding exists before the core | Adopted at the first decision as the core's holding; the rule moves it to the target past the band (a sale of the excess is a row the owner confirms under A) |
| A second line of the fund under another contract id | Holds the core (built) |
| Inputs that read differently after a month was decided | `core_target_drift` holds the month (built) |
| Cash sits in bills | The planned need starts at the decision; the buy waits for settled EUR inside the five-session window |

## 6. Acceptance tests and live witness

Synthetic tests that must pass before A is offered (1–14) and before
Fully armed may include the core (15–20). Built in phase 1, each with its
test: 1 (`TestParityWithTheLab`), 2 and 11 (`TestMeasureRefusesWhatIsNotAMonthEndDecision`,
`TestCoreTargetHolds`), 3 (`TestCoreTargetFrozenMonthDriftAndCeiling`), 4
(`TestSizeTargetRoomScaleAndCap`), 5 and the band and minimum of 6
(`TestDecideBandMinimumAndHeadroom`, `TestCoreTargetTrendOffSellsAllAndBlockedBuysStillSell`),
the headroom of 6 (`TestCoreTargetBuyCutToTheGapCapMargin`), the contract
and data guards of 9 and 10 that need no order (`TestCoreTargetHolds`,
`TestGuardHoldsSuspectData`), 20 (`TestCoreHeadroomNeverBreaksTheBudget`),
the monotone target (`TestTargetNonIncreasingInDrawdownAndVolatility`),
and for the mandate fixes `TestBroadIndexTakesTheIndexGap`,
`TestMandateGateRefusesAnAdditionThatBreachesAGapCap`,
`TestMandateReductionBroadIndexCutAtTheIndexGap` and
`TestMandateMeasuresIBCFOnTheIndex`. The rest need the phase 2 rows and
exception.

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

Decided 2026-10-14 (the questions as asked, the answers verbatim):

- **D1. Do the sleeve shares bind each sleeve, or only the whole book?**
  "No, whole book only." The core uses the room the owner's sleeve leaves,
  and the forced reduction cuts the largest contributor whatever its
  sleeve. In a book whose owner sleeve fills the budget, the core holds
  nothing; the target shows it as a buy cut to no headroom.
- **D2. If IBKR refuses Adaptive for IBCF, may core orders use a plain
  DAY limit at the mid?** "Yes, for the core only." Tick-snapped, never
  crossing the mid against the owner, never repriced; the 2026-10-09 "no
  fallback" rule stays for every other automatic order. If IBKR accepts
  Adaptive, the predicate widens, with a test.
- **D3. When USD securities alone exceed 20% of net liquidation, what
  then?** "Report now, futures hedge later." Excess USD cash is converted;
  USD securities above 20% of NAV are reported; micro EUR/USD futures are
  the hedge once Canary's mandate and order paths carry futures (L);
  never a USD loan.
- **D4. Which gap shock does a broad index fund take?** "12% for broad
  index funds"; single names keep 25%. Built as `index_gap_shock_pct` over
  the compiled list `mandate.BroadIndexInstruments`, matched on symbol and
  currency: SPY, SPX, SPXW, XSP, QQQ, NDX, IWM, RUT and DIA in USD, and the
  UCITS funds IBCF (IE00B3ZW0K18), SXR8 and CSPX (IE00B5BMR087), VUSA
  (IE00B3XXRP09), VUAA (IE00BFMXXD54), EUNL, IWDA and SWDA (IE00B4L5Y983),
  VWCE (IE00BK5BQT80), VWRL and VGWL (IE00B3RBWM25). A line not on the list
  keeps 25%, the stricter stress; an addition is a reviewed change.

Open, proposed for the owner before phase 2:

- **D5. Does the pinned contract leave protection's automatic scope?**
  Protection proposals cover every held line; a protection sale of IBCF
  lowers the holding and the next decision may buy back under the
  headroom. Proposal: exclude the pinned contract from protection's
  automatic sales; the mandate's forced reduction still covers it.

## Proposed policy

The owner writes these; the code carries no default that acts. A missing
key holds the core and names the key, in the core only: it never enters
the constitution's approval list, so a half-written table changes nothing
else. Phase 1 reads the keys below (`risk.ConstitutionMandateCore`; the
template in `canary policy default constitution` shows them commented
out); the build brief's `[core]` is this `[mandate.core]`.

```toml
[mandate.core]                  # absent: the core is inactive
rule            = "core-ibcf-v1" # compiled; carries the lab spec hash
con_id          = 0              # IBKR contract id, written after resolving the ISIN on IBIS
isin            = "IE00B3ZW0K18"
exchange        = "IBIS"         # Xetra
currency        = "EUR"
scale_pct       = 25             # ramp: 25, then 50, then 100; 0 suspends
band_pct        = 10
min_order_base  = 2500
max_value_pct   = 40             # of the budget base × room
```

The core also reads two `[mandate]` keys and holds while either is
unwritten: `[mandate.sleeves] core_vol_pct` and `index_gap_shock_pct` (the
owner's 12, D4).

Phase 2 adds the window keys (`window_sessions = 5`, `send_after =
"15:45"` Europe/Berlin) with the rows that read them, and the cash rule's
`[cash.currency_cap.USD]` (`max_share_pct = 20`, `target_share_pct =
15`). An unknown key refuses the constitution, so neither is written
before the code that reads it.

The sleeve's share is the existing `[mandate.sleeves] core_vol_pct`. The
rule's own parameters (the 253-session window, ten months, span 63, 252
returns, the 15% floor) are compiled with the rule version and are not
policy keys; changing one is a new lab spec version and a new rule.

## Files

Built in phase 1:

```
internal/mandate/index.go          broad index list (D4), IBCF's returns proxy (S5)
internal/core/                     pure evaluator; parity fixture test
internal/core/testdata/            synthetic fixture shared with the lab
internal/rpc/core.go               core.target method and result types
internal/daemon/core_target.go     verdict, frozen monthly state, cadence
internal/daemon/mandate_history.go keeps the core instrument's series (Xetra freshness)
internal/daemon/mandate_verdict.go the gap-cap check on additions (B1)
internal/risk/mandate.go           [mandate.core] and index_gap_shock_pct
internal/cli/core.go               canary core [--json]
internal/mcp/tools.go              canary_core, read-only
lab: lab/core_rule.py, lab/core_fixture.py, tests/test_core_rule.py,
     specs/s0_core_sleeve.yaml (2408d09e6189)
```

Phase 2:

```
internal/daemon/core_rebalance.go  rows, sizing, the typed buy exception
```
