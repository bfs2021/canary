# Protective stop exemption and guard

Updated: 2026-10-12 20:53 CEST
Status: implemented

Contract per `.agents/docs/daemon-cli-trading-contract.md` and
`.agents/docs/risk-policy-contract.md`.

## Decision

- **Goal:** a broker-side GTC trailing stop sized to a whole long stock
  position (bucket `trailing_stop`) can be placed. Before this change two gates
  in `validateOrderRiskAuthority` refused it: `[trading].max_notional` binds
  apparent exits, and every risk-reducing stock sell is re-read as
  `open_short` because `reqAllOpenOrders` cannot prove future manual-TWS
  activity, so it needs `allow_stock_short`. The proposal row still read
  `ready`; the owner learned at prepare/preview (`order_risk_limit`).
- **Policy owner and approval:** the owner, 2026-10-05 17:36 CEST, "exempt
  with the guard".
- **Amendment (2026-10-12 19:55 CEST):** the short re-read is gone. The
  owner: "I'm happy to lift a rule, but the system needs to work with common
  sense: if my confirmed (through API) position size is 70, one must be able
  to sell 70. Everything else is bollocks. If someone sells at the same time
  through TWS, that's unfortunate and should be restricted outside of Desk."
  A sell of at most the confirmed long position of its exact contract is
  admitted as the close or reduction it is, for a stock, ETF, option, bill or
  bond and any order type, whatever else is working on the contract, Canary's
  own full-position stop and hand orders in TWS included, and whether or not
  the open-order inventory can be read (`validateOrderRiskAuthority`;
  risk-policy.md "Order limits" item 10). "Confirmed" is the current,
  complete, account-scoped portfolio projection every order position read
  passes (`confirmedPositionProjection`); a stale or incomplete projection
  refuses in plain words. Only an order that opens or flips a short needs
  `allow_stock_short` or `allow_option_sell_to_open`. The incident: Canary
  proposed the Rulebook exit of 70 held calls with no working orders and
  refused it as a sell-to-open; a hand full close of a stock failed while
  Canary's own full-position stop was working. This supersedes the narrower
  stock-only admission under the inventory guard of 2026-10-10 07:28 CEST
  ("Admit them", `heldStockExitAdmitted`, 2f871699), which is removed. The
  notional cap exemption below is unchanged: it stays with protective stops
  and keeps its competing-sell condition, so the inventory is still read for
  a stop and for a delta-reducing exit, and the readiness blockers below
  still describe that exemption.
- **Amendment (2026-10-12 20:53 CEST, residual-risk fix):** the double-sell
  window the 19:55 admission left open beside Canary's own stop is closed
  with one-cancels-all at the broker ("One-cancels-all beside Canary's own
  stop" below). The owner's boundary stands: a hand order placed in TWS is
  the owner's problem; two orders Canary itself placed or admitted must
  never double-sell. The guard stays as the second line and now covers
  Canary-placed option trails too.
- **Amendment (2026-10-10 07:28 CEST, superseded 2026-10-12):** the short
  re-read excuse covered a sale of a held long of any order type under the
  same inventory guard (owner: "Admit them"; `heldStockExitAdmitted`).
- **Amendment (2026-10-05 20:28 CEST):** the notional cap and
  `allow_stock_short` moved to the risk constitution's `[order_limits]`
  (owner decision 2026-10-05 19:56 CEST); the cap now scales with the book.
  The exemption is unchanged and applies against the cap in force.
- **Amendment (2026-10-07 08:42 CEST):** the delta-reducing exit exemption
  (risk-policy.md, "Delta-reducing exits", owner decision 2026-10-07 08:13
  CEST) stands beside this one; an order passes the notional cap when either
  applies. This exemption is not folded into it: it reads the open-order
  inventory rather than deltas, it also excuses the short re-read, and it
  admits an over-hedged stock's stop that the delta rule would not. Its
  behaviour is unchanged.
- **Enforcement class:** pre-trade hard gate (narrowed), plus an automatic
  post-trade correction of Canary's own stops.

## Meaning

- **Exemption (`protective_exit.go`).** `protectiveStockExitExempt` holds for
  a single-contract `STK`/`ETF` `SELL` whose order type is `TRAIL`,
  `TRAIL LIMIT`, `STP` or `STP LMT`, with `position.Before > 0`,
  `quantity <= Before` and effect `close` or `reduce`, when the complete,
  current all-client open-order inventory of the order's own account and mode
  shows other working `SELL` orders for the exact contract (hand orders
  included; an order without a ConID that names the same symbol counts) whose
  remaining quantity plus this one is at most `Before`. A modify whose target
  is a working sell stop and whose new quantity is below the target's
  remaining quantity is exempt without the competing-sell condition: it can
  only shrink what is already working. Unavailable, stale, incomplete or
  foreign-scope inventory never exempts. The exempt order skips the notional
  cap (until 2026-10-12 also the short re-read, which no longer exists); FX
  evidence checks, `max_option_contracts`, option rules, bond rules and
  strategy rules are unchanged.
- **Where it is read.** Preview reads the inventory once per candidate;
  admission (`bindPreviewOrderRiskAuthority`) reads it again, so a hand sale
  entered after the preview withdraws the exemption; the first-byte guard
  reuses the admission evidence with the re-read position and issues no broker
  request. Proposal provenance is not required: the CLI preview of the same
  proposal has none.
- **Readiness.** A stock/ETF trailing-stop row the gates would refuse because
  the exemption cannot apply carries one typed blocker:
  `protective_exit_competing_sell` (not executable),
  `protective_exit_inventory_unavailable` (waiting for the broker) or
  `protective_exit_exceeds_position`. With `allow_stock_short = true` the row
  carries none; the notional cap then decides at preview from exact-session
  FX evidence the row does not have.
- **One-cancels-all beside Canary's own stop (`protective_oca.go`,
  `protective_oca_trading.go`; 2026-10-12 20:53 CEST).** Every `SELL` stop
  or trail Canary places (a stock, ETF or option; the Rulebook's
  `trailing_stop` bucket and `option-profit-lock-v1` trails included) is
  sent in its own IBKR one-cancels-all group, `canary-oca-<order ref>`,
  type 2. The IB API evidence: `Order.proto` `ocaGroup = 27` (string) and
  `ocaType = 30` (int32); the API's `Order.OCAType` is "1 =
  CANCEL_WITH_BLOCK, 2 = REDUCE_WITH_BLOCK, 3 = REDUCE_NON_BLOCK", a group
  is joined by naming it, and OCA covers STK, OPT, BOND, CASH, FUT, FOP and
  WAR (github.com/ajef/ibapi, `proto/Order.proto`, `order.go`,
  `order_sample.go` `OneCancelsAll`). With type 2 the broker reduces the
  group's other members when one fills, in whole or in part, and routes one
  member at a time ("block"), so two members never fill at once. IB's word
  for the reduction is "proportionately"; Canary relies on no reading of it
  beyond this: no member can sell what another member sold. The membership
  is decided at admission (`placeOrder`, after `bindPreviewOrderRiskAuthority`,
  under `brokerWriteMu`), stamped on the wire order and journaled
  (`oca_group`, `oca_type` on the event and the view) and reported beside
  the reviewed draft on the place result (`OrderPlaceResult.oca_group`,
  `.oca_type`; `draft` stays the reviewed draft, so a prepared proposal
  still submits exactly what was reviewed), never on the signed draft; a
  modify re-sends the view's membership unchanged; the broker's
  openOrder echo (proto field 27/30, `OrderLifecycleEvent.OcaGroup`) is the
  authoritative report of it. `planProtectiveOCA` reads Canary's own open
  sell stops on the exact contract from the journal, with the broker's
  remaining quantity and group echo where the complete inventory shows them
  (`canaryWorkingSellStops`; a row the complete inventory no longer shows is
  gone unless it moved inside the snapshot's cache window; an unavailable
  inventory leaves the journal rows standing without an echo): no stop, or
  the stops and this sale together within the confirmed position → the sale
  is plain (a stop still gets its own group); exactly one stop whose echoed
  group has type 2 → the sale joins it by name, and nothing is modified;
  otherwise → the stops are cut, in order of reference, to what the sale
  leaves of the position, cancelled when nothing is left, through the
  guard's grant, origin and write path (`protectiveOCAStep`), each change
  awaited in the journal for `protectiveOCAConfirmWait` (3 s; a cancelled or
  gone stop, or an acknowledged quantity at or below the target, confirms),
  and only then is the sale sent, plain. The fallback exists because a
  working order's OCA group cannot be changed: the API offers no operation
  for it other than a full modify re-send whose acceptance of a changed group
  is undocumented, and Canary sends no broker request it cannot verify. A
  change the gates refuse, a send with disposition may-have-written, a stop
  that filled meanwhile or no answer in time refuses the sale with the reason
  (`ErrTradingDisabled`) and leaves the stop as the broker last reported it;
  an unlinked sale is never sent beside Canary's own stop. A stop Canary did
  not place is never linked or changed: the sale goes out as it is and the
  guard reports the hand stop. Decision log: `protective_oca.link` (sent),
  `.shrink` and `.cancel` (sent or failed, on the stop's reference),
  `.refuse` (refused, on the sale's). Daemon log: one line per link, change
  or refusal.
- **Guard (`protective_stop_guard.go`).** After each proposal refresh and the
  pre-authorised and queued cycles (the proposal cadence, 30 s by default) the
  daemon plans, for each working broker order that is a stock, ETF or option
  `SELL` stop or trail and that the journal tracks as Canary-placed (reserved
  order ID, order reference, preview token, same account and mode), against
  the headroom `position − committed`, where committed is the remaining
  quantity of Canary's own working sales of the contract that are not stops
  and not in the stop's one-cancels-all group (a linked sale is left to the
  broker; a hand sale in TWS is the owner's; a Canary sale sent inside the
  inventory's cache window and not yet shown counts from the journal):
  headroom `<= 0` (or under one whole unit) → cancel; `0 < headroom <
  remaining` → shrink to `floor(headroom)` through a replacement preview and
  the ordinary modify path, keeping trail amount or percent, current stop
  price, limit offset, trigger method, TIF and outside-RTH (a mandated stop
  keeps the broker's current ratchet through the shrink; one larger than the
  position first follows the position, the next pass takes the sale off it).
  A stop Canary did not place is reported (daemon log and decision log, once
  per reason), never touched; the report names a Canary sale working beside
  it. The modify path and the replacement preview accept `OPT` orders for
  this; a modify never changes the security type.
- **Evidence and debounce.** A pass needs a concrete broker scope, a current
  portfolio projection (`classifyPortfolioStreamHealth == current`: completed
  receipt for the account, no short download, fresh) with every row in scope,
  the complete open-order inventory for that scope, and the journal. Without
  any of them the pass is paused and the settle clock is cleared. A step acts
  only after it was planned on consecutive passes at least 20 s apart, and the
  write path re-plans under `brokerWriteMu` and acts only if the same step is
  still planned.
- **Authority.** Writes use the origin `daemon-protective-guard`
  (`rpc.OrderOriginDaemonProtectiveGuard`). The write gate accepts it only
  while the guard holds a grant naming the exact order and change (cancel, or
  shrink to exactly the planned quantity below the remaining quantity).
  Trading mode, gateway, pins, storage and journal health, preview token,
  broker WhatIf and the protective modify rule (`validateProtectiveModifyQuantity`)
  all still decide.
- **Freeze.** `trading.freeze` blocks every new write including modifies and
  allows cancels (`docs/docs/reference/config.md`). The guard follows that: a
  shrink waits while frozen, a cancel goes through.
- **Audit and notice.** Each change is journaled on the order with the guard
  origin and written to `events.jsonl` as `protective_stop_guard.shrink`,
  `.cancel` or `.report` with outcome `sent`, `failed`, `blocked` or
  `reported`. The owner's notice is the existing Protection
  reconciliation-required episode for the mismatched order, which recovers
  when the change lands; the guard adds no new alert kind.

## Residual risk accepted

- While the daemon is down, a hand sale is not followed: the stop can stay
  larger than the position until Canary is back and the guard has run two
  passes.
- Between a hand sale and the guard's second pass (about 30–60 s) a triggered
  stop can still sell shares no longer held.
- **Beside Canary's own stop (closed 2026-10-12 20:53 CEST).** Until this
  fix a full close sent through Canary beside its own full-position stop
  left both working until one filled: the close first, and the stop could
  trigger for the 30–60 s before the guard's cancel and sell the position
  again, short; the stop first, and the close worked on, unguarded, for as
  long as its limit was reachable; an option profit-lock trail was not
  guarded at all. Now the sale joins the stop's one-cancels-all group, or
  the stop is cut first and the sale refused when that fails, and the guard
  counts an unlinked Canary sale against its stops and covers option trails
  (`TestCloseJoinsCanarysOwnStopOCAGroup`,
  `TestCloseShrinksAnUnlinkedStopBeforeItIsSent`,
  `TestCloseIsRefusedWhenTheStopCannotBeChanged`). What remains: a stop
  placed before this release has no group, so its first sale through Canary
  shrinks it before the sale is sent, lowering protection while the sale
  works unfilled; after a linked partial fill the broker's "proportionate"
  reduction may leave the stop smaller than the rest of the position, which
  protects less, never sells more, and a mandated stop grows back to the
  position on the guard's next passes; a sale whose change to the stop is
  sent but unconfirmed within 3 s is refused while the stop stays at its
  new, smaller size. Whether a second fill would actually open a short is
  the broker's: a margin account sells a shortable stock short, a naked
  option sale needs the option permission level.
- A hand sale in TWS beside Canary's stop, or beside a Canary sale, is the
  owner's (decision 2026-10-12 19:55 CEST): nothing links it, and the two
  can sell the line twice until the guard's next passes.
- A shrink needs a submit-eligible replacement preview (a quote and an
  accepted broker WhatIf); while it cannot get one
  the guard logs the failure and retries every pass. A freeze holds a shrink
  for as long as it is set.

## Verification

- `internal/daemon/protective_exit_test.go`: exemption boundary table (stop vs
  limit, long vs flat/short, quantity above position, competing sell, stale
  inventory, ETF, option unaffected, shrink of a working stop), inventory
  counting, and trailing-stop row readiness; `TestHeldLongExitAdmission`
  (70 of 70 held calls with no working orders, beside a full-position stop,
  and without the inventory; 71 of 70 and a short open refused; a stock
  full close beside Canary's stop; bond and bill sales of the held face
  beside a working sale and without the inventory; a sale above the face
  refused; the cap still binds) and `TestHeldLongExitNeedsAConfirmedPosition`
  (a stale receipt, an incomplete or short download and another account's
  rows refuse in plain words).
- `internal/daemon/order_preview_bond_request_test.go`: a bond sale of the
  held face is admitted beside working sales and without the open-order
  list; above the face it is refused as a short.
- `internal/daemon/protective_stop_guard_trading_test.go` (trading build):
  shrink, cancel at zero (also while frozen), cancel on the first settle
  after a full close fills, shrink held by the freeze, kill switch, stale
  projection and settle restart, hand order untouched, origin grant.
- `internal/daemon/protective_oca_test.go`: the planner table (no stop; a
  stop born in its own group; an echoed group joined; a sale that fits is
  plain; a stop without a group cancelled before a full close and shrunk
  before a reduction; the broker's remaining counts; a group of another type
  not joined; no inventory shrinks from the journal and never joins; a stop
  the complete inventory no longer shows is gone; one sent inside the cache
  window counts; two stops cut in order; a new stop beside a legacy stop; a
  buy never linked) and `TestOCAMembershipSurvivesJournalReplay` (the
  membership survives status, execution and legacy openOrder events; the
  broker's echo reaches the journal; a hand order's group is reported).
- `internal/daemon/protective_oca_trading_test.go` (trading build, through
  preview, admission and the fake broker, with a model of an IBKR type-2
  group): close fills first, stop fills first, partial fills reduce the
  other; a legacy stop is cancelled or shrunk, confirmed, then the sale is
  sent; a sale that fits needs no change; a refused, uncertain or unanswered
  change refuses the sale and leaves the stop; no stop and a hand stop send
  the sale plain and the hand stop is reported; an option close joins the
  profit trail; a new stop is born in its own group; the guard shrinks a
  stop for an unlinked Canary sale and leaves it for a linked one; the guard
  shrinks and cancels an option trail.
- `pkg/ibkr/place_order_oca_test.go`: the membership round-trips the proto
  wire (fields 27 and 30) and half a membership is refused.
