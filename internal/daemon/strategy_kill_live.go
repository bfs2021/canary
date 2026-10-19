package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/osauer/canary/v2/internal/core"
	"github.com/osauer/canary/v2/internal/daemon/corestore"
	"github.com/osauer/canary/v2/internal/register"
	"github.com/osauer/canary/v2/internal/rpc"
)

// The kill rule's drawdown test for a live entry (strategy-evaluator.md §3:
// "the live measurement (the sleeve's own fills) comes with the first rule
// that sends"; built 2026-10-19). A live entry keeps no shadow record, so
// its drawdown is measured on its own fills: at each decision close the
// sleeve's equity is the cash its own fills moved plus the units they left
// times the decision close, kept once per decision in the daemon's event
// log (scope strategy/<id>, never read by an order path). The drawdown is
// the fall from the running peak in units of K = S / 0.15, S the sleeve's
// risk allocation in money at that decision, as on the shadow record. Units
// the owner sells by hand leave the measure alone: it reads the entry's own
// trading, not the line's holding.

const (
	liveMarkType  = "strategy.mark.v1"
	liveMarkLimit = 10000
)

// liveMark is one decision's mark of a live entry's own sleeve.
type liveMark struct {
	Version      int       `json:"version"`
	Strategy     string    `json:"strategy"`
	Session      string    `json:"session"`
	Close        float64   `json:"close"`
	Units        float64   `json:"units"`
	Cash         float64   `json:"cash"`
	SleeveBudget float64   `json:"sleeve_budget"`
	RecordedAt   time.Time `json:"recorded_at"`
}

func liveMarkScope(id string) string { return "strategy/" + id }

func liveMarkKey(id, session string) string { return "strategy:" + id + ":" + session + ":mark" }

// liveMarks reads a live entry's marks in the order they were written.
func (s *Server) liveMarks(ctx context.Context, id string) ([]liveMark, error) {
	if s.coreStore == nil {
		return nil, errors.New("no daemon store")
	}
	events, err := s.coreStore.LoadEvents(ctx, corestore.EventQuery{ScopeKey: liveMarkScope(id), Type: liveMarkType, Limit: liveMarkLimit})
	if err != nil {
		return nil, err
	}
	out := make([]liveMark, 0, len(events))
	for _, ev := range events {
		var m liveMark
		if err := json.Unmarshal(ev.PayloadJSON, &m); err != nil {
			return nil, fmt.Errorf("strategy mark %s cannot be read: %w", ev.EventKey, err)
		}
		out = append(out, m)
	}
	return out, nil
}

// recordLiveMark keeps a live entry's mark of its frozen month's decision,
// once: the background refresh is its only writer, and the event log's key
// keeps it unique under retries.
func (s *Server) recordLiveMark(ctx context.Context, res *rpc.StrategyTargetResult, r register.Resolved, now time.Time) {
	if s.coreStore == nil || res == nil || r.State != register.StateLive || r.Entry.ID == "" || res.Decision == nil || res.Decision.FrozenAt.IsZero() || res.Decision.Drift ||
		res.Sizing == nil || !(res.Decision.LineClose() > 0) || !(res.Sizing.Current.SleeveBudget > 0) || res.Instrument == nil || res.Instrument.ConID <= 0 {
		return
	}
	key := liveMarkKey(r.Entry.ID, res.Decision.Decision)
	if _, ok, err := s.coreStore.GetEvent(ctx, liveMarkScope(r.Entry.ID), key); err != nil || ok {
		return
	}
	own := s.strategyOwnFills(ctx, r.Entry.ID, res.Instrument.ConID)
	if s.liveMarkFillsForTest != nil {
		own = s.liveMarkFillsForTest()
	}
	m := liveMark{Version: 1, Strategy: r.Entry.ID, Session: res.Decision.Decision, Close: res.Decision.LineClose(), Units: own.Units, Cash: own.Cash, SleeveBudget: res.Sizing.Current.SleeveBudget, RecordedAt: now}
	raw, err := json.Marshal(m)
	if err != nil {
		return
	}
	if _, err := s.coreStore.AppendEvents(ctx, []corestore.EventInput{{ScopeKey: liveMarkScope(r.Entry.ID), EventKey: key, Type: liveMarkType, Action: "mark",
		Origin: coreEventOriginDaemon, OccurredAt: now, PayloadJSON: raw}}); err != nil {
		s.warnf("strategy %s mark %s: %v; the next refresh tries again under the same key", r.Entry.ID, m.Session, err)
	}
}

// strategyKillLive is the kill test of a live entry on its own fills: the
// kept marks, and this reading marked at the decision close when it is not
// kept yet. Past the bound the entry holds before the month can be frozen.
func (s *Server) strategyKillLive(ctx context.Context, r register.Resolved, mark killMark, conID int) *rpc.KillView {
	if s.coreStore == nil {
		return nil
	}
	marks, err := s.liveMarks(ctx, r.Entry.ID)
	if err != nil {
		return nil
	}
	kill := &rpc.KillView{MaxDrawdownK: r.KillDrawdownK, Source: rpc.KillSourceOwnFills}
	// The sleeve's own P&L starts at zero, as the paper position does.
	peak := 0.0
	at := func(equity, sleeveBudget float64) {
		peak = max(peak, equity)
		if dd := (peak - equity) / (sleeveBudget / core.VolFloor); dd > kill.DrawdownK {
			kill.DrawdownK = dd
		}
		kill.Measured, kill.Decisions = true, kill.Decisions+1
	}
	marked := false
	for _, m := range marks {
		if m.Close > 0 && m.SleeveBudget > 0 {
			at(m.Cash+m.Units*m.Close, m.SleeveBudget)
			marked = marked || m.Session == mark.session
		}
	}
	if !marked && mark.session != "" && mark.close > 0 && mark.sleeveBudget > 0 {
		own := s.strategyOwnFills(ctx, r.Entry.ID, conID)
		at(own.Cash+own.Units*mark.close, mark.sleeveBudget)
	}
	switch {
	case kill.MaxDrawdownK == nil:
		kill.Note = "kill_drawdown_k unwritten: the kill rule cannot fire until the lab's design run writes its bound"
	case kill.Measured && kill.DrawdownK > *kill.MaxDrawdownK:
		kill.Breached = true
	case !kill.Measured:
		kill.Note = "no decision marked yet: the drawdown is measured on its own fills from its first decision live"
	}
	return kill
}
