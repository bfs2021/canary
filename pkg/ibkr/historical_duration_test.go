package ibkr

import "testing"

// IBKR accepts a day count only up to a year of daily bars; a longer span is
// asked for in years, rounded up, so a 400-day or three-year lookback is a
// valid request (ruled out as the scorecard's history failure, 2026-10-18).
func TestHistoricalDurationBeyondAYearIsAskedInYears(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		days int
		want string
	}{
		{0, "1 D"}, {30, "30 D"}, {365, "365 D"}, {366, "2 Y"}, {400, "2 Y"}, {730, "2 Y"}, {1095, "3 Y"},
	} {
		if got := formatHistoricalDuration(tc.days); got != tc.want {
			t.Fatalf("formatHistoricalDuration(%d) = %q, want %q", tc.days, got, tc.want)
		}
	}
	if got := normalizeHistoricalDuration("400 D"); got != "2 Y" {
		t.Fatalf("normalizeHistoricalDuration(400 D) = %q", got)
	}
}
