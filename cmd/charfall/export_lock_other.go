//go:build !darwin && !linux && !freebsd && !openbsd && !netbsd && !dragonfly

package main

import (
	"errors"
	"os"
)

func lockExportDirectory(destination string) (*os.File, error) {
	return nil, errors.New("export recovery requires native directory locking on this platform")
}
