package daemon

import (
	"strings"
	"testing"
)

// The harvest and stock-trail numbers (owner 2026-10-24 08:55 CEST) are
// written into an existing options table at their defaults on the first run,
// a value already written is kept, a second run changes nothing, and a file
// without them reads the defaults.
func TestOptionExitHarvestAndStockTrailDefaultsAreWritten(t *testing.T) {
	file := cashPolicyTestFile + "\n[buckets.trailing_stop.options]\nenabled = false\nmin_dte = 14\n"
	before, _, err := parseProtectionPolicy([]byte(file))
	if err != nil {
		t.Fatal(err)
	}
	if o := before.Buckets.TrailingStop.Options; o.RecoverCostGainPct != 100 || o.StockTrailRangeMultiple != 2.5 {
		t.Fatalf("absent keys did not read their defaults: %+v", o)
	}
	out, changes, notes, err := migrateProtectionPolicyFile([]byte(file), "v9.9.9")
	if err != nil || len(notes) != 0 {
		t.Fatalf("migration %v %v", err, notes)
	}
	text := string(out)
	for _, want := range []string{"recover_cost_gain_pct = 100", "stock_trail_range_multiple = 2.5", "Added by Canary v9.9.9 at its default"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q after %v:\n%s", want, changes, text)
		}
	}
	if again, changes, _, err := migrateProtectionPolicyFile(out, "v9.9.9"); err != nil || len(changes) != 0 || string(again) != text {
		t.Fatalf("a second run changed %v (%v)", changes, err)
	}
	owned := strings.Replace(file, "min_dte = 14\n", "min_dte = 14\nrecover_cost_gain_pct = 150\n", 1)
	out, _, _, err = migrateProtectionPolicyFile([]byte(owned), "v9.9.9")
	if err != nil || !strings.Contains(string(out), "recover_cost_gain_pct = 150") || strings.Contains(string(out), "recover_cost_gain_pct = 100") {
		t.Fatalf("an owner's value was rewritten (%v):\n%s", err, out)
	}
	bad := strings.Replace(file, "min_dte = 14\n", "min_dte = 14\nstock_trail_range_multiple = 0\n", 1)
	if _, _, err := parseProtectionPolicy([]byte(bad)); err == nil {
		t.Fatal("a zero stock-trail multiple was accepted")
	}
}
