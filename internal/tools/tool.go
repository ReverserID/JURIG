// Package tools defines the agent's callable capabilities as native Go
// subprocess wrappers around portable RE binaries (no MCP).
package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"
)

// Env is the shared execution environment handed to every tool.
type Env struct {
	// WorkDir is the per-target scratch directory.
	WorkDir string
	// ResolveBin maps a logical tool name ("jadx", "radare2", "adb") to an
	// executable path, checking bundled portable tools then PATH.
	ResolveBin func(name string) (string, error)
	// Emit streams a progress line to the UI. kind is "cmd"|"out"|"err".
	Emit func(kind, msg string)
	// Ask poses a question to the human operator and blocks for the answer.
	// options is an optional shortlist of suggested answers. Nil in headless.
	Ask func(question string, options []string) string
}

func (e *Env) emit(kind, msg string) {
	if e.Emit != nil {
		e.Emit(kind, msg)
	}
}

// Tool is one capability the model may invoke.
type Tool interface {
	Name() string
	Description() string
	Schema() map[string]any
	Run(ctx context.Context, input json.RawMessage, env *Env) (string, error)
}

// runCmd executes bin+args in dir with a timeout, returning combined output.
func runCmd(ctx context.Context, env *Env, timeout time.Duration, dir, bin string, args ...string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	env.emit("cmd", bin+" "+shellJoin(args))
	cmd := exec.CommandContext(cctx, bin, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	out := buf.String()
	if cctx.Err() == context.DeadlineExceeded {
		return out, fmt.Errorf("timeout after %s", timeout)
	}
	if err != nil {
		return out, fmt.Errorf("%v", err)
	}
	return out, nil
}

func shellJoin(args []string) string {
	s := ""
	for i, a := range args {
		if i > 0 {
			s += " "
		}
		s += a
	}
	return s
}

// schema is a small helper to build a JSON Schema object.
func schema(props map[string]any, required ...string) map[string]any {
	// Always emit an array (never JSON null) — some providers (Kimi/Moonshot)
	// reject `"required": null`.
	if required == nil {
		required = []string{}
	}
	return map[string]any{
		"type":       "object",
		"properties": props,
		"required":   required,
	}
}

func strProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}
func arrProp(desc string) map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": desc}
}
func boolProp(desc string) map[string]any {
	return map[string]any{"type": "boolean", "description": desc}
}
