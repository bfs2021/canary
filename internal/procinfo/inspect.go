package procinfo

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Invocation keeps launch data separate from display text. Environment is
// private process metadata; callers must not log it or include it in output.
type Invocation struct {
	Args        []string
	Environment []string
}

// Inspect returns lossless launch arguments and environment, or an error.
// Unsupported/unreadable processes must never be reconstructed from ps text.
func Inspect(pid int) (Invocation, error) {
	if pid <= 0 {
		return Invocation{}, errors.New("invalid PID")
	}
	if useProcfs() {
		p, err := procfsProcess(procRoot, pid)
		if err != nil {
			return Invocation{}, err
		}
		raw, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "environ"))
		if err != nil {
			return Invocation{}, fmt.Errorf("read process scope: %w", err)
		}
		return Invocation{Args: p.Args, Environment: splitCmdline(raw)}, nil
	}
	return inspectNative(pid)
}

// Darwin KERN_PROCARGS2 contains argc, executable, padding, argc NUL-terminated
// arguments and the environment. Use argc rather than dropping empty arguments.
func parseDarwinInvocation(raw []byte) (Invocation, error) {
	bad := errors.New("incomplete process arguments")
	if len(raw) < 4 {
		return Invocation{}, bad
	}
	argc := int(binary.NativeEndian.Uint32(raw[:4]))
	raw = raw[4:]
	if argc < 1 || argc > len(raw) {
		return Invocation{}, bad
	}
	end := bytes.IndexByte(raw, 0)
	if end < 0 {
		return Invocation{}, bad
	}
	raw = raw[end+1:]
	raw = bytes.TrimLeft(raw, "\x00")
	result := Invocation{}
	for range argc {
		end = bytes.IndexByte(raw, 0)
		if end < 0 {
			return Invocation{}, bad
		}
		result.Args = append(result.Args, string(raw[:end]))
		raw = raw[end+1:]
	}
	for _, entry := range splitCmdline(raw) {
		if strings.Contains(entry, "=") {
			result.Environment = append(result.Environment, entry)
		}
	}
	return result, nil
}
