package mcp

import (
	"strings"
	"testing"
)

func TestCoreToolDescriptionsIncludeStandingAuthority(t *testing.T) {
	for _, name := range []string{"canary_core", "canary_strategy"} {
		t.Run(name, func(t *testing.T) {
			tool, ok := lookupTool(name)
			if !ok {
				t.Fatal("missing tool")
			}
			desc := strings.ToLower(tool.Description)
			for _, want := range []string{"standing mandate", "fully armed", "reading this tool never submits an order"} {
				if !strings.Contains(desc, want) {
					t.Errorf("missing authority boundary %q", want)
				}
			}
			for _, stale := range []string{"daemon never sends it by itself", "requires owner device confirmation", "it never sends by itself"} {
				if strings.Contains(desc, stale) {
					t.Errorf("stale per-order-only claim %q", stale)
				}
			}
			if tool.ReadOnlyHint == nil || !*tool.ReadOnlyHint {
				t.Fatal("read-only MCP contract changed")
			}
		})
	}
}
