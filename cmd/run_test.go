package cmd

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

func TestWrapBlockStripsIndentationAndWraps(t *testing.T) {
	lipgloss.SetColorProfile(termenv.TrueColor)
	block := "exit status 3\n          ERROR  Agent processing failed: 401 Unauthorized invalid x-api-key      \n"
	out := wrapBlock(block, 40)

	for _, line := range strings.Split(out, "\n") {
		if w := lipgloss.Width(line); w > 40 {
			t.Errorf("line is %d cells wide (limit 40): %q", w, line)
		}
		// Harness error boxes arrive with leading decoration; the display
		// form must start at the left edge so re-wrapped lines align.
		if strings.HasPrefix(line, " ") {
			t.Errorf("line retains leading whitespace: %q", line)
		}
	}
	if !strings.Contains(out, "exit status 3") {
		t.Errorf("content lost: %q", out)
	}
	if strings.Contains(out, "  \n") || strings.HasSuffix(out, "  ") {
		t.Errorf("trailing whitespace not stripped: %q", out)
	}
}

func TestWrapBlockPreservesNewlines(t *testing.T) {
	out := wrapBlock("short one\nshort two", 40)
	if out != "short one\nshort two" {
		t.Errorf("short lines should pass through: %q", out)
	}
}
