# Protective stop exemption and guard

Updated: 2026-10-12 20:40 CEST
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
- **Guard (`protective_stop_guard.go`).** After each proposal refresh and the
  pre-authorised and queued cycles (the proposal cadence, 30 s by default) the
  daemon plans, for each working broker order that is a stock `SELL` stop and
  that the journal tracks as Canary-placed (reserved order ID, order
  reference, preview token, same account and mode): position `<= 0` (or under
  one whole share) → cancel; `0 < position < remaining` → shrink to
  `floor(position)` through a replacement preview and the ordinary modify
  path, keeping trail amount or percent, current stop price, limit offset,
  trigger method, TIF and outside-RTH. A stop Canary did not place is
  reported (daemon log and decision log, once per reason), never touched.
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
- **Double sell beside Canary's own stop (owner decision 2026-10-12 19:55
  CEST).** A full close of a stock sent through Canary while its own
  full-position stop is working is admitted, and nothing shrinks or cancels
  that stop when the close is sent; both work at the broker until one fills.
  If the close fills first, the stop is cancelled by the guard on the first
  settle after the projection reads flat: the position change reaches the
  projection on the broker's portfolio stream, the next pass (30 s cadence)
  plans the cancel, and the pass at least 20 s later sends it, so the stop
  can still trigger for about 30–60 s after the fill and sell the whole
  position again, short (`TestProtectiveStopGuardCancelsOwnStopAfterAFullCloseFills`).
  If the stop triggers first, the close is a working limit order the guard
  never touches (it manages only stops it placed), so it can fill whenever
  the price returns to its limit, for as long as it works; only the owner
  cancels it, and the Protection reconciliation episode names the mismatch.
  A Canary-placed option profit-lock trail (`option-profit-lock-v1`) is not
  guarded at all, so beside an option full close both orderings are open for
  as long as the other order works. Whether the second fill actually opens a
  short is the broker's: a margin account sells a shortable stock short, a
  naked option sale needs the option permission level. A pre-emptive shrink
  or cancel of Canary's own stop when a full close is sent would close the
  stock windows; it is not built, pending the owner's decision.
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
