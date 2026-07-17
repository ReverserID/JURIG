package highlight

import (
	"strings"
	"testing"

	"github.com/muesli/reflow/ansi"
)

// TestCodeWidthBound proves no rendered line exceeds the width budget, so the
// TUI border can never be broken by long code lines.
func TestCodeWidthBound(t *testing.T) {
	src := "package x;\npublic class VeryLong { String s = \"" + strings.Repeat("A", 400) + "\"; }\n"
	const width = 60
	out := Code(src, "Foo.java", width, 0, 2)
	for _, ln := range strings.Split(out, "\n") {
		if w := ansi.PrintableRuneWidth(ln); w > width {
			t.Fatalf("line width %d exceeds budget %d: %q", w, width, ln)
		}
	}
}

func TestIsCode(t *testing.T) {
	if !IsCode("Foo.java", "") {
		t.Fatal("java should be code")
	}
	if !IsCode("x.smali", "") {
		t.Fatal("smali should be code")
	}
	if IsCode("data.bin", "\x00\x01binary") {
		t.Fatal("binary should not be code")
	}
}
