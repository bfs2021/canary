package update

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestInstallCanonicalPublicationFailurePreservesOld(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(t.TempDir(), "new")
	target := filepath.Join(dir, "canary")
	if err := os.WriteFile(src, []byte("complete new binary"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("complete old binary"), 0755); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("publication refused")
	if err := installCanonical(src, dir, func(string, string) error { return injected }); !errors.Is(err, injected) {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "complete old binary" {
		t.Fatalf("canonical=%q err=%v", got, err)
	}
}

func TestInstallCanonicalSurvivesPublicationSignals(t *testing.T) {
	if os.Getenv("CANARY_TEST_ATOMIC_CHILD") == "1" {
		dir := os.Getenv("CANARY_TEST_ATOMIC_DIR")
		phase := os.Getenv("CANARY_TEST_ATOMIC_PHASE")
		cleanup := CleanupOnSignal()
		defer cleanup()
		err := installCanonical(filepath.Join(dir, "source"), dir, func(from, to string) error {
			if phase == "after" {
				if err := os.Rename(from, to); err != nil {
					return err
				}
			}
			if err := os.WriteFile(filepath.Join(dir, "ready"), []byte("ready"), 0600); err != nil {
				return err
			}
			for {
				time.Sleep(time.Second)
			}
		})
		t.Fatal(err)
	}
	for _, phase := range []string{"before", "after"} {
		for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGKILL} {
			t.Run(phase+sig.String(), func(t *testing.T) {
				dir := t.TempDir()
				old := bytes.Repeat([]byte("old"), 1024)
				next := bytes.Repeat([]byte("new"), 1024)
				if err := os.WriteFile(filepath.Join(dir, "source"), next, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "canary"), old, 0755); err != nil {
					t.Fatal(err)
				}
				cmd := exec.Command(os.Args[0], "-test.run=^TestInstallCanonicalSurvivesPublicationSignals$")
				cmd.Env = append(os.Environ(), "CANARY_TEST_ATOMIC_CHILD=1", "CANARY_TEST_ATOMIC_DIR="+dir, "CANARY_TEST_ATOMIC_PHASE="+phase)
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = cmd.Process.Kill() })
				deadline := time.Now().Add(10 * time.Second)
				for {
					if _, err := os.Stat(filepath.Join(dir, "ready")); err == nil {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("child did not reach publication boundary")
					}
					time.Sleep(10 * time.Millisecond)
				}
				if err := cmd.Process.Signal(sig); err != nil {
					t.Fatal(err)
				}
				if err := cmd.Wait(); err == nil {
					t.Fatal("interrupted child succeeded")
				}
				got, err := os.ReadFile(filepath.Join(dir, "canary"))
				if err != nil {
					t.Fatal(err)
				}
				want := old
				if phase == "after" {
					want = next
				}
				if !bytes.Equal(got, want) {
					t.Fatal("canonical was absent, partial or wrong at interrupted boundary")
				}
			})
		}
	}
}
