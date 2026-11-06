//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package ui

import (
	"os"

	"golang.org/x/sys/unix"
)

// keysOneByOne stops the terminal from echoing and from holding keys until Enter, and keeps Ctrl+C a signal.
func keysOneByOne(f *os.File) (func(), error) {
	fd := int(f.Fd())
	old, err := unix.IoctlGetTermios(fd, ioctlGetTermios)
	if err != nil {
		return nil, err
	}
	t := *old
	t.Lflag &^= unix.ECHO | unix.ICANON
	t.Cc[unix.VMIN] = 1
	t.Cc[unix.VTIME] = 0
	if err := unix.IoctlSetTermios(fd, ioctlSetTermios, &t); err != nil {
		return nil, err
	}
	return func() { _ = unix.IoctlSetTermios(fd, ioctlSetTermios, old) }, nil
}

func colorOn(*os.File) bool { return true }
