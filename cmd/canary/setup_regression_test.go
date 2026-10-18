package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

func TestSetupKeepsCredentialBackupsPrivate(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("desktop setup is macOS-only")
	}
	t.Setenv("HOME", t.TempDir())
	path, err := claudeDesktopConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	input := []byte(`{"mcpServers":{"other":{"command":"synthetic","env":{"SYNTHETIC_TOKEN":"not-a-real-secret"}}}}`)
	if err := os.WriteFile(path, input, 0600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if code := setupClaudeDesktop(); code != 0 {
			t.Fatalf("setup exit %d", code)
		}
	}
	backups, err := filepath.Glob(path + ".bak-*")
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 2 {
		t.Errorf("got %d backups, want two collision-free backups", len(backups))
	}
	for _, backup := range backups {
		info, err := os.Stat(backup)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Errorf("backup permissions = %o", info.Mode().Perm())
		}
		data, err := os.ReadFile(backup)
		if err != nil || !strings.Contains(string(data), "not-a-real-secret") {
			t.Fatal("backup lost original secret")
		}
	}
}

func TestSetupBackupSecuresOldFilesWithoutFollowingSymlinks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claude_desktop_config.json")
	old := path + ".bak-20261018-000000"
	if err := os.WriteFile(old, []byte("synthetic old backup"), 0644); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "untouched")
	if err := os.WriteFile(outside, []byte("untouched"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path+".bak-link"); err != nil {
		t.Fatal(err)
	}
	if err := secureClaudeDesktopBackups(path); err != nil {
		t.Fatal(err)
	}
	backup, err := writeClaudeDesktopBackup(path, []byte("synthetic new backup"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{old, backup} {
		info, err := os.Stat(name)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("backup not private: %s %v", name, err)
		}
	}
	info, err := os.Stat(outside)
	if err != nil || info.Mode().Perm() != 0644 {
		t.Fatal("followed backup symlink")
	}
}

func TestSetupSecuresOrphanedBackupsBeforeReadingConfig(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("desktop setup is macOS-only")
	}
	for _, config := range []string{"", "invalid JSON", `{"mcpServers":null}`} {
		t.Run(config, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			path, err := claudeDesktopConfigPath()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			backup := path + ".bak-legacy"
			if err := os.WriteFile(backup, []byte("synthetic secret"), 0644); err != nil {
				t.Fatal(err)
			}
			if config != "" {
				if err := os.WriteFile(path, []byte(config), 0600); err != nil {
					t.Fatal(err)
				}
			}
			code := setupClaudeDesktop()
			if (config == "" && code != 0) || (config != "" && code != 1) {
				t.Fatalf("exit %d", code)
			}
			info, err := os.Stat(backup)
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatal("old backup remains exposed")
			}
			if config != "" {
				data, _ := os.ReadFile(path)
				if string(data) != config {
					t.Fatal("invalid current config changed")
				}
			}
		})
	}
}

func TestSetupAppBackupsCannotCollide(t *testing.T) {
	path := filepath.Join(t.TempDir(), "com.osauer.ibkr-app.plist")
	original := []byte("synthetic original custom LaunchAgent")
	names := map[string]bool{}
	for range 20 {
		backup, err := writePrivateSetupBackup(path, original)
		if err != nil {
			t.Fatal(err)
		}
		if names[backup] {
			t.Fatal("backup collision")
		}
		names[backup] = true
		got, err := os.ReadFile(backup)
		if err != nil || string(got) != string(original) {
			t.Fatal("backup lost original contents")
		}
	}
}
