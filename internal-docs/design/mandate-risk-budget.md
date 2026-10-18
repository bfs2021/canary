# Mandate risk budget (ex-ante volatility budget and the additions gate)

Updated: 2026-10-18 16:35 CEST
Status: increment 1 implemented 2026-10-14 (measurement, verdict, additions
gate); increment 2 implemented 2026-10-14 (forced reduction, below);
increment 3 implemented 2026-10-18 (allocations, below). The
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
    measurement and the additions gate of increment 1. The order of the
    cuts was replaced on 2026-10-18 08:06 CEST (allocations, below): the sleeve
    furthest over its allocation first, within it the largest contributor.
  - Sleeve volatility shares of the 15%: owner 7, core 6, overlay 4. Recorded
    here as `[mandate.sleeves]`; enforcement per sleeve comes later. Since
    2026-10-18 the machine's `[mandate.sleeves]` numbers size its allocations
    ("Risk allocations" below); the owner's allocation is the rest of the
    budget, and `owner_vol_pct` is recorded and not read.
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
- Status: implemented (increments 1, 2 and 3, allocations). Active only with
  `[mandate]` written; the forced reduction is automatic only with
  `mandate_reduction` pre-authorised.

## Meaning

- Capital or exposure base: the **risk base** = account equity (NLV in the
  base currency) − `mandate.tax_reserve_base`. The **budget base** is the
  high-water mark of the cash-flow-adjusted risk base since
  `mandate.started_at`, translated into today's capital terms: `budget_base =
  HWM(equity − flows_since_start) − tax_reserve + flows_since_start`. It rises
  with every new high, once a second reading at least five minutes later
  confirms it (at the lower of the two), and moves one-for-one with declared
  or statement flows after the start, so a withdrawal lowers the budget and a
  deposit raises it, while neither creates a drawdown or a false high. The tax
  reserve shifts both the high-water mark and the current risk base by the
  same amount, so changing it never changes the drawdown, only the base.
  Changing `started_at` starts the series again (a fresh high-water mark
  seeded from the next observation).
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
- Broad index gap (owner decision D4, 2026-10-14, recorded in
  `core-sleeve.md`: "12% for broad index funds"): an underlying whose
  every leg is on the compiled list `mandate.BroadIndexInstruments` (SPY,
  SPX, SPXW, XSP, QQQ, NDX, IWM, RUT, DIA in USD; IBCF, SXR8, CSPX, VUSA,
  VUAA, EUNL, IWDA, SWDA, VWCE, VWRL, VGWL by ISIN-named listing; matched on
  symbol and currency together) is stressed by `index_gap_shock_pct`
  instead of `gap_shock_pct`; single names keep 25. The key is optional:
  absent, every underlying takes `gap_shock_pct`, and a file without it
  keeps its fingerprint. The forced reduction's gap cut and the additions
  gate read the same per-underlying gap.
- Returns proxy (core sleeve design review S5, 2026-10-14): IBCF in EUR,
  the S&P 500 hedged to EUR at fund level, moves in the simulation with
  SPY's local-currency daily returns, with no currency leg in a EUR book.
  Its own Xetra close (17:30 CET) is four and a half hours before the US
  close every other US line is measured at, and asynchronous closes
  understate its correlation with them. The book therefore reads no IBCF
  series, so a missing or stale one can never make the verdict unknown;
  an IBCF order the book does not hold is priced at its limit, and one
  without a limit is refused. The list is compiled
  (`mandate.ReturnsProxies`).
- Threshold or rule, including units: `max_loss_pct` (D, percent of the
  budget base), `vol_target_pct` (percent a year of the budget base),
  `tax_reserve_base` (base currency), `started_at` (date, YYYY-MM-DD),
  `gap_shock_pct` (percent; the proposal is 25), `gap_room_pct` (percent of
  the remaining loss room one name may lose in a gap; the proposal is
  33.3), `[mandate.sleeves]` `owner_vol_pct`, `core_vol_pct`,
  `overlay_vol_pct` (percent a year; the machine's two size its allocations, the
owner's is recorded and not read: "Risk allocations" below). The
  proposals are written into the file by the owner; the code carries no
  default that acts. A `[mandate]` table missing a material key is active
  and unknown: the verdict names the unapproved keys and additions are
  refused until the owner writes them (data absence never renders ok).
- Verdict: `within` (risk ≤ budget and no gap breach), `over` (risk >
  budget), `reduce` (risk > 1.25 × budget, or any gap-cap breach),
  `unknown` (anything the measurement lacks). The result carries
  `status_since`, when the status last changed as the daemon has seen it
  across evaluations and restarts (a `mandate_status` state document; each
  change journaled as `mandate_status_changed` with how long the old status
  held), because a machine sleeve cannot trade while the verdict is unknown
  and the owner asked how often and how long it is (2026-10-18);
  `canary mandate` prints it as "Since".
- Enforcement class: pre-trade hard gate for additions (`mandate_budget`
  blocker), at the same four enforcement points as the drawdown brake (order
  preview, strategy preview, admission, the final wire guard), with the
  brake's risk-adding classification: a close or reduce passes, a BUY of a put
  on a Rulebook hedge symbol passes, cancels never reach it. One exemption is
  the mandate's alone: a BUY of a bill (security type `BILL`, as IBKR names
  the line) passes whatever the verdict reads, as a held bill is measured as
  carrying no equity or volatility risk (2026-10-17 06:47 CEST, owner: "If
  cash like investments do not count against it, I'm fine with 15/30"). A bond
  (`BOND`, including a bill IBKR lists as a bond) stays refused, because its
  interest-rate risk is not measured and a long bond could carry real risk
  counted as zero; a bond fund (`STK`) is measured as before; the drawdown
  brake keeps refusing a bill purchase while engaged. The exemption is not
  widened further. Allocations (2026-10-18, "Risk allocations" below): an
  addition passes only when its own sleeve is inside its allocation and stays
  there with the order added, on top of every whole-book check here. Release
  is automatic: the moment a fresh verdict reads `within`, additions pass;
  there is no latch and no reset. The incremental pre-trade check re-evaluates
  the book with the proposed leg added and refuses an addition that would take
  the annualised risk over the budget even while the standing verdict reads
  `within`, and one that would create a gap-cap breach or deepen one
  (2026-10-14, the core sleeve's design review B1: an addition the gap caps
  refuse would otherwise be admitted and then sold back by the forced
  reduction). An option order the evaluator cannot price (no limit, a limit
  below intrinsic value, an expiry reached, an implied volatility that does
  not solve) fails closed (2026-10-14, the same review): a sale is refused, as
  a short option's loss has no bound without a volatility; a buy is judged by
  a volatility-free bound, its premium at the limit added to the underlying's
  gap loss, and √252 × units × (the largest rate × the root mean square of the
  underlying's daily moves × spot, plus the call's spot or the put's strike ×
  the rate's moves) added to the book's risk (a vanilla option's price moves
  at most as much as its underlying, and the decay shifts every scenario
  alike), and refused when either does not fit or without a limit. A held
  contract with its IV is priced as before. An addition on a name or in a
  currency the book does not hold is measured on the series the history store
  keeps for it (spot from its last close when the book holds none); while none
  is fresh, the order is refused (`mandate_budget`: "no daily history for X
  yet; the desk is fetching it", or the base currency's exchange rate against
  the order's) and the series is requested on the background lane under the
  same budget, so a retry after the fetch is measured. A book that cannot be
  measured with the order added refuses it too; nothing passes unmeasured
  (2026-10-14 16:57 CEST, from the core-sleeve design review).
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
  The verdict is judged at a clock read once the capital state is in hand,
  so an equity observation that lands while the evaluation starts (its own
  account read, or another the store was persisting) is current, never one
  from the future (2026-10-16: scheduled verdicts read unknown while
  reads measured).

## Forced reduction

Owner decisions, recorded verbatim:

- 2026-10-14 06:26 CEST, decision 3 "Protection that acts": "on a budget
  breach, one reduce-only batch sized back to budget, largest risk
  contributor first, pre-authorised with the 30-minute veto. The brake
  releases when risk is back in budget, not when P&L recovers." Owner: "yes
  to all".
- 2026-10-14 06:26 CEST: "The system should run by itself within risk
  boundaries."
- 2026-10-18 08:06 CEST, replacing "largest risk contributor first" in
  decision 3: "Accept all three: give the machine its own seat (replacing D1
  and largest-contributor-first), put s1 into its six-month shadow test, and
  go for the core venue fix with the core fund out of protection's automatic
  sales." The recommendation accepted (mandate budget review, 2026-10-17,
  decision 1): "When the book is over budget, the sleeve furthest over its
  seat is cut first." The 30-minute veto and every other rule here are
  unchanged ("Risk allocations" below).
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
- **Sizing, afresh at every verdict.** Greedy, with the daemon's own evaluator
  on the book the verdict measured; a standing batch is never carried forward
  as it was. Cut by cut, the book re-evaluated after each: a name over its gap
  cap has its largest gap loser cut until the name's gap loss is under the cap
  (a gap cut never leaves the book riskier); with no breach left, the sleeve
  furthest over its allocation is cut first and, within it, its largest Euler
  contributor (2026-10-18, "Risk allocations" below), by the smallest whole
  step that reaches the target or, while another sleeve is over its own
  allocation, brings that sleeve to its allocation, whichever comes first, or,
  when no step on that line does, by the step that takes the most risk off;
  the next cut is chosen afresh on the book re-evaluated, and follows only
  when needed. Steps are contracts for options and share lots for stocks (1
  below 100 shares, 10 to 999, 100 from 1,000). Damping: a line whose position
  has not moved keeps the last generation's cut when the fresh cut is smaller
  by at most one step, so a steady risk does not re-size a row every verdict;
  a larger ease shrinks it, a rise grows it.
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
  P&L over the same span: the difference of the two daily figures within
  one broker day, the new day's own figure across the broker's daily reset
  (the old day's last reading taken as its close); zero where the broker
  reports none, so without P&L the whole move counts. The reset is read
  from the figures themselves and from the session date, never from the
  calendar alone: IBKR starts its day at its own hour, before the New York
  midnight the session date turns at, so a reading whose daily figure sits
  within a quarter of the flow threshold of zero while the equity moved by
  no more than that figure is the first of a new broker day, and so is a
  reading in a new session date (2026-10-18 after the 2026-10-16 incident:
  a reading at 23:59:59 New York still carried the trading day's session
  while the broker's figure was already the new day's zero, and the
  difference charged a whole down day to a flow; every machine sleeve was
  refused from Friday night on, and Saturday's statement did not clear it,
  below). More than max(1% of equity, 1,000 in base currency) is a
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
  covering its day has been reconciled: its day is the New York date of
  the reading that raised it, the date IBKR's statements run by, so a
  reading after UTC midnight but before the New York date turns belongs
  to the trading day still open and that day's statement covers it (the
  first build compared UTC dates, and Friday's statement, fetched Saturday
  morning, never covered a suspicion raised at 23:59 New York Friday).
  How the owner clears it: declare
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
  day. Two more holds are accepted the same way: a reading taken long
  before the broker's day closed leaves that day's later P&L untracked, so
  the next day's first reading charges it to a flow (the broker's
  previous-day equity is not read; the statement clears it); and a flow
  landing in the very reading that carries the broker's reset is caught
  but measured against the within-day step, so a declaration alone does
  not clear it before the statement does. One unit of an order may exceed
  a cap, judged by the order gate as for the governor. After any fill the
  batch waits for the next verdict (at most two minutes).
- **Verification** (synthetic books;
  `internal/daemon/mandate_reduction_test.go`,
  `mandate_reduction_review_test.go`, `mandate_reduction_automatic_test.go`,
  `mandate_reduction_trading_test.go`, `scorecard_authority_test.go`,
  `internal/mandate/mandate_test.go`,
  `internal/app/alerts/presentation_test.go`, CLI goldens
  `proposals_list_mandate*`, `mandate_reduce`): the dominant name first and
  sized to the target by its smallest lot, the second name only when needed;
  the expiry cut only by its own need; one reading watches (gap breach
  included), two act, reads never count; a standing batch shrinks, grows and
  damps one lot; a covered position left to the owner and named; a protective
  put never sold and its deliverable shares kept (exercise never flips the
  line); the book's risk falls after every sendable step; a stop-held line out
  of the plan, a partly filled sale in it; the maximum loss reduces to the
  hedges and encodes; a glitch never raises the mark; an unexplained equity
  move holds the room until declared or a statement covers it, a P&L-explained
  one does not; the broker's daily reset before the New York date turns is
  no flow, a flow in that reading is, and the statement for the New York
  date covers a suspicion raised after UTC midnight
  (`TestMandateFlowCheckSurvivesTheBrokerDailyPnLReset`, `mandate_gate_test.go`);
  the merge under the latch and with automatic channels; the
  veto clock kept within one lot, renewed on a larger growth and in a new
  session or episode, a veto holding the line; the next session's row a new
  revision; `unknown`, stale and changed-book verdicts hold a standing batch
  with its revision; `within` withdraws, a sent record stays; a latched brake
  keeps the veto; not pre-authorised waits for the hand; pre-authorised places
  one order after the veto with origin `daemon-preauthorised`; the prepared
  submit refuses a held row; rank order and no hold-up after a refusal;
  reduce-only on long and short lines; order-cap carry-over; a gap breach cut
  to its cap; absent `[mandate]` changes nothing.

## Risk allocations

Owner decision, verbatim, 2026-10-18 08:06 CEST: "Accept all three: give
the machine its own seat (replacing D1 and largest-contributor-first), put
s1 into its six-month shadow test, and go for the core venue fix with the
core fund out of protection's automatic sales." The first clause accepts
decision 1 of the mandate budget review of 2026-10-17: "seats inside the
whole-book limits. The machine's seat is only as big as what it actually
runs (each strategy's stage times its share; nothing today), and you have
the rest. When the book is over budget, the sleeve furthest over its seat is
cut first. Each position keeps the seat of whoever opened it. The 30%
maximum loss, the budget and the gates still cover everything. This replaces
D1 and "largest risk contributor first"."

It replaces two records: D1 "No, whole book only" (`core-sleeve.md` §7),
under which the machine had only the room the owner's book left and could
never count on any; and "largest risk contributor first" in decision 3 of
2026-10-14 06:26 CEST (Forced reduction, above). No strategy runs in the
overlay yet.

Terms (owner, 2026-10-18 08:50 CEST: "on terms, always use the ones that
professionals use, risk budget or risk allocation, not seats or share."):
the review and the decision say "seat"; this record says **risk budget** for
the whole book's volatility budget, **sleeve** for each trader's part of the
book and **risk allocation** for a sleeve's part of the risk budget, as the
owner's strategy-engine map does (Desk `docs/strategy-engine.md`).

### Sleeves

- **Owner**: the owner's own trading, by hand, in TWS or the console, and
  anything the owner's own Rulebook rules open (the owner wrote the rule).
- **Core**: the core rule (`core-ibcf-v1`, the lab's s0), `core-sleeve.md`.
- **Overlay**: the risk allocation for a lab strategy on index options once
  one passes its gates; none runs in it yet.

A machine sleeve is a lab strategy Canary runs at a live stage. Rule
buckets that only reduce (protection, hygiene, the budget governor, the
forced reduction, expiry closes) are no sleeve: they place no units, they
take them away.

### How an allocation is measured

- **Use**: a sleeve's use is the sum of its legs' Euler contributions,
  `contribution_pct / 100 × sd_annual_base`, in base currency a year, the
  budget's own unit. The contributions add up to the book's risk exactly, so
  the uses do too; a hedge's negative contribution lowers its sleeve's use,
  and an addition that diversifies the book can have a negative use.
- **One contract, two sleeves**: the Euler contribution of a contract is
  linear in its units (q × ∂σ/∂q), so a contract held by two sleeves splits
  its contribution exactly in proportion to the units each holds.
- **Allocation**: a machine sleeve's allocation is reserved only as far as it
  runs: its stage times its allocation, in percent a year of the budget base,
  scaled by the room exactly as the budget is: `allocation_base = stage_pct /
  100 × target_pct / 100 × budget_base × room`. The core's stage is
  `[mandate.core] scale_pct` and its allocation `[mandate.sleeves]
  core_vol_pct`; a lab strategy's stage is the lab's ladder (R3 25, R4 50,
  established 100) and the overlay's allocation `overlay_vol_pct`. A sleeve
  that sends no orders runs nothing: its stage is 0 whatever the policy says.
  In this build the core sends none (phase 1 is read-only; its rows are phase
  2) and no strategy runs in the overlay, so both stages are 0 and the
  machine's allocation is zero, as the review said: "nothing today".
- **The owner's allocation is the rest**: `budget − Σ machine allocations`.
  The allocations add up to the budget, so the book is inside the budget
  exactly when the uses do not exceed the allocations in sum, and a book over
  budget always has a sleeve over its allocation. Today the owner's allocation
  is the whole budget. The constitution refuses machine allocations
  (`core_vol_pct + overlay_vol_pct`) above `vol_target_pct`, so the owner's
  allocation is never negative; the evaluator also floors it at zero.
- **The owner's 7 is no longer read**: the numbers of 2026-10-14 (owner 7,
  core 6, overlay 4) were standalone figures whose sum (17) exceeded the
  target because the sleeves are not perfectly correlated. Allocations count
  use, which adds up, so the owner's allocation is the rest: 15 − 6 − 4 = 5
  points once both machine sleeves run at full stage, not 7. `owner_vol_pct`
  is recorded and not read.
- **Use and standalone risk**: a sleeve's use is its standalone risk times its
  correlation with the book, so it never exceeds its standalone risk; others'
  holdings can raise it at most to that. A sleeve whose standalone risk fits
  its allocation is therefore inside its allocation whatever the others hold.
  The core sizes its volatility to its allocation on its own closes, the mandate
  measures it on the index's returns, and every core buy is cut to its
  allocation on the mandate's measure (`coreHeadroomFits`); between decisions
  a smaller room shrinks the allocation and the core's standalone risk does
  not follow, so correlated additions elsewhere can lift the core's use past
  its allocation (residual risk, below). The verdict serves each sleeve's
  standalone risk beside its use.
- **A flat sleeve keeps its allocation**: the allocation is a reservation by
  stage, not a holding. The core out of the market (trend off) keeps its
  allocation empty. The owner's room shrinks the day a machine stage rises:
  the core at 25 takes 1.5 of the 15 points, a tenth of the budget.

### Placement

Each position stays in the sleeve of whoever opened it, fixed for its life.

- **The core's contract is reserved to the core**: every unit, whoever bought
  it (`core-sleeve.md`, "Adopting an existing IBCF holding"; the build brief
  of 2026-10-18: "The core fund stays reserved to the core"). The core's
  contract is the line Canary resolves for the ISIN and the one the core last
  used (`mandateCoreContracts`, the venue fix of 2026-10-18), and a written
  `[mandate.core] con_id`. An order on it, the owner's hand included, is the
  core's addition, judged against the core's allocation. While the core runs
  nothing that allocation is zero, so a hand buy of the fund is refused once
  the core's use would be above zero; a sale passes. While no contract is
  resolved nothing is reserved: the core then runs nothing, so it has no
  allocation to keep, and a holding is adopted once its contract is known.
- **Orders are placed by contract id**, which the order path resolves before
  every gate point (`resolvePreviewOrderContract`); a second listing of the
  fund under another contract id is the owner's, and the core's target holds
  while one is held.
- **Everything else is the owner's**, until a machine sleeve's own order
  path places units by its fills. In this build no path places units in a
  machine sleeve, so placement is total: the core's contract or the owner.
- **Split by fill lineage** (the rule the first machine order path that can
  trade a contract the owner may also hold must journal; nothing here is
  built before that path):
  1. An opening fill (it raises the position's size in its direction)
     places its units in the sleeve of the order's origin. The core's
     reserved contract is the core's whatever the origin.
  2. A closing fill of a sleeve's own order takes that sleeve's units. A
     forced-reduction fill takes units of the sleeve its row was sized for
     (the row names it, `mandate.sleeve`), and the row's cut is capped at
     that sleeve's units on the contract. A fill of a row the same-contract
     merge built from several buckets takes units in the order of the needs
     merged, the mandate's first. A fill acting on the whole line
     (protection, hygiene, an expiry, an exercise or assignment, another
     broker lifecycle event) takes every sleeve's units in proportion. The
     owner's hand sale takes the owner's units first, then the machine's in
     proportion.
  3. A position that crosses zero starts over: the new side belongs to the
     origin of the fill that crossed.
  4. The placed units of a contract always sum to the broker's position. A
     machine sleeve placed more units than the line holds, or a fill the
     journal cannot match while a machine order on the contract works or is
     of unknown outcome, makes that contract's placement unknown, until the
     order journal's reconciliation resolves the order or a statement
     covering the day accounts for the units.
  5. Placement is never edited: no tag and no re-placement. A position that
     must move does so by a recorded owner instruction.
  Before any machine order path exists, two gate gaps close: a combination
  (BAG) order passes the mandate gate unmeasured while the verdict reads
  within (`mandateUnpricedOrderGate`, an unmodelled security type), and a
  BUY of a put on a Rulebook hedge symbol counts as a hedge whoever sends
  it; a machine sleeve's order gets neither pass.
- **Unknown placement never guesses**: while any contract's placement is
  unknown the allocations are unknown and the allocation check refuses every
  risk-adding order (the uses add up, so one unknown contract leaves every
  sleeve's use unknown). The forced reduction does not hold for it: it sizes
  in the whole book's order (largest contributor first) and says so, because a
  protection that acts must not wait on bookkeeping. With placement total in
  this build, the allocations are unknown only when the verdict is.

### The gate

Every whole-book rule above is unchanged and binds first, at the same four
points: the standing verdict must read `within` ("No addition by anyone while
the whole book is over budget"), the book with the order must stay inside the
budget, and the order must create or deepen no gap-cap breach; the bill and
hedge exemptions stand. Then the allocation: the order is placed (the core's
contract the core's, anything else the owner's), and a risk-adding order is
refused (`mandate_budget`) while its sleeve's use is over its allocation, or
when its sleeve's use with the order added would be. The owner's room is the
owner's allocation less the owner's use: the budget, less the machine's
allocations, less what the owner's book uses.

- The check judges the order's own sleeve. By the bound above, an order can
  raise another sleeve's use at most to that sleeve's standalone risk.
- A sleeve over its allocation adds nothing, even an order that would lower its
  use, as the book over budget adds nothing.
- An option buy the evaluator cannot price is judged by its volatility-free
  bound (Enforcement class, above): its sleeve's standalone risk plus that
  bound must fit the allocation (use ≤ standalone, and the standalone risk grows
  by at most the bound).
- The gate view (`gate.refuses_additions`) reads the owner's additions: it
  refuses while the verdict is not within, and also while the owner's allocation
  is over.
- `canary core` reads "no buy: the core sends no orders yet, so it has no
  allocation" while the core runs nothing; once it runs, its buys are cut to its
  allocation as to the budget and the gap caps.

### The forced reduction

The trigger, the episode, the target (0.95 × budget), the gap phase, the
expiry gate, covered and protected lines, the veto clock and every other
rule are unchanged. Only the risk phase changes:

- On the book re-evaluated after each cut, the sleeve furthest over its
  allocation (use less allocation, in base currency; ties in the order owner,
  core, overlay) is cut, and within it its largest Euler contributor. A sleeve
  with nothing left to reduce passes the turn to the next one.
- The cut is the smallest whole step that takes the book to the target or,
  while another sleeve is over its own allocation, that takes this sleeve
  inside its allocation, whichever comes first; when neither is reachable on
  that line, the step that takes the most risk off. It is never more than the
  old rule would cut on that line, so every sendable order still lowers the
  book's risk. Once no other sleeve is over its allocation, the sleeve chosen
  carries the rest down to the target, the margin included.
- So the owner's book is cut for the owner's excess and the machine's for
  the machine's: while the machine's sleeves are inside their allocations, the
  batch cuts only the owner's lines; a machine sleeve over its allocation is cut
  before the owner's lines once it is the furthest over. With the machine
  holding nothing, as today, the batch is the old one.
- The allocations' order never stops short where the old order reaches the
  target: if it does, the batch is sized largest contributor first and says
  so ("the allocations' order stops short of the target").
- Each row names its sleeve (`mandate.sleeve`) and why it was chosen ("the
  largest risk contributor left in your sleeve, the sleeve furthest over its
  allocation").
- A core holding while the core runs nothing (allocation zero) is over its
  allocation by its whole use and is cut once it is the furthest over. The
  core's fund leaves protection's automatic sales (the owner's third clause of
  2026-10-18 08:06 CEST, built separately); the mandate's forced reduction
  still covers it, as the review recommended.

### Policy keys

The stage lives in the strategy register (owner decision 2026-10-18 09:01
CEST; [strategy-register.md](strategy-register.md)): `[mandate.core]` is its
first entry and every later machine strategy a `[mandate.strategies.<id>]`
table naming its compiled rule, the lab spec hash it implements, its sleeve,
its risk allocation, its stage (shadow, live or suspended) and the scale it
sizes at. A machine sleeve reserves its allocation times its scale only while
its entry is live and its rule sends orders; shadow, suspended and an entry
that holds reserve nothing, except that a live entry on hold keeps its
reservation while its sleeve holds positions. The core's allocation stays
`[mandate.sleeves] core_vol_pct` and the overlay's `overlay_vol_pct`; the
owner's allocation is the rest. No key acts by default.

### Surfaces

`canary mandate` (and `--json`, MCP `canary_mandate`) lists each allocation:
its use against its allocation, its standalone risk, and its stage and
allocation or why it runs nothing; the verdict carries them as
`verdict.allocations`. Desk shows each allocation's use against its allocation
on Risk › Mandate. An allocation refusal names the sleeve, its use, its
allocation and, for the owner, the machine's allocations.

### Safety review (2026-10-18) and how it is folded in

An independent review of this design and its first build found no blocker
and four serious items.

| # | Finding | Resolution |
|---|---|---|
| S1 | The core's reservation is not in the owner's quoted words; with the core's allocation at zero a hand buy of the fund is refused and a held fund line is cut first in a breach | Kept: the build brief carries it ("The core fund stays reserved to the core"), and `core-sleeve.md` has reserved the instrument since 2026-10-14. Stated plainly in Placement and The gate as a consequence the owner sees; the core holds no fund today |
| S2 | Allocations key on `con_id` and the compiled symbol; the venue fix makes `con_id` optional and resolves the line by ISIN | Placement by contract id only, through one function (`mandateCoreContracts`) that takes the venue fix's resolved line when both land; the symbol rule is dropped, since the order path resolves a contract id before every gate point |
| S3 | "Allocations unknown ⇒ the batch holds" would stop a pre-authorised protection on bookkeeping | Unknown allocations refuse additions only; the batch sizes in the whole book's order and says so; rule 4 gets its recovery (journal reconciliation or a statement) |
| S4 | "A sleeve inside its allocation stays there" holds only while its standalone risk on the mandate's measure fits its allocation | Restated as use ≤ standalone; every core buy cut to its allocation on the mandate's measure; the drift between decisions recorded as residual risk |
| M1-M8 | the overlay without a strategy; the owner's 7 no longer read; the gate's treatment of a sleeve already over; a guard against stopping short; overfull machine allocations; lineage and gate gaps for later; stale lines; `canary core` copy | Each folded in above, with a test where it acts (Verification) |

### Residual risk accepted

A sleeve's use moves with the rest of the book by correlation, bounded by its
standalone risk; the core's allocation shrinks with the room between monthly
decisions while its holding does not, so a correlated addition elsewhere can
take the core over its allocation, and the next breach then cuts the core
first. Allocations inherit every limit of the measurement. A machine
allocation that is empty stays reserved, so the owner's room is smaller than
the budget while a machine stage is live. The split by lineage is specified,
not built: it lands with the first machine order path that can share a
contract with the owner.

### Verification

Synthetic books only. `internal/mandate` (`allocations_test.go`): uses add up
to the book's risk and allocations to the budget, the core's allocation is
reserved only as far as it runs: its stage times its allocation and the
owner's the rest, a contract split by units splits its contribution in
proportion, every allocation is zero at room 0, an overfull machine leaves the
owner a zero allocation, a sleeve whose standalone risk fits its allocation
stays inside it whatever is added elsewhere. `internal/daemon`
(`mandate_allocations_test.go`): the allocation plan gives the core no stage
while it sends no orders and places its contract with it; with a live core
allocation the gate refuses an owner addition past the owner's allocation that
the whole budget would take, and passes one inside; with the core running
nothing the same order passes; a hand buy of the core's contract is the core's
and refused while its allocation is zero, a sale passes; an unpriced option
buy is judged by its sleeve's standalone risk plus the bound; the forced
reduction cuts the sleeve furthest over first, leaves a sleeve inside its
allocation alone, stops a sleeve's cut at its allocation while another is
over, never stops short where the old order reaches the target, every row
alone lowers the risk, and with the machine empty the batch is the old one;
the constitution refuses machine allocations above the target. Core: the
headroom-cut buy passes the gate with the allocation
(`TestCoreTargetBuyCutToTheGapCapMargin`). CLI golden `mandate_allocations`.

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
| Allocations and placement | the verdict (`verdict.allocations`), from `[mandate.sleeves]`, `[mandate.core] scale_pct` and the core's contract, and this build's order paths | `mandate.AllocationReport` | with the verdict | unknown ⇒ additions refused; the batch sizes in the whole book's order |

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
- Cross-surface parity checks: `canary mandate` golden render (room 0:
  `mandate_no_room`); the MCP tool `canary_mandate`;
  `TestOnlyTheDaemonEvaluates` lists `mandate.Evaluate`. A room-0 verdict
  with risk crosses the daemon's `mandate.risk` writer with the ratio null
  (`TestMandateRoomZeroVerdictCrossesTheWire`, added 2026-10-15 07:41 CEST).
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
internal/daemon/mandate_status.go      when the verdict's status last changed (status_since)
internal/daemon/mandate_reduction.go   forced reduction: sizing, hold, release, rows
internal/daemon/mandate_allocations.go       allocation plan and placement, the allocation check
internal/cli/mandate.go                canary mandate [--json]
internal/mcp/tools.go                  canary_mandate
```
