//go:build trading

package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestTradingUpdateRefusesSilentCapabilityLoss(t *testing.T) {
	var out bytes.Buffer
	code := RunUpdate(context.Background(), []string{"--force", "--no-restart"}, "v3.22.2", strings.NewReader(""), &out, &out)
	if code != 2 || !strings.Contains(out.String(), "standard read-only update channel") {
		t.Fatalf("code=%d output=%s", code, &out)
	}
}
