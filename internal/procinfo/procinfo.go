// Package procinfo reads the identity of local processes for lifecycle
// commands (stop, restart, update activation) and best-effort discovery.
//
// Every reader returns a process's command line in the shape `ps -o args=`
// prints it: argv joined by single spaces. Callers decide identity from that
// text exactly as before; this package only changes where the text comes
// from. On Linux it is read from procfs, the same source procps reads, so
// BusyBox systems (Alpine) and images without ps behave like any other
// distribution. Elsewhere (macOS, the BSDs) the system ps is used unchanged.
// A failure names its cause, including ps's own stderr, so a caller can say
// why a process could not be verified instead of a bare exit status.
package procinfo

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

// Process is one row of a process listing.
type Process struct {
	PID     int
	PPID    int
	Command string // argv joined by single spaces, as `ps -o args=` prints it
}

// ErrNoProcess reports that no process holds the requested PID.
var ErrNoProcess = errors.New("no such process")

// procRoot is where Linux procfs is mounted.
const procRoot = "/proc"

func useProcfs() bool { return runtime.GOOS == "linux" }

// CommandLine returns pid's command line. Under a recognised user-mode
// emulator on Linux (qemu-user through binfmt_misc) it is the emulated
// program's command line; see guestArgs.
func CommandLine(ctx context.Context, pid int) (string, error) {
	if pid <= 0 {
		return "", errors.New("invalid PID")
	}
	if useProcfs() {
		proc, err := procfsProcess(procRoot, pid)
		if err != nil {
			return "", err
		}
		return proc.Command, nil
	}
	out, err := runPS(ctx, "-p", strconv.Itoa(pid), "-o", "args=")
	if err != nil {
		return "", err
	}
	cmdline := strings.TrimSpace(string(out))
	if cmdline == "" {
		return "", errors.New("empty command line")
	}
	return cmdline, nil
}

// List returns every process whose command line can be read. Kernel threads
// and zombies, which have no command line, are left out.
func List(ctx context.Context) ([]Process, error) {
	if useProcfs() {
		return procfsList(procRoot)
	}
	out, err := runPS(ctx, "-axo", "pid=,ppid=,args=")
	if err != nil {
		return nil, err
	}
	var procs []Process
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		if proc, ok := parsePSRow(sc.Text()); ok {
			procs = append(procs, proc)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return procs, nil
}

// parsePSRow splits a `ps -axo pid=,ppid=,args=` row. The command stays
// whole because the executable path in it may itself contain spaces.
func parsePSRow(line string) (Process, bool) {
	pidText, rest, _ := strings.Cut(strings.TrimSpace(line), " ")
	ppidText, command, _ := strings.Cut(strings.TrimSpace(rest), " ")
	pid, err := strconv.Atoi(pidText)
	if err != nil {
		return Process{}, false
	}
	ppid, err := strconv.Atoi(ppidText)
	if err != nil {
		return Process{}, false
	}
	command = strings.TrimSpace(command)
	if command == "" {
		return Process{}, false
	}
	return Process{PID: pid, PPID: ppid, Command: command}, true
}

// Names maps each live pid in pids to its short command name, as `ps -o
// comm=` prints it. It is best effort: unreadable processes are left out.
func Names(ctx context.Context, pids []int) map[int]string {
	if len(pids) == 0 {
		return nil
	}
	names := map[int]string{}
	if useProcfs() {
		for _, pid := range pids {
			if name, err := procfsName(procRoot, pid); err == nil {
				names[pid] = name
			}
		}
		return names
	}
	list := make([]string, 0, len(pids))
	for _, pid := range pids {
		list = append(list, strconv.Itoa(pid))
	}
	out, err := runPS(ctx, "-o", "pid=,comm=", "-p", strings.Join(list, ","))
	if err != nil {
		return names
	}
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		pidText, name, ok := strings.Cut(strings.TrimSpace(sc.Text()), " ")
		if !ok {
			continue
		}
		pid, err := strconv.Atoi(pidText)
		name = strings.TrimSpace(name)
		if err == nil && name != "" {
			names[pid] = name
		}
	}
	return names
}

// State returns pid's scheduler state code; it starts with "Z" for a zombie.
func State(pid int) (string, error) {
	if pid <= 0 {
		return "", errors.New("invalid PID")
	}
	if useProcfs() {
		return procfsState(procRoot, pid)
	}
	out, err := runPS(context.Background(), "-p", strconv.Itoa(pid), "-o", "stat=")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// runPS runs the system ps. On failure the error carries ps's arguments and
// its stderr, which Output alone would only attach to the *exec.ExitError.
func runPS(ctx context.Context, args ...string) ([]byte, error) {
	out, err := exec.CommandContext(ctx, "ps", args...).Output()
	if err == nil {
		return out, nil
	}
	detail := ""
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		if stderr := strings.TrimSpace(string(exitErr.Stderr)); stderr != "" {
			detail = ": " + firstLine(stderr)
		}
	}
	return nil, fmt.Errorf("ps %s: %w%s", strings.Join(args, " "), err, detail)
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return strings.TrimSpace(line)
}
