package daemon

import (
	"github.com/osauer/canary/v2/internal/rpc"
	"testing"
)

func TestReportingCostsRequiresConfirmedAccount(t *testing.T) {
	recs := fillQuoteTestRecords()[1:3]
	recs[0].Mode, recs[1].Mode = "disabled", "disabled"
	rt := &fillQuoteRuntime{records: map[string]fillQuoteStored{
		"one": {Account: "SYNTHETIC_ACCOUNT_A", Record: recs[0]},
		"two": {Account: "SYNTHETIC_ACCOUNT_B", Record: recs[1]},
	}}
	srv := &Server{fillQuotes: rt}
	res, err := srv.handleReportingCosts(t.Context(), &rpc.Request{Params: []byte(`{}`)})
	if err == nil || res != nil {
		t.Fatalf("unknown account must refuse report, got %+v, %v", res, err)
	}
}

func TestCostTotalsKeepHistoricalCurrenciesSeparate(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		records := fillQuoteTestRecords()[1:3]
		records[0].BaseCurrency, records[0].FXRate = "EUR", new(0.9)
		records[1].BaseCurrency, records[1].FXRate = "USD", new(1.0)
		if reverse {
			records[0], records[1] = records[1], records[0]
		}
		got := buildReportingCosts(records, "", "paper", 15, fillQuotePoll{}, false, fillQuoteTestAt)
		if err := rpc.ValidateReportingCostsResult(got); err != nil {
			t.Fatal(err)
		}
		totals := got.ByMonth[0]
		if got.BaseCurrency != "" || totals.HalfSpreadBase != nil || len(totals.ByCurrency) != 2 || totals.ByCurrency[0].Currency != "EUR" || !closeTo(totals.ByCurrency[0].HalfSpreadBase, 9) || totals.ByCurrency[1].Currency != "USD" || !closeTo(totals.ByCurrency[1].HalfSpreadBase, 10) {
			t.Fatalf("mixed units: %+v", got)
		}
	}
}
func TestCostMeasurementRequiresExplicitLiveFeed(t *testing.T) {
	for _, feed := range []string{rpc.MarketDataLive, rpc.MarketDataDelayed, rpc.MarketDataFrozen, rpc.MarketDataDelayedFrozen, "", "unexpected"} {
		rec := fillQuoteTestRecords()[1]
		rec.Quote.DataType = feed
		got := costFillFromRecord(rec)
		if feed == rpc.MarketDataLive {
			if got.Status != rpc.CostFillMeasured {
				t.Fatal(got)
			}
		} else if got.Status != rpc.CostFillUnknown || got.HalfSpread != nil || got.HalfSpreadBase != nil || got.Reason == "" {
			t.Fatalf("%s: %+v", feed, got)
		}
	}
	rec := fillQuoteTestRecords()[1]
	rec.BaseCurrency = ""
	got := costFillFromRecord(rec)
	if got.HalfSpreadBase != nil || got.FXRate != nil {
		t.Fatal("currency-free conversion", got)
	}
}
