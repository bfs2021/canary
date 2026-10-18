# Option-chain recorder

Updated: 2026-10-18 (coverage follows the declarations: the strategy
register, [strategy-register.md](strategy-register.md), section 4; first
version 2026-10-14)

Status: current authority for `internal/chainrec`, `internal/daemon/chain_recorder.go`,
`chains.recorded`, `chains.recorded_rows` and `canary chains recorded`.

The desk cannot buy affordable historical option data, so it records its own.
Once a session, shortly before the close, the recorder reads a fixed slice of
the option chain of every name the desk's holdings and registered strategies
declare and appends one row per contract: quotes and sizes, model IV and greeks, open interest, volume.
After a year the desk owns an options panel for its own names, and the cost
models can use spreads that were actually quoted instead of assumed ones.

The recorder is a reader. It never prepares, previews or sends an order, and
no setting, schedule or row can reach an order path.

## Switch and settings

The recorder needs no switch to run (owner decision 2026-10-18 09:01 CEST:
"strategies declaring the data Canary collects"): it records what holdings
and registered strategies declare. The owner keeps one off switch. While
nothing is declared and no option is held, a due session runs nothing: no
file is created, no line is opened, nothing is sent, and the read surfaces
report no store. Switching it off or on needs no restart.

| Key | Default | Meaning |
| --- | --- | --- |
| `chains.recorder.enabled` | unset: on | The owner's off switch: `false` stops the daily snapshot; `null` runs it again |
| `chains.recorder.symbols` | none | Research names, recorded after held option underlyings and registered strategies' declared chains; `null` restores none |

Both are runtime keys of the platform settings document, version 6
([platform-settings.md](platform-settings.md)). The off switch is the owner's
(class `owner`: a human terminal or the paired console); the research list is
a collection key any origin may change, an agent session included. Both
govern market-data reads only. Before 2026-10-18 the switch was off while
unset and the list's default was SPX, SPY and QQQ, the names of s1, which was
dropped.

Everything else is the fixed v1 plan below, in `internal/chainrec/chainrec.go`,
and changes with this note. The Mini-SPX index option (one tenth of SPX) is not
in the default list: the gateway layer routes that symbol as a stock, so its
chain cannot be discovered until `pkg/ibkr` classifies it as an index, a change
that would also alter quotes elsewhere and needs its own decision.

## What one run records

**Underlyings.** The underlyings of open USD option positions (from the
cached portfolio, alphabetically), then the chains registered strategies
declare (live strategies first, then shadow, suspended and held ones), then
the research list in order, de-duplicated with every source named
(`held`, `strategy:<id>`, `explicit`, joined by `+`; runs before 2026-10-18
wrote `explicit+held`), at most 12. The rest are recorded as
`skipped_symbol_cap` with their source. When the portfolio cannot be read the
run continues with the declarations and the research list and says so
(`held_unavailable`).

**Expiries**, from the symbol's security-definition listing, never an expiry
that ends on the run's own session date:

- the two nearest *standard monthlies*: the listing in the symbol's own trading
  class on the third Friday, or on the trading day before the third Friday when
  that Friday is an exchange holiday (Juneteenth 2026). IBKR keys AM-settled
  index monthlies on their last trading day, the day before settlement, so the
  SPX-class listing on that day counts as the monthly (SPX AM on Thursday);
- the nearest *weekly*: the last listed expiry of a calendar week, on a Friday,
  or on a Thursday whose Friday is a holiday, that is not a standard monthly
  date. It uses the symbol's own class when listed there, otherwise another
  class (SPXW for SPX).

A name with monthlies only records two expiries.

**Strikes.** Within spot ± 20 %, at most 40 per expiry, each as a call and a
put. Above 40, half are the strikes nearest the money and half are spread
evenly by listed-strike index over the rest of the window, both edges included:
the panel keeps the at-the-money spread and the wings of the smile. Strikes come
from the listing and are filtered by a bulk contract resolution (one
contract-details request per expiry) under the gamma fan-out's rule: a complete
resolution, or a timeout, refusal or truncation of it, keeps only the contracts
it cached; any other failure keeps every listed strike, each then resolved when
its line is requested.

**Spot** is the chain fetch's own spot read (last, else mid, mark or previous
close), recorded with its source and data type.

## Schedule

The run starts 30 minutes before the regular US equity close of a trading
session (`marketcal`, `us_equity`): 15:30 New York, 12:30 on an early-close day.
Weekends, holidays and dates outside the embedded calendar's coverage have no
run. A daemon that starts inside the window still runs while at least 10
minutes remain. The close is the deadline: no contract is requested after it.

A session is recorded at most once. A run that stops early, or one the daemon
never finished because it stopped, is never retried; it reads as `partial` or
`interrupted` with its coverage.

When the gateway is not ready at the start, the run waits up to 5 minutes for
it, then starts anyway and records the outage: every symbol
`chain_unavailable`, the run `failed`.

## Limits and request budget

The recorder runs beside the live display, the protection engine and order
entry on the daemon's one gateway connection, and is shaped to lose to them.
Each contract gets a short streaming line rather than an IBKR snapshot request:
IBKR refuses the open-interest generic tick on snapshot requests (error 321),
and the model tick arrives on the same line.

| Resource | Limit | Mechanism |
| --- | --- | --- |
| Market-data lines | at most 4 option lines at once, one per worker | each contract opens a line, collects at most 3 s and releases it |
| Line reserve | never opens a line while 20 or fewer of the pool's lines (100) are free | `Connector.MarketDataLoad` before every request; waits 1 s and re-checks |
| Message pacing | option lines, the spot read and the bulk contract resolution ride the background pacing lane; the one listing request per underlying is an ordinary request | at most 8 background reservations may queue ahead of an interactive request; orders and heartbeats never queue behind it |
| Pacing errors | a pacing refusal, or a code-100 pacing error on the connection from anyone, pauses the run 5 s, doubling to 80 s | five consecutive signals stop the run (`partial`, stop reason `pacing`) |
| Entitlement | four refusals for one symbol with no quote yet | that symbol's remaining contracts are not requested |
| Gateway loss | a refusal for a lost connection or a lost IBKR backend link | stops the run |
| Retries | none | every contract is requested at most once; what was not reached is recorded `not_attempted` with the reason |

Request budget per run, with *n* underlyings (default 3, at most 12):

| Step | Requests per underlying | Default run (3) | Largest run (12) |
| --- | --- | --- | --- |
| Security-definition listing | 1 (`reqSecDefOptParams`) | 3 | 12 |
| Spot | 1 underlying quote line held at most 5 s (shared when the display already holds it); a quote snapshot when that read is empty | up to 6 messages | up to 24 messages |
| Bulk contract resolution | 1 contract-details request per expiry, up to 3 | 9 | 36 |
| Contracts | at most 3 × 40 × 2 = 240 lines, 2 messages each (request, cancel) | 720 lines, 1,440 messages | 2,880 lines, 5,760 messages |

So a default run sends at most about 1,460 messages, a largest run about
5,830, and no historical-data request (the spot fallback to a daily bar applies only while
the market is closed). With 4 lines and a contract taking 0.5 to 3 s, the
recorder sends at most about 16 messages a second against the limiter's 40 (IBKR
allows 50). A default run takes about 3 minutes when quotes arrive promptly and
at most 9; a largest run about 12 minutes and at most 36, in which case the
close cuts it and the far strikes go unrecorded first: work is ordered by
distance from the money across every symbol and expiry.

## Coverage

Every contract discovery selects gets exactly one row, whatever happened to it.
Coverage counts those rows per run and per underlying:

| Count | Meaning |
| --- | --- |
| planned | contracts discovery selected |
| attempted | contracts whose line was requested |
| quoted | a bid, ask or last arrived |
| two-sided | both a bid and an ask arrived |
| with IV / greeks / OI | the model tick's IV, the model delta, open interest arrived |
| not attempted | never requested: deadline, pacing, gateway loss, an unentitled symbol, or never reached by an interrupted run |

Row statuses: `quoted`, `model_only` (IV or greeks but no price), `no_quote`
(the line opened and nothing arrived in 3 s), `no_contract`, `no_entitlement`,
`pacing`, `gateway_unavailable`, `rejected`, `timeout`, `not_attempted`.

Run statuses: `complete` (every planned contract attempted), `partial` (some
not attempted, or an underlying never discovered), `failed` (nothing
attempted), `running` (this daemon is executing it), `interrupted` (begun and
never closed). Underlying statuses: `planned`, `chain_unavailable`,
`no_expiries`, `spot_unavailable`, `skipped_symbol_cap`, `not_discovered`.

A missing value is unobserved, never zero: a far strike without a bid has no
bid, not a bid of 0.

## Storage

`option-chains.db` beside `daemon.db` in the daemon's state directory, created
by the first run with private file modes. It is a separate SQLite file (WAL,
`synchronous=NORMAL`) so the growing panel never enlarges, backs up with, or
slows an out-of-place upgrade of the daemon's authority store. Every table is
append-only: triggers refuse UPDATE and DELETE. Retention is unlimited.

### Data dictionary

`runs`: one row per run, written when it starts.

| Column | Meaning |
| --- | --- |
| `run_id` | run identity |
| `session_date` | US equity session date |
| `session_close`, `scheduled_at`, `deadline`, `started_at` | RFC 3339 UTC |
| `early_close` | 1 on an early-close day |
| `settings_json` | the explicit list and the fixed plan the run used |

`run_symbols`: one row per underlying per run, written after its discovery:
`symbol`, `source` (explicit, held, explicit+held), `status`, `detail` (bounded
broker text), `spot`, `spot_source`, `spot_data_type`, `spot_at`, `planned`,
`expiries_json` (date, trading class, slot, strikes in window, selected,
contracts), `discovered_at`.

`quotes`: one row per contract per run, unique on (`run_id`, `symbol`,
`trading_class`, `expiry`, `strike`, `option_right`).

| Column | Meaning |
| --- | --- |
| `symbol`, `trading_class` | underlying and IBKR trading class (SPX, SPXW, SPY, ...) |
| `expiry` | IBKR's listed last-trade date, YYYY-MM-DD |
| `expiry_slot` | `monthly_1`, `monthly_2`, `weekly` |
| `strike`, `option_right` | strike; C or P |
| `status`, `detail` | row status; the reason when not observed |
| `con_id` | contract id when known |
| `bid`, `ask`, `last` | prices; NULL when not quoted |
| `bid_size`, `ask_size` | displayed size in contracts |
| `volume` | the session's contract volume (tick 8) |
| `open_interest` | open interest (ticks 27/28) |
| `iv`, `delta`, `gamma`, `vega`, `theta` | IBKR's model computation (tick 13; 83 when delayed) |
| `underlying_price` | the underlying price in that model tick |
| `model_source` | `live` or `delayed` |
| `feed` | the line's market-data type: live, frozen, delayed, delayed-frozen |
| `requested_at_ms`, `observed_at_ms` | when the line was requested and the collection ended (Unix ms, UTC) |
| `quote_received_at_ms` | when this process received the newest bid or ask tick; IBKR quote ticks carry no exchange time |
| `last_trade_at_ms` | the broker's own last-trade time (tick 45), when sent |

`run_outcomes`: one row when a run closes: `finished_at`, `status`,
`stop_reason`, `planned`, `attempted`, `quoted`, `held_unavailable`. A run
without one is running or interrupted.

### Size

Measured with synthetic rows: about 220 bytes per row on disk, the unique index
included. A default run (3 underlyings, 720 rows) adds about 155 KB, about 40 MB
over a year of 252 sessions. A largest run (12 underlyings, 2,880 rows) adds
about 620 KB, about 155 MB a year; five years at that rate stay under 0.8 GB.

## Read surfaces

- `chains.recorded` (`symbol`, `date`, `limit`; 10 runs by default, at most
  250): the switch, the explicit list, the fixed plan, the next run, the store's
  size and the runs newest first with their coverage per underlying.
- `chains.recorded_rows` (`run_id` or `date`, optional `symbol`, cursor
  `after`, `limit` up to 10,000): the rows in recording order.
- `canary chains recorded [--symbol S] [--date D] [--limit N] [--json|--csv]`
  renders the runs (golden: `chains_recorded*.txt`); `--csv` pages every
  selected run's rows into one CSV on stdout, oldest run first, with the
  columns above (times as RFC 3339 UTC, empty cells unobserved).
- `canary settings show` prints the switch and the list. No MCP tool: the
  panel is research data for the owner, not a model input, and has no write.

## Staying off the hot path

- The run executes in its own goroutine; the schedule loop is the only caller.
- Reads open the store only when the file exists; they never create it.
- Lines, pacing and the reserve are checked before every contract (above); a
  daemon busy with interactive work slows the recorder, never the reverse.
- `canary status` lists `chain-recorder: recording` as a background task while
  a run is active, which also defers the idle shutdown until the run closes.

## Verification

Unit tests cover expiry selection (daily ETF listings, SPX AM/PM classes, the
holiday-shifted monthly and weekly, monthly-only names, adjusted classes),
strike selection and caps, symbol merging and the cap, the schedule (regular,
early close, holidays, weekends, outside coverage, late start), pacing backoff
and stop, the line reserve, the deadline, gateway loss, the entitlement
give-up, discovery failures, persistence and read-back with paging, the
append-only triggers, interrupted runs, the settings document upgrade, and the
recorder staying off by default.

Needs a live session: SPX AM monthly contracts resolving under the bulk
resolution, model ticks and open interest arriving within 3 s for each class,
actual run duration and line usage beside the live display, and the pacing
behaviour of a full 12-underlying run.
