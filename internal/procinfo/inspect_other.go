//go:build !darwin

package procinfo

import "errors"

func inspectNative(int) (Invocation, error) {
	return Invocation{}, errors.New("lossless process arguments are unavailable on this platform")
}
