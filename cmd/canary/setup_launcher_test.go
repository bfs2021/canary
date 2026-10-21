package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStableMCPExecutableSurvivesManagedRotation(t *testing.T) {
	root := t.TempDir()
	old := filepath.Join(root, "generation 1", "canary")
	next := filepath.Join(root, "generation 2", "canary")
	for _, p := range []string{old, next} {
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("fixture"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	launcher := filepath.Join(root, "canary")
	if err := os.Symlink(old, launcher); err != nil {
		t.Fatal(err)
	}
	recorded := stableMCPExecutable(old, launcher)
	if recorded != launcher {
		t.Fatalf("recorded disposable generation: %q", recorded)
	}
	if err := os.Remove(launcher); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(next, launcher); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(old); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(recorded); err != nil {
		t.Fatalf("host command broken after rotation: %v", err)
	}
}

func TestStableMCPExecutableRejectsOtherProgram(t *testing.T) {
	root := t.TempDir()
	running := filepath.Join(root, "running")
	other := filepath.Join(root, "canary")
	for _, p := range []string{running, other} {
		if err := os.WriteFile(p, []byte("fixture"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", root)
	for _, invocation := range []string{other, "canary", "missing"} {
		if got := stableMCPExecutable(running, invocation); got != running {
			t.Fatalf("accepted other program %q: %q", invocation, got)
		}
	}
	t.Chdir(root)
	if got := stableMCPExecutable(running, "./running"); got != running {
		t.Fatalf("relative invocation: %q", got)
	}
}
