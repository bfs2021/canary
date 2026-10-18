//go:build !darwin && !linux

package cli

func isTerminalFD(int) bool { return false }
