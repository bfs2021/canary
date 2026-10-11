package ibkr

import (
	"strconv"
	"testing"
	"time"
)

func TestTickSizePreservesBrokerDecimalUnits(t *testing.T) {
	for _, server := range []int{163, 164, 203, 219, 220, 226} {
		for _, tick := range []int{0, 3, 8, 21, 27, 28, 69, 70, 74, 89} {
			for _, tc := range []struct {
				raw  string
				want int64
			}{
				{"0", 0}, {"999999", 999999}, {"1000000", 1000000}, {"1000000.0", 1000000},
				{"123456789", 123456789}, {"123456789.75", 123456789}, {"1e6", 1000000},
				{"9223372036854775807", 9223372036854775807}, {"9223372036854775807.0", 9223372036854775807},
			} {
				got, ok := parseTickSize(server, tick, tc.raw)
				if !ok || got != tc.want {
					t.Fatalf("server=%d tick=%d raw=%s: %d %v, want %d", server, tick, tc.raw, got, ok, tc.want)
				}
			}
		}
	}
	for _, raw := range []string{"", "-1", "-0.1", "NaN", "Inf", "Infinity", "1/2", "9223372036854775808", "9223372036854775808.0", "1e1000000000", "invalid"} {
		if got, ok := parseTickSize(203, 8, raw); ok {
			t.Errorf("accepted %q = %d", raw, got)
		}
	}
}

func TestRTVolumeKeepsLargeTotalVolume(t *testing.T) {
	for _, raw := range []string{"123456789", "123456789.0"} {
		price, volume, at, ok := parseRTVolumeTick("25.5;100;"+strconv.FormatInt(1700000000000, 10)+";"+raw+";25.4;true", 203)
		if !ok || price != 25.5 || volume != 123456789 || !at.Equal(time.UnixMilli(1700000000000)) {
			t.Fatalf("%s: %v %v %v %v", raw, price, volume, at, ok)
		}
	}
}

func TestLargeLiveAndDelayedVolumeReachSubscriptionUnscaled(t *testing.T) {
	sub := &Subscription{}
	connector := &Connector{subscriptions: map[string]*Subscription{"SYN": sub}, reqIDMap: map[int]string{42: "SYN"}}
	for _, tick := range []string{"8", "74"} {
		connector.handleTickSize([]string{"2", "6", "42", tick, "25000000"})
		if sub.Volume != 25000000 || !sub.VolumeObserved || sub.VolumeAt.IsZero() {
			t.Fatalf("tick %s lost volume observation: %+v", tick, sub)
		}
		before := sub.VolumeAt
		connector.handleTickSize([]string{"2", "6", "42", tick, "-1"})
		if sub.Volume != 25000000 || sub.VolumeAt != before {
			t.Fatal("invalid volume overwrote an observation")
		}
	}
}
