package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestUpdateRejectsOperandsBeforeAnyAction(t *testing.T) {
	for _, args := range [][]string{{"--force", "--no-restart", "v3.23.0", "--check"}, {"--force", "--restart", "v3.23.0", "--check"}, {"v3.23.0", "--help"}, {"--", "v3.23.0"}} {
		var out, errOut bytes.Buffer
		if code := RunUpdate(t.Context(), args, "v3.23.0", strings.NewReader(""), &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "unexpected argument") {
			t.Fatalf("%q: exit=%d out=%q err=%q", args, code, out.String(), errOut.String())
		}
	}
}

func TestOfflineHelpCannotConsumeHelpAndExecute(t *testing.T) {
	for _, tc := range []struct {
		cmd  string
		args []string
		code int
	}{
		{"positions", []string{"--symbol", "--help"}, 2},
		{"orders", []string{"history", "--since", "--help"}, 2},
		{"positions", []string{"--nonexistent", "--help"}, 2},
		{"positions", []string{"--help"}, 0},
		{"orders", []string{"history", "--help"}, 0},
		{"policy", []string{"help", "override"}, 0},
		{"strategy", []string{"--help"}, 0},
	} {
		t.Run(tc.cmd+strings.Join(tc.args, "_"), func(t *testing.T) {
			var out, errOut bytes.Buffer
			env := &Env{HelpOnly: true, Stdout: &out, Stderr: &errOut}
			if code := Run(context.Background(), env, tc.cmd, tc.args); code != tc.code {
				t.Fatalf("exit=%d out=%q err=%q", code, out.String(), errOut.String())
			}
		})
	}
}

func TestStrategyInvalidIDsFailWithoutRPC(t *testing.T) {
	for _, args := range [][]string{nil, {"--json"}, {"core", "extra"}, {"core", "extra", "--json"}} {
		var out, errOut bytes.Buffer
		if code := Run(t.Context(), &Env{Stdout: &out, Stderr: &errOut}, "strategy", args); code != 2 || !strings.Contains(errOut.String(), "exactly one") {
			t.Fatalf("%q exit=%d out=%q err=%q", args, code, out.String(), errOut.String())
		}
	}
}
