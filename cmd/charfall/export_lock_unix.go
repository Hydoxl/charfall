//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly

package main

import (
	"fmt"
	"os"
	"syscall"
)

func lockExportDirectory(destination string) (*os.File, error) {
	directory, err := os.Open(destination)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(directory.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		directory.Close()
		return nil, fmt.Errorf("cannot lock output directory; another export may be running: %w", err)
	}
	return directory, nil
}
