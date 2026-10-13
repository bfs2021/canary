package daemon

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/osauer/canary/v2/internal/rpc"
)

// reduceOrderSource tags the order journal so a discretionary percentage trim
const reduceOrderSource = "manual_reduce"

// reducePriceBoundPct is how far, in percent of the reviewed mid, the market
// may have moved before a tokened reduce submit refuses to send the reviewed
// limit (price_moved). The limit itself never moves: the owner confirmed it.
// Every preview discloses the bound it will be held to (terms.price_bound).
// Owner decision 2026-10-13 06:59 CEST ("0.5% stocks 2% options"): stocks,
// ETFs and every other contract 0.5%; options 2%, because an option's mid
// moves several tenths of a percent within seconds.
const (
	reducePriceBoundPct       = 0.5
	reduceOptionPriceBoundPct = 2.0
)

// reducePriceBoundPctFor is the owner's band for the contract's security type.
func reducePriceBoundPctFor(secType string) float64 {
	if strings.EqualFold(strings.TrimSpace(secType), "OPT") {
		return reduceOptionPriceBoundPct
	}
	return reducePriceBoundPct
}

// reduceAdaptiveNote is the terms' plain statement when a reduction asked for
// or would default to Adaptive on a contract the broker layer does not send
// it for; the order is then a plain limit at the mid.
const reduceAdaptiveNote = "Adaptive not supported for this contract"

// reduceDedupeTTL is how long a submit RequestRef is remembered so a
// double-tap or client retry replays the prior result instead of placing again.
const reduceDedupeTTL = 90 * time.Second

// reduceDedupeEntry is one remembered reduce submit, single or basket.
type reduceDedupeEntry struct {
	at     time.Time
	single *rpc.TradeProposalReduceResult
	basket *rpc.TradeProposalReducePortfolioResult
}

// isProtectiveShort reports whether a holding carries short (bearish) delta
// short stock. Used only by the single-position reduce path (IncludeHedges
// sign-matching, with no separate hedge flag. When the broker delta is known
// exclusion fails safe toward protecting the position rather than silently
func isProtectiveShort(row rpc.PositionView) bool {
	if row.Quantity == 0 {
		return false
	}
	if positionWireSecType(row.SecType) == "OPT" {
		if row.Delta != nil {
			return *row.Delta < 0
		}
		right := strings.ToUpper(strings.TrimSpace(row.Right))
		return (right == "P" && row.Quantity > 0) || (right == "C" && row.Quantity < 0)
	}
	if row.Delta != nil {
		return *row.Delta < 0
	}
	return row.Quantity < 0
}

// reduceEligible scopes the discretionary trim to stocks/ETFs (long or short)
func reduceEligible(row rpc.PositionView) bool {
	switch strings.ToUpper(strings.TrimSpace(row.SecType)) {
	case rpc.SecTypeOption, "OPT":
		return row.Quantity > 0
	case rpc.SecTypeStock, "STK", "ETF":
		return row.Quantity != 0
	default:
		return false
	}
}

// findReducePosition resolves the holding the trim acts on. ConID is the
// unambiguous key; a bare Symbol is accepted only when it matches exactly one
func findReducePosition(pos *rpc.PositionsResult, conID int, symbol string) (rpc.PositionView, []rpc.TradingBlocker) {
	if pos == nil {
		return rpc.PositionView{}, []rpc.TradingBlocker{{Code: "positions_unavailable", Message: "current positions are unavailable", Action: "Retry once the daemon has refreshed positions."}}
	}
	rows := make([]rpc.PositionView, 0, len(pos.Stocks)+len(pos.Options))
	rows = append(rows, pos.Stocks...)
	rows = append(rows, pos.Options...)
	if conID > 0 {
		for _, row := range rows {
			if row.ConID == conID {
				return row, nil
			}
		}
		return rpc.PositionView{}, []rpc.TradingBlocker{{Code: "position_not_found", Message: fmt.Sprintf("no held position matches con_id %d", conID), Action: "Refresh positions and choose a current holding."}}
	}
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	if symbol == "" {
		return rpc.PositionView{}, []rpc.TradingBlocker{{Code: "bad_request", Message: "reduce requires con_id or a stock symbol", Action: "Pass con_id (preferred) or a unique stock symbol."}}
	}
	var matches []rpc.PositionView
	for _, row := range pos.Stocks {
		if strings.EqualFold(strings.TrimSpace(row.Symbol), symbol) {
			matches = append(matches, row)
		}
	}
	switch len(matches) {
	case 0:
		return rpc.PositionView{}, []rpc.TradingBlocker{{Code: "position_not_found", Message: fmt.Sprintf("no held stock matches symbol %q", symbol), Action: "Pass con_id to target an option leg, or choose a current stock holding."}}
	case 1:
		return matches[0], nil
	default:
		return rpc.PositionView{}, []rpc.TradingBlocker{{Code: "ambiguous_symbol", Message: fmt.Sprintf("symbol %q matches %d stock positions", symbol, len(matches)), Action: "Pass con_id to disambiguate."}}
	}
}

// reduceQuantityForPercent floors percent×|position| to whole units so the trim
// never rounds up into a flip, and treats 100 as an exact close. It returns a
// blocker when the position is not reducible or the percentage rounds to zero.
func reduceQuantityForPercent(position float64, percent int) (int, []rpc.TradingBlocker) {
	posAbsInt, _ := closeReduceQuantity(position)
	unit := "shares"
	if posAbsInt == 1 {
		unit = "share"
	}
	if posAbsInt < 1 {
		return 0, []rpc.TradingBlocker{{Code: "position_not_reducible", Message: "position magnitude is below one whole unit", Action: "There is nothing whole to reduce."}}
	}
	if percent <= 0 || percent > 100 {
		return 0, []rpc.TradingBlocker{{Code: "bad_request", Message: fmt.Sprintf("percent %d must be between 1 and 100", percent), Action: "Choose 25, 50, 75, or 100."}}
	}
	if percent >= 100 {
		return posAbsInt, nil
	}
	qty := int(math.Floor(float64(percent)/100.0*float64(posAbsInt) + 1e-9))
	if qty < 1 {
		return 0, []rpc.TradingBlocker{{Code: "percent_too_small", Message: fmt.Sprintf("reducing %d%% of %d %s rounds to zero units", percent, posAbsInt, unit), Action: "Choose a larger percentage, or 100% to close the position."}}
	}
	if qty > posAbsInt {
		qty = posAbsInt
	}
	return qty, nil
}

// reduceAdaptivePriority resolves the priority a reduce asks for. Empty means
// Normal (the reduction default); "none" asks for a plain limit; Normal,
// Patient and Urgent are IBKR's priorities. Anything else is refused rather
// than silently mapped to a default.
func reduceAdaptivePriority(requested string) (string, []rpc.TradingBlocker) {
	switch strings.ToLower(strings.TrimSpace(requested)) {
	case "":
		return "Normal", nil
	case "none", "off", "plain":
		return "", nil
	case "normal":
		return "Normal", nil
	case "patient":
		return "Patient", nil
	case "urgent":
		return "Urgent", nil
	default:
		return "", []rpc.TradingBlocker{{Code: "bad_request", Message: fmt.Sprintf("adaptive_priority %q is not Normal, Patient, Urgent or none", strings.TrimSpace(requested)), Action: "Choose Normal, Patient or Urgent, or none for a plain limit."}}
	}
}

// reduceAdaptiveSupported reports whether the reduce order for this contract
// can carry IBKR's Adaptive algorithm as Canary sends it: a US stock or ETF
// limit routed SMART for the regular session, the one shape the broker layer
// admits (pkg/ibkr validateAdaptiveOrder). An option cannot: the broker layer
// refuses an Adaptive option order, so the reduction sends a plain limit at
// the mid and its terms say so.
func reduceAdaptiveSupported(contract rpc.ContractParams) bool {
	switch strings.ToUpper(strings.TrimSpace(contract.SecType)) {
	case "STK", "ETF":
	default:
		return false
	}
	if exchange := strings.ToUpper(strings.TrimSpace(contract.Exchange)); exchange != "" && exchange != "SMART" {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(contract.Currency), "USD")
}

// preparedReduce holds the resolved order intent and disclosure for a trim.
type preparedReduce struct {
	row      rpc.PositionView
	params   rpc.OrderPreviewParams
	secType  string
	action   string
	qty      int
	hedge    bool
	algoNote string
}

// prepareReduce resolves the holding for a single-position trim and delegates to
// prepareReduceForRow. Any returned blockers are terminal (no token minted) and
// carry remediation. It performs no broker write — the caller routes the result
func (s *Server) prepareReduce(ctx context.Context, p rpc.TradeProposalReduceParams) (preparedReduce, []rpc.TradingBlocker, error) {
	posResult, err := s.reducePositions(ctx)
	if err != nil {
		return preparedReduce{}, nil, err
	}
	row, blockers := findReducePosition(posResult, p.ConID, p.Symbol)
	if len(blockers) > 0 {
		return preparedReduce{}, blockers, nil
	}
	prep, blockers := prepareReduceForRow(row, p)
	return prep, blockers, nil
}

// reducePositions is the one positions read behind a reduce preview, sweep
// and tokened submit; tests replace it through reducePositionsForTest.
func (s *Server) reducePositions(ctx context.Context) (*rpc.PositionsResult, error) {
	if s.reducePositionsForTest != nil {
		return s.reducePositionsForTest(ctx)
	}
	return s.handlePositionsList(ctx, &rpc.Request{})
}

// prepareReduceForRow applies the hedge exclusion, sizes the order, and builds
// the gated preview params for an already-resolved position row. It is the
// single-position trim's primitive: it never reads positions or writes to the
// broker. Terminal blockers (not_reducible, hedge_excluded, percent_too_small,
// ...) come back with the row echoed so callers can disclose what was acted on.
func prepareReduceForRow(row rpc.PositionView, p rpc.TradeProposalReduceParams) (preparedReduce, []rpc.TradingBlocker) {
	hedge := isProtectiveShort(row)
	if !reduceEligible(row) {
		return preparedReduce{row: row, hedge: hedge}, []rpc.TradingBlocker{{Code: "not_reducible", Message: fmt.Sprintf("%s %s is not eligible for a percentage reduce", row.Symbol, positionWireSecType(row.SecType)), Action: "The reduce action covers stocks/ETFs and long options."}}
	}
	if hedge && !p.IncludeHedges {
		return preparedReduce{row: row, hedge: hedge}, []rpc.TradingBlocker{{Code: "hedge_excluded", Message: fmt.Sprintf("%s is a protective short (hedge) and is excluded from the reduce workflow", row.Symbol), Action: "Set include_hedges to trim this hedge deliberately."}}
	}
	adaptive, blockers := reduceAdaptivePriority(p.AdaptivePriority)
	if len(blockers) > 0 {
		return preparedReduce{row: row, hedge: hedge}, blockers
	}
	qty, blockers := reduceQuantityForPercent(row.Quantity, p.Percent)
	if len(blockers) > 0 {
		return preparedReduce{row: row, hedge: hedge}, blockers
	}
	prep := buildPreparedReduce(row, qty, p.TimeoutMs, adaptive)
	prep.hedge = hedge
	return prep, nil
}

// buildPreparedReduce assembles the gated order-preview params for a row whose
// quantity has already been resolved — by reduceQuantityForPercent (single
// position) or by the portfolio sweep's dollar-delta pro-rata allocator. It is
// the shared tail both sizing paths converge on. adaptive is the resolved
// priority ("" for a plain limit); it rides on the draft only where the
// contract supports Adaptive, otherwise the terms carry reduceAdaptiveNote.
func buildPreparedReduce(row rpc.PositionView, qty int, timeoutMs int, adaptive string) preparedReduce {
	secType := positionWireSecType(row.SecType)
	action := rpc.OrderActionSell
	if row.Quantity < 0 {
		action = rpc.OrderActionBuy
	}
	contract := proposalContractFromPosition(row, secType)
	params := rpc.OrderPreviewParams{
		Action:    action,
		Contract:  contract,
		Quantity:  qty,
		OrderType: rpc.OrderTypeLMT,
		Strategy:  rpc.OrderStrategyPatientLimit,
		TIF:       rpc.OrderTIFDay,
		TimeoutMs: timeoutMs,
		Source:    reduceOrderSource,
	}
	prep := preparedReduce{row: row, params: params, secType: secType, action: action, qty: qty}
	switch {
	case adaptive == "":
	case reduceAdaptiveSupported(contract):
		prep.params.AdaptivePriority = adaptive
	default:
		prep.algoNote = reduceAdaptiveNote
	}
	return prep
}

// preparedReduceWithQty is the portfolio sweep's entry point: qty is already
// caller should never offer an out-of-scope row or a zero qty) before
func preparedReduceWithQty(row rpc.PositionView, qty int, timeoutMs int, adaptive string) (preparedReduce, []rpc.TradingBlocker) {
	if !reduceEligible(row) {
		return preparedReduce{row: row}, []rpc.TradingBlocker{{Code: "not_reducible", Message: fmt.Sprintf("%s %s is not eligible for a portfolio trim", row.Symbol, positionWireSecType(row.SecType)), Action: "The sweep covers stocks/ETFs and long options."}}
	}
	if qty < 1 {
		return preparedReduce{row: row}, []rpc.TradingBlocker{{Code: "qty_zero", Message: "this holding's allocated risk share rounded to less than one unit", Action: "Nothing to trim at this percentage; choose a larger percentage."}}
	}
	return buildPreparedReduce(row, qty, timeoutMs, adaptive), nil
}

// reduceClock returns the daemon clock, honoring the test override.
func (s *Server) reduceClock() time.Time {
	if s != nil && s.now != nil {
		return s.now().UTC()
	}
	return time.Now().UTC()
}

// reduceResultBase seeds the result with the resolved disclosure fields shared
func reduceResultBase(prep preparedReduce, percent int, now time.Time) rpc.TradeProposalReduceResult {
	secType := prep.secType
	if secType == "" && prep.row.SecType != "" {
		secType = positionWireSecType(prep.row.SecType)
	}
	return rpc.TradeProposalReduceResult{
		ConID:            prep.row.ConID,
		Symbol:           strings.ToUpper(strings.TrimSpace(prep.row.Symbol)),
		SecType:          secType,
		Action:           prep.action,
		Percent:          percent,
		PositionQuantity: prep.row.Quantity,
		ReduceQuantity:   prep.qty,
		HedgeLike:        prep.hedge,
		AsOf:             now,
	}
}

// reduceQuoteMid is the mid a reduce prices and bounds from: the quote's own
// midpoint, else the bid-ask mid.
func reduceQuoteMid(quote rpc.OrderQuoteSnapshot) (float64, bool) {
	if quote.Midpoint != nil && *quote.Midpoint > 0 && !math.IsInf(*quote.Midpoint, 0) {
		return *quote.Midpoint, true
	}
	if quote.Bid != nil && quote.Ask != nil && *quote.Bid > 0 && *quote.Ask >= *quote.Bid {
		return (*quote.Bid + *quote.Ask) / 2, true
	}
	return 0, false
}

// reducePriceBound states the band the reviewed mid admits for the
// contract's security type: ±reducePriceBoundPctFor(secType).
func reducePriceBound(reviewed rpc.OrderQuoteSnapshot, secType string) (rpc.TradeProposalReducePriceBound, bool) {
	mid, ok := reduceQuoteMid(reviewed)
	if !ok {
		return rpc.TradeProposalReducePriceBound{}, false
	}
	pct := reducePriceBoundPctFor(secType)
	span := mid * pct / 100
	return rpc.TradeProposalReducePriceBound{ReferenceMid: mid, MaxMovePct: pct, Low: mid - span, High: mid + span}, true
}

// reducePriceBoundCheck holds the current mid to the reviewed bound. The
// reviewed limit is never re-priced: a mid outside the band refuses the
// submit and asks for a new preview.
func reducePriceBoundCheck(reviewed, current rpc.OrderQuoteSnapshot, secType string) (rpc.TradeProposalReducePriceBound, []rpc.TradingBlocker) {
	bound, ok := reducePriceBound(reviewed, secType)
	if !ok {
		return bound, []rpc.TradingBlocker{{Code: "price_bound_unavailable", Message: "the reviewed quote has no mid to bound the market move by", Action: "Preview again with a live bid and ask."}}
	}
	mid, ok := reduceQuoteMid(current)
	if !ok {
		return bound, []rpc.TradingBlocker{{Code: "quote_unavailable", Message: "no current bid and ask to check the reviewed terms against", Action: "Preview again once a live quote is available; nothing was sent."}}
	}
	if mid < bound.Low-1e-9 || mid > bound.High+1e-9 {
		return bound, []rpc.TradingBlocker{{Code: "price_moved", Message: fmt.Sprintf("the mid moved to %.4f, outside the reviewed %.4f ±%.2f%% (%.4f to %.4f)", mid, bound.ReferenceMid, bound.MaxMovePct, bound.Low, bound.High), Action: "Preview again at the current market; the reviewed limit was not sent."}}
	}
	return bound, nil
}

// reduceAlgoNoteFor says, for a draft without Adaptive, whether the contract
// is the reason.
func reduceAlgoNoteFor(draft rpc.OrderDraft) string {
	if draft.AdaptivePriority == "" && !reduceAdaptiveSupported(draft.Contract) {
		return reduceAdaptiveNote
	}
	return ""
}

// reduceTermsFor is the exact-terms block a token binds, written from the
// signed draft and the quote it was priced from.
func reduceTermsFor(draft rpc.OrderDraft, quote rpc.OrderQuoteSnapshot, tokenID string, expiresAt time.Time, algoNote string) *rpc.TradeProposalReduceTerms {
	terms := &rpc.TradeProposalReduceTerms{
		PreviewTokenID:   tokenID,
		ExpiresAt:        expiresAt,
		Contract:         draft.Contract,
		Action:           draft.Action,
		Quantity:         draft.Quantity,
		OrderType:        draft.OrderType,
		LimitPrice:       draft.LimitPrice,
		TIF:              draft.TIF,
		AdaptivePriority: draft.AdaptivePriority,
		AlgoNote:         algoNote,
	}
	if draft.AdaptivePriority != "" {
		terms.Algo = "Adaptive"
	}
	terms.PriceBound, _ = reducePriceBound(quote, draft.Contract.SecType)
	return terms
}

// reduceAttachPreview fills the token, terms and sanitized preview of one
// minted reduce preview into the result.
func reduceAttachPreview(res *rpc.TradeProposalReduceResult, preview *rpc.OrderPreviewResult, algoNote string) {
	res.PreviewToken = preview.PreviewToken
	res.PreviewTokenID = preview.PreviewTokenID
	res.PreviewTokenExpiresAt = preview.PreviewTokenExpiresAt
	res.SubmitEligible = preview.SubmitEligible
	res.Preview = sanitizeProposalPreviewForProposal(preview, rpc.TradeProposal{})
	res.Terms = reduceTermsFor(preview.Draft, preview.Quote, preview.PreviewTokenID, preview.PreviewTokenExpiresAt, algoNote)
}

func (s *Server) tradeProposalReducePreview(ctx context.Context, p rpc.TradeProposalReduceParams) (*rpc.TradeProposalReduceResult, error) {
	now := s.reduceClock()
	prep, blockers, err := s.prepareReduce(ctx, p)
	if err != nil {
		return nil, err
	}
	if len(blockers) > 0 {
		res := reduceResultBase(prep, p.Percent, now)
		res.Blockers = blockers
		return &res, nil
	}
	preview, err := s.previewOrder(ctx, prep.params)
	if err != nil {
		res := reduceResultBase(prep, p.Percent, now)
		res.Blockers = []rpc.TradingBlocker{{Code: "preview_failed", Message: err.Error(), Action: "Refresh positions and preview again."}}
		return &res, nil
	}
	res := reduceResultBase(prep, p.Percent, now)
	if blockers := reduceCloseReduceBlockers(preview); len(blockers) > 0 {
		res.Blockers = blockers
		return &res, nil
	}
	reduceAttachPreview(&res, preview, prep.algoNote)
	if !preview.SubmitEligible {
		res.PreviewToken = ""
		res.Blockers = previewNotSubmitEligibleBlockers(preview)
		return &res, nil
	}
	res.Accepted = true
	return &res, nil
}

func (s *Server) tradeProposalReduceSubmit(ctx context.Context, p rpc.TradeProposalReduceParams) (*rpc.TradeProposalReduceResult, error) {
	now := s.reduceClock()
	// Write gate before the dedupe replay: a freeze engaged between the
	// original call and a RequestRef retry must surface as a blocker, not
	// replay a pre-freeze result.
	if blockers := s.proposalSubmitWriteBlockers(p.Origin); len(blockers) > 0 {
		res := rpc.TradeProposalReduceResult{ConID: p.ConID, Percent: p.Percent, Blockers: blockers, AsOf: now}
		return &res, nil
	}
	if e, ok := s.reduceReplay(p.RequestRef); ok && e.single != nil {
		clone := *e.single
		clone.Replayed = true
		return &clone, nil
	}
	if strings.TrimSpace(p.PreviewToken) != "" {
		res, err := s.reduceSubmitToken(ctx, p, now)
		if err != nil {
			return nil, err
		}
		s.reduceRemember(p.RequestRef, reduceDedupeEntry{single: res})
		return res, nil
	}
	prep, blockers, err := s.prepareReduce(ctx, p)
	if err != nil {
		return nil, err
	}
	if len(blockers) > 0 {
		res := reduceResultBase(prep, p.Percent, now)
		res.Blockers = blockers
		return &res, nil
	}
	preview, err := s.previewOrder(ctx, prep.params)
	if err != nil {
		res := reduceResultBase(prep, p.Percent, now)
		res.Blockers = []rpc.TradingBlocker{{Code: "preview_failed", Message: err.Error(), Action: "Refresh positions and preview again."}}
		return &res, nil
	}
	res := reduceResultBase(prep, p.Percent, now)
	reduceAttachPreview(&res, preview, prep.algoNote)
	res.PreviewToken = "" // consumed below; a submit never hands the token out
	if blockers := reduceCloseReduceBlockers(preview); len(blockers) > 0 {
		res.Blockers = blockers
		return &res, nil
	}
	if !preview.SubmitEligible {
		res.Blockers = previewNotSubmitEligibleBlockers(preview)
		return &res, nil
	}
	place, err := s.proposalPlaceOrder(ctx, rpc.OrderPlaceParams{PreviewToken: preview.PreviewToken, TimeoutMs: p.TimeoutMs, Origin: p.Origin})
	if err != nil {
		res.Blockers = []rpc.TradingBlocker{{Code: "submit_failed", Message: err.Error(), Action: "Reconcile open orders before retrying."}}
		return &res, nil
	}
	res.Accepted = place.Accepted
	res.Place = place
	res.OrderRef = place.OrderRef
	res.Message = place.Message
	res.RequestRef = strings.TrimSpace(p.RequestRef)
	s.reduceRemember(p.RequestRef, reduceDedupeEntry{single: &res})
	return &res, nil
}

// reduceTokenOutcome is one reduce token taken through the submit checks:
// the verified payload and the current row when the token read, the bound
// the market was held to, and either the place result or the blockers that
// stopped it. Nothing is sent when blockers is non-empty.
type reduceTokenOutcome struct {
	tokenID  string
	payload  orderPreviewTokenPayload
	verified bool
	row      rpc.PositionView
	rowFound bool
	bound    rpc.TradeProposalReducePriceBound
	place    *rpc.OrderPlaceResult
	blockers []rpc.TradingBlocker
}

// reduceVerifyToken checks that a token is a current, submit-eligible reduce
// preview of this daemon's gate: the signature, authority epoch, expiry,
// mode, account and endpoint are the order path's own checks; the source
// keeps an ordinary order preview out of the reduce path, whose further
// checks assume a reduction of a held position.
func (s *Server) reduceVerifyToken(token string) (orderPreviewTokenPayload, []rpc.TradingBlocker) {
	payload, err := s.verifyPreviewTokenForPlace(token)
	if err != nil {
		code := "token_invalid"
		if strings.Contains(err.Error(), "expired") {
			code = "token_expired"
		}
		return orderPreviewTokenPayload{}, []rpc.TradingBlocker{{Code: code, Message: err.Error(), Action: "Preview the reduction again and submit the new token; nothing was sent."}}
	}
	if payload.Draft.Source != reduceOrderSource {
		return orderPreviewTokenPayload{}, []rpc.TradingBlocker{{Code: "token_not_reduce", Message: "the preview token is not a reduce preview", Action: "Submit a token from `proposals reduce`; an order preview goes through `order place`."}}
	}
	return payload, nil
}

// reduceTokenPositionCheck holds the reviewed quantity to the position as it
// is now: the holding must still be there on the reviewed side, and the
// reviewed reduction may not exceed what is held (owner decision 2026-10-12
// 19:55 CEST: a sale of at most the confirmed long is admitted). A changed
// position refuses in plain words; the quantity is never resized.
func reduceTokenPositionCheck(pos *rpc.PositionsResult, draft rpc.OrderDraft) (rpc.PositionView, []rpc.TradingBlocker) {
	row, blockers := findReducePosition(pos, draft.Contract.ConID, "")
	if len(blockers) > 0 {
		for i := range blockers {
			if blockers[i].Code == "position_not_found" {
				blockers[i] = rpc.TradingBlocker{Code: "position_changed", Message: fmt.Sprintf("%s is no longer held; the reviewed reduction of %d cannot be sent", strings.ToUpper(strings.TrimSpace(draft.Contract.Symbol)), draft.Quantity), Action: "Refresh positions; nothing was sent."}
			}
		}
		return rpc.PositionView{}, blockers
	}
	sell := strings.EqualFold(draft.Action, rpc.OrderActionSell)
	if (sell && row.Quantity <= 0) || (!sell && row.Quantity >= 0) {
		return row, []rpc.TradingBlocker{{Code: "position_changed", Message: fmt.Sprintf("%s is now %s, not on the reviewed side", strings.ToUpper(strings.TrimSpace(row.Symbol)), formatPositionQuantity(row.Quantity)), Action: "Preview the reduction again; nothing was sent."}}
	}
	held, _ := closeReduceQuantity(row.Quantity)
	if draft.Quantity > held {
		return row, []rpc.TradingBlocker{{Code: "position_changed", Message: fmt.Sprintf("%s is now %s held; the reviewed reduction of %d exceeds it", strings.ToUpper(strings.TrimSpace(row.Symbol)), formatPositionQuantity(row.Quantity), draft.Quantity), Action: "Preview the reduction again at the current position; nothing was sent."}}
	}
	return row, nil
}

func formatPositionQuantity(q float64) string {
	if q == math.Trunc(q) {
		return fmt.Sprintf("%d", int64(q))
	}
	return fmt.Sprintf("%g", q)
}

// reduceTokenLeg takes one reduce token through every submit check and, when
// all pass, places exactly the token's draft. The checks stop at the first
// refusal; a refusal here sends nothing. pos is the positions read the
// caller made once for the whole request.
func (s *Server) reduceTokenLeg(ctx context.Context, token string, pos *rpc.PositionsResult, timeoutMs int, origin string) reduceTokenOutcome {
	var out reduceTokenOutcome
	payload, blockers := s.reduceVerifyToken(token)
	if len(blockers) > 0 {
		out.blockers = blockers
		return out
	}
	out.payload, out.verified, out.tokenID = payload, true, payload.TokenID
	row, blockers := reduceTokenPositionCheck(pos, payload.Draft)
	out.row, out.rowFound = row, row.ConID != 0
	if len(blockers) > 0 {
		out.blockers = blockers
		return out
	}
	legCtx, cancel := legContext(ctx, timeoutMs)
	defer cancel()
	quote, err := s.fetchPreviewQuoteBound(legCtx, payload.Draft.Contract, orderPreviewTimeout(timeoutMs), nil)
	if err != nil {
		out.blockers = []rpc.TradingBlocker{{Code: "quote_unavailable", Message: err.Error(), Action: "Preview again once a live quote is available; nothing was sent."}}
		return out
	}
	out.bound, blockers = reducePriceBoundCheck(payload.Quote, quote, payload.Draft.Contract.SecType)
	if len(blockers) > 0 {
		out.blockers = blockers
		return out
	}
	place, err := s.proposalPlaceOrder(legCtx, rpc.OrderPlaceParams{PreviewToken: token, TimeoutMs: timeoutMs, Origin: origin})
	if err != nil {
		code := "submit_failed"
		action := "Reconcile open orders before retrying."
		if errors.Is(err, errOrderPreviewTokenAlreadyUsed) {
			code, action = "token_consumed", "This token was already submitted; check open orders, then preview again for a further reduction."
		}
		out.blockers = []rpc.TradingBlocker{{Code: code, Message: err.Error(), Action: action}}
		return out
	}
	out.place = place
	return out
}

// reduceSubmitToken places exactly the reviewed terms of one reduce token.
func (s *Server) reduceSubmitToken(ctx context.Context, p rpc.TradeProposalReduceParams, now time.Time) (*rpc.TradeProposalReduceResult, error) {
	res := &rpc.TradeProposalReduceResult{Percent: p.Percent, RequestRef: strings.TrimSpace(p.RequestRef), AsOf: now}
	pos, err := s.reducePositions(ctx)
	if err != nil {
		res.Blockers = []rpc.TradingBlocker{{Code: "positions_unavailable", Message: err.Error(), Action: "Retry once the daemon has refreshed positions; nothing was sent."}}
		return res, nil
	}
	out := s.reduceTokenLeg(ctx, strings.TrimSpace(p.PreviewToken), pos, p.TimeoutMs, p.Origin)
	reduceApplyTokenOutcome(res, out)
	return res, nil
}

// reduceApplyTokenOutcome writes a token outcome into the single result: the
// terms the token bound, the position it was checked against, and the place
// outcome or the blockers.
func reduceApplyTokenOutcome(res *rpc.TradeProposalReduceResult, out reduceTokenOutcome) {
	if out.verified {
		draft := out.payload.Draft
		res.ConID = draft.Contract.ConID
		res.Symbol = strings.ToUpper(strings.TrimSpace(draft.Contract.Symbol))
		res.SecType = draft.Contract.SecType
		res.Action = draft.Action
		res.ReduceQuantity = draft.Quantity
		res.PreviewTokenID = out.payload.TokenID
		res.PreviewTokenExpiresAt = out.payload.ExpiresAt
		res.SubmitEligible = true
		res.Terms = reduceTermsFor(draft, out.payload.Quote, out.payload.TokenID, out.payload.ExpiresAt, reduceAlgoNoteFor(draft))
	}
	if out.rowFound {
		res.PositionQuantity = out.row.Quantity
		res.HedgeLike = isProtectiveShort(out.row)
	}
	if len(out.blockers) > 0 {
		res.Blockers = out.blockers
		return
	}
	res.Accepted = out.place.Accepted
	res.Place = out.place
	res.OrderRef = out.place.OrderRef
	res.Message = out.place.Message
}

// reduceReplay returns the remembered result for a repeated RequestRef, if
// the reference is within reduceDedupeTTL.
func (s *Server) reduceReplay(ref string) (reduceDedupeEntry, bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return reduceDedupeEntry{}, false
	}
	s.reduceDedupeMu.Lock()
	defer s.reduceDedupeMu.Unlock()
	now := s.reduceClock()
	for k, e := range s.reduceDedupe {
		if now.Sub(e.at) > reduceDedupeTTL {
			delete(s.reduceDedupe, k)
		}
	}
	e, ok := s.reduceDedupe[ref]
	return e, ok
}

func (s *Server) reduceRemember(ref string, e reduceDedupeEntry) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return
	}
	s.reduceDedupeMu.Lock()
	defer s.reduceDedupeMu.Unlock()
	if s.reduceDedupe == nil {
		s.reduceDedupe = make(map[string]reduceDedupeEntry)
	}
	e.at = s.reduceClock()
	s.reduceDedupe[ref] = e
}

// reduceCloseReduceBlockers is the defense-in-depth gate: even though the size
// is floored to the held quantity, a stale-position broker preview that
// classifies the order as anything but close/reduce fails closed here, mirroring
// proposalPreviewSafetyBlockers for the proposal path.
func reduceCloseReduceBlockers(preview *rpc.OrderPreviewResult) []rpc.TradingBlocker {
	if preview == nil {
		return []rpc.TradingBlocker{{Code: "preview_missing", Message: "order preview result is unavailable", Action: "Preview the reduce again."}}
	}
	if !isRiskReducing(preview.Position.Effect) {
		return []rpc.TradingBlocker{{Code: "effect_not_close_reduce", Message: fmt.Sprintf("preview effect %q is not close/reduce", preview.Position.Effect), Action: "Refresh positions and preview again; the reduce never opens, increases, or flips exposure."}}
	}
	return nil
}

func (s *Server) handleTradeProposalsReducePreview(ctx context.Context, req *rpc.Request) (*rpc.TradeProposalReduceResult, error) {
	var p rpc.TradeProposalReduceParams
	if err := decodeParams(req.Params, &p); err != nil {
		return nil, err
	}
	return s.tradeProposalReducePreview(ctx, p)
}

func (s *Server) handleTradeProposalsReduceSubmit(ctx context.Context, req *rpc.Request) (*rpc.TradeProposalReduceResult, error) {
	var p rpc.TradeProposalReduceParams
	if err := decodeParams(req.Params, &p); err != nil {
		return nil, err
	}
	// Serialize the check-then-act broker write against every other writer,
	// matching handleTradeProposalsSubmit/handleOrderPlace. proposalPlaceOrder
	// -> placeOrder does not self-lock; the handler holds the mutex.
	s.brokerWriteMu.Lock()
	defer s.brokerWriteMu.Unlock()
	return s.tradeProposalReduceSubmit(ctx, p)
}
