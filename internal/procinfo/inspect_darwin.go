package procinfo

import "golang.org/x/sys/unix"

func inspectNative(pid int) (Invocation, error) {
	raw, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		return Invocation{}, err
	}
	return parseDarwinInvocation(raw)
}
