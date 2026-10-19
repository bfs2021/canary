# Strategy register (one list of machine strategies in the risk policy)

Written: 2026-10-18 09:20 CEST. Revised 09:45 CEST after the independent
review (section "Independent review"). Status: built (increments 1 to 4,
section "Build"), 2026-10-18. Revised 18:40 CEST: the strategy evaluator
(`strategy-evaluator.md`) runs every entry through one code path; sections
1, 3, 4, 9 and 12 say what that changed. Nothing here writes policy:
every entry and every promotion is the owner's, written by the owner into
`risk-policy.toml`. Revised 2026-10-19 09:00 CEST: every live entry serves
its month's row (Desk `docs/strategy-engine.md`, build 2; owner instruction
2026-10-19 07:07 CEST, "task build 2 and then 6 as well"); sections 2, 5 and
9 and the new section "Rows for every live entry" say what that changed.

This record follows `.agents/docs/risk-policy-contract.md`. It records the
owner's decision of 2026-10-18 09:01 CEST and proposes the schema, the code
and the core's entry; the owner writes the entry.

Sources read: Canary main `f1a1b17c` (risk allocations `0647e376`, the core
venue fix `6c834cc7`), `mandate-risk-budget.md`, `core-sleeve.md`,
`decision-scorecard.md`, `option-chain-recorder.md`, `desk-execution.md`,
`platform-settings.md`, the agent hook `hooks/canary-pre-tool-use.sh` (the
plugin's `claude-plugin/hooks` links to it) and `.codex/rules/canary.rules`;
Desk main `c256851` (`docs/strategy-engine.md`, `docs/automatic-trading.md`);
the lab's README ladder and `registry/registry.jsonl` (`0870f3b`).

## Decision

- Goal and protected behaviour: every machine strategy that may add risk is
  one owner-written entry in Canary's risk policy, so that its rule version,
  its sleeve, its risk allocation and its ladder stage have one runtime home
  that the additions gate, the forced reduction, the core's verdict and the
  data collection all read. Protected behaviour: no machine strategy adds
  risk, or reserves part of the risk budget, without an entry the owner wrote
  naming the exact compiled rule and the lab spec it implements.
- Policy owner: the desk owner.
- Human decision, verbatim, 2026-10-18 09:01 CEST: "Accept the four review
  fixes: one register for all machine strategies, the ladder stage in
  Canary's risk policy, shadow on the production path, and strategies
  declaring the data Canary collects." The four fixes are findings 2 to 5 of
  the architecture review in Desk's `docs/strategy-engine.md`:
  2. one register: an entry-rule template that is to add risk on its own is
     registered as a strategy with its own sleeve and allocation; until then
     its fills count in the owner's sleeve, since the owner configures and
     arms it;
  3. the ladder stage in Canary's risk policy, beside the rule version (spec
     hash), sleeve and allocation, as `[mandate.core]` already does with
     `rule` and `scale_pct`; the owner writes each promotion, citing the
     lab's verdict; Canary holds a strategy whose spec hash does not match;
  4. shadow is the production path read-only: Canary serves the strategy's
     target and rows without sending, and a record keeps each session's
     intent and simulated fill;
  5. each registered strategy declares its data; Canary collects what
     holdings and registered strategies declare, inside its pacing and line
     budgets, journaled; the owner decides purchases and the safety controls
     only.
- Earlier decisions this builds on: risk allocations inside the whole-book
  limits, reserved only to the extent a strategy runs (owner, 2026-10-18
  08:06 CEST; `mandate-risk-budget.md`, "Risk allocations"); s1 dropped (08:21); the
  core's contract by ISIN and out of protection's automatic sales (08:06,
  `core-sleeve.md`); the professional terms (08:50: "always use the ones
  that professionals use, risk budget or risk allocation"). Canary's code and
  copy say risk allocation since `16b52c52` (`mandate.AllocationPlan`,
  `verdict.allocations`).
- Current authority: `risk-policy.toml` `[mandate.core]` and
  `[mandate.sleeves]`; code `internal/risk/mandate.go`,
  `internal/daemon/mandate_allocations.go`, `internal/daemon/core_target.go`,
  `internal/core`.
- Status: approved by the owner's decision above; built 2026-10-18.

## In brief

1. **One register.** `[mandate.core]` is the register's first entry, kept
   under its name with every key, so today's file stays valid with the same
   fingerprint. Every later machine strategy is a `[mandate.strategies.<id>]`
   table with the same common keys. Code reads one typed list,
   `ConstitutionMandate.Register()`, resolved once against the compiled
   rules (`register.Resolve`).
2. **Each entry** names its compiled rule and the lab spec hash it
   implements, its sleeve, its risk allocation, its stage (`shadow`, `live`
   at a scale of 25, 50 or 100, or `suspended`). A missing key holds that
   strategy and names the key; a rule Canary does not carry, or a spec hash
   that is not the compiled rule's, holds it. Nothing acts by default.
3. **The stage feeds the risk allocation.** A live strategy reserves its
   allocation times its scale; shadow and suspended reserve nothing; a live
   strategy that holds keeps its reservation while its sleeve holds
   positions, so a build or a key fix never reshuffles the allocations.
4. **Shadow runs the production code read-only** on a paper position, with
   its decisions, intents and simulated fills in an append-only record that
   no order path reads.
5. **Rules declare their data.** Canary collects what holdings and
   registered strategies declare, inside the existing budgets, with the
   declarer recorded. The option-chain recorder runs on the declarations
   without a switch. The settings registry gives every key a class the
   daemon enforces by origin: agents may write collection keys only; the
   owner keeps the safety controls, the protection control and the off
   switches.

## Meaning

- Base: the mandate's budget base × room, as for every risk allocation; an
  allocation is percent a year of the budget base at full room.
- Aggregation unit: the strategy, one per sleeve.
- Classification: an entry adds risk only through its rule's rows while
  live, under every existing gate; shadow and suspended add none; a hold
  adds none.
- Enforcement class: the register is policy; the mandate gate, the forced
  reduction and the core's own checks stay the enforcement points.
- Unknown and partial posture: a missing key, an unknown rule or a mismatched
  hash holds the strategy, named; a file that contradicts itself is refused
  and the last good policy stays.

## 1. The register

### Two forms, one list

```toml
[mandate.core]                 # the first entry: id "core", sleeve "core"
rule      = "core-ibcf-v1"     # existing
spec_hash = "2408d09e…"        # new, optional in this form (below)
stage     = "shadow"           # new, optional in this form (below)
scale_pct = 25                 # existing: the scale it sizes at
# isin, currency, band_pct, min_order_base, max_value_pct: existing, the core's own

[mandate.strategies.s9-example]  # every later strategy
rule                = "…"       # a rule compiled into this Canary
spec_hash           = "…"       # the lab registry's hash of the spec it implements
sleeve              = "s9"      # the sleeve its positions belong to
risk_allocation_pct = 2         # percent a year of the budget base
stage               = "shadow"  # shadow, live or suspended
scale_pct           = 25        # the scale it sizes at: 25, 50 or 100; reserved only while live
```

`ConstitutionMandate.Register()` returns one `StrategyEntry` per entry, the
core first, then the others by id. `register.Resolve` gives each its
effective state once per verdict; the allocation plan, the core's buy block
and the shadow record read that one result.

Why the core keeps its table rather than moving into `[mandate.strategies]`:
the live file, its fingerprint and the core's own instrument keys stay as
the owner wrote them, and no migration rewrites the owner's file. A
`[mandate.strategies.core]` table is refused: the core has one form.

### Common keys

| Key | Meaning | `[mandate.core]` | `[mandate.strategies.<id>]` |
|---|---|---|---|
| id | The strategy's name in the register | `core`, implied | the table key: lower-case letters, digits and hyphens, at most 32, not `core` |
| `rule` | The compiled rule's id | required (as today) | required |
| `spec_hash` | The lab registry's hash of the spec the rule implements, as the owner cites it | optional while not live; required to go live | required |
| sleeve | Where its positions count | `core`, implied | `sleeve`, required: lower-case, at most 24, unique, never `owner`, `rule` or `core` |
| risk allocation | Percent a year of the budget base | `[mandate.sleeves] core_vol_pct` (as today) | `risk_allocation_pct`, required; for `sleeve = "overlay"`, `[mandate.sleeves] overlay_vol_pct` instead, and `risk_allocation_pct` is refused |
| `stage` | `shadow`, `live` or `suspended` | optional: absent reads `shadow` (below), the technical state of an entry not yet written live, never a required step (owner, 2026-10-18 21:05 CEST: "we NEVER want shadow") | required |
| `scale_pct` | The scale the rule sizes at: 25 (small), 50 (larger), 100 (full), the lab's R3, R4 and established; reserved only while live | as today in [0, 100], 0 suspending, while `stage` is unwritten; once `stage` is written, 25, 50 or 100, and a suspension is `stage = "suspended"` | required in `shadow` and `live`, 25, 50 or 100; not read while `suspended` |

A compiled instance's parameters (the core's ten months, span 63, the 15%
floor) are compiled with the rule version and are never keys. The
evaluator's owner numbers are the entry's keys (`strategy-evaluator.md`
§2): the parameterised rule `timed-holding-v1` reads `isin`, `currency`,
`signal`, `band_pct`, `min_order_base` and `max_value_pct` from its entry,
with `con_id`, `exchange` and `kill_drawdown_k` optional, and holds on one
unwritten, named as `mandate.strategies.<id>.<key> unwritten`. Any other
key is unknown and refuses the file.

### The core's two new keys

The register's rule is that a missing key holds. The core's first form is
the one exception, so that today's file keeps today's behaviour:

- `stage` absent reads `shadow`, the stage the core has run in since phase
  1. Shadow adds no risk and reserves nothing, so the absent key acts on
  nothing; it is shown as "stage unwritten: shadow, as before the register".
  The core goes live only when the owner writes `stage = "live"`.
- `spec_hash` absent: the compiled rule's own spec stands, shown and
  recorded as "compiled, unwritten", never as cited. A written hash must
  match. `stage = "live"` without a written `spec_hash` holds the core and
  names the key: no strategy goes live without the owner's citation.

Every other `[mandate.core]` rule is unchanged (`CoreUnapprovedKeys`, the
venue fix's resolution).

### What refuses the file and what holds one strategy

Refuses the constitution (the last good policy stays): a value out of range
or of the wrong shape, an unknown key, `[mandate.strategies.core]`, two
entries naming one sleeve or one rule, a reserved sleeve name,
`risk_allocation_pct` on an overlay entry, a scale off the ladder where the
ladder applies, machine allocations above the budget's target:
`core_vol_pct + overlay_vol_pct + Σ risk_allocation_pct ≤ vol_target_pct`
(today's check, widened to every entry), and a live entry whose written
`kill_drawdown_k` is 1 K or more (the whole sleeve invested at its stage: the
kill rule cannot fire before the sleeve is gone; `register.Refusals`,
2026-10-19).

Holds that strategy only, named on it, never in the constitution's approval
list:

| Hold | Named as |
|---|---|
| A required key unwritten | `mandate.strategies.<id>.<key> unwritten` (for the core, the existing `CoreUnapprovedKeys`, plus `mandate.core.spec_hash` while `stage = "live"`) |
| The rule is not compiled into this Canary | "rule X is not compiled into this Canary" |
| The written spec hash is not the compiled rule's | "spec_hash Y is not the spec rule X implements (Z)" |
| The rule runs in the other form | "rule X runs only as [mandate.core]" or the reverse |
| An overlay entry while `overlay_vol_pct` is unwritten | `mandate.sleeves.overlay_vol_pct unwritten` |
| A live entry's `kill_drawdown_k` unwritten (the core's included) | "kill rule: kill_drawdown_k is unwritten in [table], so the kill rule cannot fire: buys stop until the bound is written" (the daemon's hold, `strategy_target.go`, 2026-10-19; a shadow entry runs on the note, since it sends nothing) |

A held strategy serves its hold: no target, no rows, no shadow record. Its
declared data is still collected while its rule is compiled (section 4).

### Fingerprints and old files

- `spec_hash` and `stage` on `[mandate.core]` and the `[mandate.strategies]`
  map are omitted from the JSON projection when absent; `FingerprintKey` and
  `EffectiveFingerprintKey` marshal the mandate directly, so every file
  written before them keeps both fingerprints byte for byte (a test pins
  today's fingerprint of a synthetic file).
- An older daemon refuses a file carrying the new keys (strict unknown-key
  decoding keeps the last good policy), so the owner writes them only after
  installing the daemon that reads them.
- `canary policy ensure` never writes the keys; the template in `canary
  policy default constitution` shows them commented out.

### The compiled rule catalogue

`internal/register` (pure) lists the rules this Canary carries:

| Field | Meaning | core-ibcf-v1 | timed-holding-v1 |
|---|---|---|---|
| `ID`, `SpecID`, `SpecHash` | The rule and the lab spec it implements | `core.RuleID`, `core.SpecID`, `core.SpecHash` | the id only: the entry cites its spec |
| `Form` | `core` (only as `[mandate.core]`) or `entry` | `core` | `entry` |
| `Sends` | Whether this build serves the rule's rows (replaces `coreSendsOrders`) | true (phase 2, 2026-10-18) | true (build 2, 2026-10-19) |
| `Bucket` | The proposal bucket of its rows, never pre-authorisable | `core_rebalance` | `strategy_rebalance` |
| `Data` | Its declared daily series and option chains (section 4) | its fund's daily closes on its listing, 253 sessions; no chains | from the entry: its fund's closes, 253 sessions |
| `Parameterised`, `Instrument`, `Trend` | Whether the entry's keys supply the fund, the filter and the numbers; else the compiled ones | compiled: IE00B3ZW0K18 EUR, `trend-sma10m` | parameterised |

A hash ties a rule id to a spec, not to its code. A compiled instance's
parity fixture is pinned to its rule and spec hash in a test, so a
behaviour change that leaves the hash alone fails the parity test, and a new
hash needs a new fixture. A parameterised rule carries no hash: the entry's
`spec_hash` is the owner's citation, and the parity guarantee rests on the
signal, each library filter being pinned to its lab fixture
(`strategy-evaluator.md` §4). `register.Resolve` gives each entry what the
evaluator runs: its instrument, filter, terms, spec and declared data.

## 2. Stages and the risk allocation

| Written | Effective | Serves | Reserves |
|---|---|---|---|
| `shadow` | shadow | the verdict, read-only; the shadow record | nothing |
| `live`, scale s, every key written, rule compiled, hash matching, `Sends` | live | the verdict and its rows as proposals, sent only through the rule's own authorisation | s/100 × its allocation |
| `live`, but this build serves no rows for the rule | the file is refused at load, naming the entry and the rule (`register.LiveRefusals`, 2026-10-19); the last good policy stays. Until 2026-10-19 such an entry ran as shadow ("live written; this build sends no orders"); that fallback is gone, since a live entry either produces rows or does not load | — | — |
| `suspended` (or the core's first-form `scale_pct = 0`) | suspended | its state only | nothing |
| `live`, with an entry hold | hold | its hold | s/100 × its allocation while its sleeve holds positions, else nothing |
| any other, with an entry hold | hold | its hold | nothing |

The allocation plan (`mandateAllocationPlan`) reads the resolved register: one
machine sleeve per entry, its allocation the entry's, its reserved fraction
above. The overlay keeps its line whenever no entry uses the sleeve, as
today ("no strategy registered in the overlay yet").

Why a held live strategy keeps its reservation while it holds positions: a
new build that changes a rule's spec hash, or a key the owner is fixing,
would otherwise drop the reservation to zero, count the whole holding as
excess (so the next breach cuts it first), and widen the owner's room until
the fix shrinks it again. The positions are still the strategy's. A held
live strategy with nothing in its sleeve reserves nothing, so an entry that
never ran never shrinks the owner's room. Only the owner's written stage,
scale or removal moves a reservation. This reads "only as big as what it
actually runs" (08:06) as: what it holds or may trade.

A rule's own data holds (the core's stale series, an unknown verdict) never
release a reservation either.

Everything else the allocation code does is unchanged: the owner's
allocation is the rest of the budget; the gate judges the order's own
sleeve; the forced reduction cuts the sleeve furthest over its allocation
first (ties in the order owner, core, overlay, then by name); a flat sleeve
keeps its reservation; each position keeps the sleeve of whoever opened it;
the core's contract is reserved to the core. Placement for a non-core
strategy follows the fill lineage rules already in `mandate-risk-budget.md`
(Placement, rules 1 to 5), built with the first order path that sends a
non-core strategy's rows, after the two gate gaps named there close.

Phase 3 (Option B) arms terms that name the rule, the spec hash, the scale
and the allocation (`core-sleeve.md`, "Option B's armed terms"); when it is
built, a register edit that changes any of them withdraws the standing
terms.

### Rows for every live entry (built 2026-10-19)

Every compiled rule sends, so a live entry runs live. The core's row
builder (`core_rebalance.go`) serves each live `[mandate.strategies.<id>]`
entry's month exactly as it serves the core's (`core-sleeve.md`, "Phase 2
as built"): at most one `strategy_rebalance` row per entry and decision, a
DAY limit at the live mid for a named session in the first five sessions
after the decision that the US also trades, due fifteen minutes after the US
cash open of its session (`core_due.go`), with the core's readiness, its
blockers (an open breach episode or a reduction of the line holds a buy; a
working order on the line holds any row), the owner's ignore, and the typed
exception to `close_reduce_only` bound to the row's own contract, currency
and sized quantity. The row carries the entry's id, rule and sleeve
(`TradeProposalCore.Strategy`, `.Rule`, `.Sleeve`), its episode is
`strategy:<id>:<account>:<mode>:<YYYY-MM>` and its revision names the
session, so two entries' rows never share a key or an episode. It reaches
the same paths as the core's: `trade.proposals`, the prepared proposal the
owner confirms on a device, and `desk.execution.submit` under the standing
mandate (class `add` for its buy, `reduce` for its sale; the session's
episode); it is never pre-authorisable and never queued to the open. The
month's decision is recorded beside the entry's frozen month
(`recordStrategyDecision`), and `canary strategy ID` shows the row and the
last decision. A live entry keeps no shadow record.

Placement (`strategy_placement.go`; `mandate-risk-budget.md`, "Placement",
as built 2026-10-19): by lineage. A live or suspended entry's units of its
line are what its own fills left (the orders its submitted rows sent, read
from the proposal and order journals by order reference), never more than
the broker holds, read afresh at every verdict; every other unit is the
owner's. While an entry is live a hand buy of its line is refused (sales
stay open), so a line the entry holds is its alone and placed whole in its
sleeve; owner's units beside it (from before its first fill, or a trade
outside Canary) split the line and hold the entry, named, until sold. An
order is placed by
its origin: the entry's own row (`OrderDraft.RegisterEntry`, daemon-set) in
its sleeve, anything else on the line in the owner's; a strategy order that
is not a stock order of its own line is refused before the hedge or the
unmeasured pass (the two gate gaps). A line the entry holds alone leaves
protection's automatic sales, as the core's (D5, extended); a line the owner
holds units of keeps them. Two strategies on one fund line (one ISIN and
currency, or one `con_id`, the core's compiled fund included) refuse the
file (`register.Refusals`). A fund the book does not hold yet is measured on
the entry's own series at the headroom and at the gate, so its first row is
never refused unmeasured. Under the standing mandate a strategy row's buy
is sent only under the register it was armed with: an edit of any key of
any entry (rule, spec, sleeve, stage, scale, allocation, fund, pins,
signal, band, minimum, cap, kill bound) withdraws the mandate for strategy
buys until the owner confirms again (`deskRegisterDigest`, checked before
journaling and again inside the fenced attempt). A row served while the
entry holds (the sale to zero its frozen month wants, "for your review")
carries `core.owner_review` and is the owner's to confirm on the device: the
standing mandate never sends it (`owner_review`, refused before journaling
and again at the wire; owner, 2026-10-19: for your review means the owner's
review). Sales, reductions and
protection still go, so an edit never stops the mandate's de-risking.

An entry may name a signal series apart from its traded line
(`signal_isin`, `signal_currency`, `signal_in`; `strategy-evaluator.md` §7):
another listed share class of the same fund that decides the month while
the traded line's own closes do not fill the window, read under
`strategy:<id>:signal:con:<id>` (and its rate under `strategy:<id>:fx:<pair>`),
never a book key. Two entries may not name one fund line; the signal line is
not a traded line and reserves nothing.

`canary status` lists each entry's stage and its row (or why none stands)
from the engine's last generation (`HealthResult.Strategies`).

## 3. Shadow on the production path

### What runs

In shadow, the daemon runs the strategy's own verdict code on a **paper
position**: the shadow holding, which starts at zero and moves only by the
record's own simulated fills. For the core, the shadow decision is
`core.Decide` with the shadow holding; the verdict `canary core` serves
keeps reading the account's real holding, as today.

Shadow sizes as the strategy would live at its written scale, with its
allocation counted as if reserved at that scale, on a hypothetical book: the
book as held with the units of the strategy's reserved contracts replaced by
the shadow holding (the core's fund is reserved to the core, so real units
would otherwise count twice). Every live check applies (the whole-book
`within`, the 0.95 headroom, the gap caps, the allocation at that scale), so
a buy the live path would refuse is recorded as the wait it would be.

The hypothetical book is evaluated on a copy: its verdict is never kept as
the mandate's verdict, never served, never counted in the forced reduction's
streak (a test pins this). Nothing is reserved: the owner's allocation is
not reduced by a strategy in shadow.

### The record

`daemon.db`'s append-only event log, scope `shadow/<strategy id>`, event
type `shadow.record.v1`, each event under a stable key (`shadow:<id>:<rule>:
<session date>:<kind>`), which the log keeps unique, so a retry or a second
evaluation never writes a record twice and the paper position cannot double.
The only writer is the background refresh after the scheduled verdict; reads
never write. Every record carries the strategy id, the rule, the spec hash
(or "compiled, unwritten"), the stage, the policy fingerprint and the time.

| Kind | When | Fields |
|---|---|---|
| `decision` | At each decision the rule makes or holds (the core: its month-end, once the decision day's close is in) | status and reasons; the rule's figures (the core: trend, σ and which binds, target shares at the written scale, the frozen digest) |
| `intent` | With the decision, before the session it is for (the core: the first session of its window) | the rule's own action on the shadow holding: kind (buy, sell, sell all, wait, none), quantity, why, the band test; the session it is for |
| `fill` | Once that session's close is in the decision-grade store | the simulated fill: the lab's R2 convention, the whole quantity at the session's close; the shadow holding after it |

The row terms (order type, limit, time in force, due time, venue, episode)
join `intent` when the core's phase 2 row builder exists: shadow and live
then call that one function, with a test that they agree, so no second row
builder is written now. The kind `controller` is reserved for Desk's
controller's would-send decisions, written through a private Desk method
once the controller runs on Torok; it is designed here and not built. Its
request ids and the shadow event keys carry the `shadow:` prefix, so they
can never collide with `desk.execution` request or episode keys.

A missing close leaves the fill pending, never guessed. A session the daemon
missed (it was stopped) is recorded on the next refresh from the kept
closes, under the same keys; a decision whose inputs are gone is recorded as
a hold with the reason.

What the record is for: the lab's R2-to-R3 gate checks that shadow fills and
signals match the backtest rules and that observed costs stay within the
double-cost case. The record gives the signals and fills from the code that
will trade; observed costs come with phase 2's rows, which read a live
quote. For the core it is also what W0 compares with the lab's
recomputation.

### It never reaches an order path

- No order-path file reads the shadow scope or event type (a test scans the
  order paths, as `TestOnlyTheDaemonEvaluates` scans evaluators).
- Shadow produces no proposal row; nothing in it can be previewed,
  submitted, prepared, queued or scheduled.
- The hypothetical verdict is never kept.

### Surfaces

The record is served on the strategy's own verdict: for the core, `shadow`
on `core.target` (`canary core` shows a Shadow section, `canary_core`
carries it): the stage, the paper position, the number of records and the
newest twelve. Golden render `core_shadow.txt`. `canary strategy ID`
serves any entry's verdict and record the same way (`strategy_shadow.txt`),
built with the evaluator.

## 4. Strategies declare their data

### Declarations

Each compiled rule declares, with its version: **series**, the daily closes
it reads (by ISIN and currency, judged on the listing's calendar, and how
many sessions); **chains**, the option-chain underlyings it reads on the
recorder's v1 plan. The recorder reads US option chains in USD on the US
session only, so a rule declaring any other chain is refused by a catalogue
test. core-ibcf-v1 declares its fund's closes on its listing, 253 sessions,
and no chains.

### What Canary collects

- Holdings: the mandate's series (every held stock and ETF, every option
  underlying, SPY, `EUR.<CCY>` for every held currency), the names a
  refused order asked for, and the recorder's held option underlyings.
- Registered strategies: every entry whose rule is compiled, in any stage,
  held or not; a chain's past can never be recorded later.
- The recorder's research list `chains.recorder.symbols`, which agents may
  set.

Series: the core's declared series is read where it is read today, in the
mandate's history pass (`coreSeriesNeed` from the resolved contract). The
pass is serial and the scheduled verdict waits for it, and the additions
gate refuses a verdict older than five minutes, so a later rule's declared
series must not join that pass: they get their own background loop after the
verdict, their own key namespace (`strategy:<id>:con:<contract id>`, never a
book key such as `u:SYM`), one series per entry; a test pins that no such
key enters the verdict's evidence. Built with the evaluator
(`refreshStrategies`, after the verdict, one read per entry per refresh).

Chains: the recorder's limits are unchanged (12 underlyings, four lines,
the reserve, the pacing back-off). Priority: held option underlyings first,
then live strategies' chains, then shadow and suspended strategies', then
the research list; the rest is recorded `skipped_symbol_cap` with its
source.

Journaled: the recorder's `run_symbols.source` names every declarer
(`held`, `explicit`, `strategy:<id>`, comma-joined), and the run row's
`settings_json` carries the declarations it ran on.

### The recorder without a switch

The recorder runs every session on the declarations. `chains.recorder.enabled`
becomes the owner's off switch: unset runs (today unset is off), `false`
stops it. The research list's default becomes empty (`null` restores `[]`,
where it restored SPX, SPY and QQQ): those were s1's names, and the overlay
has no strategy since 08:21. A session with nothing declared records nothing
and opens no store.

On the live machine the stored settings document carries no recorder block
(read 2026-10-18), so the switch is unset: once the daemon that carries this
is installed, the recorder records the held option underlyings once a
session, 30 minutes before the US close, inside its limits. A changelog line
says so.

### Settings: classes the daemon enforces

The settings registry (`rpc.SettingsKeys`) gives every key one class. The
daemon decides by the write's origin, deny by default; the CLI marks every
agent session's write `agent` (`DetectWriteOrigin`), and the console stamps
its own writes as the paired device:

| Class | Keys | Who may write |
|---|---|---|
| `safety` | `trading.freeze`, `trading.tws_order_control.enabled` | a human terminal only (as today) |
| `owner` | `features.stock_protection.enabled` (a protection control), the collection off switches `chains.recorder.enabled`, `regime.journal.enabled`, `stress.journal.enabled`, and every preference (display, rulebook, earnings overrides, the sweep's currency ordering) | a human terminal or the owner's paired device; an agent or a missing origin is refused |
| `collection` | `chains.recorder.symbols` | any origin; it governs market-data reads only |

Today an agent can write every key but `trading.*` at the daemon, and the
hook's guard can be stepped round: it refuses `settings set` only when the
two words are adjacent, while the CLI finds `set` after any flags
(`canary settings --json set …` runs). The classes close that at the daemon.
The hook then mirrors the CLI: any `canary settings` command with a `set`
word is a settings write, refused unless every other word is `--json` or a
`chains.recorder.symbols=` assignment; the refusal names the class. Tests
cover a flag before the verb, after the assignment, `-json`, a mixed
command and composition. The Codex rules match token prefixes and cannot
match a `key=value` token, so Codex agents stay refused for every settings
write; a rule for the flag-first form is added, and the daemon is the guard
either way. Data purchases are not settings; they stay the owner's.

## 5. Desk's entry rules and the scorecard

How the scorecard sleeve resolver classifies fills
(`scorecardSleeveResolver`): `rule` when the submitted proposal's bucket is
a rule bucket, or the journal origin is `daemon-preauthorised` or
`daemon-protective-guard`, or the transmit origin is `daemon_internal`;
otherwise the owner's.

- **Registered strategies' rows (2026-10-19).** A `strategy_rebalance` fill
  is matched by its order reference to the submitted row and scores in the
  entry's sleeve, origin `proposal:strategy_rebalance`, with the entry's id
  as its strategy reference; a `core_rebalance` fill in the core's, as before.
- **Entry rules today.** Desk cannot arm them, and `desk.execution.submit`
  (served since 2026-10-19 for proposal rows under the standing mandate)
  refuses an entry-rule addition `intent_not_served`, so no entry rule sends
  anything; the owner's console Adds are the owner's. The decision ("its fills count in the owner's sleeve") holds
  today. The live scorecard (2026-10-18) counts 518 owner entries and tags
  only 26 lifecycle fills as `rule`, none by proposal or origin.
- **What is not settled.** The review traced journaled fill events to an
  empty origin, which the journal stores as `daemon_internal`; by the
  resolver's last clause such a fill would read as `rule`, owner fills
  included, and an owner order queued for the open would too. The live tags
  show no such fill, which suggests the resolver's journal join may match
  none at all. Either way the journal-to-resolver path needs a round-trip
  test (append a lifecycle fill, read the evidence, resolve) before any
  classification rule is changed; the fix is then to classify a fill by its
  order's staged attempt (joined by order reference), never by a lifecycle
  event's transmit origin. That is its own task, not this build.
- **When Desk's automatic submission is served**, it must journal its own
  origin (`desk-automatic`) with the item's source, and the resolver must
  count an entry-rule addition as the owner's (coverage origin
  `desk-entry-rule`), a registered strategy's row in its sleeve, and a
  protection or reduction proposal as `rule` by its bucket. A condition of
  that work, with its test.
- **When an entry rule is to add risk on its own**, it is registered: a
  compiled rule with a lab spec, a `[mandate.strategies.<id>]` entry with its
  own sleeve and allocation, shadow first, its orders through Canary's rows.
  Desk's entry-rule settings then configure nothing that adds risk outside
  the register.
- Register sleeves in the scorecard (a registered strategy's bucket in its
  sleeve, and whether a hand trade of the core's fund scores as the owner's
  decision or the core's, `core-sleeve.md` item 5) are decided with the
  round-trip work; until a registered strategy sends, they change nothing.

## 6. Authority and evidence

| Concept | Authoritative source | Typed field/contract | Freshness/finality | Fallback or blocker |
|---|---|---|---|---|
| Entries | `risk-policy.toml` `[mandate.core]`, `[mandate.strategies.<id>]` | `risk.StrategyEntry` via `ConstitutionMandate.Register()` | the manager's 30 s reread; a change is a revision (fingerprinted) | absent ⇒ no machine strategy; a missing key ⇒ that strategy holds |
| Compiled rules | the binary | `register.Rule` | per build | not compiled ⇒ hold |
| Effective stage and reservation | `register.Resolve` in the daemon | the verdict's `register` and its allocation plan | with the mandate verdict | hold ⇒ reserves nothing, unless live with positions |
| Shadow record | `daemon.db` event log, scope `shadow/<id>` | `shadow.record.v1` | append-only, unique per key | storage failure ⇒ retried on the next refresh under the same key; the verdict is unaffected |
| Declared data | the catalogue and the book | series needs, recorder plan | per refresh, per run | over the cap ⇒ `skipped_symbol_cap`, named |
| Settings classes | `rpc.SettingsKeys` | `SettingsKeySpec.Class` | per write | a wrong origin ⇒ refused |

## 7. Exceptions and change control

- Who may change an entry: the owner, by editing the file with a higher
  `policy_version`, recording the instruction and the time in the file. An
  agent writes no entry, no promotion and no stage on its own initiative.
- A promotion (shadow to live, a scale step) cites the lab's recorded verdict
  in the provenance comment; the code checks the hash, not the verdict.
- Rollback: `stage = "suspended"` stops the strategy and releases its
  reservation; removing the entry ends it. Its positions stay in its sleeve
  until sold; the forced reduction cuts a sleeve over its allocation first.
- No entry can admit an order the mandate gate refuses: it reserves room
  inside the budget and never widens it.

## 8. Operating cadence

- Daily: `canary mandate` lists the register (stage written and effective,
  reservation, holds, declared data); `canary core` the core's stage and its
  shadow records.
- At a strategy's decision: the shadow record's `decision` and `intent`
  after the decision close; its `fill` after the next session's close.
- Missed run: the next refresh records a missed session from the kept
  closes under the same keys; nothing is guessed.
- Before a promotion: the lab's R2 verdict on the shadow record, then the
  owner's edit.

## 9. Safety invariants

1. No machine strategy reserves allocation or sends rows without an entry
   the owner wrote naming a compiled rule, a signal the library carries and
   is parity-tested for, and the lab spec it cites: a compiled instance's
   hash must match, a parameterised rule's is the citation
   (`strategy-evaluator.md` §4).
2. A missing key holds only its strategy and never changes the rest of the
   policy.
3. Old files keep their fingerprints and their behaviour.
4. Shadow runs the live code on a paper position; it produces no proposal
   row, its hypothetical verdict is never kept, and no order path reads its
   record.
5. A reservation moves only with the owner's written stage, scale or
   removal, never with data or a build.
6. Collection runs on declarations inside the existing budgets; agents may
   widen the research list only; every safety control, the protection
   control and every off switch stay with the owner, enforced at the daemon.
7. Entry-rule fills count as the owner's until the rule is registered.

| Failure | Guard |
|---|---|
| A new build changes a rule's spec while the policy names the old one | the hash check holds the strategy; a live one keeps its reservation while it holds positions |
| The owner writes `live` on a rule this build serves no rows for | the file is refused at load, naming the entry; the last good policy stays |
| Two entries for one sleeve or rule | the file is refused; the last good policy stays |
| A shadow fill written twice | the event log's unique key; one writer |
| The core's real holding and the shadow holding both counted | the hypothetical book replaces the reserved contract's units |
| A declared series ages the verdict | later rules' series ride their own loop after the verdict |
| An agent writes a protection or safety setting | refused at the daemon by class; the hook refuses it first |

## 10. Verification

Synthetic data only.

- `internal/risk` and the policy manager: a file without the new keys keeps
  both fingerprints (pinned); the core's absent `stage` reads shadow and its
  absent `spec_hash` the compiled spec; `live` without `spec_hash` holds the
  core and names the key; with `stage` written the core's scale must be on
  the ladder; a new entry missing any key holds and names it, and the
  constitution's approval list is unchanged; duplicate sleeves or rules, a
  reserved sleeve, `[mandate.strategies.core]`, an overlay entry with
  `risk_allocation_pct`, and allocations above the target each refuse the
  file.
- `internal/register`: every row of section 2; the parity fixture pinned to
  the rule and its hash; no non-US chain declaration.
- `internal/daemon`: the allocation plan reads the register (the core live
  at 25 reserves a quarter of `core_vol_pct` only when its rule sends;
  shadow, suspended, a held entry without positions, `Sends` false with no
  stage reserve nothing; a held live entry with positions keeps its
  reservation; a data hold keeps a live reservation); the overlay line kept;
  the core's buy block reads the register; the shadow recorder writes
  decision, intent and fill once each under retries and a second
  evaluation, the paper position starts at zero and moves only by fills, the
  reserved contract's real units are replaced, a missing close leaves the
  fill pending; the hypothetical verdict never reaches the verdict cache;
  the order-path scan finds no reader of the shadow scope; the recorder runs
  with the switch unset and stops on `false`, its plan orders held, live,
  shadow, research names; the settings classes refuse an agent's and a
  missing origin's write of `owner` and `safety` keys, a paired device's of
  `safety` keys, and accept an agent's `collection` write.
- Hook: `canary settings set chains.recorder.symbols=SPX` passes; a flag
  before the verb, a flag after, `-json`, `trading.freeze=true`,
  `features.stock_protection.enabled=false`, `chains.recorder.enabled=false`,
  a mixed command and a composed command are refused.
- CLI goldens: `mandate_register`, `core_shadow`, `chains_recorded*`.

## 11. The core's entry (proposed; the owner writes it)

Nothing needs writing for today's behaviour: without the two new keys the
core runs in shadow, as now. To cite the lab's spec in the policy, as the
register asks of every strategy, the owner may add to `[mandate.core]`,
after installing the daemon that reads them:

```toml
spec_hash = "2408d09e618904c84219cacf6bc0f599e1a00ee3c4b04b0effbcc4521f45a441"  # lab s0_core_sleeve, registry seq 30 (2026-10-14)
stage     = "shadow"
```

With `stage` written, `scale_pct` must be 25, 50 or 100 (the live file has
25). The core goes live only with `stage = "live"`, once phase 2's rows
exist, at `scale_pct = 25` for the first step (W2).

## 12. Build

One Canary branch, `strategy-register`; each increment passes `make test`
and `make check`:

1. Settings classes at the daemon and the hook fix (review B1).
2. The register: schema, `Register()`, validation, the catalogue and
   `Resolve`; the allocation plan and the core's buy block read it;
   `canary mandate` shows it.
3. The core's shadow record, served on `canary core` and `canary_core`.
4. The recorder on declarations: the off switch, the empty research list,
   the declared chains, the plan's priority; changelog, goldens, docs.

Not in this build: the `controller` kind, `intent` row terms (with phase
2), register sleeves in the scorecard and the resolver's round-trip fix (its
own task), placement for non-core strategies. The declared-series loop and
the generic surface (`canary strategy ID`) came with the evaluator
(`strategy-evaluator.md`, 2026-10-18).

Desk: `docs/strategy-engine.md` moves findings 2 to 5 from target to state;
a re-pin only if Desk reads a new field.

## Residual risk accepted

- The core's first form lets its stage and spec hash go unwritten while in
  shadow; every other entry must write them.
- The shadow fill follows the lab's R2 convention (the session's close), not
  the midpoint limit the live row will send; it is not evidence of the live
  fill rate, and observed costs come only with phase 2's rows.
- The recorder's line and message use is on every session; it was designed
  to lose to interactive work.
- The register checks the spec hash, not the lab's verdict: a promotion the
  lab did not pass is the owner's decision, recorded in the file.
- Until the round-trip test, how the scorecard classifies journaled fills is
  not established; the decision about entry-rule fills holds because no
  entry rule sends.

## Independent review

An independent review of the first draft (2026-10-18, read-only, against
`f1a1b17c`) found one blocker and four serious items.

| # | Finding | Resolution |
|---|---|---|
| B1 | The hook refuses `settings set` only when the words are adjacent; the CLI finds `set` after flags, and the daemon checks origin only for `trading.*`, so an agent can write any other key (stock protection, the journals) today | Classes enforced at the daemon by origin, deny by default; `features.stock_protection.enabled` is an owner key; the hook mirrors the CLI and allows only a collection assignment; tests for each shape (section 4) |
| S1 | The draft's account of the scorecard resolver did not hold: journaled fills carry an empty origin stored as `daemon_internal`, so the owner-queued fix had nothing to match | Live tags show only lifecycle; the resolver's journal path needs a round-trip test first, then classification by the order's staged attempt; out of this build (section 5) |
| S2 | Declared series in the mandate's serial history pass can age the verdict past five minutes; a declared need sharing a book key can replace the book's contract or calendar | The core's need stays where it is; later rules' series get their own loop, namespace and compiled cap (section 4) |
| S3 | An entry hold releases a live reservation, so a build event reshuffles allocations and cuts the strategy first | A held live strategy keeps its reservation while its sleeve holds positions (section 2) |
| S4 | Shadow: the core's real holding counted twice; `intent` row terms would be a second row builder; observations are not unique; the hypothetical verdict could be kept | Real units replaced in the hypothetical book; `intent` is the rule's action, row terms with phase 2's one builder; the event log's unique keys and one writer; the hypothetical verdict never kept, with a test (section 3) |
| M1-M12 | Chain priority and US-only chains; the recorder flip's changelog, goldens and docs, and the live setting unset; the overlay line kept; one `Resolve` read by every call site; the core's scale on the ladder once `stage` is written; the parity fixture pinned to the hash; Option B terms withdrawn on a register edit and namespaced keys; terms; Meaning and Operating cadence sections; "compiled, unwritten"; hand trades of the core's fund in the scorecard; no Codex copy of the hook | Each folded in above |

## Files

```
internal/register/                     compiled rule catalogue, Resolve (the evaluator's instance per entry)
internal/risk/register.go              [mandate.strategies], Register(), validation, the evaluator's keys
internal/risk/mandate.go               the core's spec_hash, stage and kill_drawdown_k
internal/daemon/mandate_allocations.go the allocation plan from the register
internal/daemon/strategy_target.go     the evaluator: every entry's verdict, the core first (was core_target.go)
internal/daemon/core_resolve.go        one contract pin per instrument
internal/daemon/shadow_record.go       the shadow recorder, its read and the kill rule's drawdown
internal/rpc/shadow.go                 the shadow record's types
internal/daemon/chain_recorder.go      declared chains, the off switch
internal/chainrec/select.go            the plan's priority and sources
internal/rpc/settings.go               settings classes
internal/daemon/platform_settings.go   class enforcement by origin
internal/cli/mandate.go, core.go       the register, the core's stage and shadow
internal/mcp/tools.go                  canary_mandate's register, canary_core's stage and shadow
hooks/canary-pre-tool-use.sh, hooks/canary-pre-tool-use_test.sh, .codex/rules/canary.rules
internal-docs/design/option-chain-recorder.md, platform-settings.md,
mandate-risk-budget.md ([mandate.overlay] proposal replaced by the register),
core-sleeve.md (the core as the register's first entry), decision-scorecard.md
(the resolver's open question)
```
