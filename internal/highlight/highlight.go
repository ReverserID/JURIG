// Package highlight renders source code with ANSI syntax highlighting
// (chroma) and hard-wraps every visual line to a fixed width so it can never
// overflow a TUI viewport/border.
package highlight

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/muesli/reflow/wrap"
)

const (
	gutterW = 6 // "1234 " width before the │
	dim     = "\x1b[38;5;240m"
	reset   = "\x1b[0m"
)

// Code returns highlighted, line-numbered, width-wrapped code. width is the
// total column budget; maxLines caps output (0 = no cap). matchLine (1-based,
// 0 = none) is marked with a ▸.
func Code(source, filename string, width, maxLines, matchLine int) string {
	if width < 20 {
		width = 20
	}
	lines := strings.Split(strings.ReplaceAll(source, "\r\n", "\n"), "\n")
	truncated := 0
	if maxLines > 0 && len(lines) > maxLines {
		truncated = len(lines) - maxLines
		lines = lines[:maxLines]
	}
	src := strings.Join(lines, "\n")

	lex := lexers.Match(filename)
	if lex == nil {
		lex = lexers.Analyse(src)
	}
	if lex == nil {
		lex = lexers.Fallback
	}
	lex = chroma.Coalesce(lex)

	style := styles.Get("dracula")
	if style == nil {
		style = styles.Fallback
	}
	fmtr := formatters.Get("terminal256")
	if fmtr == nil {
		fmtr = formatters.Fallback
	}

	it, err := lex.Tokenise(nil, src)
	if err != nil {
		return src
	}
	var buf bytes.Buffer
	if err := fmtr.Format(&buf, style, it); err != nil {
		return src
	}

	hlLines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	codeW := width - gutterW - 2 // minus gutter + "│ "
	if codeW < 10 {
		codeW = 10
	}
	var b strings.Builder
	for i, l := range hlLines {
		lineNo := i + 1
		mark := " "
		if lineNo == matchLine {
			mark = "▸"
		}
		// hard-wrap this one logical line (ANSI-aware) to codeW
		sub := strings.Split(wrap.String(l, codeW), "\n")
		for j, s := range sub {
			if j == 0 {
				fmt.Fprintf(&b, "%s%s%4d %s│%s %s\n", dim, mark, lineNo, "", reset, s)
			} else {
				fmt.Fprintf(&b, "%s     │%s %s\n", dim, reset, s)
			}
		}
	}
	if truncated > 0 {
		fmt.Fprintf(&b, "%s     … (+%d more lines)%s\n", dim, truncated, reset)
	}
	return b.String()
}

// IsCode guesses whether a filename/content is source worth highlighting.
func IsCode(filename, content string) bool {
	switch strings.ToLower(ext(filename)) {
	case ".java", ".kt", ".smali", ".xml", ".json", ".c", ".cc", ".cpp", ".h",
		".hpp", ".go", ".js", ".ts", ".py", ".rb", ".rs", ".swift", ".m", ".gradle",
		".properties", ".yaml", ".yml", ".toml", ".sh", ".sql", ".php", ".dart":
		return true
	}
	// content heuristic
	for _, k := range []string{"package ", "import ", "class ", "public ", "func ", "<?xml", "def "} {
		if strings.Contains(content, k) {
			return true
		}
	}
	return false
}

// Lang returns a short language label for a filename (for card titles).
func Lang(filename string) string {
	e := strings.TrimPrefix(strings.ToLower(ext(filename)), ".")
	if e == "" {
		return "text"
	}
	return e
}

func ext(name string) string {
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		return name[i:]
	}
	return ""
}
