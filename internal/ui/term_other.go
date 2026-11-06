//go:build !(darwin || dragonfly || freebsd || linux || netbsd || openbsd || windows)

package ui

import (
	"errors"
	"os"
)

func keysOneByOne(*os.File) (func(), error) { return nil, errors.ErrUnsupported }

func colorOn(*os.File) bool { return false }
