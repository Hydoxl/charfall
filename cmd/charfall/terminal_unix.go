//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly

package main

import (
	"os"

	"golang.org/x/sys/unix"
)

func terminalSize(file *os.File) (width, height int, terminal bool) {
	size, err := unix.IoctlGetWinsize(int(file.Fd()), unix.TIOCGWINSZ)
	if err != nil {
		return 0, 0, false
	}
	width, height = int(size.Col), int(size.Row)
	if width == 0 {
		width = 80
	}
	if height == 0 {
		height = 24
	}
	return width, height, true
}
