# TWS order control

Updated: 2026-10-11 20:30 CEST (owner decision "TWS B", 2026-10-11 19:36 CEST)

Canary's connection to TWS is pinned to its own client id. TWS lets only a
connection with client id 0 bind, and so change, the orders a human entered in
TWS by hand. Until this design, `canary orders open` listed those orders under
`untracked` and nothing in Canary could touch them.

Option B, chosen by the owner: a separate client-0 connection, opened only for
an owner-confirmed cancel or change of one such order. It binds the order,
cancels or changes it, waits for TWS's answer and disconnects. The main
connection, its client id, its order-id namespace and its paths stay as they
are. Side effect, by TWS design: once bound, the order is an API order for the
rest of the TWS session.

## Ownership

| Layer | Owner | What it decides |
| --- | --- | --- |
| Wire | `pkg/ibkr/tws_control.go`, `RunTWSOrderControl` | The client-0 session: connect, bind, identify, act, wait, disconnect; the terms check on the bound order; the outcome |
| Daemon | `internal/daemon/tws_order_control.go` | The switch and its paper proof, the eligibility of untracked rows, the ordinary broker-write gates, the inventory identity check, the request journal, the receipt |
| Contract | `internal/rpc/tws_order_control.go` | `order.tws_cancel`, `order.tws_modify`, params, terms, outcomes, blocker codes; `OrdersOpenResult.UntrackedControl` |
| Settings | `trading.tws_order_control.enabled`, `.paper_proven_at` | Registry key, document version 5 ([platform-settings.md](platform-settings.md)) |
| CLI | `internal/cli/order_tws.go` | `canary order tws-cancel`, `tws-modify`; `orders open` shows what the path may do |
| Docs | `docs/docs/operate/orders.md` | The user-facing contract and the paper-proof runbook |

## The wire session

`RunTWSOrderControl(ctx, cfg, action, target, replacement)`:

1. Gate (`cfg.gate`): an endpoint, a concrete account, mode `paper` with a DU
   account in any build, mode `live` only in the trading build. The target
   needs a permanent id and the reviewed symbol, side, quantity and type; a
   modify needs a whole positive replacement quantity at most the reviewed
   one and above the filled quantity, a positive limit, and a DAY LMT stock
   or ETF order (the protobuf placeOrder encoder sends DAY limit orders).
2. A bare `Connection{ClientID: 0, AutoReconnect: false, MaxClientIDRetries: 1}`,
   never the Connector (`NewConnector` rewrites client 0 to 1). Handlers for
   openOrder, orderStatus, openOrderEnd, the legacy error frame and the
   protobuf notice are installed before the connect; `Disconnect` is deferred.
3. Connect within `ConnectTimeout`. TWS answering 326 (client id in use) is
   `ErrTWSOrderControlSlotBusy`; any other failure `ErrTWSOrderControlConnect`.
4. Bind: `reqOpenOrders` (message 5), which for client 0 binds the manual
   orders, and a wait for `openOrderEnd` within `BindTimeout`. TWS follows each
   openOrder with its orderStatus; the session merges the filled quantity and
   the status into the bound record. No `openOrderEnd` is
   `ErrTWSOrderControlBind`. `reqAutoOpenOrders` is not sent: it would make
   TWS keep handing future manual orders to client 0, which a one-request
   session does not want.
5. Identify (`findTWSOrder`): the bound order whose `PermID` equals the
   target's. Absent, or owned by another API client (`ClientID` present and
   not 0): `ErrTWSOrderControlNotFound`. Filled, cancelled, inactive or
   deactivated: `ErrTWSOrderControlNotWorking`. Any reviewed term that differs
   (contract id, symbol, security type, side, order type, time in force,
   quantity, filled, limit, stop or trail price): `ErrTWSOrderControlChanged`,
   naming the first difference.
6. Drain the answers the bind produced, so only callbacks after the send can
   settle it. Cancel: `cancelOrder` with the bound order id. Modify: the bound
   order under its bound id, as client 0, with the new quantity and limit and
   everything else as bound; the bound id is registered as this connection's
   open order first, so the namespace claim does not refuse an id below its
   frontier. A send the transport reports as possibly written is uncertain;
   a definite pre-wire refusal is refused.
7. Wait within `AckTimeout`: a cancel is confirmed on `Cancelled` or
   `ApiCancelled`, pending on `PendingCancel`; a modify is confirmed only on
   the openOrder echo carrying the new quantity and limit (a status alone
   could describe the old order), pending on `Submitted`, `PreSubmitted` or
   `PendingSubmit`. A broker error for the bound id outside the 2100–2199
   warning range is `ErrTWSOrderControlRefused` with the code. Silence is
   `ErrTWSOrderControlUncertain`. Nothing is retried.
8. Disconnect, always.

The receipt carries the action, permanent id, bound order id, outcome,
broker status and code, the bound order as TWS reported it, the server
version and the connect and disconnect times.

## The daemon

`twsOrderControl` holds `brokerWriteMu` for the whole request (one broker
write at a time; the client-0 slot is one), then:

1. `authorizeBrokerWriteTransaction(origin, cancel)`: the ordinary gates. A
   cancel passes the freeze, a change does not. A blocked gate is a refused
   result carrying the gate's blockers.
2. `twsOrderControlAvailability(mode)`: enabled; paper available; live only
   with `paper_proven_at` and broker writes in the build. Otherwise refused
   with `tws_order_control_off` and the status.
3. The journal, when the caller brought a request id: a repeat with the same
   digest (action, permanent id, terms, replacement) answers with the
   journaled outcome, or uncertain when no outcome row exists; another digest
   under the same id is refused (`tws_request_conflict`). A request without an
   id is journaled under a daemon-made one.
4. `twsOrderControlTarget`: the broker's all-client open-order inventory
   (the Protection producer's cached `reqAllOpenOrders`, at most 1.5 s of
   wait). Unavailable: refused. A row the journal tracks: refused, the
   ordinary path owns it. Absent: refused. Terms that differ from the reviewed
   ones: refused, naming the difference. The target handed to the wire takes
   the inventory row's exact terms, including the stop or trail price the
   reviewed terms do not carry.
5. The run, through `twsOrderControlRun` (a test seam; nil runs
   `ibkrlib.RunTWSOrderControl`) against the daemon's own gateway host and
   port, mode and account, with its TLS settings.
6. The outcome row. After an accepted paper outcome,
   `recordTWSOrderControlPaperProof` writes `paper_proven_at` once, through
   the audited settings update under the requester's origin.

The journal rows live in the core store event log, scope `daemon`:

```
tws-control:<account>:<mode>:request:<request id>   action, digest, perm id, origin, "bound_from_tws", terms, replacement
tws-control:<account>:<mode>:outcome:<request id>   outcome, bound order id, broker status and code, blockers, message
```

They are deliberately not order-journal rows: the order journal's reserved
order ids advance the client-15 floor, and a bound client-0 id is not one of
them.

`untrackedBrokerOrders` lends eligibility to the rows the path can reach
(`markTWSOrderControlEligibility`): every working row with a permanent id is
cancel-eligible, a DAY LMT stock or ETF row with an unfilled remainder is
modify-eligible. `OrdersOpenResult.UntrackedControl` says why rows are not.

## Failure semantics, in one table

| Case | Where | Result | Order |
| --- | --- | --- | --- |
| Switch off, live unproven, build read-only | daemon | refused `tws_order_control_off` | untouched |
| Gate blocked (mode, account, freeze on a change) | daemon | refused with the gate's blockers | untouched |
| Inventory unavailable or stale | daemon | refused `tws_inventory_unavailable` | untouched |
| Not in the inventory, or tracked by the journal | daemon | refused `tws_order_not_found` / `tws_order_not_eligible` | untouched |
| Terms differ from the review | daemon, then wire | refused `tws_order_changed`, naming the term | untouched |
| Client 0 already connected (326) | wire | refused `tws_client0_busy` | untouched |
| Connect failure | wire | refused `tws_control_connect` | untouched |
| No `openOrderEnd` | wire | refused `tws_bind_incomplete` | untouched |
| Filled, cancelled or inactive at bind | wire | refused `tws_order_not_working` | untouched |
| TWS error for the bound id | wire | refused `tws_broker_refused` with the code | untouched |
| Sent, no answer within the wait | wire | uncertain `tws_outcome_uncertain`, journaled | unknown: read `orders open` |
| Request id reused with another intent | daemon | refused `tws_request_conflict` | untouched |
| Request journaled, no outcome row (crash) | daemon, on repeat | uncertain | unknown |

## Tests

- `pkg/ibkr/tws_control_test.go`: a loopback TWS at protocol version 203
  (handshake, startAPI, reqOpenOrders, cancelOrder, placeOrder, heartbeat)
  scripted per case: confirmed cancel and modify, every changed term, a filled
  or absent order, a busy client-0 slot, an unfinished bind, silence after
  the send (sent exactly once), a broker refusal with its code, a replay
  after a cancel, and the gates.
- `internal/daemon/tws_order_control_test.go`: the switch (off, terminal-only,
  control generation), live and the proof, eligibility, the version-5
  document and the strict refusal of a leaked version-4 field.
- `internal/daemon/tws_order_control_trading_test.go`: the handler against a
  paper daemon with the run seam: the confirmed cancel with the journal rows
  and the proof, the modify's replacement, the replay and the conflict, the
  uncertain outcome never retried, every refusal before the run, the freeze,
  and `orders open`.

## Not done here

- A TRAIL or GTC manual order is cancelled, never changed: cancel and
  re-enter it in TWS.
- The paper proof is the owner's to run on a paper TWS login
  (`docs/docs/operate/orders.md`, "Paper proof"); this design was not run
  against a live TWS.
- The MCP surface stays read-only; the HTTP app has no route for this path.
