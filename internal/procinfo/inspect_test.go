package procinfo

import (
	"encoding/binary"
	"os"
	"slices"
	"testing"
)

func TestExactArgumentsPreserveEmptyAndSpacedValues(t *testing.T) {
	args := []string{"/tmp/two spaces/canary", "app", "--state-dir", "/tmp/app  state", "--public-url", ""}
	root := procTree(t, true, map[int]fakeProc{12: {argv: args}})
	got, err := procfsProcess(root, 12)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Args, args) {
		t.Fatalf("args=%q want %q", got.Args, args)
	}
	raw := binary.NativeEndian.AppendUint32(nil, uint32(len(args)))
	raw = append(raw, []byte("/tmp/canary\x00\x00\x00")...)
	for _, arg := range args {
		raw = append(raw, []byte(arg)...)
		raw = append(raw, 0)
	}
	raw = append(raw, []byte("HOME=/tmp/home space\x00XDG_RUNTIME_DIR=/tmp/runtime\x00\x00")...)
	inv, err := parseDarwinInvocation(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(inv.Args, args) || !slices.Equal(inv.Environment, []string{"HOME=/tmp/home space", "XDG_RUNTIME_DIR=/tmp/runtime"}) {
		t.Fatalf("invocation=%+v", inv)
	}
}

func TestNativeInspectOwnProcess(t *testing.T) {
	got, err := Inspect(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Args, os.Args) {
		t.Fatalf("argv=%q want %q", got.Args, os.Args)
	}
	if len(got.Environment) == 0 {
		t.Fatal("process scope environment unavailable")
	}
}
