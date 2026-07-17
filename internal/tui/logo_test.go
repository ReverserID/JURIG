package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

func TestGhostFramesAligned(t *testing.T) {
	for i, f := range ghostFrames {
		if n := strings.Count(f, "\n"); n != 4 {
			t.Fatalf("frame %d has %d newlines, want 4 (5 lines)", i, n)
		}
	}
}

func TestRenderLogoAnimates(t *testing.T) {
	lipgloss.SetColorProfile(termenv.TrueColor) // force color as in a real TTY
	m := &model{}
	a := m.renderLogo()
	m.frame = 3
	b := m.renderLogo()
	if !strings.Contains(a, "\x1b[") {
		t.Fatal("logo should be colored (ANSI)")
	}
	if a == b {
		t.Fatal("logo should change across frames")
	}
}
