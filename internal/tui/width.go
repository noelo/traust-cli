package tui

import (
	"os"

	"github.com/charmbracelet/x/ansi"
	"golang.org/x/term"
)

// DefaultPlainWidth is the fallback line width for non-TTY output.
const DefaultPlainWidth = 120

// TerminalWidth returns the terminal width of stdout, or a sane default
// when stdout is not a terminal.
func TerminalWidth() int {
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 {
		return w
	}
	return DefaultPlainWidth
}

// Wrap wraps a (possibly ANSI-styled) string hard at width, preserving
// escape sequences across the wrap.
func Wrap(s string, width int) string {
	if width < 4 {
		width = 4
	}
	return ansi.Wrap(s, width, "")
}

// Truncate clips a (possibly ANSI-styled) string to width, appending tail
// when something was cut.
func Truncate(s string, width int, tail string) string {
	if width < 1 {
		width = 1
	}
	return ansi.Truncate(s, width, tail)
}
