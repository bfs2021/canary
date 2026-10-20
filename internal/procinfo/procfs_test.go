package procinfo

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// fakeProc is one synthetic /proc/<pid> entry.
type fakeProc struct {
	argv  []string // written NUL-separated with a trailing NUL; nil = empty cmdline
	exe   string   // symlink target; "" = no link
	stat  string   // full stat line; "" = derived from comm/state/ppid
	comm  string
	state string
	ppid  int
}

// procTree writes a synthetic procfs under a temp dir. withSelf controls
// whether the "self" entry exists, which tells "process gone" from "procfs
// not mounted".
func procTree(t *testing.T, withSelf bool, procs map[int]fakeProc) string {
	t.Helper()
	root := t.TempDir()
	if withSelf {
		if err := os.Mkdir(filepath.Join(root, "self"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Entries a real procfs also has that are not processes.
	for _, name := range []string{"sys", "net"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "uptime"), []byte("1 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for pid, p := range procs {
		dir := filepath.Join(root, strconv.Itoa(pid))
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		var cmdline string
		if len(p.argv) > 0 {
			cmdline = strings.Join(p.argv, "\x00") + "\x00"
		}
		if err := os.WriteFile(filepath.Join(dir, "cmdline"), []byte(cmdline), 0o644); err != nil {
			t.Fatal(err)
		}
		if p.exe != "" {
			if err := os.Symlink(p.exe, filepath.Join(dir, "exe")); err != nil {
				t.Fatal(err)
			}
		}
		stat := p.stat
		if stat == "" {
			comm, state := p.comm, p.state
			if comm == "" {
				comm = "proc"
			}
			if state == "" {
				state = "S"
			}
			stat = strconv.Itoa(pid) + " (" + comm + ") " + state + " " + strconv.Itoa(p.ppid) + " 1 1 0 -1 4194560 0 0 0 0\n"
		}
		if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestProcfsProcessReadsArgvAsPSPrintsIt(t *testing.T) {
	t.Parallel()
	root := procTree(t, true, map[int]fakeProc{
		26: {argv: []string{"/tmp/h/.local/bin/canary", "daemon"}, exe: "/tmp/h/.local/bin/canary", ppid: 1},
		27: {argv: []string{"/opt/Application Support/Desk/runtime/canary", "daemon", "--foreground"}, ppid: 26},
		// A comm with spaces and a ")" must not shift the ppid field.
		28: {argv: []string{"python3", "x.py"}, stat: "28 (odd) name) R 27 1 1 0\n"},
		// A process that rewrote its title into one string.
		29: {argv: []string{"canary: worker pool"}, ppid: 1},
	})
	for _, tc := range []struct {
		pid     int
		command string
		ppid    int
	}{
		{26, "/tmp/h/.local/bin/canary daemon", 1},
		{27, "/opt/Application Support/Desk/runtime/canary daemon --foreground", 26},
		{28, "python3 x.py", 27},
		{29, "canary: worker pool", 1},
	} {
		proc, err := procfsProcess(root, tc.pid)
		if err != nil {
			t.Fatalf("procfsProcess(%d): %v", tc.pid, err)
		}
		if proc.PID != tc.pid || proc.Command != tc.command || proc.PPID != tc.ppid {
			t.Fatalf("procfsProcess(%d) = %+v, want command %q ppid %d", tc.pid, proc, tc.command, tc.ppid)
		}
	}
}

func TestProcfsProcessErrorsNameTheCause(t *testing.T) {
	t.Parallel()
	root := procTree(t, true, map[int]fakeProc{
		40: {argv: nil, state: "Z"}, // zombie or kernel thread: no command line
	})
	if _, err := procfsProcess(root, 41); !errors.Is(err, ErrNoProcess) {
		t.Fatalf("absent pid err = %v, want ErrNoProcess", err)
	}
	if _, err := procfsProcess(root, 40); err == nil || !strings.Contains(err.Error(), "empty command line") {
		t.Fatalf("empty cmdline err = %v, want an empty command line error", err)
	}

	unmounted := procTree(t, false, nil)
	_, err := procfsProcess(unmounted, 41)
	if err == nil || errors.Is(err, ErrNoProcess) || !strings.Contains(err.Error(), "needs procfs mounted at "+unmounted) {
		t.Fatalf("unmounted procfs err = %v, want an actionable procfs error", err)
	}
	if _, err := procfsList(filepath.Join(unmounted, "missing")); err == nil || !strings.Contains(err.Error(), "needs procfs mounted") {
		t.Fatalf("unmounted list err = %v, want an actionable procfs error", err)
	}
}

// TestGuestArgsUnderQEMUUser covers the observed Docker Desktop shape: an
// amd64 binary on an arm64 host runs as
// "/usr/bin/qemu-x86_64 <filename> <argv0> <args>" with exe naming qemu.
func TestGuestArgsUnderQEMUUser(t *testing.T) {
	t.Parallel()
	const qemu = "/usr/bin/qemu-x86_64"
	const bin = "/tmp/h/.local/bin/canary"
	root := procTree(t, true, map[int]fakeProc{
		// preserve-argv0 flag, spawned as exec.Command(bin, "daemon")
		10: {argv: []string{qemu, bin, bin, "daemon"}, exe: qemu},
		// preserve-argv0 flag, started from a shell through PATH
		11: {argv: []string{qemu, bin, "canary", "daemon", "--foreground"}, exe: qemu},
		// no preserve-argv0 flag: the kernel replaced argv0 by the filename
		12: {argv: []string{qemu, bin, "daemon"}, exe: qemu},
		// static interpreter in another directory
		13: {argv: []string{"/usr/libexec/qemu-binfmt/qemu-x86_64-static", bin, bin, "app"}, exe: "/usr/libexec/qemu-binfmt/qemu-x86_64-static"},
		// emulated pid 1 whose argv qemu already shows as the guest's
		14: {argv: []string{bin, "daemon"}, exe: qemu},
	})
	for pid, want := range map[int]string{
		10: bin + " daemon",
		11: bin + " daemon --foreground",
		12: bin + " daemon",
		13: bin + " app",
		14: bin + " daemon",
	} {
		got, err := procfsProcess(root, pid)
		if err != nil || got.Command != want {
			t.Fatalf("pid %d command = %q err %v, want %q", pid, got.Command, err, want)
		}
	}
}

// TestGuestArgsKeepsRawCommandUnlessTheKernelNamesQEMU proves the emulator
// rule cannot be steered by argv text: stripping needs the exe link to name
// the same qemu-user interpreter as argv[0], and what remains is the whole
// guest command, so a wrapper stays a wrapper.
func TestGuestArgsKeepsRawCommandUnlessTheKernelNamesQEMU(t *testing.T) {
	t.Parallel()
	const qemu = "/usr/bin/qemu-x86_64"
	const bin = "/tmp/h/.local/bin/canary"
	root := procTree(t, true, map[int]fakeProc{
		// argv claims qemu, the kernel says python
		20: {argv: []string{qemu, bin, "daemon"}, exe: "/usr/bin/python3.12"},
		// exe unreadable (another user's process)
		21: {argv: []string{qemu, bin, "daemon"}},
		// exe is qemu, argv[0] names a different interpreter
		22: {argv: []string{"/usr/bin/qemu-aarch64", bin, "daemon"}, exe: qemu},
		// a shell wrapper under emulation remains a shell wrapper
		23: {argv: []string{qemu, "/bin/sh", "sh", "-c", bin + " daemon"}, exe: qemu},
		// emulators outside the narrow list keep their raw command
		24: {argv: []string{"/run/rosetta/rosetta", bin, "canary", "daemon"}, exe: "/run/rosetta/rosetta"},
		25: {argv: []string{"/usr/bin/qemu-system-x86_64", bin, "daemon"}, exe: "/usr/bin/qemu-system-x86_64"},
		// an ordinary process whose exe equals argv[0]
		26: {argv: []string{"/usr/bin/sleep", "600"}, exe: "/usr/bin/sleep"},
	})
	for pid, want := range map[int]string{
		20: qemu + " " + bin + " daemon",
		21: qemu + " " + bin + " daemon",
		22: "/usr/bin/qemu-aarch64 " + bin + " daemon",
		23: "/bin/sh -c " + bin + " daemon",
		24: "/run/rosetta/rosetta " + bin + " canary daemon",
		25: "/usr/bin/qemu-system-x86_64 " + bin + " daemon",
		26: "/usr/bin/sleep 600",
	} {
		got, err := procfsProcess(root, pid)
		if err != nil || got.Command != want {
			t.Fatalf("pid %d command = %q err %v, want %q", pid, got.Command, err, want)
		}
	}
}

func TestProcfsListSkipsNonProcessesAndKernelThreads(t *testing.T) {
	t.Parallel()
	root := procTree(t, true, map[int]fakeProc{
		1:   {argv: []string{"/sbin/init"}, ppid: 0},
		2:   {argv: nil, comm: "kthreadd"},
		300: {argv: []string{"/tmp/h/.local/bin/canary", "mcp", "--profile", "monitor"}, ppid: 299},
		299: {argv: []string{"/usr/bin/claude"}, comm: "claude", ppid: 1},
	})
	procs, err := procfsList(root)
	if err != nil {
		t.Fatal(err)
	}
	got := map[int]Process{}
	for _, p := range procs {
		got[p.PID] = p
	}
	if len(got) != 3 {
		t.Fatalf("procfsList = %+v, want pids 1, 299 and 300", procs)
	}
	if p := got[300]; p.PPID != 299 || p.Command != "/tmp/h/.local/bin/canary mcp --profile monitor" {
		t.Fatalf("pid 300 = %+v", p)
	}
	if name, err := procfsName(root, 299); err != nil || name != "claude" {
		t.Fatalf("procfsName(299) = %q, %v", name, err)
	}
	if state, err := procfsState(root, 2); err != nil || state != "S" {
		t.Fatalf("procfsState(2) = %q, %v", state, err)
	}
}

func TestProcfsStateReportsZombie(t *testing.T) {
	t.Parallel()
	root := procTree(t, true, map[int]fakeProc{50: {argv: nil, comm: "canary", state: "Z", ppid: 1}})
	if state, err := procfsState(root, 50); err != nil || state != "Z" {
		t.Fatalf("procfsState(50) = %q, %v; want Z", state, err)
	}
	if _, err := procfsState(root, 51); !errors.Is(err, ErrNoProcess) {
		t.Fatalf("procfsState(51) err = %v, want ErrNoProcess", err)
	}
}

func TestParsePSRowKeepsInstallPathWithSpaces(t *testing.T) {
	t.Parallel()
	const executable = "/Users/x/Library/Application Support/Desk/runtime/canary"
	proc, ok := parsePSRow("  4711   901 " + executable + " mcp --profile monitor")
	if !ok || proc.PID != 4711 || proc.PPID != 901 || proc.Command != executable+" mcp --profile monitor" {
		t.Fatalf("parsePSRow = %+v %v", proc, ok)
	}
	if _, ok := parsePSRow("PID PPID ARGS"); ok {
		t.Fatal("header row parsed as a process")
	}
}

// TestLiveProcessThroughPlatformReader exercises the reader this platform
// uses for real (procfs on Linux, ps elsewhere) against a child process.
func TestLiveProcessThroughPlatformReader(t *testing.T) {
	t.Parallel()
	child := exec.Command("sleep", "30")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = child.Process.Signal(syscall.SIGKILL)
		_, _ = child.Process.Wait()
	})
	pid := child.Process.Pid
	// Natively argv0 is "sleep" as given; under qemu-user emulation the
	// guest argv0 is reported as the file the kernel executed.
	want := map[string]bool{"sleep 30": true, child.Path + " 30": true}
	cmdline, err := CommandLine(t.Context(), pid)
	if err != nil || !want[cmdline] {
		t.Fatalf("CommandLine(%d) = %q, %v; want \"sleep 30\"", pid, cmdline, err)
	}
	procs, err := List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range procs {
		if p.PID == pid {
			found = p.PPID == os.Getpid() && p.Command == cmdline
		}
	}
	if !found {
		t.Fatalf("List did not report pid %d as \"sleep 30\" with parent %d", pid, os.Getpid())
	}
	if state, err := State(pid); err != nil || strings.HasPrefix(state, "Z") {
		t.Fatalf("State(%d) = %q, %v; want a live state", pid, state, err)
	}
	if names := Names(t.Context(), []int{pid}); filepath.Base(names[pid]) != "sleep" {
		t.Fatalf("Names(%d) = %q, want sleep", pid, names[pid])
	}
}
