# Desk execution contract

Agreed 2026-10-10 07:20 CEST between the Desk controller session and the
Canary execution session, on the owner's instruction to share request and
receipt types before either side implements. Types only: no handler, route,
CLI command or install. The types are `internal/rpc/desk_execution.go`, aliased
for Desk in `desk_execution.go`.

## One machinery, two authorisations

- **Manual batch.** `desk.execution.prepare_batch` names proposals by key and
  served revision. Canary previews each, assigns request and episode
  identities, and returns exact terms, their digest and a private `batch_ref`
  (never browser, log or argv). Proposals it cannot prepare come back as
  refused receipts and do not hold the rest. Send order is reductions, then
  protection, then additions (owner, 2026-10-09 19:20 CEST); this ordering
  is a property of the manual batch only. `desk.execution.submit` with
  `batch` carries one device confirmation of those terms, verified in its own
  signature domain.
- **Automatic.** `desk.execution.submit` with `automatic` carries the
  mandate's terms ID, private capability, the generation and preferences hash
  the controller reconciled against, and the items in the controller's order.
  The only order rule the machinery adds is that no addition is listed before
  a protection or reduction; protection and reductions keep their existing
  priority and additions their FIFO order.

Both forms reach the same per-item admission: Canary's own classification of
the exact order, policy, the hard drawdown brake, funding and margin, and for
automatic items the mandate fence under the wire lock before the first byte.
Capacity is shared because both pass the same serialized admission.

## Identity and outcomes

- `request_id` is the idempotency key: the same ID and intent returns the
  original receipt; a different intent under it is refused.
- `episode_id` is the consumption key: after an accepted or unknown item, the
  episode yields no further order, including after partial fills or a
  cancelled remainder.
- Every receipt binds `request_id` and the intent digest. Outcomes: `accepted`
  (a known order under `order_ref`), `refused` (definitive, nothing reached
  the broker), `unknown` (may have reached it; resolved only by lookup), and
  on lookup only `absent` (the durable request journal, written before any
  dispatch, holds no record). A lookup that cannot read the journal fails; it
  never answers `absent`.
- Each item is sent on its own. After an `unknown` item, later additions in
  the same submission are not sent (`prior_outcome_unknown`); protection and
  reductions still are.
- Blocker codes Desk may branch on: `drawdown_brake`, `episode_consumed`,
  `request_conflict`, `authority_changed`, `class_mismatch`,
  `prior_outcome_unknown`, `proposal_changed`. Any other code is a refusal.

## Discovery

`desk.execution.capabilities` reports the contract version, dispatched
methods and served authorisations. A daemon that does not report them is
unavailable to Desk; there is no fallback to another write path.

## Journal (served since 2026-10-10)

Every item is journaled in the core store's append-only event log before
anything can reach the broker. The request row and the episode row commit
together under unique keys scoped to account and mode, so a retry finds its
request, a reused ID with another intent is refused, and an episode yields at
most one request across restarts. Outcome rows follow: accepted and refused
are final, unknown can later be resolved by either. `lookup` reads this
journal: no request row answers `absent`; a request row without an outcome
answers `unknown`; an unreadable journal fails the call. Capabilities list
`capabilities` and `lookup` only, with no authorisations, until submit is
served.

## Reductions of a held long

A limit or Adaptive reduction of a held long is admitted when it sells at
most the position the broker's current projection confirms, whatever else
is working on the contract (owner decision 2026-10-12 19:55 CEST;
risk-policy.md, "Order limits", item 10). The short re-read that refused it
under this contract until then is gone; `allow_stock_short` and
`allow_option_sell_to_open` judge only an order that opens or flips a short.

## Reduce handoff (served since 2026-10-12)

Desk's per-position Reduce menu and its Reduce book basket ride on
`trade_proposals.reduce_preview` and `reduce_portfolio_preview`, which
return a signed preview token per order with `terms`: the exact contract,
side, quantity, order type, limit, TIF and algorithm, the expiry, and the
`price_bound` the submit holds the market to. The owner confirms those
terms; `reduce_submit` with `preview_token` and `reduce_portfolio_submit`
with `preview_tokens` place them verbatim, each basket leg on its own. The
daemon never re-sizes or re-prices a tokened reduction: it refuses with
`token_expired`, `token_invalid`, `token_consumed`, `position_changed`
(the reviewed quantity exceeds the confirmed position, or the side changed),
`price_moved` (the current mid is outside the reviewed mid ±0.5% for stocks or ±2% for options (owner 2026-10-13 06:59 CEST), the
disclosed default pending the owner's own figure), or the order gate's own
reason, and the place path's admission is unchanged: Canary's own stop on
the line is joined or shrunk first. `request_ref` names one submission;
a retry with the same reference replays the recorded result (90 s) and
nothing is sent twice. Reductions default to IBKR Adaptive `Normal` on a
limit at the mid where the broker layer sends Adaptive (a US stock or ETF
routed SMART); elsewhere the terms say "Adaptive not supported for this
contract" and the order is a plain limit. The un-tokened `--submit`, which
previews at the live book and places in one call, remains the hand path
and the paired app's portfolio sweep.
