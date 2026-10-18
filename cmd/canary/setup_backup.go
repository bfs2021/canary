package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// secureClaudeDesktopBackups repairs prior backup permissions even when the
// current configuration is missing or cannot be parsed.
func secureClaudeDesktopBackups(path string) error {
	dir, prefix := filepath.Dir(path), filepath.Base(path)+".bak-"
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), prefix) || !entry.Type().IsRegular() {
			continue
		}
		fd, err := unix.Open(filepath.Join(dir, entry.Name()), unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
		if err != nil {
			return fmt.Errorf("secure existing backup: %w", err)
		}
		f := os.NewFile(uintptr(fd), entry.Name())
		info, statErr := f.Stat()
		if statErr == nil && info.Mode().IsRegular() {
			statErr = f.Chmod(0600)
		}
		closeErr := f.Close()
		if statErr != nil {
			return statErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

// writeClaudeDesktopBackup creates a unique private backup, removing a partial
// result on failure and never following an existing destination symlink.
func writeClaudeDesktopBackup(path string, data []byte) (string, error) {
	dir, prefix := filepath.Dir(path), filepath.Base(path)+".bak-"
	f, err := os.CreateTemp(dir, prefix+time.Now().Format("20060102-150405")+"-*")
	if err != nil {
		return "", err
	}
	name, complete := f.Name(), false
	defer func() {
		_ = f.Close()
		if !complete {
			_ = os.Remove(name)
		}
	}()
	if _, err := f.Write(data); err != nil {
		return "", err
	}
	if err := f.Sync(); err != nil {
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	complete = true
	return name, nil
}
