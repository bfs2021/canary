package main

import (
	"os"
	"testing"
)

func TestMCPHelpExitsWithOpenInput(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	old := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = old }()
	for _, arg := range []string{"--help", "-h"} {
		if profile, code := parseMCPArgs([]string{arg}, os.Stdout, os.Stderr); code != 0 || profile != "" {
			t.Fatalf("help would start profile=%q code=%d", profile, code)
		}
		if code := runMCP([]string{arg}); code != 0 {
			t.Fatalf("help exit %d", code)
		}
	}
}
