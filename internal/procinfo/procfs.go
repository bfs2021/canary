package procinfo

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// The procfs readers take the mount point as an argument so tests can run
// them against a synthetic tree on any platform.

// procfsProcess reads one process: its command line from <pid>/cmdline,
// parent from <pid>/stat, and the executable link <pid>/exe, which only
// matters for recognising a user-mode emulator.
func procfsProcess(root string, pid int) (Process, error) {
	dir := filepath.Join(root, strconv.Itoa(pid))
	raw, err := os.ReadFile(filepath.Join(dir, "cmdline"))
	if err != nil {
		return Process{}, procfsError(root, pid, err)
	}
	args := splitCmdline(raw)
	if len(args) == 0 {
		// Kernel threads and zombies have no command line; ps would print a
		// bracketed name that no identity check can match.
		return Process{}, fmt.Errorf("empty command line in %s", filepath.Join(dir, "cmdline"))
	}
	exe, _ := os.Readlink(filepath.Join(dir, "exe")) // unreadable for other users; only needed for emulators
	proc := Process{PID: pid, Command: strings.Join(guestArgs(args, exe), " ")}
	if data, err := os.ReadFile(filepath.Join(dir, "stat")); err == nil {
		if stat, err := parseStat(string(data)); err == nil {
			proc.PPID = stat.ppid
		}
	}
	return proc, nil
}

// procfsList reads every numeric entry under root. Processes that exit
// while the listing runs, and those without a command line, are skipped.
func procfsList(root string) ([]Process, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("list processes: process inspection on Linux needs procfs mounted at %s: %w", root, err)
	}
	var procs []Process
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 || !entry.IsDir() {
			continue
		}
		proc, err := procfsProcess(root, pid)
		if err != nil {
			continue
		}
		procs = append(procs, proc)
	}
	return procs, nil
}

// procfsName reads the short command name, the field ps prints as comm.
func procfsName(root string, pid int) (string, error) {
	data, err := os.ReadFile(filepath.Join(root, strconv.Itoa(pid), "stat"))
	if err != nil {
		return "", procfsError(root, pid, err)
	}
	stat, err := parseStat(string(data))
	if err != nil {
		return "", fmt.Errorf("%s: %w", filepath.Join(root, strconv.Itoa(pid), "stat"), err)
	}
	return stat.name, nil
}

// procfsState reads the one-letter scheduler state ("Z" for a zombie).
func procfsState(root string, pid int) (string, error) {
	data, err := os.ReadFile(filepath.Join(root, strconv.Itoa(pid), "stat"))
	if err != nil {
		return "", procfsError(root, pid, err)
	}
	stat, err := parseStat(string(data))
	if err != nil {
		return "", fmt.Errorf("%s: %w", filepath.Join(root, strconv.Itoa(pid), "stat"), err)
	}
	return stat.state, nil
}

// procfsError tells a process that has gone from a procfs that is missing
// altogether, which needs the user to act.
func procfsError(root string, pid int, err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		if _, selfErr := os.Stat(filepath.Join(root, "self")); selfErr != nil {
			return fmt.Errorf("process inspection on Linux needs procfs mounted at %s: %w", root, err)
		}
		return fmt.Errorf("%s: %w", filepath.Join(root, strconv.Itoa(pid)), ErrNoProcess)
	}
	return err
}

// splitCmdline splits the NUL-separated argv of <pid>/cmdline. A process
// that rewrote its title into one string yields one field, as ps shows it.
func splitCmdline(raw []byte) []string {
	raw = bytes.TrimRight(raw, "\x00")
	if len(raw) == 0 {
		return nil
	}
	parts := bytes.Split(raw, []byte{0})
	args := make([]string, len(parts))
	for i, part := range parts {
		args[i] = string(part)
	}
	return args
}

type procStat struct {
	name  string
	state string
	ppid  int
}

// parseStat reads "pid (comm) state ppid ...". comm may itself contain
// spaces and parentheses, so it runs to the last ")".
func parseStat(data string) (procStat, error) {
	open := strings.IndexByte(data, '(')
	closing := strings.LastIndexByte(data, ')')
	if open < 0 || closing < open {
		return procStat{}, errors.New("malformed stat")
	}
	fields := strings.Fields(data[closing+1:])
	if len(fields) < 2 {
		return procStat{}, errors.New("malformed stat")
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return procStat{}, errors.New("malformed stat")
	}
	return procStat{name: data[open+1 : closing], state: fields[0], ppid: ppid}, nil
}

// guestArgs returns the emulated program's argv when the kernel ran args
// through a qemu-user interpreter registered with binfmt_misc, and args
// unchanged otherwise.
//
// The kernel then starts the interpreter with argv
//
//	interpreter filename [original-argv0] original-argv1 ...
//
// where original-argv0 is present only with the preserve-argv0 flag, and
// <pid>/exe names the interpreter. Both must agree before anything is
// stripped: argv[0] can be any text, the exe link cannot. The interpreter
// list is deliberately narrow (qemu-user only, the one emulator this was
// exercised under); others such as Rosetta for Linux, FEX or box64 keep their
// raw command line, which the lifecycle identity checks then refuse. The
// emulated program's argv0 is reported as the filename the kernel executed;
// an original argv0 is recognised only when it names the same base file, so
// the result is exactly the command a native run would show, never a
// substring of a longer one.
func guestArgs(args []string, exe string) []string {
	if len(args) < 2 || exe == "" || args[0] != exe || !isQEMUUser(exe) {
		return args
	}
	filename := args[1]
	rest := args[2:]
	if len(rest) > 0 && filepath.Base(rest[0]) == filepath.Base(filename) {
		rest = rest[1:]
	}
	return append([]string{filename}, rest...)
}

// isQEMUUser reports whether path names a qemu-user interpreter such as
// /usr/bin/qemu-x86_64 or /usr/libexec/qemu-binfmt/qemu-aarch64-static.
func isQEMUUser(path string) bool {
	base := strings.TrimSuffix(filepath.Base(path), "-static")
	arch, ok := strings.CutPrefix(base, "qemu-")
	if !ok || arch == "" {
		return false
	}
	for _, r := range arch {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
			return false
		}
	}
	return true
}
