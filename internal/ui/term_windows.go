package ui

import (
	"os"

	"golang.org/x/sys/windows"
)

// keysOneByOne stops the console from echoing and from holding keys until Enter, and keeps Ctrl+C a signal.
func keysOneByOne(f *os.File) (func(), error) {
	h := windows.Handle(f.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(h, &mode); err != nil {
		return nil, err
	}
	keys := mode&^(windows.ENABLE_ECHO_INPUT|windows.ENABLE_LINE_INPUT) | windows.ENABLE_PROCESSED_INPUT | windows.ENABLE_VIRTUAL_TERMINAL_INPUT
	if err := windows.SetConsoleMode(h, keys); err != nil {
		return nil, err
	}
	return func() { _ = windows.SetConsoleMode(h, mode) }, nil
}

// colorOn turns on the console's escape sequences, which older consoles leave off.
func colorOn(f *os.File) bool {
	h := windows.Handle(f.Fd())
	var mode uint32
	if windows.GetConsoleMode(h, &mode) != nil {
		return false
	}
	return mode&windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING != 0 ||
		windows.SetConsoleMode(h, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING) == nil
}
