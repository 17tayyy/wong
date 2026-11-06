//go:build darwin || dragonfly || freebsd || netbsd || openbsd

package ui

import "golang.org/x/sys/unix"

const ioctlGetTermios, ioctlSetTermios = unix.TIOCGETA, unix.TIOCSETA
