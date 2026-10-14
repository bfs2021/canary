# Mandate risk budget (ex-ante volatility budget and the additions gate)

Updated: 2026-10-14 16:48 CEST
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
  flows_since_start`. It rises with every new high, once a second reading
  at least five minutes later confirms it (at the lower of the two), and
  moves one-for-one with declared or statement flows after the start, so a
  withdrawal lowers the budget and a deposit raises it, while neither
  creates a drawdown or a false high. The tax reserve shifts both the high-water mark and the
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
  over the budget even while the standing verdict reads `within`. An
  addition on a name or in a currency the book does not hold is measured on
  the series the history store keeps for it (spot from its last close when
  the book holds none); while none is fresh, the order is refused
  (`mandate_budget`: "no daily history for X yet; the desk is fetching
  it", or the base currency's exchange rate against the order's) and the
  series is requested on the background lane under the same budget, so a
  retry after the fetch is measured. A book that cannot be measured with
  the order added refuses it too; nothing passes unmeasured (2026-10-14
  16:57 CEST, from the core-sleeve design review).
- Unknown, stale, partial, and unavailable-data posture: the verdict is
  `unknown`, and additions are refused, when the equity observation is
  missing or older than `capital.max_equity_age_minutes` (or that horizon
  is unapproved), the base currency is unproven or differs from
  `capital.base_currency`, the positions read is not current for the
  selected account, any held equity or option leg lacks a decision-grade
  series or its series is stale, fewer than 253 shared dates exist, an
  option leg lacks its IV or spot, a leg lacks an FX rate, the cached
  verdict is older than five minutes, the mandate section is incomplete, or
  an equity move the book's P&L does not explain suggests an undeclared
  cash flow (see Forced reduction).
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

Revised 2026-10-14 10:17 CEST after the independent safety review of the
first build (coverage, a batch that never shrank, the veto clock, the
prepared path, netting and the debounce), and again 2026-10-14 16:48 CEST
after the second review (a veto clock that let a row grow unboundedly,
covered calls, protective puts at exercise, undeclared cash flows, the
gap-breach trigger, a DAY order that expired unfilled). Decision relayed
with the second review: the batch does not buy back short calls and does
not cut the shares that cover them; a covered position needs the owner's
hand (the owner's policy forbids selling options to open, so short calls
come only from hand trades, and a two-leg unwind needs a combination order
Canary does not route automatically).

What the daemon does (`internal/daemon/mandate_reduction.go`, bucket
`mandate_reduction`):

- **Trigger.** While `[mandate]` is written, the proposal engine reads the
  kept verdict for the policy in force on every refresh. The verdict cache
  counts a streak of scheduled verdicts (the background cadence, every two
  minutes, which a read's own evaluation never moves) reading over or
  reduce, each counted reading at least five minutes after the last one
  and at most fifteen; a read's evaluation never counts, and any reading,
  scheduled or not, that is neither over nor reduce resets it, as does a new
  policy. A batch starts on two counted readings, whether over, reduce or a
  gap-cap breach, so polling cannot build it in seconds and one bad reading
  cannot start it; only a running episode carries a breach on without them.
  One reading reports `watching` and proposes nothing. `unknown` never
  sizes anything.
- **Episode.** The first batch opens a breach episode (`episode` on the
  status and on every row); it lasts until a verdict reads `within`.
- **Target and hysteresis.** The batch is sized to risk ≤ 0.95 × budget with
  every gap cap met, so the next verdict reads `within` rather than `over`
  by a hair; release is at `within` (risk ≤ budget, no gap breach). The 5%
  band is compiled (`mandateReductionTargetFactor`), not a policy key.
- **Sizing, afresh at every verdict.** Greedy, with the daemon's own
  evaluator on the book the verdict measured; a standing batch is never
  carried forward as it was. Cut by cut, the book re-evaluated after each: a
  name over its gap cap has its largest gap loser cut until the name's gap
  loss is under the cap (a gap cut never leaves the book riskier); with no
  breach left, the largest Euler contributor is cut by the smallest whole
  step that reaches the target or, when no step on that line does, by the
  step that takes the most risk off, and the next contributor follows only
  when needed. Steps are contracts for options and share lots for stocks (1
  below 100 shares, 10 to 999, 100 from 1,000). Damping: a line whose
  position has not moved keeps the last generation's cut when the fresh cut
  is smaller by at most one step, so a steady risk does not re-size a row
  every verdict; a larger ease shrinks it, a rise grows it.
- **Covered and protected shares.** The batch never buys a short option
  back and never sells the shares that cover short options of their own
  underlying (short calls over long shares, short puts over a short sale):
  those shares and those options are left out and named, the batch cuts
  other contributors instead, and where the target cannot be met without
  them the status says "a covered position needs your hand" and names
  them. Shares above the cover are cut normally. Nor are shares ever cut
  below what the long options on them would deliver or take at exercise
  (long puts on a long line, long calls on a short sale), so exercising
  them never flips the line; those options are protection and never sold
  (the budget governor's test, `budgetLegIsProtection`, which also keeps
  hedge-listed puts and legs the option policy derives as protection).
  Every order the batch may send alone lowers the book's risk.
- **Expiry.** An in-the-money long option within three US option sessions
  of expiry is cut ahead of the batch only where its exercise, as delivered
  stock, would by itself push the planned book over budget (and add risk),
  by the fewest contracts that keep the exercised book inside the target;
  the batch is then sized again around that cut. An option whose exercise
  changes little is left to the variance.
- **Working orders.** A line a working stop or trail holds leaves the plan
  (netting would hold its row as long as the stop works), the next
  contributor is cut instead, and the line is named under `left_out`. A line
  with an immediate order in flight (a sale partly filled, the rest
  working) stays in the plan, since that cut is on its way; its row waits
  behind the working order (netting) until it fills or is cancelled.
- **Every row** is reduce-only (a sale of a long line, a buy-back of a short
  one, never past zero), a DAY limit at the mark like the budget governor's
  rows, `never_skip_veto`, and carries the verdict it was sized on
  (`mandate.verdict_status`, `verdict_at`, `verdict_fingerprint`), its
  episode, rank, phase (`expiry`, `gap`, `risk`), the line's share of the
  risk, the risk the cut takes off and the risk after the batch.
- **Order caps.** One order is held to the lower of
  `[buckets.risk_reduction].max_order_notional` and the `[order_limits]` cap
  in force, at the row's mark; `mandate.units` keeps the whole cut and the
  status `plan` both numbers. After the fill the next verdict measures the
  book as it then is and sizes the remainder. As in the governor, an order
  is never held below one unit: one contract or share above a cap is the
  order gate's to judge, and it passes only as a delta-reducing exit.
- **Veto clock within an episode.** A row re-sized within its episode
  (its revision changes) keeps the veto clock of the record it replaces,
  which never reached the broker, only while its size stays within one lot
  above the size that clock, and its notice, was started for: shrinking,
  or growing by up to a lot, keeps it, so a batch re-sized at every verdict
  is never starved. Growing further starts a fresh clock: a new notice (a
  new alert episode, keyed by the line, the episode, the clock and its
  size; the push copy itself carries no figures by the presentation
  contract, the Protection row shows the order) and a full window, once
  per such growth, never per verdict, so "sell 100" can never become
  "close 3,000" on the old clock. A record whose clock started before the
  regular session it would be sent in, as after an overnight hold, never
  goes out on it: when that session opens it gets a fresh notice and a full
  window. The owner's veto holds the line for the rest of the episode,
  whatever its size; a sent, failed or otherwise ended record passes no
  clock on. Each order stays held to the caps.
- **One session's order.** A row's revision includes the trading date
  (New York) of the verdict it was sized on, so a DAY order that expired
  unfilled (its record terminal for that revision) is proposed afresh, with
  a fresh window, the next session instead of stranding the batch.
- **Hold.** A verdict that is `unknown`, older than five minutes, or
  measured on another book than the one held (a fill, a hand trade:
  `book_changed`) sizes nothing. Standing rows are served held
  (`mandate.held`, `hold_reason`): preview, submit, the device-confirmed
  prepared submit and the owner's order queue refuse them
  (`mandate_sizing_held`), the scheduler holds a due record on its original
  window (`automaticHoldMandateVerdict`) and the notice reads "Reduction
  held" (`protection_auto_held_mandate`). A held row whose own line moved is
  dropped.
- **Release.** A verdict reading `within` ends the episode and withdraws
  every standing row; a waiting automatic record is superseded and never
  sent. Nothing is cancelled at the broker: an order already working stays.
- **One row per contract and side.** Where another bucket (the budget
  governor, the issuer trim, theta hygiene) also wants to sell the line, the
  same-contract merge keeps one row sized by the largest need, never the sum
  (precedence on a tie: after the governor, before theta hygiene). A held
  mandate row takes no part in the merge. While the drawdown brake is
  latched, a pre-authorised stock trailing stop is placed at once and a
  mandate sale waits its full window, so the sale never covers the stop
  then: the stop goes first, and netting holds the sale behind it.
- **Submission.** Only the existing scheduler and `submitAutomatic`: one
  durable record per key and revision, the notice, the full veto window even
  under a latched drawdown brake, one grant under `brokerWriteMu`, origin
  `daemon-preauthorised`, every write gate unchanged. Records are dispatched
  in rank order within a cycle; one row's refusal never holds the others.
  Not pre-authorised, the rows are ordinary proposals that wait for the
  owner's hand. They are not queueable for the open. At a stress open they
  wait 30 minutes after the options open, like the governor's rows.
- **The maximum loss.** At a drawdown of D the room, the budget and every
  gap cap are zero, so every name is in breach and the batch acts on one
  reading: it reduces the book to what it may not cut (hedge-listed puts,
  protection, multi-leg units, ignored rows). At the maximum loss the
  mandate reduces the book to its hedges (owner, 2026-10-14 ~16:10 CEST, in
  chat: "Yes, flatten to hedges"; the 30-minute veto still applies). The
  verdict's ratio to a zero budget is unbounded and encodes as
  `ratio_to_budget: null` (read back as unbounded), so the verdict keeps
  serving instead of failing to encode.
- **A glitchy equity reading.** A new high-water mark counts once a second
  reading at least five minutes later confirms it, at the lower of the two
  (`mandate_capital.go`): one spike never raises the mark, which would read
  as a drawdown from then on. A single low print that collapses the room
  breaches every gap cap but is one reading: it only watches.
- **Undeclared cash flows.** The owner withdraws regularly, and a flow not
  yet declared moves equity: a withdrawal would read as a drawdown (less
  room, a smaller budget and gap cap, a batch), a deposit would raise the
  high-water mark and read as a drawdown once declared. With every equity
  observation the capital state keeps the broker's daily P&L of the same
  read and judges the unexplained part of the move since the last
  observation: the move net of declared and statement flows, less the
  P&L over the same span (the new session's own P&L across a session
  start; zero where the broker reports none, so without P&L the whole move
  counts). More than max(1% of equity, 1,000 in base currency) is a
  suspected flow (`flow_suspect`, journaled `mandate_flow_suspected`); the
  floor stands for the accruals a broker posts to cash that its daily P&L
  may not carry (interest, a dividend, a fee), and a flow that small moves
  the budget by under 150 a year at the 15% target. While suspected the
  high-water mark does not move and the capital reads unknown, so the
  verdict is unknown: additions stay refused, no new mandate rows,
  standing rows held. The unexplained part is then measured cumulatively
  from the observation before the suspicion, the P&L summed step by step,
  so a later P&L move never offsets it (a 3% withdrawal stays suspect
  through a +1% day; an 8% withdrawal on a −4% day is flagged). It clears
  (`mandate_flow_cleared`) only when that cumulative part is back under
  the threshold, because a declared or statement flow accounts for it or
  the reading that raised it is undone (a glitch), or when a statement
  covering its day has been reconciled. How the owner clears it: declare
  the flow (`canary policy capital-event deposit|withdrawal --amount …
  --effective-at …`), or let the next statement reconciliation cover the
  day. A suspicion nothing explains stays in the safe direction, the room
  held, until one of the two (revised 2026-10-14 17:27 CEST after the
  third review: the first version let a later P&L move offset a flow and
  did not judge a move without P&L).
- **Surfaces.** `canary proposals` (and `--details`, JSON, MCP
  `canary_proposals`) lists the rows with bucket, rank, share of the risk
  and the verdict in plain words under a Mandate header; `canary mandate`
  (and `canary_mandate`) shows the batch, the rows pending and the veto end.
  The pre-authorised notice is `protection_auto_mandate_reduction` (no "now"
  variant). The scorecard counts the fills as `rule`
  (`proposal:mandate_reduction`), never the owner's.
- **Residual risk accepted.** The sizing inherits the measurement's limits
  (one year of history, today's IV, no rates or dividends). A covered
  position, and shares long options would deliver, are never reduced by
  the batch: where they carry the breach, the book stays over budget until
  the owner acts. A line a stop holds is not reduced while the stop works.
  Within an episode a row may grow by one lot on its clock: the owner sees
  the current size on the Protection row, not in the push. The flow check
  reads the broker's daily P&L: a P&L feed that lags a fast market, or
  is missing, can suspect a flow (the safe direction: the room is held)
  until it catches up, the flow is declared or a statement covers the
  day. One unit of an order may exceed a cap, judged by the order gate as
  for the governor. After any fill the batch waits for the next verdict
  (at most two minutes).
- **Verification** (synthetic books; `internal/daemon/mandate_reduction_test.go`,
  `mandate_reduction_review_test.go`, `mandate_reduction_automatic_test.go`,
  `mandate_reduction_trading_test.go`, `scorecard_authority_test.go`,
  `internal/mandate/mandate_test.go`, `internal/app/alerts/presentation_test.go`,
  CLI goldens `proposals_list_mandate*`, `mandate_reduce`): the dominant
  name first and sized to the target by its smallest lot, the second name
  only when needed; the expiry cut only by its own need; one reading
  watches (gap breach included), two act, reads never count; a standing
  batch shrinks, grows and damps one lot; a covered position left to the
  owner and named; a protective put never sold and its deliverable shares
  kept (exercise never flips the line); the book's risk falls after every
  sendable step; a stop-held line out of the plan, a partly filled sale in
  it; the maximum loss reduces to the hedges and encodes; a glitch never
  raises the mark; an unexplained equity move holds the room until
  declared or a statement covers it, a P&L-explained one does not; the
  merge under the latch and with automatic channels; the veto clock kept
  within one lot, renewed on a larger growth and in a new session or
  episode, a veto holding the line; the next session's row a new revision; `unknown`, stale and changed-book
  verdicts hold a standing batch with its revision; `within` withdraws, a
  sent record stays; a latched brake keeps the veto; not pre-authorised
  waits for the hand; pre-authorised places one order after the veto with
  origin `daemon-preauthorised`; the prepared submit refuses a held row;
  rank order and no hold-up after a refusal; reduce-only on long and short
  lines; order-cap carry-over; a gap breach cut to its cap; absent
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
non-base currency, and, for a day, any name or currency a refused order
asked for. It holds the last 320 daily closes (at least 253 are
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
  option addition whose IV cannot be derived from its limit price, on an
  underlying with fresh history, is not judged incrementally (the standing
  verdict still gates it). All stated on
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
