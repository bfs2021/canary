package chainrec

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Broker is the gateway surface a run needs. Every method is a read; none
// can reach an order path.
type Broker interface {
	// HeldUnderlyings lists the underlyings of open USD option positions.
	HeldUnderlyings(ctx context.Context) ([]string, error)
	// ListedExpiries is the symbol's security-definition listing.
	ListedExpiries(ctx context.Context, symbol string) ([]ListedExpiry, error)
	// Spot is the underlying price the strike window centres on.
	Spot(ctx context.Context, symbol string) (Spot, error)
	// Prepare resolves the expiries' contracts in bulk (one request per
	// expiry) and reports which strikes are listed. A nil filter keeps
	// every strike.
	Prepare(ctx context.Context, symbol, tradingClass string, expiries []string) ListedFilter
	// Load reports the connection's market-data pressure; false when no
	// connection is attached.
	Load() (Load, bool)
	// Quote opens one line for the contract, collects for at most window
	// and releases the line. A partial observation is not an error; a
	// refusal returns a *Failure.
	Quote(ctx context.Context, c Contract, window time.Duration) (Quote, error)
}

// ListedFilter reports whether a contract is listed. known is false when the
// bulk resolution could not prove it either way; such a contract is kept.
type ListedFilter func(expiry string, strike float64, right string) (listed, known bool)

// Load is the connection's market-data pressure.
type Load struct {
	LinesHeld     int
	LinesCapacity int
	// LastPacingError is when the broker last answered with a pacing error
	// (code 100), whichever request caused it.
	LastPacingError time.Time
}

// Sink persists a run as it progresses, so a run cut short by a restart
// keeps what it recorded.
type Sink interface {
	BeginRun(ctx context.Context, start RunStart) (int64, error)
	RecordSymbol(ctx context.Context, runID int64, plan SymbolPlan) error
	RecordRows(ctx context.Context, runID int64, rows []Row) error
	FinishRun(ctx context.Context, runID int64, outcome Outcome) error
}

// RunStart is a run's immutable header.
type RunStart struct {
	Slot      Slot
	StartedAt time.Time
	Settings  Settings
}

// SymbolPlan is one underlying's discovery result.
type SymbolPlan struct {
	Symbol       string
	Source       string
	Status       string
	Detail       string
	Spot         Spot
	Expiries     []PlannedExpiry
	Planned      int
	DiscoveredAt time.Time
}

// PlannedExpiry is one selected expiry's strike selection.
type PlannedExpiry struct {
	Date         string
	TradingClass string
	Slot         string
	InWindow     int
	Selected     int
	Contracts    int
}

// Outcome closes a run.
type Outcome struct {
	FinishedAt      time.Time
	Status          string
	StopReason      string
	Planned         int
	Attempted       int
	Quoted          int
	HeldUnavailable bool
}

// Clock is the run's time source; tests advance it without sleeping.
type Clock interface {
	Now() time.Time
	Sleep(ctx context.Context, d time.Duration) error
}

// RealClock is the wall clock.
type RealClock struct{}

// Now returns time.Now.
func (RealClock) Now() time.Time { return time.Now() }

// Sleep waits d or until ctx ends.
func (RealClock) Sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// StopStore ends a run whose rows could not be written.
const StopStore = "store_error"

const rowFlushSize = 200

// Runner executes one run.
type Runner struct {
	Broker Broker
	Sink   Sink
	Clock  Clock
	Days   TradingDays
	Logf   func(format string, args ...any)
	// Lines, Reserve and Window override MaxLines, LineReserve and
	// ContractWindow when positive.
	Lines   int
	Reserve int
	Window  time.Duration
}

func (r *Runner) logf(format string, args ...any) {
	if r.Logf != nil {
		r.Logf(format, args...)
	}
}

// Run records one slot. It returns the outcome it stored; an error means the
// run could not be begun or closed in the store.
func (r *Runner) Run(ctx context.Context, slot Slot, settings Settings) (Outcome, error) {
	if r.Clock == nil {
		r.Clock = RealClock{}
	}
	runID, err := r.Sink.BeginRun(ctx, RunStart{Slot: slot, StartedAt: r.Clock.Now(), Settings: settings})
	if err != nil {
		return Outcome{}, fmt.Errorf("begin chain recorder run: %w", err)
	}
	ex := &execution{r: r, ctx: ctx, runID: runID, slot: slot, symOK: map[string]int{}, symEntitlement: map[string]int{}}
	outcome := ex.run(settings)
	closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := r.Sink.FinishRun(closeCtx, runID, outcome); err != nil {
		return outcome, fmt.Errorf("finish chain recorder run: %w", err)
	}
	return outcome, nil
}

type execution struct {
	r     *Runner
	ctx   context.Context
	runID int64
	slot  Slot

	mu             sync.Mutex
	pending        []Row
	planned        int
	attempted      int
	quoted         int
	notAttempted   int
	stop           string
	incomplete     bool
	backoffUntil   time.Time
	consecutive    int
	lastPacingSeen time.Time
	symOK          map[string]int
	symEntitlement map[string]int
}

func (ex *execution) run(settings Settings) Outcome {
	r := ex.r
	out := Outcome{}
	held, err := r.Broker.HeldUnderlyings(ex.ctx)
	if err != nil {
		out.HeldUnavailable = true
		r.logf("chain recorder: held underlyings unavailable: %v", err)
	}
	selected, skipped := ResolveSymbols(held, settings.Declared, settings.Symbols)
	for _, s := range skipped {
		ex.recordSymbol(SymbolPlan{Symbol: s.Symbol, Source: s.Source, Status: SymbolSkippedCap, DiscoveredAt: r.Clock.Now()})
	}
	if load, ok := r.Broker.Load(); ok {
		ex.lastPacingSeen = load.LastPacingError
	}

	var work []Contract
	for i, choice := range selected {
		if reason := ex.discoveryStop(); reason != "" {
			ex.incomplete = true
			ex.recordSymbol(SymbolPlan{Symbol: choice.Symbol, Source: choice.Source, Status: SymbolNotDiscovered, Detail: reason, DiscoveredAt: r.Clock.Now()})
			continue
		}
		plan, contracts := ex.discover(i, choice)
		plan.DiscoveredAt = r.Clock.Now()
		ex.recordSymbol(plan)
		work = append(work, contracts...)
	}
	orderContracts(work)
	ex.planned = len(work)
	ex.quote(work)
	ex.flush(true)

	ex.mu.Lock()
	defer ex.mu.Unlock()
	out.FinishedAt = r.Clock.Now()
	out.Planned, out.Attempted, out.Quoted = ex.planned, ex.attempted, ex.quoted
	out.StopReason = ex.stop
	switch {
	case ex.stop == StopStore:
		out.Status = RunFailed
	case ex.attempted == 0:
		out.Status = RunFailed
		if out.StopReason == "" && ex.planned == 0 {
			out.StopReason = "no_contracts"
		}
	case ex.notAttempted > 0 || ex.incomplete:
		out.Status = RunPartial
	default:
		out.Status = RunComplete
	}
	return out
}

// discoveryStop names why discovery must not start another symbol.
func (ex *execution) discoveryStop() string {
	if ex.ctx.Err() != nil {
		return StopCancelled
	}
	if !ex.r.Clock.Now().Before(ex.slot.Deadline) {
		return StopDeadline
	}
	ex.mu.Lock()
	defer ex.mu.Unlock()
	return ex.stop
}

func (ex *execution) discover(symIndex int, choice SymbolChoice) (SymbolPlan, []Contract) {
	r := ex.r
	plan := SymbolPlan{Symbol: choice.Symbol, Source: choice.Source}
	listed, err := r.Broker.ListedExpiries(ex.ctx, choice.Symbol)
	if err != nil || len(listed) == 0 {
		plan.Status, plan.Detail = SymbolChainUnavailable, shortError(err, "no listed expiries")
		return plan, nil
	}
	expiries := SelectExpiries(choice.Symbol, listed, ex.slot.SessionDate, r.Days)
	if len(expiries) == 0 {
		plan.Status, plan.Detail = SymbolNoExpiries, "no standard monthly or weekly expiry listed after today"
		return plan, nil
	}
	spot, err := r.Broker.Spot(ex.ctx, choice.Symbol)
	if err != nil || spot.Price <= 0 {
		plan.Status, plan.Detail = SymbolSpotUnavailable, shortError(err, "no underlying price")
		return plan, nil
	}
	plan.Spot = spot
	byClass := map[string][]string{}
	for _, e := range expiries {
		byClass[e.TradingClass] = append(byClass[e.TradingClass], e.Date)
	}
	filters := map[string]ListedFilter{}
	for class, dates := range byClass {
		filters[class] = r.Broker.Prepare(ex.ctx, choice.Symbol, class, dates)
	}
	var out []Contract
	for _, e := range expiries {
		filter := filters[e.TradingClass]
		listedRight := func(k float64, right string) bool {
			if filter == nil {
				return true
			}
			listed, known := filter(e.Date, k, right)
			return listed || !known
		}
		var candidates []float64
		for _, k := range e.Strikes {
			if listedRight(k, "C") || listedRight(k, "P") {
				candidates = append(candidates, k)
			}
		}
		chosen, inWindow := SelectStrikes(candidates, spot.Price, StrikeWindowPct, MaxStrikesPerExpiry)
		ranks := atmRanks(chosen, spot.Price)
		pe := PlannedExpiry{Date: e.Date, TradingClass: e.TradingClass, Slot: e.Slot, InWindow: inWindow, Selected: len(chosen)}
		for _, k := range chosen {
			for _, right := range []string{"C", "P"} {
				if !listedRight(k, right) {
					continue
				}
				out = append(out, Contract{Symbol: choice.Symbol, TradingClass: e.TradingClass, Expiry: e.Date, Slot: e.Slot,
					Strike: k, Right: right, rank: ranks[k], sym: symIndex})
				pe.Contracts++
			}
		}
		plan.Expiries = append(plan.Expiries, pe)
	}
	plan.Planned = len(out)
	plan.Status = SymbolPlanned
	return plan, out
}

func (ex *execution) quote(work []Contract) {
	r := ex.r
	lines := r.Lines
	if lines <= 0 {
		lines = MaxLines
	}
	window := r.Window
	if window <= 0 {
		window = ContractWindow
	}
	jobs := make(chan Contract)
	var wg sync.WaitGroup
	for range min(lines, max(len(work), 1)) {
		wg.Go(func() {
			for c := range jobs {
				if reason := ex.admit(c); reason != "" {
					ex.record(Row{Contract: c, Status: StatusNotAttempted, Detail: reason})
					continue
				}
				q, err := r.Broker.Quote(ex.ctx, c, window)
				ex.record(ex.classify(c, q, err))
			}
		})
	}
	for _, c := range work {
		jobs <- c
	}
	close(jobs)
	wg.Wait()
}

func (ex *execution) classify(c Contract, q Quote, err error) Row {
	row := Row{Contract: c, Quote: q}
	switch {
	case err == nil:
		row.Status = q.Status()
	case ex.ctx.Err() != nil || errors.Is(err, context.Canceled):
		row.Status, row.Detail = StatusNotAttempted, StopCancelled
	default:
		row.Status, row.Detail = failureStatus(err), shortError(err, "")
	}
	return row
}

// admit blocks until the contract may open a line, or names why it is not
// requested at all: the run stopped, the deadline passed, or the symbol
// proved unentitled. It yields to the rest of the daemon while free lines
// are at or below the reserve and while a pacing backoff runs.
func (ex *execution) admit(c Contract) string {
	r := ex.r
	reserve := r.Reserve
	if reserve <= 0 {
		reserve = LineReserve
	}
	for {
		if ex.ctx.Err() != nil {
			return StopCancelled
		}
		now := r.Clock.Now()
		ex.mu.Lock()
		if ex.stop != "" {
			stop := ex.stop
			ex.mu.Unlock()
			return stop
		}
		if ex.symEntitlement[c.Symbol] >= EntitlementGiveUp && ex.symOK[c.Symbol] == 0 {
			ex.mu.Unlock()
			return StopSymbolNoEntitle
		}
		if !now.Before(ex.slot.Deadline) {
			ex.stop = StopDeadline
			ex.mu.Unlock()
			return StopDeadline
		}
		wait := time.Duration(0)
		if load, ok := r.Broker.Load(); ok {
			if load.LastPacingError.After(ex.lastPacingSeen) {
				ex.lastPacingSeen = load.LastPacingError
				ex.pacingSignalLocked(now)
				ex.mu.Unlock()
				continue
			}
			if load.LinesCapacity > 0 && load.LinesCapacity-load.LinesHeld <= reserve {
				wait = time.Second
			}
		}
		if until := ex.backoffUntil.Sub(now); until > wait {
			wait = until
		}
		ex.mu.Unlock()
		if wait <= 0 {
			return ""
		}
		if err := r.Clock.Sleep(ex.ctx, min(wait, ex.slot.Deadline.Sub(now))); err != nil {
			return StopCancelled
		}
	}
}

// pacingSignalLocked pauses the run, doubling the pause per consecutive
// signal, and stops it after MaxPacingSignals in a row.
func (ex *execution) pacingSignalLocked(now time.Time) {
	ex.consecutive++
	if ex.consecutive >= MaxPacingSignals {
		if ex.stop == "" {
			ex.stop = StopPacing
			ex.r.logf("chain recorder: %d consecutive pacing signals; stopping the run", ex.consecutive)
		}
		return
	}
	backoff := min(PacingBackoffBase<<(ex.consecutive-1), PacingBackoffMax)
	ex.backoffUntil = maxTime(ex.backoffUntil, now.Add(backoff))
}

func (ex *execution) record(row Row) {
	ex.mu.Lock()
	switch row.Status {
	case StatusNotAttempted:
		ex.notAttempted++
	case StatusPacing:
		ex.attempted++
		ex.pacingSignalLocked(ex.r.Clock.Now())
	case StatusUnavailable:
		ex.attempted++
		if ex.stop == "" {
			ex.stop = StopGateway
		}
	case StatusEntitlement:
		ex.attempted++
		ex.symEntitlement[row.Symbol]++
	case StatusQuoted, StatusModelOnly:
		ex.attempted++
		ex.consecutive = 0
		ex.symOK[row.Symbol]++
		if row.Status == StatusQuoted {
			ex.quoted++
		}
	default:
		ex.attempted++
		ex.consecutive = 0
	}
	ex.pending = append(ex.pending, row)
	full := len(ex.pending) >= rowFlushSize
	ex.mu.Unlock()
	if full {
		ex.flush(false)
	}
}

func (ex *execution) flush(final bool) {
	ex.mu.Lock()
	defer ex.mu.Unlock()
	if len(ex.pending) == 0 || ex.stop == StopStore {
		return
	}
	ctx := ex.ctx
	if final {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.WithoutCancel(ex.ctx), 10*time.Second)
		defer cancel()
	}
	if err := ex.r.Sink.RecordRows(ctx, ex.runID, ex.pending); err != nil {
		ex.r.logf("chain recorder: store rows: %v", err)
		ex.stop = StopStore
		return
	}
	ex.pending = ex.pending[:0]
}

func (ex *execution) recordSymbol(plan SymbolPlan) {
	if err := ex.r.Sink.RecordSymbol(ex.ctx, ex.runID, plan); err != nil {
		ex.r.logf("chain recorder: store symbol %s: %v", plan.Symbol, err)
		ex.mu.Lock()
		ex.stop = StopStore
		ex.mu.Unlock()
	}
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// shortError keeps a stored detail to one bounded line of broker text.
func shortError(err error, fallback string) string {
	if err == nil {
		return fallback
	}
	msg := strings.Join(strings.Fields(err.Error()), " ")
	if len(msg) > 160 {
		msg = msg[:160]
	}
	return msg
}
