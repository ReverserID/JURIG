package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// AskUserTool lets the agent ask the human operator a scoping question
// (like Claude Code) and wait for the answer — e.g. "test dynamically with
// frida on an emulator/adb, or static only?".
type AskUserTool struct{}

func (t *AskUserTool) Name() string { return "ask_user" }
func (t *AskUserTool) Description() string {
	return "Ask the human operator a question and wait for their answer. Use EARLY to lock scope: what they want found, and whether dynamic testing (frida + emulator/adb) is allowed or static-only. Also use when you hit a real fork you cannot decide. Provide 2-4 concrete options when possible. Returns the operator's reply."
}
func (t *AskUserTool) Schema() map[string]any {
	return schema(map[string]any{
		"question": strProp("the question to ask the operator"),
		"options":  arrProp("optional suggested answers (2-4)"),
	}, "question")
}
func (t *AskUserTool) Run(_ context.Context, input json.RawMessage, env *Env) (string, error) {
	var in struct {
		Question string   `json:"question"`
		Options  []string `json:"options"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", err
	}
	if env.Ask == nil {
		return "No interactive operator available. Proceed autonomously with your best judgment.", nil
	}
	ans := env.Ask(in.Question, in.Options)
	if strings.TrimSpace(ans) == "" {
		return "(operator gave no answer — proceed with your best judgment)", nil
	}
	return "Operator answered: " + ans, nil
}

// SearchCodeTool greps decompiled sources (or any dir) for a regex, returning
// file:line matches. This is the efficient alternative to reading files one by
// one — the agent should search first, then read only the hits.
type SearchCodeTool struct{}

func (t *SearchCodeTool) Name() string { return "search_code" }
func (t *SearchCodeTool) Description() string {
	return "Regex-search files under a directory (default the jadx sources) and return file:line: matches. FAST — use this to locate code instead of reading files blindly. Good queries: crypto keys, URLs, 'SecretKeySpec', 'loadLibrary', class/method names, 'https://', 'Authorization'."
}
func (t *SearchCodeTool) Schema() map[string]any {
	return schema(map[string]any{
		"pattern": strProp("regular expression to search for"),
		"dir":     strProp("directory to search (default <workdir>/jadx/sources)"),
		"glob":    strProp("optional filename suffix filter, e.g. .java or .smali"),
		"max":     map[string]any{"type": "integer", "description": "max matches (default 60)"},
	}, "pattern")
}
func (t *SearchCodeTool) Run(_ context.Context, input json.RawMessage, env *Env) (string, error) {
	var in struct {
		Pattern string `json:"pattern"`
		Dir     string `json:"dir"`
		Glob    string `json:"glob"`
		Max     int    `json:"max"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", err
	}
	re, err := regexp.Compile(in.Pattern)
	if err != nil {
		return "", fmt.Errorf("bad regex: %w", err)
	}
	dir := in.Dir
	if dir == "" {
		dir = filepath.Join(env.WorkDir, "jadx", "sources")
	} else {
		dir = resolvePath(env, dir)
	}
	if in.Max <= 0 {
		in.Max = 60
	}

	var out strings.Builder
	matches, filesHit := 0, 0
	err = filepath.Walk(dir, func(p string, fi os.FileInfo, werr error) error {
		if werr != nil || fi.IsDir() {
			return nil
		}
		if in.Glob != "" && !strings.HasSuffix(p, in.Glob) {
			return nil
		}
		if matches >= in.Max {
			return filepath.SkipDir
		}
		f, e := os.Open(p)
		if e != nil {
			return nil
		}
		defer f.Close()
		// paths relative to WorkDir so read_file can use them directly
		rel, e2 := filepath.Rel(env.WorkDir, p)
		if e2 != nil {
			rel = p
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		ln := 0
		hitInFile := false
		for sc.Scan() {
			ln++
			line := sc.Text()
			if re.MatchString(line) {
				trimmed := strings.TrimSpace(line)
				if len(trimmed) > 160 {
					trimmed = trimmed[:160] + "…"
				}
				fmt.Fprintf(&out, "%s:%d: %s\n", filepath.ToSlash(rel), ln, trimmed)
				matches++
				hitInFile = true
				if matches >= in.Max {
					break
				}
			}
		}
		if hitInFile {
			filesHit++
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if matches == 0 {
		return "no matches for /" + in.Pattern + "/ under " + dir, nil
	}
	header := fmt.Sprintf("%d matches in %d files for /%s/", matches, filesHit, in.Pattern)
	if matches >= in.Max {
		header += " (capped)"
	}
	return header + "\n" + out.String(), nil
}
