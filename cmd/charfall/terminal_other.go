//go:build !darwin && !linux && !freebsd && !openbsd && !netbsd && !dragonfly

package main

import "os"

func terminalSize(file *os.File) (width, height int, terminal bool) {
	return 0, 0, false
}
