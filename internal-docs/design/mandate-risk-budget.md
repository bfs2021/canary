# Mandate risk budget (ex-ante volatility budget and the additions gate)

Updated: 2026-10-14 09:24 CEST
Status: increment 1 implemented 2026-10-14 (measurement, verdict, additions
gate); increment 2 implemented 2026-10-14 (forced reduction, below). The
feature is inactive until the owner writes `[mandate]` into
`risk-policy.toml`; the forced reduction places nothing by itself until the
owner lists `mandate_reduction` under `[authority].pre_authorised`.

This record follows `.agents/docs/risk-policy-contract.md` and the
daemon/CLI trading contract. It records human policy decisions; it creates
none. Every number below that acts was written by the owner or is a
proposal the owner confirms by writing it into the policy file.

## Decision

- Goal and protected asset or behavior: keep the book's ex-ante risk inside
  a volatility budget derived from the owner's maximum tolerated loss, so
  that a loss of the whole budget stays inside the mandate's limit, and
  refuse any order that adds risk while the book is outside the budget or
  while the budget cannot be measured. Protected asset: the risk base
  (account equity after the tax reserve). Protected behaviour: no
  risk-adding order while the book is over budget or unmeasured.
- Policy owner: the desk owner (single-trader desk).
- Human decisions, recorded verbatim from the chat of 2026-10-14 06:26 to
  06:52 CEST:
  - Maximum loss D = 30%, measured on account equity minus a tax reserve
    ("Tax comes from this account"; the reserve is an owner number,
    currently estimated EUR 70,000), counted fresh from 2026-10-14 with a
    cash-flow-adjusted high-water mark ("Start fresh today").
  - Volatility target = D/2 = 15% a year of the risk base.
  - Protection that acts: on a budget breach, a reduce-only batch back to
    budget, largest risk contributor first, pre-authorised with the
    30-minute veto; release when risk is back in budget, not when P&L
    recovers. Built in increment 2 ("Forced reduction" below) on the
    measurement and the additions gate of increment 1.
  - Sleeve volatility shares of the 15%: owner 7, core 6, overlay 4. Recorded
    here as `[mandate.sleeves]`; enforcement per sleeve comes later.
- Departure from risk-policy.md decision 3 ("No auto-increase: deposits,
  profits, and live events never raise declared risk capital"): the
  mandate's budget follows the high-water mark of the cash-flow-adjusted
  risk base, so it rises with gains and falls with flows. This is
  deliberate and owner-decided ("Start fresh today", a high-water mark). The
  declared risk capital, the protected floor and the drawdown brake are
  untouched; the owner switches those numbers separately. The two controls
  coexist: the brake refuses on realised drawdown of declared capital, the
  mandate refuses on ex-ante risk against a budget that moves with the book.
- Current authoritative document/code/fingerprint: `risk-policy.toml`
  `[mandate]` (part of `risk-constitution-fp-v1` once written; absent it
  stays out of every fingerprint, like `drawdown.release`); code
  `internal/risk/mandate.go` (schema), `internal/mandate` (the pure
  evaluator), `internal/daemon/mandate_*.go` (capital state, history store,
  verdict, gate).
- Status: implemented (increments 1 and 2). Active only with `[mandate]`
  written; the forced reduction is automatic only with `mandate_reduction`
  pre-authorised.

## Meaning

- Capital or exposure base: the **risk base** = account equity (NLV in the
  base currency) − `mandate.tax_reserve_base`. The **budget base** is the
  high-water mark of the cash-flow-adjusted risk base since
  `mandate.started_at`, translated into today's capital terms:
  `budget_base = HWM(equity − flows_since_start) − tax_reserve +
  flows_since_start`. It rises with every new high and moves one-for-one
  with declared or statement flows after the start, so a withdrawal lowers
  the budget and a deposit raises it, while neither creates a drawdown or
  a false high. The tax reserve shifts both the high-water mark and the
  current risk base by the same amount, so changing it never changes the
  drawdown, only the base. Changing `started_at` starts the series again
  (a fresh high-water mark seeded from the next observation).
- Drawdown and room: `drawdown = max(0, HWM − adjusted risk base now)`,
  `drawdown_pct = drawdown / HWM`, `room = clamp(1 − drawdown_pct / (D /
  100), 0, 1)`. Room is 1 at a new high and 0 once the loss reaches D.
- Budget: `budget_annual_sd = vol_target_pct / 100 × budget_base × room`.
  With D = 30 and the target 15, the budget is half the remaining loss room
  per year of volatility; at room 0 the budget is 0 and any measured risk
  is over it.
- Aggregation unit: the whole account, per position (exact contract) and
  per underlying for contributions; per underlying for gap stresses.
- Measurement: historical simulation over the last 252 daily log-returns on
  dates every needed series shares, with full revaluation of every leg:
  - stock and ETF: linear in the underlying's scenario return;
  - option: Black-Scholes (European, rate 0, dividend yield 0) at the leg's
    current implied volatility as IBKR's model reports it on the position
    row, the underlying moved by the scenario return and one calendar day
    of time decay; a leg whose IV is missing makes the verdict unknown (no
    guess);
  - a leg in another currency: its scenario value is converted to base
    with that currency's scenario FX return, read jointly from the same
    date grid (the EUR.USD series for USD legs; `EUR.<CCY>` for any other
    held currency);
  - bills, bonds and currency conversions carry no equity or volatility
    risk here and are listed as not modelled; any other security type
    (future, index, CFD, fund, warrant) makes the verdict unknown.
  Outputs: 1-day P&L sd and annualised sd (×√252) in base currency; 99%
  expected shortfall (the mean loss of the worst ⌈1%⌉ scenarios); Euler
  risk contributions per position and per underlying
  (cov(leg, total) / var(total), which sum to 100% of the total by
  construction); the FX share of the risk (the same decomposition of each
  leg's base P&L into its local-currency part and its currency part);
  named stresses: each underlying alone −`gap_shock_pct`, and
  +`gap_shock_pct` where a short-call or short-stock leg loses on the
  upside; the index −10% applied through each name's historical beta to SPY
  over the same 252 returns (SPY itself at beta 1).
- Gap cap per underlying: `room × D / 100 × budget_base × gap_room_pct /
  100`; a stress loss above it is a breach.
- Threshold or rule, including units: `max_loss_pct` (D, percent of the
  budget base), `vol_target_pct` (percent a year of the budget base),
  `tax_reserve_base` (base currency), `started_at` (date, YYYY-MM-DD),
  `gap_shock_pct` (percent; the proposal is 25), `gap_room_pct` (percent of
  the remaining loss room one name may lose in a gap; the proposal is
  33.3), `[mandate.sleeves]` `owner_vol_pct`, `core_vol_pct`,
  `overlay_vol_pct` (percent a year; recorded, not enforced). The
  proposals are written into the file by the owner; the code carries no
  default that acts. A `[mandate]` table missing a material key is active
  and unknown: the verdict names the unapproved keys and additions are
  refused until the owner writes them (data absence never renders ok).
- Verdict: `within` (risk ≤ budget and no gap breach), `over` (risk >
  budget), `reduce` (risk > 1.25 × budget, or any gap-cap breach),
  `unknown` (anything the measurement lacks).
- Enforcement class: pre-trade hard gate for additions (`mandate_budget`
  blocker), at the same four enforcement points as the drawdown brake
  (order preview, strategy preview, admission, the final wire guard), with
  exactly the brake's risk-adding classification: a close or reduce passes,
  a BUY of a put on a Rulebook hedge symbol passes, cancels never reach it.
  The exemption is not widened. Release is automatic: the moment a fresh
  verdict reads `within`, additions pass; there is no latch and no reset.
  The incremental pre-trade check re-evaluates the book with the proposed
  leg added and refuses an addition that would take the annualised risk
  over the budget even while the standing verdict reads `within`.
- Unknown, stale, partial, and unavailable-data posture: the verdict is
  `unknown`, and additions are refused, when the equity observation is
  missing or older than `capital.max_equity_age_minutes` (or that horizon
  is unapproved), the base currency is unproven or differs from
  `capital.base_currency`, the positions read is not current for the
  selected account, any held equity or option leg lacks a decision-grade
  series or its series is stale, fewer than 253 shared dates exist, an
  option leg lacks its IV or spot, a leg lacks an FX rate, the cached
  verdict is older than five minutes, or the mandate section is incomplete.
  Every reason is named on the verdict. Nil is unavailable, never zero.

## Forced reduction

Owner decisions, recorded verbatim:

- 2026-10-14 06:26 CEST, decision 3 "Protection that acts": "on a budget
  breach, one reduce-only batch sized back to budget, largest risk
  contributor first, pre-authorised with the 30-minute veto. The brake
  releases when risk is back in budget, not when P&L recovers." Owner: "yes
  to all".
- 2026-10-14 06:26 CEST: "The system should run by itself within risk
  boundaries."
- The bucket ships absent from the embedded default and inactive; the owner
  arms it by listing it under `[authority].pre_authorised`.

What the daemon does (`internal/daemon/mandate_reduction.go`, bucket
`mandate_reduction`):

- **Trigger.** While `[mandate]` is written, the proposal engine reads the
  kept verdict for the policy in force on every refresh. Rows appear when the
  verdict reads `reduce`, or `over` on two consecutive verdicts (the verdict
  cache counts the streak: each reading at most five minutes after the one
  before, under the same constitution fingerprint; `within`, `unknown`, a
  longer gap or a new policy resets it). One `over` reading reports
  `watching` and proposes nothing. `unknown` never sizes anything.
- **Target and hysteresis.** The batch is sized to risk ≤ 0.95 × budget with
  every gap cap met, so the next verdict reads `within` rather than `over`
  by a hair; release is at `within` (risk ≤ budget, no gap breach). The 5%
  band is compiled (`mandateReductionTargetFactor`), not a policy key.
- **Sizing.** Greedy, with the daemon's own evaluator on the book the
  verdict measured. (1) Expiry first: a long option in the money within
  three US option sessions of expiry, whose exercise as delivered stock would
  leave the book over budget and riskier than holding the option, is cut
  before anything else, to the fewest contracts whose delivered shares keep
  the book inside the target, or closed. (2) Then cut by cut, the book
  re-evaluated after each: a name over its gap cap has its largest gap loser
  cut until the name's gap loss is under the cap; with no breach left, the
  largest Euler contributor is cut by the smallest whole step that reaches
  the target or, when no step on that line does, by the step that takes the
  most risk off (risk is convex in one line's size, so that is the minimum,
  never more), and the next contributor follows only when needed. Steps are
  contracts for options and share lots for stocks (1 below 100 shares, 10 to
  999, 100 from 1,000). Legs never cut: a long put on a Rulebook hedge
  symbol, a leg of a multi-leg unit (the strategy workflow reduces it), a
  row the owner ignored; the status names them under `left_out`.
- **Every row** is reduce-only (a sale of a long line, a buy-back of a short
  one, never past zero), a DAY limit at the mark like the budget governor's
  rows, `never_skip_veto`, and carries the verdict it was sized on
  (`mandate.verdict_status`, `verdict_at`, `verdict_fingerprint`), its rank,
  phase (`expiry`, `gap`, `risk`), the line's share of the risk, the risk
  the cut takes off and the risk after the batch.
- **Order caps.** One order is held to the lower of
  `[buckets.risk_reduction].max_order_notional` and the `[order_limits]` cap
  in force, at the row's mark; `mandate.units` keeps the whole cut and the
  status `plan` both numbers. After the fill the next verdict measures the
  book as it then is and sizes the remainder (carry-over, as the governor).
- **A standing batch** is kept while the verdict stays over budget: a row
  whose line is still held at the quantity it was sized on keeps its cut,
  order, rank and the verdict it was sized on (`confirmed_at` names the
  verdict that kept it), so its revision and veto window stand; the batch
  is extended only if the new verdict needs more.
- **Hold.** A verdict that is `unknown`, older than five minutes, or
  measured on another book than the one held (a fill, a hand trade:
  `book_changed`) sizes nothing. Standing rows are served held
  (`mandate.held`, `hold_reason`): preview and submit refuse them
  (`mandate_sizing_held`), the scheduler holds a due record on its original
  window (`automaticHoldMandateVerdict`) and the notice reads "Reduction
  held" (`protection_auto_held_mandate`). A held row whose own line moved is
  dropped.
- **Release.** A verdict reading `within` withdraws every standing row; a
  waiting automatic record is superseded and never sent. Nothing is
  cancelled at the broker: an order already working stays.
- **One row per contract and side.** Where another bucket (the budget
  governor, the issuer trim, theta hygiene) also wants to sell the line, the
  same-contract merge keeps one row sized by the largest need, never the sum
  (precedence on a tie: after the governor, before theta hygiene). A held
  mandate row takes no part in the merge, so it never stands for a row that
  can be sent. Working-order netting applies as to every reduction: any
  same-side order working for the contract, a protective trailing stop
  included, blocks the row until it fills or is cancelled.
- **Submission.** Only the existing scheduler and `submitAutomatic`: one
  durable record per key and revision, the notice, the full veto window even
  under a latched drawdown brake, one grant under `brokerWriteMu`, origin
  `daemon-preauthorised`, every write gate unchanged. Records are dispatched
  in rank order within a cycle; one row's refusal never holds the others.
  Not pre-authorised, the rows are ordinary proposals that wait for the
  owner's hand. They are not queueable for the open. At a stress open they
  wait 30 minutes after the options open, like the governor's rows.
- **Surfaces.** `canary proposals` (and `--details`, JSON, MCP
  `canary_proposals`) lists the rows with bucket, rank, share of the risk
  and the verdict in plain words under a Mandate header; `canary mandate`
  (and `canary_mandate`) shows the batch, the rows pending and the veto end.
  The pre-authorised notice is `protection_auto_mandate_reduction` (no "now"
  variant). The scorecard counts the fills as `rule`
  (`proposal:mandate_reduction`), never the owner's.
- **Residual risk accepted.** The sizing inherits the measurement's limits
  (one year of history, today's IV, no rates or dividends). A line carrying
  a working order (a stop among them) is blocked by netting and the batch
  does not re-plan around it in the same cycle; the status says the target
  is not reached once nothing else reduces. Two verdicts can be read 30
  seconds apart (a read refreshes a verdict older than that), so the
  two-reading rule is a debounce, not a time filter. After any fill the
  batch waits for the next verdict (at most two minutes).
- **Verification** (synthetic books; `internal/daemon/mandate_reduction_test.go`,
  `mandate_reduction_automatic_test.go`, `mandate_reduction_trading_test.go`,
  `scorecard_authority_test.go`, `internal/app/alerts/presentation_test.go`,
  CLI goldens `proposals_list_mandate*`, `mandate_reduce`): the dominant
  name first and sized to the target by its smallest lot, the second name
  only when needed; an in-the-money call two sessions before expiry first;
  one `over` watches and two reduce, one `reduce` reduces; the streak
  resets; `unknown`, stale and changed-book verdicts size nothing and hold a
  standing batch with its revision; `within` withdraws, a sent record stays;
  a latched brake keeps the veto; not pre-authorised waits for the hand;
  pre-authorised places one order after the veto with origin
  `daemon-preauthorised`; rank order and no hold-up after a refusal;
  reduce-only on long and short lines; order-cap carry-over; a gap breach
  cut to its cap; one row per contract with the governor; absent
  `[mandate]` changes nothing.

## Decision-grade history

The chart-history store (`market_history_store.go`) is display-only: it
serves what it has, labels gaps and never refuses. The mandate keeps its
own series (`mandate_history.go`, state documents of kind
`mandate_history`, one per contract identity, scope `mandate/history/<id>`)
with the source time of every read (`fetched_at`, the broker's `source`
label, the session that answered), and refuses to judge on a series that is
missing or stale. A series covers every held stock and ETF, the underlying
of every held option, SPY (the index proxy) and `EUR.<CCY>` for every held
non-base currency. It holds the last 320 daily closes (at least 253 are
needed) and is refreshed on the background pacing lane within the existing
sixty-per-ten-minutes historical budget: at most one refresh per name per
broker session, only when the series lacks the last completed session, and
never more than two reads in flight. A read asks the broker for 400
calendar days so a mixed US and European book still shares 253 dates after
alignment. A series is fresh while its last close is no more than one
completed US session behind; a failed refresh keeps the last good series
and its clock, so the verdict says stale rather than inventing a bar. The
fetch helper `fetchMarketHistoryDays` is reused; the display store's
documents are neither read nor written.

## Authority And Evidence

| Concept | Authoritative source | Typed field/contract | Freshness/finality | Fallback or blocker |
|---|---|---|---|---|
| D, vol target, tax reserve, start date, gap numbers, sleeves | `risk-policy.toml` `[mandate]` | `risk.ConstitutionMandate` | manager reread every 30 s; a change is a revision (fingerprinted) | table absent ⇒ feature inactive, no behaviour change; incomplete ⇒ verdict unknown, additions refused |
| Equity, base currency | account summary success path | `AccountResult.NetLiquidation/BaseCurrency/AsOf` | `capital.max_equity_age_minutes` | missing or stale ⇒ unknown |
| High-water mark, flows at start, daily risk-base samples | daemon runtime state (the `risk_capital` document, `state.mandate`) | `mandateCapitalStateV1` | persisted with the adjusted peak, CAS in SQLite | storage failure ⇒ the last committed state, disclosed |
| Flows since start | the capital store's effective flows (statement when the constitution has statement reconciliation, else declared) | `effectiveFlowsLocked` | per observation | a flow value-dated before the start but incorporated after it shifts the budget; the owner resets `started_at` |
| Positions, IV, spot, FX | one current `positions.list` read of the selected account | `PositionView.Quantity/Mark/IV/Underlying/FXRate/Delta` | current portfolio authority | not current ⇒ unknown |
| Daily closes | `mandate_history` state documents | `mandateHistorySeries` | last close within one completed session | missing or stale ⇒ unknown |
| Verdict | daemon-owned evaluation (`mandate.risk`) | `rpc.MandateRiskResult` | refreshed every two minutes and on every read; the gate reads a verdict no older than five minutes | older ⇒ unknown ⇒ additions refused |
| Blocker | `mandateBudgetGate` | `TradingBlocker{Code: "mandate_budget"}` | per order, at four points | — |

The exercise path binds its own position authority and never binds risk
(`exerciseBound`); an exercise is reduce-only by the opportunity policy and
cannot add risk, so the wire guard's risk block (which runs only when
`binding.riskBound` is set) covers every path that can add risk: order
place and order modify both bind through `bindPreviewOrderRiskAuthority`.
This is stated here because the gate's placement inside `if
binding.riskBound` was reviewed as the riskiest integration point.

## Exceptions And Change Control

- Who may grant an exception: the owner, by revising `[mandate]` (raise D or
  the target, lower the reserve, move the start) with a higher
  `policy_version`. No one-shot override control exists for this table.
- Required reason, scope, expiry, and audit record: the file edit and its
  fingerprint; the high-water mark advances and starts are journaled
  (`mandate_hwm_advanced`, `mandate_started`) with the fingerprint.
- Whether exceptions can increase risk: a revision can raise the budget; no
  exception can admit an order the standing verdict refuses.
- Rollback trigger and operator action: remove `[mandate]` and raise
  `policy_version`; the gate is then inactive and the state document keeps
  its high-water mark for a later restart.
- Policy version/fingerprint transition: a file written before this table
  existed keeps both fingerprints byte for byte (pointer table, omitted
  when nil). An older daemon refuses a file that carries `[mandate]`
  (strict unknown-key rejection keeps the last good policy); `canary policy
  ensure` never writes the table and its migration leaves it as written.
  The table needs schema 2, as every feature added since the schema split.

## Operating Cadence

- Pre-open or daily review artifact: `canary mandate` (budget, risk,
  room, contributions, stresses, evidence clocks).
- Pre-trade artifact and acknowledgement: the order preview's
  `mandate_budget` blocker, naming the verdict and its reasons.
- Post-trade/EOD reconciliation artifact: none new; the verdict refreshes
  from the positions and the equity as they then are.
- Weekly review and breach follow-up: `canary policy check` reports the
  realised drawdown of the risk base against D and the realised 20- and
  60-day volatility against the target (`mandate_realised_drawdown`,
  `mandate_realised_volatility`), and names inconsistent mandate keys
  (`mandate_keys_inconsistent`).
- Missed-run or stale-report escalation: stale equity, positions or history
  reads unknown and refuses additions; nothing passes silently.
- Shadow/manual observation period before hard enforcement: none. The owner
  activates the gate by writing the table.

## Verification

- Fixture scenarios (synthetic books only): `internal/mandate` tests pin
  the evaluator (contributions sum to 100%, put-call parity, deep in the
  money ≈ intrinsic, gap caps as specified, beta of SPY on itself is 1,
  FX conversion, missing IV ⇒ unknown); `internal/daemon` tests pin the
  gate at preview, admission and the wire guard (over budget ⇒
  `mandate_budget`; a reduction passes; stale history ⇒ unknown ⇒ refused;
  back within ⇒ additions pass without a reset; absent `[mandate]` ⇒ no
  behaviour change), the capital state (high-water mark, flows, room, a
  reserve change leaves the drawdown unchanged), the history store
  (freshness, one refresh per session), the constitution (fingerprint
  unchanged while absent, unknown mandate keys refused, migration leaves
  the table unchanged) and the policy check rules.
- Property/invariant tests: contributions sum to 100%; room in [0, 1];
  budget 0 at room 0; the gate never refuses a close or reduce.
- Cross-surface parity checks: `canary mandate` golden render; the MCP tool
  `canary_mandate`; `TestOnlyTheDaemonEvaluates` lists `mandate.Evaluate`.
- Redacted before/after artifact: none against a live gateway (fixtures
  only, by instruction).
- Residual risk accepted: the measurement is historical (one year of
  returns, no regime weighting), options are revalued at today's IV (no
  volatility scenario), rates and dividends are zero in the model, and an
  option addition whose IV cannot be derived from the preview quote is not
  judged incrementally (the standing verdict still gates it). All stated on
  the verdict and here.

## Files

```
internal/risk/mandate.go               [mandate] schema, validation, unapproved keys
internal/mandate/                      pure evaluator: Black-Scholes, historical simulation, verdict
internal/rpc/mandate.go                mandate.risk method, result types, FetchMandateRisk
internal/daemon/mandate_capital.go     high-water mark, room, daily samples (inside the risk_capital document)
internal/daemon/mandate_history.go     decision-grade daily closes, background refresh
internal/daemon/mandate_verdict.go     evaluation cache, RPC handler, mandate_budget gate
internal/daemon/mandate_reduction.go   forced reduction: sizing, hold, release, rows
internal/cli/mandate.go                canary mandate [--json]
internal/mcp/tools.go                  canary_mandate
```
