package daemon

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/osauer/canary/v2/internal/config"
	"github.com/osauer/canary/v2/internal/daemon/corestore"
	"github.com/osauer/canary/v2/internal/flexstmt"
	"github.com/osauer/canary/v2/internal/rpc"
)

// replayFlexDay is one synthetic daily statement: the SYNX call position and
// mark on that day, and the executions reported on it.
type replayFlexDay struct {
	day      string
	quantity float64
	mark     float64
	trades   string
}

func replayFlexXML(account string, days []replayFlexDay) string {
	var b strings.Builder
	b.WriteString("<FlexQueryResponse><FlexStatements>")
	for _, d := range days {
		compact := strings.ReplaceAll(d.day, "-", "")
		fmt.Fprintf(&b, `<FlexStatement accountId="%s" fromDate="%s" toDate="%s" whenGenerated="%s;230000">`, account, compact, compact, compact)
		fmt.Fprintf(&b, `<Trades>%s</Trades><OpenPositions>`, d.trades)
		if d.quantity != 0 {
			fmt.Fprintf(&b, `<OpenPosition accountId="%s" assetCategory="OPT" currency="USD" fxRateToBase="0.9" symbol="SYNX  261016C00100000" conid="9001" underlyingSymbol="SYNX" reportDate="%s" position="%g" multiplier="100" markPrice="%g" costBasisMoney="401" levelOfDetail="SUMMARY" />`, account, compact, d.quantity, d.mark)
		}
		b.WriteString(`</OpenPositions><SecuritiesInfo><SecurityInfo assetCategory="OPT" currency="USD" symbol="SYNX  261016C00100000" conid="9001" underlyingSymbol="SYNX" multiplier="100" strike="100" expiry="20261016" putCall="C" /></SecuritiesInfo>`)
		fmt.Fprintf(&b, `<ConversionRates><ConversionRate dateTime="%s;000000" fromCurrency="USD" toCurrency="EUR" rate="0.9" /></ConversionRates>`, compact)
		fmt.Fprintf(&b, `<EquitySummaryInBase><EquitySummaryByReportDateInBase reportDate="%s" total="100000" /></EquitySummaryInBase>`, compact)
		b.WriteString("</FlexStatement>")
	}
	b.WriteString("</FlexStatements></FlexQueryResponse>")
	return b.String()
}

func replayFlexTrade(account, id, day, side string, quantity, price, net, realized float64, openClose string) string {
	compact := strings.ReplaceAll(day, "-", "")
	return fmt.Sprintf(`<Trade accountId="%s" assetCategory="OPT" currency="USD" fxRateToBase="0.9" symbol="SYNX  261016C00100000" conid="9001" underlyingSymbol="SYNX" multiplier="100" tradeID="t-%s" ibOrderID="o-%s" ibExecID="e-%s" transactionID="x-%s" tradeDate="%s" tradeTime="150000" reportDate="%s" buySell="%s" quantity="%g" tradePrice="%g" proceeds="0" IBCommission="-1" IBCommissionCurrency="USD" taxes="0" openCloseIndicator="%s" cost="0" fifoPnlRealized="%g" mtmPnl="0" netCash="%g" levelOfDetail="EXECUTION" />`,
		account, id, id, id, id, compact, compact, side, quantity, price, openClose, realized, net)
}

func TestEdgeReplayReadsTheProjectionHistoryAndPolicyInForce(t *testing.T) {
	const account = "UREPLAYSYNTH"
	now := time.Date(2026, time.September, 30, 12, 0, 0, 0, time.UTC)
	raw := []byte(replayFlexXML(account, []replayFlexDay{
		{day: "2026-09-01"},
		{day: "2026-09-02", quantity: 2, mark: 2.00, trades: replayFlexTrade(account, "buy", "2026-09-02", "BUY", 2, 2.00, -401, 0, "O")},
		{day: "2026-09-03", quantity: 2, mark: 1.50},
		{day: "2026-09-04", quantity: 2, mark: 0.70},
		{day: "2026-09-07", trades: replayFlexTrade(account, "sell", "2026-09-07", "SELL", -2, 0.50, 99, -302, "C")},
	}))
	statements, err := flexstmt.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	queryFingerprint := flexQueryFingerprint("424242")
	files, days, records, versions, err := buildStatementProjection([]statementProjectionFile{{name: "replay.xml", size: int64(len(raw)), digest: sha256.Sum256(raw), data: raw, statements: statements}}, now, queryFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	store, err := corestore.Open(t.Context(), corestore.Options{Path: filepath.Join(privateTestDir(t), "daemon.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.ReplaceStatementProjection(t.Context(), statementProjectionScopeForSelection(flexEvidenceSelection{ActiveQueryFingerprint: queryFingerprint}), files, days, records, versions); err != nil {
		t.Fatal(err)
	}
	port := 4001
	srv := &Server{
		cfg:       &config.Resolved{Gateway: config.Gateway{Account: account, Port: &port}, Flex: config.Flex{Enabled: true, QueryID: "424242"}},
		coreStore: store, now: func() time.Time { return now },
	}
	seedReplayHistory(t, srv, store, now)

	result, err := srv.handleEdgeReplay(t.Context(), &rpc.Request{Params: json.RawMessage(`{"rules":["loss_exit"]}`)})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != rpc.EdgeStateCurrent || !result.NotExecution || result.BaseCurrency != "EUR" || result.Scope.Contracts != 1 {
		t.Fatalf("replay = %+v", result)
	}
	if loss := result.Parameters.Rules[0]; loss.Name != "loss_exit_pct" || loss.Value != 60 || !strings.Contains(result.Parameters.PolicySource, "Rulebook") {
		t.Fatalf("parameters = %+v", result.Parameters)
	}
	if result.Parameters.HalfSpreadPct != nil || !strings.Contains(result.Parameters.HalfSpreadSource, "no measured option fill") {
		t.Fatalf("half-spread without fills = %v %q", result.Parameters.HalfSpreadPct, result.Parameters.HalfSpreadSource)
	}
	var run *rpc.EdgeReplayRun
	for i := range result.Runs {
		if result.Runs[i].ID == rpc.EdgeReplayRuleLossExit && result.Runs[i].Prices == rpc.EdgeReplayPricesRecorded {
			run = &result.Runs[i]
		}
	}
	if run == nil || len(run.ExitsByReason) != 1 || run.ExitsByReason[0].Units != 2 || run.Coverage.StatementDays != 3 {
		t.Fatalf("loss-exit run = %+v; the cached history must decide 2026-09-04", run)
	}
	// 0.70 x 100 less the 0.50 average commission, at 0.9, against a cost of 401 x 0.9.
	if want := (2*(70-0.5) - 401) * 0.9; math.Abs(run.PNLBase-want) > 1e-6 {
		t.Fatalf("loss-exit P/L = %.4f, want %.4f", run.PNLBase, want)
	}
	if !result.Reconciliation.Reconciled || result.Reconciliation.ComparedContracts != 1 {
		t.Fatalf("reconciliation = %+v", result.Reconciliation)
	}
}

// seedReplayHistory retains a daily SYNX series under the key the replay
// reads, as the market view would have left it.
func seedReplayHistory(t *testing.T, srv *Server, store *corestore.Store, now time.Time) {
	t.Helper()
	p := rpc.MarketHistoryParams{Contract: rpc.ContractParams{Symbol: "SYNX", SecType: "STK", Exchange: "SMART", Currency: "USD"}, Range: "6M"}
	key, normalized, err := marketHistoryIdentity(p)
	if err != nil {
		t.Fatal(err)
	}
	result := rpc.MarketHistoryResult{TimestampKind: "session_date", PriceBasis: "TRADES", RegularHoursOnly: true, Contract: normalized.Contract, Range: "6M", Interval: "1 day", Source: "hmds", AsOf: now, CoverageStatus: "complete"}
	result.Contract.ConID = 4242
	for day := now.AddDate(0, -5, 0); day.Before(now.AddDate(0, 0, -1)); day = day.AddDate(0, 0, 1) {
		if day.Weekday() == time.Saturday || day.Weekday() == time.Sunday {
			continue
		}
		at := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC)
		result.Points = append(result.Points, rpc.MarketHistoryPoint{At: at, Value: 95, Open: new(95.0), High: new(96.0), Low: new(94.0)})
	}
	result.RequestedStart, result.Start, result.End = result.Points[0].At, result.Points[0].At, result.Points[len(result.Points)-1].At
	payload, err := json.Marshal(storedMarketHistory{Version: 1, Identity: key, FullReadAt: now, Result: result})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveChartCache(t.Context(), key, payload); err != nil {
		t.Fatal(err)
	}
	if got := replayHistoryRange(nil, time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC), srv.edgeNow()); got != "6M" {
		t.Fatalf("history range = %s, want the cached 6M", got)
	}
}

func TestEdgeReplayNeedsFlex(t *testing.T) {
	srv := &Server{cfg: &config.Resolved{}}
	result, err := srv.handleEdgeReplay(t.Context(), &rpc.Request{Params: json.RawMessage(`{}`)})
	if err != nil || result.State != rpc.EdgeStateActionRequired || result.Reason != "flex_configuration_required" || !result.NotExecution {
		t.Fatalf("result = %+v, %v", result, err)
	}
	if _, err := srv.handleEdgeReplay(t.Context(), &rpc.Request{Params: json.RawMessage(`{"rules":["E"]}`)}); err == nil {
		t.Fatal("unknown rule accepted")
	}
}
