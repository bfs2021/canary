# Long-option exits: protect, harvest, time

Written 2026-10-24 08:05 CEST at the owner's instruction (07:57 CEST) as the
requirements for how the desk runs a long option once it is held. It states
what must be true, not how; the mechanics live in the designs it names.
Current mechanics: [option-exit-policy.md](option-exit-policy.md).

Long options on volatile stocks make their money by staying in winners and
keep it by cutting losers and resizing. An exit has one of three jobs, each
with its own rules. Status per requirement: **built**, **partly**, **missing**,
as of Canary 278cc600.

## Protect: cut what is wrong

| ID | Requirement | Canary today |
|---|---|---|
| LO-P1 | An exit fires when the stock breaks the idea (a level on the underlying, or the event that was the thesis), not when the option's price wobbles. | missing |
| LO-P2 | A last-resort loss on the premium backs it up. | built: loss exit at −60% (Rulebook loss line), full close, owner approval |
| LO-P3 | A protect exit sells with a patient limit inside the spread, never at market. | partly: previews price patient limits; the profit trail is a broker trail that becomes a market order |

## Harvest: keep winners, take risk off

| ID | Requirement | Canary today |
|---|---|---|
| LO-H1 | A winner that grows past its share of the risk budget (premium per name over 15% of NLV) is trimmed back to it; the rest stays on. | partly: the cap blocks additions (2026-10-23); book-level budget reductions exist; no per-name trim |
| LO-H2 | After a large gain, enough is sold to recover the cost; the rest runs. | missing: every exit is a full close |
| LO-H3 | A winner can be rolled up or out: sell it, buy a further strike or later expiry, keep the direction with less money at risk. | missing: deferred since 2026-09-28 |
| LO-H4 | Any trail follows the stock and scales to how much the stock moves, so one big option day does not stop the position out. | missing: today's trail is on the option premium, armed at +50%, full close |

## Time: never let decay or expiry decide

| ID | Requirement | Canary today |
|---|---|---|
| LO-T1 | An in-the-money option is closed or rolled before expiry, so nothing is exercised by accident. | built: expiry close at the Rulebook's runway (7 days), owner approval |
| LO-T2 | An option is rolled or closed before its last weeks, when decay speeds up, unless the owner keeps it on purpose. | partly: theta hygiene proposes closes; no roll |

## Control and budget

| ID | Requirement | Canary today |
|---|---|---|
| LO-C1 | An armed exit acts after a short veto window; the owner can stop it, not stall it. | partly: running for stock stops; option buckets exist (`option_loss_exit`, `option_profit_trail`) but are not pre-authorised |
| LO-C2 | A position counts against the premium budget at its exit distance while its exit is armed, at full premium otherwise. | missing |
| LO-C3 | Every exit says which job it does and why, in one line. | partly: proposals carry reasons; no job label |

## Verification

| ID | Requirement | Canary today |
|---|---|---|
| LO-V1 | Before a rule acts without the owner, it is replayed on the owner's own option trades and compared with what the owner did, net of spread and fees, every price marked as recorded or modelled. | missing: Edge builds no option counterfactual (its method says so) |
