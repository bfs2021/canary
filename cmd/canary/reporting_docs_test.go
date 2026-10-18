package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestManualReportingRecipePreservesTokenOnFailedRead(t *testing.T) {
	data, err := os.ReadFile("../../docs/docs/start/reporting.md")
	if err != nil {
		t.Fatal(err)
	}
	_, section, ok := strings.Cut(string(data), "### Manual configuration fallback")
	if !ok {
		t.Fatal("missing recipe")
	}
	_, section, ok = strings.Cut(section, "```sh\n")
	if !ok {
		t.Fatal("missing shell block")
	}
	snippet, _, _ := strings.Cut(section, "```")
	for _, shell := range []string{"bash", "zsh"} {
		bin, err := exec.LookPath(shell)
		if err != nil {
			continue
		}
		for _, input := range []string{"", "\n", " \t\n", "synthetic-token\n"} {
			t.Run(shell+"/"+input, func(t *testing.T) {
				home := t.TempDir()
				path := filepath.Join(home, ".config", "ibkr", "flex-token")
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, bin, "-c", snippet)
				cmd.Env = append(os.Environ(), "HOME="+home)
				cmd.Stdin = strings.NewReader(input)
				output, runErr := cmd.CombinedOutput()
				got, _ := os.ReadFile(path)
				if input == "synthetic-token\n" {
					if runErr != nil || string(got) != input {
						t.Fatalf("successful recipe: %v %s", runErr, output)
					}
				} else if runErr == nil || string(got) != "original" {
					t.Fatalf("cancelled recipe replaced token: %v", runErr)
				}
				if strings.Contains(string(output), "synthetic-token") {
					t.Fatal("echoed secret")
				}
				info, err := os.Stat(path)
				if err != nil || info.Mode().Perm() != 0600 {
					t.Fatal("token not private")
				}
			})
		}
	}
}
