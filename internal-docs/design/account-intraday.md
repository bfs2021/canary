# Intraday account value

Design record: 2026-10-26 21:00 CET, requested for the Desk console's
Portfolio header, which draws the session's net liquidation beside the Net
liquidation figure: "Start with opening, if markets close, show last trading
day" (owner, relayed 2026-10-26).

## Why the daemon records it

No intraday account-value series existed. The risk-capital state keeps one
equity sample per day (`DailyEquity`); statements are end of day; the broker's
`reqPnL` stream carries P&L, not a value path. Account state belongs to the
daemon, so the daemon records the series and adapters only read it.

## Recording

- `runAccountIntradayLoop` ticks every 30 seconds. Inside a US-equity regular
  or early-close session window (`marketcal`, half-open `[open, close)` per
  window) it takes one sample per five-minute bucket counted from the session
  open. IBKR refreshes account values about every three minutes, so a finer
  bucket would repeat values, not add evidence.
- A sample is net liquidation from `readAccountSnapshot`, the shared,
  single-flight `reqAccountSummary` read (15-second reuse), only when its
  provenance is `request` (never the cached fallback), the value is finite and
  positive, the base currency is proven, the broker scope is concrete and
  unchanged across the read, and the read's own clock lies inside the bucket.
  Its clock is the read's observation time; IBKR supplies no valuation time.
- A session keeps one currency. A sample in another currency is dropped and
  leaves a gap.
- Nothing is interpolated or carried forward. A bucket without an admissible
  read stays empty; a gap is the absence of points.

## Retention

One state document (`account_intraday`, daemon scope) holds, per opaque
broker-scope fingerprint (the Daily P&L authority's key, no account identity),
the two newest sessions that have samples: the current session and the last
one before it. At most 4 scopes (live and paper with room to change) and at
most one point per bucket (79 for a 6.5-hour session) are kept. The document is
written on every new sample; a write failure leaves the sample out of memory
too, so a restart never shows a point the reader saw vanish.

## Read

`account.intraday` (`canary_account_intraday`, read-only, no arguments)
returns, for the current broker scope only:

- `market_session`: today's US-equity calendar session (date, state, open,
  close, `is_open`), so a reader can tell "before the open" from "closed day";
- `sessions`: retained sessions, newest first, each with `date`, `open`,
  `close`, `base_currency`, `interval_seconds` (300) and `points`
  (`at`, `value`), ascending;
- `opening_value` on a session only when its first point falls in the first
  bucket; otherwise absent, never the first later point.

`status` is `available`, or `unavailable` with a `reason` when the scope is not
concrete or the store is not bound. An empty `sessions` list is "nothing
recorded yet", not a flat line.

Readers draw a session from its open to its close, break the line where two
points are more than two buckets apart, and label any session that is not
today's by its date.

## Impact map

- Canary: `internal/daemon/account_intraday.go` (store, loop, handler),
  `internal/rpc/account_intraday.go` (types, method, params), method timing,
  dispatch in `server.go`, store binding beside the other SQLite authorities,
  `internal/mcp` tool and the generated MCP reference and server card.
- Desk: a re-pin to the commit that serves the tool, one observer
  (`account_intraday`, one-minute cadence), its simulation fixture and the
  header sparkline (`internal/web/views/portfolio-account.js`) drawn by the
  shared `priceChart`.
- Not touched: risk capital, Rulebook inputs, any order or authority path.
  The series is display evidence only and feeds no decision.
