package ibkr

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"
)

// TWS order control is the one path on which Canary touches an order placed
// by hand in TWS (owner decision "TWS B", 2026-10-11 19:36 CEST). It opens a
// separate, short-lived connection as client id 0, the only client TWS lets
// bind its manual orders, binds them with reqOpenOrders, finds the exact
// order by its permanent id and the terms the owner reviewed, cancels or
// modifies it, waits a bounded time for the broker's answer, and
// disconnects. The daemon's own connection, its client id and its order
// namespace are not involved. Side effect, by TWS design: once bound, the
// order is an API order for the rest of the TWS session.
//
// Every failure leaves the order untouched or says exactly what happened:
// a busy client-0 slot, a connect failure, an incomplete bind, an absent or
// changed order and a broker refusal are all refusals with the order
// untouched; a send that may have reached TWS without a confirming callback
// is reported uncertain and is never retried here.

// TWSOrderControlAction names what the owner confirmed.
const (
	TWSOrderControlCancel = "cancel"
	TWSOrderControlModify = "modify"
)

// TWS order control outcomes.
const (
	// TWSOrderControlConfirmed means the broker confirmed the cancel or the
	// replacement before the connection closed.
	TWSOrderControlConfirmed = "confirmed"
	// TWSOrderControlPending means the broker acknowledged the request
	// (PendingCancel, or a PreSubmitted echo) without a final status before
	// the connection closed; a later open-order read settles it.
	TWSOrderControlPending = "pending"
	// TWSOrderControlRefused means nothing changed: a gate, the bind, the
	// identity check or the broker refused before or instead of acting.
	TWSOrderControlRefused = "refused"
	// TWSOrderControlUncertain means the request may have reached TWS and
	// nothing proves either way. It is settled by reading the broker's open
	// orders again, never by a blind retry.
	TWSOrderControlUncertain = "uncertain"
)

// TWS order control refusals. Each wraps a reason; none means the order
// changed.
var (
	ErrTWSOrderControlGate       = errors.New("tws order control gate")
	ErrTWSOrderControlSlotBusy   = errors.New("client id 0 is already connected to TWS")
	ErrTWSOrderControlConnect    = errors.New("could not open the client-0 control connection")
	ErrTWSOrderControlBind       = errors.New("TWS did not finish listing its manual orders")
	ErrTWSOrderControlNotFound   = errors.New("the order is not among the manual TWS orders")
	ErrTWSOrderControlNotWorking = errors.New("the order is no longer working")
	ErrTWSOrderControlChanged    = errors.New("the order differs from the reviewed terms")
	ErrTWSOrderControlRefused    = errors.New("TWS refused the request")
	// ErrTWSOrderControlUncertain is returned with a receipt whose Outcome
	// is TWSOrderControlUncertain.
	ErrTWSOrderControlUncertain = errors.New("the request may have reached TWS; no confirmation arrived")
)

// TWSOrderControlConfig is the control connection: the same TWS endpoint
// and account the daemon is connected to, in the mode it trades in.
type TWSOrderControlConfig struct {
	// Authority fences client-0 writes against the primary connector's
	// unpublication. It never supplies the client-0 connection or order id.
	Authority *Connector
	// AuthoritySession is the primary session captured by daemon admission.
	AuthoritySession       ConnectorSessionBinding
	authorityOutboundState uint64
	// NewWireGuard creates one final-send guard and its release. The release
	// runs after the frame write, before waiting for any broker callback.
	NewWireGuard func() (func() error, func())
	Host         string
	Port         int
	// Mode is "paper" or "live". Paper requires a DU account and is
	// available in every build; live requires the trading build.
	Mode    string
	Account string
	// UseTLS and EnableTLSFallback mirror the daemon's gateway settings.
	UseTLS            bool
	EnableTLSFallback bool
	// ConnectTimeout bounds dial, handshake and startAPI (default 10s).
	// BindTimeout bounds reqOpenOrders to openOrderEnd (default 10s).
	// AckTimeout bounds the wait for the broker's answer to the cancel or
	// the replacement (default 5s).
	ConnectTimeout time.Duration
	BindTimeout    time.Duration
	AckTimeout     time.Duration
}

// TWSOrderControlTarget is the order as the owner reviewed it. The bound
// order must match every field, so a change TWS made meanwhile (a fill, a
// re-price by hand) refuses the request instead of acting on terms the owner
// did not see.
type TWSOrderControlTarget struct {
	PermID     int
	ConID      int // checked when both sides carry one
	Symbol     string
	SecType    string
	Action     string
	Quantity   float64
	Filled     float64
	OrderType  string
	LimitPrice float64
	AuxPrice   float64
	TIF        string
}

// TWSOrderControlReplacement is the new quantity and limit price of a
// modify. Only LMT orders are modified; the contract, side, type and
// time-in-force stay as bound.
type TWSOrderControlReplacement struct {
	Quantity   float64
	LimitPrice float64
}

// TWSOrderControlReceipt is what happened, for the journal and the caller.
type TWSOrderControlReceipt struct {
	Action        string
	PermID        int
	BoundOrderID  int
	Outcome       string
	BrokerStatus  string
	BrokerCode    int
	BrokerMessage string
	// Bound is the order as TWS reported it on binding; zero when the bind
	// did not reach it.
	Bound          OrderLifecycleEvent
	ServerVersion  int
	ConnectedAt    time.Time
	DisconnectedAt time.Time
}

const (
	defaultTWSOrderControlConnectTimeout = 10 * time.Second
	defaultTWSOrderControlBindTimeout    = 10 * time.Second
	defaultTWSOrderControlAckTimeout     = 5 * time.Second
)

func (cfg TWSOrderControlConfig) withDefaults() TWSOrderControlConfig {
	if cfg.ConnectTimeout <= 0 {
		cfg.ConnectTimeout = defaultTWSOrderControlConnectTimeout
	}
	if cfg.BindTimeout <= 0 {
		cfg.BindTimeout = defaultTWSOrderControlBindTimeout
	}
	if cfg.AckTimeout <= 0 {
		cfg.AckTimeout = defaultTWSOrderControlAckTimeout
	}
	return cfg
}

// gate refuses a configuration the path must not act on: no endpoint, no
// concrete account, a mode other than paper or live, a paper mode without
// a DU account, or a live mode in the read-only build.
func (cfg TWSOrderControlConfig) gate() error {
	if strings.TrimSpace(cfg.Host) == "" || cfg.Port <= 0 {
		return fmt.Errorf("%w: endpoint is required", ErrTWSOrderControlGate)
	}
	account := strings.TrimSpace(cfg.Account)
	if account == "" || strings.EqualFold(account, "All") {
		return fmt.Errorf("%w: a concrete account is required", ErrTWSOrderControlGate)
	}
	switch strings.ToLower(strings.TrimSpace(cfg.Mode)) {
	case paperOrderMode:
		if !strings.HasPrefix(strings.ToUpper(account), "DU") {
			return fmt.Errorf("%w: paper mode requires a DU account", ErrTWSOrderControlGate)
		}
	case "live":
		if !tradingEnabled {
			return fmt.Errorf("%w: %w", ErrTWSOrderControlGate, ErrTradingDisabled)
		}
	default:
		return fmt.Errorf("%w: mode must be paper or live", ErrTWSOrderControlGate)
	}
	return nil
}

func (t TWSOrderControlTarget) validate() error {
	if t.PermID <= 0 {
		return fmt.Errorf("%w: a permanent order id is required", ErrTWSOrderControlGate)
	}
	if strings.TrimSpace(t.Symbol) == "" || strings.TrimSpace(t.Action) == "" || strings.TrimSpace(t.OrderType) == "" {
		return fmt.Errorf("%w: the reviewed symbol, side and order type are required", ErrTWSOrderControlGate)
	}
	if t.Quantity <= 0 || math.IsNaN(t.Quantity) || math.IsInf(t.Quantity, 0) {
		return fmt.Errorf("%w: the reviewed quantity must be positive", ErrTWSOrderControlGate)
	}
	return nil
}

func (r TWSOrderControlReplacement) validate(target TWSOrderControlTarget) error {
	if !strings.EqualFold(strings.TrimSpace(target.OrderType), "LMT") {
		return fmt.Errorf("%w: only LMT orders are modified; cancel and re-enter other types in TWS", ErrTWSOrderControlGate)
	}
	if tif := strings.ToUpper(strings.TrimSpace(target.TIF)); tif != "" && tif != "DAY" {
		return fmt.Errorf("%w: only DAY limit orders are modified; cancel and re-enter a %s order in TWS", ErrTWSOrderControlGate, tif)
	}
	if secType := strings.ToUpper(strings.TrimSpace(target.SecType)); secType != "" && secType != "STK" && secType != "ETF" {
		return fmt.Errorf("%w: only stock and ETF orders are modified; cancel and re-enter a %s order in TWS", ErrTWSOrderControlGate, secType)
	}
	if r.Quantity <= 0 || math.IsNaN(r.Quantity) || math.IsInf(r.Quantity, 0) || r.Quantity != math.Trunc(r.Quantity) {
		return fmt.Errorf("%w: the replacement quantity must be a positive whole number", ErrTWSOrderControlGate)
	}
	if r.Quantity > target.Quantity {
		return fmt.Errorf("%w: the replacement quantity cannot exceed the reviewed quantity", ErrTWSOrderControlGate)
	}
	if r.Quantity <= target.Filled {
		return fmt.Errorf("%w: the replacement quantity must exceed the filled quantity", ErrTWSOrderControlGate)
	}
	if r.LimitPrice <= 0 || math.IsNaN(r.LimitPrice) || math.IsInf(r.LimitPrice, 0) {
		return fmt.Errorf("%w: the replacement limit price must be positive", ErrTWSOrderControlGate)
	}
	return nil
}

// twsOrderControlSession collects the broker's answers on the control
// connection: the bound orders, the status callbacks and the error notices
// for one order id.
type twsOrderControlSession struct {
	mu       sync.Mutex
	bound    []OrderLifecycleEvent
	bindEnd  chan struct{}
	bindOnce sync.Once
	answers  chan twsOrderControlAnswer
}

type twsOrderControlAnswer struct {
	event OrderLifecycleEvent // openOrder or orderStatus echo
	code  int                 // broker error code; zero for an echo
	msg   string
}

func newTWSOrderControlSession() *twsOrderControlSession {
	return &twsOrderControlSession{bindEnd: make(chan struct{}), answers: make(chan twsOrderControlAnswer, 64)}
}

func (s *twsOrderControlSession) attach(conn *Connection) {
	conn.RegisterHandler(msgOpenOrder, func(fields []string) {
		ev, ok := ParseOrderLifecycleEvent(fields)
		if !ok || ev.Type != OrderLifecycleEventOpenOrder || ev.WhatIf {
			return
		}
		s.mu.Lock()
		s.bound = append(s.bound, ev)
		s.mu.Unlock()
		s.offer(twsOrderControlAnswer{event: ev})
	})
	conn.RegisterHandler(msgOpenOrderEnd, func([]string) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.bindOnce.Do(func() { close(s.bindEnd) })
	})
	conn.RegisterHandler(msgOrderStatus, func(fields []string) {
		ev, ok := ParseOrderLifecycleEvent(fields)
		if !ok || ev.Type != OrderLifecycleEventStatus {
			return
		}
		// During the bind, TWS follows each openOrder with its orderStatus;
		// that is where the filled quantity and the working status live.
		s.mu.Lock()
		for i := range s.bound {
			if s.bound[i].OrderID == ev.OrderID {
				s.bound[i].Status, s.bound[i].Filled, s.bound[i].Remaining = ev.Status, ev.Filled, ev.Remaining
			}
		}
		s.mu.Unlock()
		s.offer(twsOrderControlAnswer{event: ev})
	})
	// Legacy error frames: [4, version, id, code, message].
	conn.RegisterHandler(msgErrMsg, func(fields []string) {
		if len(fields) < 5 {
			return
		}
		id, _ := strconv.Atoi(strings.TrimSpace(fields[2]))
		code, _ := strconv.Atoi(strings.TrimSpace(fields[3]))
		s.offer(twsOrderControlAnswer{event: OrderLifecycleEvent{Type: OrderLifecycleEventError, OrderID: id}, code: code, msg: strings.TrimSpace(fields[4])})
	})
	// Protocol-buffer notices carry the same id, code and message.
	conn.SetSystemNoticeHandler(func(note *systemNotification, _ reqAliasEntry) {
		if note == nil {
			return
		}
		s.offer(twsOrderControlAnswer{event: OrderLifecycleEvent{Type: OrderLifecycleEventError, OrderID: int(note.tickerID)}, code: note.code, msg: note.message})
	})
}

func (s *twsOrderControlSession) offer(a twsOrderControlAnswer) {
	select {
	case s.answers <- a:
	default:
	}
}

// beginBind forgets what the connect's startup burst delivered: with open
// orders downloaded on connection, TWS sends client 0's orders and an
// openOrderEnd before nextValidId, inside Connect. Without it that
// openOrderEnd ended the bind before the answer to its own reqOpenOrders,
// whose late statuses then read as the action's acknowledgement. It returns
// the channel the bind's own openOrderEnd closes.
func (s *twsOrderControlSession) beginBind() <-chan struct{} {
	s.mu.Lock()
	s.bound = nil
	s.bindEnd = make(chan struct{})
	s.bindOnce = sync.Once{}
	end := s.bindEnd
	s.mu.Unlock()
	s.drain()
	return end
}

func (s *twsOrderControlSession) boundOrders() []OrderLifecycleEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]OrderLifecycleEvent(nil), s.bound...)
}

// drain discards the answers the bind produced, so only callbacks that
// follow the sent request can settle it.
func (s *twsOrderControlSession) drain() {
	for {
		select {
		case <-s.answers:
		default:
			return
		}
	}
}

func newTWSOrderControlConnection(cfg TWSOrderControlConfig) *Connection {
	return NewConnection(&ConnectionConfig{
		Host: cfg.Host, Port: cfg.Port, ClientID: 0, Account: cfg.Account,
		ConnectTimeout: cfg.ConnectTimeout, MaxClientIDRetries: 1, MaxRetries: 1,
		AutoReconnect: false, ManagedConnectionLogging: true,
		UseTLS: cfg.UseTLS, EnableTLSFallback: cfg.EnableTLSFallback,
	})
}

// RunTWSOrderControl cancels or modifies one manual TWS order as described
// above. The receipt is always returned, with the error that names the
// refusal or the uncertainty; a nil error means the outcome is confirmed or
// pending.
func RunTWSOrderControl(ctx context.Context, cfg TWSOrderControlConfig, action string, target TWSOrderControlTarget, replacement TWSOrderControlReplacement) (receipt TWSOrderControlReceipt, err error) {
	receipt = TWSOrderControlReceipt{Action: action, PermID: target.PermID, Outcome: TWSOrderControlRefused}
	if ctx == nil {
		return receipt, fmt.Errorf("%w: context is nil", ErrTWSOrderControlGate)
	}
	cfg = cfg.withDefaults()
	if err := cfg.gate(); err != nil {
		return receipt, err
	}
	if err := target.validate(); err != nil {
		return receipt, err
	}
	switch action {
	case TWSOrderControlCancel:
	case TWSOrderControlModify:
		if err := replacement.validate(target); err != nil {
			return receipt, err
		}
	default:
		return receipt, fmt.Errorf("%w: action must be cancel or modify", ErrTWSOrderControlGate)
	}
	if err := ctx.Err(); err != nil {
		return receipt, fmt.Errorf("%w: %w", ErrTWSOrderControlGate, err)
	}

	if cfg.AuthoritySession.connection != nil {
		cfg.authorityOutboundState = cfg.AuthoritySession.connection.outboundSessionState.Load()
	}
	conn := newTWSOrderControlConnection(cfg)
	if cfg.Authority != nil {
		conn.publicationBarrier = &cfg.Authority.publicationBarrier
		conn.evidenceBarrier = &cfg.Authority.evidenceBarrier
	}
	session := newTWSOrderControlSession()
	session.attach(conn)
	defer func() {
		_ = conn.Disconnect()
		receipt.DisconnectedAt = time.Now().UTC()
	}()

	connectCtx, cancelConnect := context.WithTimeout(ctx, cfg.ConnectTimeout)
	err = conn.Connect(connectCtx)
	cancelConnect()
	if err != nil {
		if isClientIDInUseError(err) {
			return receipt, fmt.Errorf("%w: close the other API client on id 0 and try again", ErrTWSOrderControlSlotBusy)
		}
		return receipt, fmt.Errorf("%w: %w", ErrTWSOrderControlConnect, err)
	}
	receipt.ConnectedAt = time.Now().UTC()
	receipt.ServerVersion = conn.ServerVersion()

	bound, err := bindTWSOrders(ctx, conn, session, cfg)
	if err != nil {
		return receipt, err
	}
	order, err := findTWSOrder(bound, target)
	if err != nil {
		return receipt, err
	}
	receipt.Bound = order
	receipt.BoundOrderID = order.OrderID

	epoch, err := conn.captureBrokerInstructionEpoch()
	if err != nil {
		return receipt, fmt.Errorf("%w: %w", ErrTWSOrderControlConnect, err)
	}
	session.drain()
	guard, release := cfg.newWireGuard()
	defer release()
	switch action {
	case TWSOrderControlCancel:
		err = conn.cancelOrderForEpochGuarded(ctx, order.OrderID, epoch, guard)
	default:
		var modified *IBKROrder
		modified, err = twsReplacementOrder(order, replacement, cfg.Account)
		if err != nil {
			return receipt, err
		}
		// The bound id is this connection's to modify: the bind proved it.
		conn.ordersMu.Lock()
		conn.openOrders[order.OrderID] = modified
		conn.ordersMu.Unlock()
		err = conn.placeOrder(ctx, modified, &epoch, guard)
	}
	release()
	if err != nil {
		if brokerSendMayHaveBeenWritten(err) {
			receipt.Outcome = TWSOrderControlUncertain
			return receipt, fmt.Errorf("%w: %w", ErrTWSOrderControlUncertain, err)
		}
		return receipt, fmt.Errorf("%w: %w", ErrTWSOrderControlRefused, err)
	}
	return awaitTWSOrderControlAnswer(ctx, session, receipt, action, replacement, cfg.AckTimeout)
}

func (cfg TWSOrderControlConfig) newWireGuard() (func() error, func()) {
	var guard func() error
	release := func() {}
	if cfg.NewWireGuard != nil {
		guard, release = cfg.NewWireGuard()
	}
	return func() error {
		if guard != nil {
			if err := guard(); err != nil {
				return err
			}
		}
		// Primary Disconnect revokes outbound authority before updating its
		// connected status. Fence that interval at the same final-write
		// boundary as the client-0 connection's own outbound generation.
		if cfg.Authority != nil {
			primary := cfg.AuthoritySession.connection
			if primary == nil || !cfg.Authority.SessionCurrent(cfg.AuthoritySession) || cfg.authorityOutboundState&1 != 0 || primary.outboundSessionState.Load() != cfg.authorityOutboundState {
				return fmt.Errorf("%w: primary broker session revoked before transmit", ErrTWSOrderControlGate)
			}
		}
		return nil
	}, sync.OnceFunc(release)
}

// bindTWSOrders asks TWS for client 0's orders, which binds its manual
// orders, and waits for the openOrderEnd terminator.
func bindTWSOrders(ctx context.Context, conn *Connection, session *twsOrderControlSession, cfg TWSOrderControlConfig) ([]OrderLifecycleEvent, error) {
	epoch, err := conn.captureBrokerInstructionEpoch()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrTWSOrderControlBind, err)
	}
	bindEnd := session.beginBind()
	msg := conn.encodeMsg(reqOpenOrders, 1)
	guard, release := cfg.newWireGuard()
	err = conn.sendMessageWithTypeContextForEpochGuarded(ctx, msg, RequestTypeOrder, epoch, true, guard)
	release()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrTWSOrderControlBind, err)
	}
	timeout := cfg.BindTimeout
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-bindEnd:
		return session.boundOrders(), nil
	case <-timer.C:
		return nil, fmt.Errorf("%w: no openOrderEnd within %s", ErrTWSOrderControlBind, timeout)
	case <-ctx.Done():
		return nil, fmt.Errorf("%w: %w", ErrTWSOrderControlBind, ctx.Err())
	}
}

// findTWSOrder picks the bound order with the reviewed permanent id and
// refuses when it is absent, owned by another API client, no longer
// working, or different from the reviewed terms.
func findTWSOrder(bound []OrderLifecycleEvent, target TWSOrderControlTarget) (OrderLifecycleEvent, error) {
	for _, ev := range bound {
		if ev.PermID != target.PermID {
			continue
		}
		if ev.ClientIDPresent && ev.ClientID != 0 {
			return OrderLifecycleEvent{}, fmt.Errorf("%w: API client %d owns it", ErrTWSOrderControlNotFound, ev.ClientID)
		}
		if ev.OrderID <= 0 {
			return OrderLifecycleEvent{}, fmt.Errorf("%w: TWS reported it without an order id", ErrTWSOrderControlBind)
		}
		if !twsOrderWorking(ev) {
			return OrderLifecycleEvent{}, fmt.Errorf("%w: status %s, filled %s of %s", ErrTWSOrderControlNotWorking, nonEmptyTWS(ev.Status, "unknown"), formatTWSQuantity(ev.Filled), formatTWSQuantity(ev.TotalQuantity))
		}
		if reason := twsOrderTermsDiffer(ev, target); reason != "" {
			return OrderLifecycleEvent{}, fmt.Errorf("%w: %s", ErrTWSOrderControlChanged, reason)
		}
		return ev, nil
	}
	return OrderLifecycleEvent{}, fmt.Errorf("%w: it was filled, cancelled or placed by another API client", ErrTWSOrderControlNotFound)
}

func twsOrderWorking(ev OrderLifecycleEvent) bool {
	switch strings.ToLower(strings.TrimSpace(ev.Status)) {
	case "filled", "cancelled", "canceled", "apicancelled", "apicanceled", "inactive", "pendingcancel":
		return false
	}
	if ev.TotalQuantity > 0 && ev.TotalQuantity-ev.Filled <= 1e-9 {
		return false
	}
	if ev.Deactivated != nil && *ev.Deactivated {
		return false
	}
	return true
}

func twsOrderTermsDiffer(ev OrderLifecycleEvent, target TWSOrderControlTarget) string {
	text := func(name, got, want string) string {
		if strings.EqualFold(strings.TrimSpace(got), strings.TrimSpace(want)) {
			return ""
		}
		return fmt.Sprintf("%s is %s, reviewed %s", name, nonEmptyTWS(got, "empty"), nonEmptyTWS(want, "empty"))
	}
	number := func(name string, got, want float64) string {
		if math.Abs(got-want) <= 1e-9 {
			return ""
		}
		return fmt.Sprintf("%s is %s, reviewed %s", name, formatTWSQuantity(got), formatTWSQuantity(want))
	}
	if target.ConID != 0 && ev.ConID != 0 && target.ConID != ev.ConID {
		return fmt.Sprintf("contract id is %d, reviewed %d", ev.ConID, target.ConID)
	}
	checks := []string{
		text("symbol", ev.Symbol, target.Symbol),
		text("security type", ev.SecType, nonEmptyTWS(target.SecType, ev.SecType)),
		text("side", ev.Action, target.Action),
		text("order type", ev.OrderType, target.OrderType),
		text("time in force", ev.TIF, nonEmptyTWS(target.TIF, ev.TIF)),
		number("quantity", ev.TotalQuantity, target.Quantity),
		number("filled", ev.Filled, target.Filled),
		number("limit price", ev.LimitPrice, target.LimitPrice),
		number("stop or trail price", ev.AuxPrice, target.AuxPrice),
	}
	for _, reason := range checks {
		if reason != "" {
			return reason
		}
	}
	return ""
}

// twsReplacementOrder is the bound order with the new quantity and limit
// price, sent under the bound order id so TWS treats it as a modification.
func twsReplacementOrder(bound OrderLifecycleEvent, replacement TWSOrderControlReplacement, account string) (*IBKROrder, error) {
	if !strings.EqualFold(bound.OrderType, "LMT") || (bound.TIF != "" && !strings.EqualFold(bound.TIF, "DAY")) {
		return nil, fmt.Errorf("%w: only DAY limit orders are modified", ErrTWSOrderControlGate)
	}
	if !bound.ModifyTermsKnown {
		return nil, fmt.Errorf("%w: broker order terms are incomplete; modify in TWS", ErrTWSOrderControlGate)
	}
	if bound.ModifyUnsupported != "" {
		return nil, fmt.Errorf("%w: %s; modify in TWS", ErrTWSOrderControlGate, bound.ModifyUnsupported)
	}
	if !bound.AlgoKnown || (bound.AlgoStrategy != "" && bound.AlgoStrategy != "Adaptive") ||
		(bound.AlgoStrategy == "" && bound.AdaptivePriority != "") ||
		(bound.AlgoStrategy == "Adaptive" && bound.AdaptivePriority != "Urgent" && bound.AdaptivePriority != "Normal" && bound.AdaptivePriority != "Patient") {
		return nil, fmt.Errorf("%w: unsupported or unknown algorithm terms; modify in TWS", ErrTWSOrderControlGate)
	}
	if replacement.Quantity <= bound.Filled {
		return nil, fmt.Errorf("%w: filled %s since review", ErrTWSOrderControlChanged, formatTWSQuantity(bound.Filled))
	}
	// The permanent id identifies the order for the journal, not the wire:
	// a modify is the bound order id with the new terms.
	order := &IBKROrder{
		OrderID: bound.OrderID, ClientID: 0, Account: nonEmptyTWS(bound.Account, account),
		Symbol: bound.Symbol, SecType: bound.SecType, ConID: bound.ConID, Exchange: bound.Exchange, Currency: bound.Currency,
		Expiry: bound.Expiry, Strike: bound.Strike, Right: bound.Right, LocalSymbol: bound.LocalSymbol, TradingClass: bound.TradingClass,
		Action: bound.Action, TotalQty: int(replacement.Quantity), OrderType: "LMT", LmtPrice: replacement.LimitPrice,
		TIF: nonEmptyTWS(bound.TIF, "DAY"), OutsideRth: bound.OutsideRth, TriggerMethod: bound.TriggerMethod, OrderRef: bound.OrderRef,
		Transmit: true, OcaGroup: bound.OcaGroup, OcaType: bound.OcaType,
		AdaptivePriority: bound.AdaptivePriority, PrimaryExch: bound.PrimaryExch, OpenClose: bound.OpenClose,
	}
	if bound.Multiplier != 0 {
		order.Multiplier = strconv.Itoa(bound.Multiplier)
	}
	if order.Exchange == "" {
		order.Exchange = "SMART"
	}
	return order, nil
}

// awaitTWSOrderControlAnswer waits a bounded time for the broker's answer
// to the sent request. A confirming status or echo is confirmed, an
// acknowledgement without finality is pending, a broker error for the bound
// id is a refusal, and silence is uncertain.
func awaitTWSOrderControlAnswer(ctx context.Context, session *twsOrderControlSession, receipt TWSOrderControlReceipt, action string, replacement TWSOrderControlReplacement, timeout time.Duration) (TWSOrderControlReceipt, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	pending := false
	for {
		select {
		case a := <-session.answers:
			if a.event.OrderID != receipt.BoundOrderID {
				continue
			}
			if a.code != 0 {
				if a.code >= 2100 && a.code < 2200 { // warnings travel with acceptance
					continue
				}
				receipt.Outcome = TWSOrderControlRefused
				receipt.BrokerCode, receipt.BrokerMessage = a.code, a.msg
				return receipt, fmt.Errorf("%w: %d %s", ErrTWSOrderControlRefused, a.code, a.msg)
			}
			status := strings.ToLower(strings.TrimSpace(a.event.Status))
			if a.event.Status != "" {
				receipt.BrokerStatus = a.event.Status
			}
			switch action {
			case TWSOrderControlCancel:
				switch status {
				case "cancelled", "canceled", "apicancelled", "apicanceled":
					receipt.Outcome = TWSOrderControlConfirmed
					return receipt, nil
				case "pendingcancel":
					pending = true
				}
			default:
				// Only the openOrder echo carrying the new terms proves the
				// replacement; a status alone could describe the old order.
				if a.event.Type == OrderLifecycleEventOpenOrder {
					if math.Abs(a.event.TotalQuantity-replacement.Quantity) <= 1e-9 && math.Abs(a.event.LimitPrice-replacement.LimitPrice) <= 1e-9 {
						receipt.Outcome = TWSOrderControlConfirmed
						return receipt, nil
					}
					continue
				}
				switch status {
				case "submitted", "presubmitted", "pendingsubmit":
					pending = true
				case "filled", "cancelled", "canceled", "apicancelled", "apicanceled", "inactive":
					receipt.Outcome = TWSOrderControlRefused
					return receipt, fmt.Errorf("%w: the order is %s", ErrTWSOrderControlNotWorking, a.event.Status)
				}
			}
		case <-timer.C:
			if pending {
				receipt.Outcome = TWSOrderControlPending
				return receipt, nil
			}
			receipt.Outcome = TWSOrderControlUncertain
			return receipt, fmt.Errorf("%w: no answer within %s", ErrTWSOrderControlUncertain, timeout)
		case <-ctx.Done():
			if pending {
				receipt.Outcome = TWSOrderControlPending
				return receipt, nil
			}
			receipt.Outcome = TWSOrderControlUncertain
			return receipt, fmt.Errorf("%w: %w", ErrTWSOrderControlUncertain, ctx.Err())
		}
	}
}

func nonEmptyTWS(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func formatTWSQuantity(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}
