package daemon

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/osauer/canary/v2/internal/core"
	"github.com/osauer/canary/v2/internal/rpc"
)

// desk.execution.submit with the automatic authorisation is the one path
// through which Desk's controller sends a served proposal row under the
// owner's standing mandate (desk-execution.md; core-sleeve.md, phase 3):
// the row the engine sized, through the same preview, admission and wire
// guard as a hand submission, with the mandate's fence re-read under the
// wire lock before the first byte. The daemon invents no authority: the
// mandate is the record the owner confirmed on a device
// (handleDeskAuthorityConfirm), presented by the controller with its private
// capability, generation and preferences hash, and every row still passes
// Canary's own gates. A refused row is journaled with its reason and its
// episode is consumed, so nothing retries it blindly; the next session's row
// is a new episode.
const (
	deskExecutionMaxItems = 20
	// deskExecutionFenceWindow bounds one item's send: admission, preview
	// and the wire guard must finish inside it or the guard refuses.
	deskExecutionFenceWindow = 2 * time.Minute
	// deskBlockerEpisodeMismatch: a strategy row's item does not name the
	// row's own episode for the session (deskExecutionStrategyEpisode).
	deskBlockerEpisodeMismatch = "episode_mismatch"
	// deskBlockerIntentNotServed: an automatic stock entry (intent.add) has
	// no served path yet; only proposal rows are sent.
	deskBlockerIntentNotServed    = "intent_not_served"
	deskBlockerJournalUnavailable = "execution_journal_unavailable"
)

// deskExecutionGrant is the request-scoped fact the write gate checks for
// the desk-automatic origin: which proposal the controller's submission is
// sending right now and the mandate fence the wire guard re-reads. It
// exists only while the submission holds brokerWriteMu.
type deskExecutionGrant struct {
	Key, Revision, Class string
	Fence                deskAuthorityFence
}

// deskExecutionClass is Canary's own classification of a served row for the
// Desk contract, mirroring Desk's batch groups: protection (stops and
// trails), reductions (sales that reduce or close) and, for a strategy row
// only, an addition (the buy of the core's or another live entry's row). A buy outside a strategy row, a cash
// sweep or a conversion has no class here and is refused class_mismatch.
func deskExecutionClass(p rpc.TradeProposal) string {
	kind, side := strings.ToUpper(strings.TrimSpace(p.OrderType)), strings.ToUpper(strings.TrimSpace(p.Action))
	switch {
	case p.Bucket == rpc.TradeProposalBucketCashSweep, p.Bucket == rpc.TradeProposalBucketCurrencyLeveling:
		return ""
	case rpc.StrategyRowBucket(p.Bucket):
		if side == rpc.OrderActionBuy {
			return rpc.DeskClassAdd
		}
		return rpc.DeskClassReduce
	case strings.HasPrefix(kind, "TRAIL"), strings.HasPrefix(kind, "STP"), p.Bucket == rpc.TradeProposalBucketTrailingStop:
		return rpc.DeskClassProtect
	case side == rpc.OrderActionBuy:
		return ""
	default:
		return rpc.DeskClassReduce
	}
}

// deskExecutionStrategyEpisode is the episode a strategy row's item must
// name: the row's month episode and the session it is for, so one decision
// yields at most one order a session, whatever the controller retains.
func deskExecutionStrategyEpisode(p rpc.TradeProposal) string {
	if p.Core == nil || p.Core.Terms.Episode == "" || p.Core.Session == "" {
		return ""
	}
	return p.Core.Terms.Episode + ":" + p.Core.Session
}

// deskExecutionStrategyName names the register entry a strategy row belongs
// to, for the journal and the receipt.
func deskExecutionStrategyName(p rpc.TradeProposal) string {
	if p.Bucket == rpc.TradeProposalBucketCoreRebalance && p.Core != nil {
		return fmt.Sprintf("strategy %s, sleeve %s", core.RuleID, nonEmptyString(p.Core.Sleeve, rpc.ScorecardSleeveCore))
	}
	if p.Bucket == rpc.TradeProposalBucketStrategyRebalance && p.Core != nil {
		return fmt.Sprintf("strategy %s (%s), sleeve %s", p.Core.Strategy, nonEmptyString(p.Core.Rule, "rule unnamed"), p.Core.Sleeve)
	}
	return ""
}

func deskFenceAction(class string) string {
	switch class {
	case rpc.DeskClassProtect:
		return "protection"
	case rpc.DeskClassReduce:
		return "reduction"
	case rpc.DeskClassAdd:
		return "entry"
	}
	return ""
}

// deskAutomaticOriginBlockers is the origin-specific policy for the
// controller's submissions: the write is accepted only while a submission
// holds a grant for one exact proposal (under brokerWriteMu) whose fence has
// not expired. Every other gate is the same as for a human write, and the
// wire guard re-reads the mandate itself.
func (s *Server) deskAutomaticOriginBlockers() []rpc.TradingBlocker {
	if s == nil {
		return []rpc.TradingBlocker{{Code: "daemon_origin_unauthorised", Message: "desk-automatic writes require the daemon's Desk execution handler"}}
	}
	grant := s.deskExecutionGrant.Load()
	if grant == nil {
		return []rpc.TradingBlocker{{Code: "daemon_origin_unauthorised", Message: "desk-automatic writes are issued only by the daemon's Desk execution handler under a standing mandate; no submission is in progress", Action: "Confirm the row on your device instead."}}
	}
	if grant.Fence.ValidUntil.IsZero() || !s.orderNow().Before(grant.Fence.ValidUntil) {
		return []rpc.TradingBlocker{{Code: rpc.DeskBlockerAuthorityChanged, Message: "the automatic submission's fence expired before admission; nothing was sent"}}
	}
	return nil
}

// deskSubmitBlockers is the proposal-side half of the origin gate: the
// revalidated proposal must be the one the grant names, Canary must classify
// it as the class the controller claimed, a strategy row's item must name the
// row's session episode, and config.toml must not pause automation.
func (e *proposalEngine) deskSubmitBlockers(prop rpc.TradeProposal, grant *deskExecutionGrant, episode string) []rpc.TradingBlocker {
	if e == nil || e.server == nil || grant == nil {
		return []rpc.TradingBlocker{{Code: "daemon_origin_unauthorised", Message: "desk-automatic submission requires the daemon's Desk execution handler"}}
	}
	if e.server.deskExecutionGrant.Load() != grant || grant.Key != prop.Key || grant.Revision != prop.Revision {
		return []rpc.TradingBlocker{{Code: "daemon_origin_unauthorised", Message: "desk-automatic submission grant does not name this proposal key and revision", Action: "Confirm the row on your device instead."}}
	}
	if class := deskExecutionClass(prop); class == "" || class != grant.Class {
		return []rpc.TradingBlocker{{Code: rpc.DeskBlockerClassMismatch, Message: fmt.Sprintf("Canary classifies this row as %q, not %q; nothing was sent", nonEmptyString(class, "no automatic class"), grant.Class)}}
	}
	if want := deskExecutionStrategyEpisode(prop); want != "" && want != episode {
		return []rpc.TradingBlocker{{Code: deskBlockerEpisodeMismatch, Message: fmt.Sprintf("a strategy row's item names the row's session episode %q; this item names %q", want, episode)}}
	}
	if paused, sections := e.server.configPausesAutomation(); paused {
		return []rpc.TradingBlocker{{Code: "config_automation_paused", Message: fmt.Sprintf("automatic submission is paused while config.toml [%s] runs on Canary's defaults", strings.Join(sections, "], [")), Action: "Fix config.toml and run `canary restart`, or confirm the row on your device."}}
	}
	return nil
}

// deskAutomaticAuthority verifies the standing mandate the controller
// presents against the daemon's own record: the terms, the private
// capability, the generation and preferences hash it reconciled against, the
// controller running in this process, an armed scope and this account. A
// mismatch refuses every item with authority_changed before any journaling;
// the wire guard re-reads the same record under the wire lock.
func (s *Server) deskAutomaticAuthority(ctx context.Context, in rpc.DeskAutomaticAuthorization, scope brokerStateScope) (deskAuthorityRecord, *rpc.TradingBlocker, error) {
	s.deskAuthorityMu.RLock()
	defer s.deskAuthorityMu.RUnlock()
	current, err := s.readDeskAuthority(ctx)
	if err != nil {
		return deskAuthorityRecord{}, nil, err
	}
	refuse := func(msg string) (deskAuthorityRecord, *rpc.TradingBlocker, error) {
		return deskAuthorityRecord{}, &rpc.TradingBlocker{Code: rpc.DeskBlockerAuthorityChanged, Message: msg + "; nothing was sent"}, nil
	}
	if current.Terms == nil || current.Terms.ID != in.ID || len(in.Capability) < 32 || len(in.Capability) > 256 {
		return refuse("no standing automatic authority matches this request")
	}
	hash := sha256.Sum256([]byte(in.Capability))
	expected, _ := hex.DecodeString(current.Terms.ControllerHash)
	if subtle.ConstantTimeCompare(hash[:], expected) != 1 {
		return refuse("private controller authority does not match the confirmed mandate")
	}
	if current.Generation != in.ExpectedGeneration {
		return refuse("the mandate's generation changed; reconcile and present it again")
	}
	if in.PreferencesHash == "" || current.PreferencesHash != in.PreferencesHash {
		return refuse("trading preferences changed since the controller reconciled")
	}
	if current.Scope != "protect" && current.Scope != "full" {
		return refuse("automatic trading is disarmed")
	}
	if !current.Running {
		return refuse("the controller is not confirmed running under this daemon process")
	}
	if current.Terms.AccountID != scope.Account || current.Terms.AccountMode != scope.Mode {
		return refuse("the confirmed mandate names another account")
	}
	return current, nil, nil
}

func (s *Server) handleDeskExecutionSubmit(ctx context.Context, req *rpc.Request) (rpc.DeskExecutionSubmitResult, error) {
	var in rpc.DeskExecutionSubmitParams
	if err := decodeDeskAuthority(req.Params, &in); err != nil {
		return rpc.DeskExecutionSubmitResult{}, err
	}
	switch {
	case (in.Batch == nil) == (in.Automatic == nil):
		return rpc.DeskExecutionSubmitResult{}, errBadRequest("exactly one authorisation, batch or automatic, is required")
	case in.Batch != nil:
		return rpc.DeskExecutionSubmitResult{}, errBadRequest("manual batch submission is not served; confirm prepared proposals on the device")
	case len(in.Items) == 0 || len(in.Items) > deskExecutionMaxItems:
		return rpc.DeskExecutionSubmitResult{}, errBadRequest(fmt.Sprintf("name 1 to %d items", deskExecutionMaxItems))
	}
	seen := map[string]bool{}
	for _, item := range in.Items {
		if err := item.Validate(); err != nil {
			return rpc.DeskExecutionSubmitResult{}, errBadRequest(err.Error())
		}
		if seen[item.RequestID] {
			return rpc.DeskExecutionSubmitResult{}, errBadRequest("request IDs within one submission are distinct")
		}
		seen[item.RequestID] = true
	}
	if err := rpc.ValidateAutomaticOrder(in.Items); err != nil {
		return rpc.DeskExecutionSubmitResult{}, errBadRequest(err.Error())
	}
	if s.tradeProposals == nil {
		return rpc.DeskExecutionSubmitResult{}, errors.New("proposal engine is unavailable")
	}
	scope, err := s.deskExecutionScope()
	if err != nil {
		return rpc.DeskExecutionSubmitResult{}, err
	}
	now := s.orderNow()
	out := rpc.DeskExecutionSubmitResult{Receipts: make([]rpc.DeskExecutionReceipt, 0, len(in.Items)), AsOf: now}
	authority, blocker, err := s.deskAutomaticAuthority(ctx, *in.Automatic, scope)
	if err != nil {
		return rpc.DeskExecutionSubmitResult{}, err
	}
	if blocker != nil {
		for _, item := range in.Items {
			out.Receipts = append(out.Receipts, deskExecutionRefusal(item, now, *blocker))
		}
		return out, nil
	}
	priorUnknown := false
	for _, item := range in.Items {
		out.Receipts = append(out.Receipts, s.deskExecutionSubmitItem(ctx, scope, authority, item, &priorUnknown, in.TimeoutMs))
	}
	return out, nil
}

func deskExecutionRefusal(item rpc.DeskExecutionItem, now time.Time, blockers ...rpc.TradingBlocker) rpc.DeskExecutionReceipt {
	r := rpc.DeskExecutionReceipt{RequestID: item.RequestID, EpisodeID: item.EpisodeID, Digest: rpc.DeskIntentDigest(item), Class: item.Intent.Class, Outcome: rpc.DeskOutcomeRefused, Blockers: blockers, AsOf: now}
	if len(blockers) > 0 {
		r.Message = blockers[0].Message
	}
	return r
}

// deskExecutionSubmitItem sends one item: journaled before anything can
// reach the broker, then one attempt through the proposal engine under the
// grant, then its outcome journaled. A retry of a journaled request answers
// with that request's receipt and sends nothing.
func (s *Server) deskExecutionSubmitItem(ctx context.Context, scope brokerStateScope, authority deskAuthorityRecord, item rpc.DeskExecutionItem, priorUnknown *bool, timeoutMs int) rpc.DeskExecutionReceipt {
	now := s.orderNow()
	class := item.Intent.Class
	if item.Intent.Proposal == nil {
		return deskExecutionRefusal(item, now, rpc.TradingBlocker{Code: deskBlockerIntentNotServed, Message: "automatic stock entries are not served; only served proposal rows are sent under the mandate"})
	}
	if class == rpc.DeskClassAdd && *priorUnknown {
		return deskExecutionRefusal(item, now, rpc.TradingBlocker{Code: rpc.DeskBlockerPriorUnknown, Message: "an earlier item of this submission ended unknown; additions after it are not sent"})
	}
	// A strategy row is sent in session at the live mid, inside its window.
	// One that is not ready now (its exchange closed, the opening window, a
	// stale quote) is refused before journaling, so the session's episode
	// stays free for the controller's next pass while the row is ready.
	if blocker := s.deskExecutionStrategyNotReady(ctx, item.Intent.Proposal.Key, item.Intent.Proposal.Revision); blocker != nil {
		return deskExecutionRefusal(item, now, *blocker)
	}
	prior, refusal, err := s.recordDeskExecutionRequest(ctx, scope, item, "automatic:"+authority.Terms.ID)
	if err != nil {
		return deskExecutionRefusal(item, now, rpc.TradingBlocker{Code: deskBlockerJournalUnavailable, Message: "the execution journal could not record the request, so nothing was sent: " + err.Error()})
	}
	if refusal != nil {
		return deskExecutionRefusal(item, now, *refusal)
	}
	if prior {
		receipt, err := s.deskExecutionReceipt(ctx, scope, item.RequestID, now)
		if err != nil {
			receipt = rpc.DeskExecutionReceipt{RequestID: item.RequestID, EpisodeID: item.EpisodeID, Digest: rpc.DeskIntentDigest(item), Class: class, Outcome: rpc.DeskOutcomeUnknown, Message: "the request was journaled before and its outcome cannot be read: " + err.Error(), AsOf: now}
		}
		return receipt
	}
	var outcome deskExecutionOutcomeRow
	if class == rpc.DeskClassAdd && authority.Scope != "full" {
		outcome = deskExecutionOutcomeRow{Outcome: rpc.DeskOutcomeRefused, Blockers: []rpc.TradingBlocker{{Code: rpc.DeskBlockerAuthorityChanged, Message: "additions are outside the armed scope: Protection sends protection and reductions only; nothing was sent"}}}
	} else {
		outcome = s.deskExecutionAttempt(ctx, authority, item, timeoutMs)
	}
	if outcome.Message == "" && len(outcome.Blockers) > 0 {
		outcome.Message = outcome.Blockers[0].Message
	}
	if err := s.recordDeskExecutionOutcome(ctx, scope, item.RequestID, outcome); err != nil {
		s.warnf("desk execution: record outcome for %s: %v", item.RequestID, err)
	}
	if outcome.Outcome == rpc.DeskOutcomeUnknown {
		*priorUnknown = true
	}
	switch outcome.Outcome {
	case rpc.DeskOutcomeAccepted:
		s.infof("desk execution: %s sent under mandate %s (%s): %s", item.Intent.Proposal.Key, authority.Terms.ID, class, outcome.OrderRef)
	default:
		s.warnf("desk execution: %s not sent under mandate %s: %s: %s", item.Intent.Proposal.Key, authority.Terms.ID, outcome.Outcome, outcome.Message)
	}
	return rpc.DeskExecutionReceipt{RequestID: item.RequestID, EpisodeID: item.EpisodeID, Digest: rpc.DeskIntentDigest(item), Class: class, Outcome: outcome.Outcome, OrderRef: outcome.OrderRef, Blockers: outcome.Blockers, Message: outcome.Message, AsOf: s.orderNow()}
}

// deskExecutionStrategyNotReady reads a strategy row's readiness before
// anything is journaled: first the served row's (its exchange closed, the
// opening window, the broker link down, a freeze), then a preview's (a
// stale or delayed quote, a spread too wide, a session the preview refuses),
// which only a quote read can tell. A row not ready now is refused with the
// readiness code and nothing is journaled, so the session's episode stays
// free for the controller's next pass. A hard refusal (the row withdrawn,
// its revision stale) passes to the attempt, which journals it. Rows outside
// a strategy pass to the gate, which decides. The preview reads the broker
// and sends nothing; the quote-stale half was added 2026-10-18 23:30 CEST
// after the review found the stale-quote refusal journaled and the episode
// consumed, against the design's "refused before journaling".
func (s *Server) deskExecutionStrategyNotReady(ctx context.Context, key, revision string) *rpc.TradingBlocker {
	engine := s.tradeProposals
	if engine == nil {
		return nil
	}
	prop, ok := servedProposal(engine.snapshotForRead(false, false), key)
	if !ok || deskExecutionStrategyEpisode(prop) == "" {
		return nil
	}
	notReady := func(r *rpc.TradeProposalReadiness) *rpc.TradingBlocker {
		if r == nil || r.Code == rpc.ReadinessReady || r.Code == rpc.ReadinessNotExecutable {
			return nil
		}
		return &rpc.TradingBlocker{Code: nonEmptyString(r.Code, rpc.ReadinessNotExecutable),
			Message: fmt.Sprintf("the strategy row is not ready now (%s): it is sent in session at the live mid, inside its window; nothing was journaled, so the controller may send it once it reads ready", nonEmptyString(r.Message, r.Code))}
	}
	if prop.Readiness != nil && prop.Readiness.Code != rpc.ReadinessReady {
		// Served not ready: a hard code here is the row's own blocker, which
		// the attempt journals as before.
		return notReady(prop.Readiness)
	}
	res, err := engine.Preview(ctx, rpc.TradeProposalPreviewParams{Key: key, Revision: revision})
	if err != nil || res.Readiness == nil {
		return nil
	}
	return notReady(res.Readiness)
}

// deskExecutionAttempt is one attempt under brokerWriteMu: the grant with
// the mandate's fence is stored for the write gate and the wire guard, the
// proposal engine revalidates, previews and places the row exactly as a hand
// submission would, and the order journal decides what the attempt proved.
func (s *Server) deskExecutionAttempt(ctx context.Context, authority deskAuthorityRecord, item rpc.DeskExecutionItem, timeoutMs int) deskExecutionOutcomeRow {
	s.brokerWriteMu.Lock()
	defer s.brokerWriteMu.Unlock()
	engine := s.tradeProposals
	if engine == nil {
		return deskExecutionOutcomeRow{Outcome: rpc.DeskOutcomeRefused, Blockers: []rpc.TradingBlocker{{Code: "proposal_engine_unavailable", Message: "proposal engine is unavailable"}}}
	}
	now := s.orderNow()
	grant := &deskExecutionGrant{Key: item.Intent.Proposal.Key, Revision: item.Intent.Proposal.Revision, Class: item.Intent.Class,
		Fence: deskAuthorityFence{ID: authority.Terms.ID, Generation: authority.Generation, PreferencesHash: authority.PreferencesHash, AdaptivePriority: authority.AdaptivePriority, Action: deskFenceAction(item.Intent.Class), ValidUntil: now.Add(deskExecutionFenceWindow)}}
	s.deskExecutionGrant.Store(grant)
	defer s.deskExecutionGrant.Store(nil)
	var placeErr error
	res, err := engine.submit(ctx, rpc.TradeProposalSubmitParams{Key: grant.Key, Revision: grant.Revision, Origin: rpc.OrderOriginDeskAutomatic, TimeoutMs: timeoutMs},
		proposalSubmitOptions{desk: grant, deskEpisode: item.EpisodeID, placeRefused: func(e error) { placeErr = e }})
	return s.deskExecutionOutcomeOf(res, err, placeErr, authority)
}

// deskExecutionOutcomeOf maps one attempt onto the contract's outcomes.
// Accepted is the engine's own acceptance. Anything else is decided by the
// order journal for the attempt's preview token, which the place path stages
// before the first frame: no attempt or a provably unsent one is refused,
// a staged attempt without a known outcome is unknown, and a send the journal
// shows reached the broker is accepted. An unreadable journal is unknown.
func (s *Server) deskExecutionOutcomeOf(res rpc.TradeProposalSubmitResult, err, placeErr error, authority deskAuthorityRecord) deskExecutionOutcomeRow {
	sent := fmt.Sprintf("sent by Desk's controller under the standing mandate %s (%s)", authority.Terms.ID, map[string]string{"protect": "Protection", "full": "Fully armed"}[authority.Scope])
	if name := deskExecutionStrategyName(res.Proposal); name != "" {
		sent += " for " + name
	}
	if err == nil && res.Accepted {
		return deskExecutionOutcomeRow{Outcome: rpc.DeskOutcomeAccepted, OrderRef: res.OrderRef, Message: sent}
	}
	reason := automaticOutcomeReason(res, err)
	if placeErr != nil {
		reason = placeErr.Error()
	}
	blockers := res.Blockers
	if len(blockers) == 0 {
		blockers = []rpc.TradingBlocker{{Code: "submit_failed", Message: reason}}
	}
	if res.PreviewTokenID == "" || s.orderJournal == nil {
		return deskExecutionOutcomeRow{Outcome: rpc.DeskOutcomeRefused, Blockers: blockers, Message: reason}
	}
	events, loadErr := s.orderJournal.LoadEvents(0)
	if loadErr != nil {
		return deskExecutionOutcomeRow{Outcome: rpc.DeskOutcomeUnknown, Blockers: blockers, Message: "the order journal cannot be read, so the send cannot be ruled out: " + loadErr.Error()}
	}
	switch outcome, orderRef := orderJournalSendOutcome(events, res.PreviewTokenID); outcome {
	case orderSendReached:
		return deskExecutionOutcomeRow{Outcome: rpc.DeskOutcomeAccepted, OrderRef: nonEmptyString(orderRef, res.OrderRef), Message: sent + "; confirmed from the order journal"}
	case orderSendUnclear:
		return deskExecutionOutcomeRow{Outcome: rpc.DeskOutcomeUnknown, Blockers: blockers, Message: "the send was staged and its outcome is not known; it may have reached the broker: " + reason}
	default:
		return deskExecutionOutcomeRow{Outcome: rpc.DeskOutcomeRefused, Blockers: blockers, Message: reason}
	}
}
