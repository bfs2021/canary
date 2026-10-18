package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSetupPreservesMCPEnvironment(t *testing.T) {
	for _, name := range []string{"canary", "ibkr"} {
		cfg := map[string]any{"theme": "dark", "mcpServers": map[string]any{name: map[string]any{"command": "old", "args": []string{"old"}, "env": map[string]any{"CANARY_SOCKET": "/tmp/private.sock", "CANARY_CONFIG": "/tmp/custom.toml", "XDG_CONFIG_HOME": "/tmp/config", "CUSTOM": "unchanged"}, "disabled": true}, "other": map[string]any{"command": "other"}}}
		data, err := mergeCanaryMCPEntry(cfg, "/new/canary")
		if err != nil {
			t.Fatal(err)
		}
		var got map[string]any
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatal(err)
		}
		servers := got["mcpServers"].(map[string]any)
		entry := servers["canary"].(map[string]any)
		if entry["command"] != "/new/canary" || entry["disabled"] != true || entry["env"].(map[string]any)["CANARY_SOCKET"] != "/tmp/private.sock" || servers["other"] == nil || got["theme"] != "dark" {
			t.Fatalf("lost config: %s", data)
		}
	}
}

func TestSetupRejectsInvalidObjectsWithoutWriting(t *testing.T) {
	for _, input := range []string{`null`, `[]`, `"text"`, `1`, `{`, `{"mcpServers":null}`, `{"mcpServers":[]}`, `{"mcpServers":{"canary":null}}`} {
		t.Run(input, func(t *testing.T) {
			if runtime.GOOS != "darwin" {
				t.Skip("desktop setup is macOS-only")
			}
			t.Setenv("HOME", t.TempDir())
			path, err := claudeDesktopConfigPath()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(input), 0600); err != nil {
				t.Fatal(err)
			}
			if code := runSetup([]string{"claude-desktop"}); code != 1 {
				t.Fatalf("exit=%d", code)
			}
			got, _ := os.ReadFile(path)
			backups, _ := filepath.Glob(path + ".bak-*")
			if string(got) != input || len(backups) != 0 {
				t.Fatal("invalid input was mutated or backed up")
			}
		})
	}
}

func TestSetupHelpAndInvalidArgumentsDoNotWrite(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"claude-desktop", "--help"}, {"claude-desktop", "-h"}, {"claude-desktop", "--unknown"}, {"claude-desktop", "extra"}} {
		t.Run(args[len(args)-1], func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			code := runSetup(args)
			want := 2
			if args[len(args)-1] == "--help" || args[len(args)-1] == "-h" {
				want = 0
			}
			if code != want {
				t.Fatalf("exit=%d want%d", code, want)
			}
			entries, err := os.ReadDir(home)
			if err != nil || len(entries) != 0 {
				t.Fatal("help/invalid setup mutated home")
			}
		})
	}
}
