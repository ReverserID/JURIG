package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// ShellTool runs an arbitrary command. Broad by design — this is an
// autonomous RE agent — but every invocation is streamed to the UI.
type ShellTool struct{}

func (t *ShellTool) Name() string { return "shell" }
func (t *ShellTool) Description() string {
	return "Run a shell command, returns combined stdout+stderr. On Windows the default engine is PowerShell (handles quoting/pipes far better than cmd). For archive extraction of apk/xapk/zip prefer the dedicated `unzip` tool — it is native and avoids all shell-quoting pitfalls. Prefer dedicated tools (unzip, radare2, jadx, adb, frida) when available."
}
func (t *ShellTool) Schema() map[string]any {
	return schema(map[string]any{
		"cmd":     strProp("command line to execute"),
		"dir":     strProp("working directory (optional)"),
		"engine":  strProp("windows only: 'powershell' (default) or 'cmd'"),
		"timeout": map[string]any{"type": "integer", "description": "timeout seconds (default 120)"},
	}, "cmd")
}
func (t *ShellTool) Run(ctx context.Context, input json.RawMessage, env *Env) (string, error) {
	var in struct {
		Cmd     string `json:"cmd"`
		Dir     string `json:"dir"`
		Engine  string `json:"engine"`
		Timeout int    `json:"timeout"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", err
	}
	if in.Timeout == 0 {
		in.Timeout = 120
	}
	dir := in.Dir
	if dir == "" {
		dir = env.WorkDir
	}
	if runtime.GOOS == "windows" {
		if in.Engine == "cmd" {
			return runCmd(ctx, env, time.Duration(in.Timeout)*time.Second, dir, "cmd", "/c", in.Cmd)
		}
		// PowerShell: pass the whole command as a single -Command argument so
		// exec quotes it correctly (the cmd.exe && quoting is what kept failing).
		return runCmd(ctx, env, time.Duration(in.Timeout)*time.Second, dir,
			"powershell", "-NoProfile", "-NonInteractive", "-Command", in.Cmd)
	}
	return runCmd(ctx, env, time.Duration(in.Timeout)*time.Second, dir, "sh", "-c", in.Cmd)
}

// ReadFileTool reads a text file (bounded).
type ReadFileTool struct{}

func (t *ReadFileTool) Name() string        { return "read_file" }
func (t *ReadFileTool) Description() string { return "Read a UTF-8 text file. Returns up to 64KB." }
func (t *ReadFileTool) Schema() map[string]any {
	return schema(map[string]any{"path": strProp("file path")}, "path")
}
func (t *ReadFileTool) Run(_ context.Context, input json.RawMessage, env *Env) (string, error) {
	var in struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", err
	}
	b, err := os.ReadFile(resolvePath(env, in.Path))
	if err != nil {
		return "", err
	}
	const max = 64 * 1024
	if len(b) > max {
		return string(b[:max]) + "\n…[truncated]", nil
	}
	return string(b), nil
}

// WriteFileTool writes a file under the work dir.
type WriteFileTool struct{}

func (t *WriteFileTool) Name() string { return "write_file" }
func (t *WriteFileTool) Description() string {
	return "Write a text file (creates parent dirs). Use for notes, frida scripts, extracted data."
}
func (t *WriteFileTool) Schema() map[string]any {
	return schema(map[string]any{
		"path":    strProp("file path"),
		"content": strProp("file content"),
	}, "path", "content")
}
func (t *WriteFileTool) Run(_ context.Context, input json.RawMessage, env *Env) (string, error) {
	var in struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", err
	}
	p := resolvePath(env, in.Path)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(p, []byte(in.Content), 0o644); err != nil {
		return "", err
	}
	return "wrote " + p + fmt.Sprintf(" (%d bytes)", len(in.Content)), nil
}

// ListDirTool lists directory entries.
type ListDirTool struct{}

func (t *ListDirTool) Name() string        { return "list_dir" }
func (t *ListDirTool) Description() string { return "List entries in a directory." }
func (t *ListDirTool) Schema() map[string]any {
	return schema(map[string]any{"path": strProp("directory path")}, "path")
}
func (t *ListDirTool) Run(_ context.Context, input json.RawMessage, env *Env) (string, error) {
	var in struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", err
	}
	p := resolvePath(env, in.Path)
	entries, err := os.ReadDir(p)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, e := range entries {
		kind := "f"
		if e.IsDir() {
			kind = "d"
		}
		fmt.Fprintf(&b, "%s  %s\n", kind, e.Name())
	}
	return b.String(), nil
}

// StringsTool extracts printable strings from a file (pure Go, no binutils).
type StringsTool struct{}

func (t *StringsTool) Name() string { return "strings" }
func (t *StringsTool) Description() string {
	return "Extract printable ASCII strings (>=min length) from a binary file."
}
func (t *StringsTool) Schema() map[string]any {
	return schema(map[string]any{
		"path": strProp("file path"),
		"min":  map[string]any{"type": "integer", "description": "min length (default 4)"},
	}, "path")
}
func (t *StringsTool) Run(_ context.Context, input json.RawMessage, env *Env) (string, error) {
	var in struct {
		Path string `json:"path"`
		Min  int    `json:"min"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", err
	}
	if in.Min <= 0 {
		in.Min = 4
	}
	b, err := os.ReadFile(resolvePath(env, in.Path))
	if err != nil {
		return "", err
	}
	var out strings.Builder
	var cur []byte
	flush := func() {
		if len(cur) >= in.Min {
			out.Write(cur)
			out.WriteByte('\n')
		}
		cur = cur[:0]
	}
	for _, c := range b {
		if c >= 0x20 && c < 0x7f {
			cur = append(cur, c)
		} else {
			flush()
		}
		if out.Len() > 128*1024 {
			break
		}
	}
	flush()
	return out.String(), nil
}

// resolvePath makes relative paths resolve against WorkDir.
func resolvePath(env *Env, p string) string {
	if filepath.IsAbs(p) || env.WorkDir == "" {
		return p
	}
	return filepath.Join(env.WorkDir, p)
}
