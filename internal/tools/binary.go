package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Radare2Tool runs a batch of r2 commands against a target binary in
// quiet mode: radare2 -q -c "cmd1; cmd2" <file>.
type Radare2Tool struct{}

func (t *Radare2Tool) Name() string { return "radare2" }
func (t *Radare2Tool) Description() string {
	return "Static analysis of a native binary (ELF/PE/Mach-O/.so). Runs radare2 commands in quiet batch mode. Useful: 'aaa' (analyze), 'afl' (list funcs), 'iI' (info), 'ii' (imports), 'iz' (strings), 'pdf @ sym.main' (disasm func)."
}
func (t *Radare2Tool) Schema() map[string]any {
	return schema(map[string]any{
		"file":     strProp("path to the binary"),
		"commands": arrProp("r2 commands to run in order"),
	}, "file", "commands")
}
func (t *Radare2Tool) Run(ctx context.Context, input json.RawMessage, env *Env) (string, error) {
	var in struct {
		File     string   `json:"file"`
		Commands []string `json:"commands"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", err
	}
	bin, err := env.ResolveBin("radare2")
	if err != nil {
		return "", err
	}
	script := strings.Join(in.Commands, "; ")
	return runCmd(ctx, env, 4*time.Minute, env.WorkDir, bin, "-q", "-c", script, resolvePath(env, in.File))
}

// GhidraTool runs Ghidra's analyzeHeadless to auto-analyze a native binary and
// optionally run a post-script (e.g. a decompiler dump). Heavy but thorough —
// use for native .so/ELF/PE when radare2 is not enough.
type GhidraTool struct{}

func (t *GhidraTool) Name() string { return "ghidra" }
func (t *GhidraTool) Description() string {
	return "Analyze a native binary with Ghidra headless (analyzeHeadless): imports + auto-analyzes into a throwaway project. Optionally runs a Ghidra post-script by path (e.g. a decompile/export script). Returns the analysis log. Requires a Ghidra install discoverable as tools_dir/ghidra."
}
func (t *GhidraTool) Schema() map[string]any {
	return schema(map[string]any{
		"file":        strProp("path to the native binary (.so/.elf/.exe/.o)"),
		"post_script": strProp("optional Ghidra post-script filename (searched in script_path)"),
		"script_path": strProp("optional dir containing the post-script"),
	}, "file")
}
func (t *GhidraTool) Run(ctx context.Context, input json.RawMessage, env *Env) (string, error) {
	var in struct {
		File       string `json:"file"`
		PostScript string `json:"post_script"`
		ScriptPath string `json:"script_path"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", err
	}
	bin, err := env.ResolveBin("ghidra") // resolves analyzeHeadless
	if err != nil {
		return "", err
	}
	proj := filepath.Join(env.WorkDir, "ghidra_proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		return "", err
	}
	args := []string{proj, "jurig", "-import", resolvePath(env, in.File), "-overwrite"}
	if in.PostScript != "" {
		if in.ScriptPath != "" {
			args = append(args, "-scriptPath", resolvePath(env, in.ScriptPath))
		}
		args = append(args, "-postScript", in.PostScript)
	}
	return runCmd(ctx, env, 20*time.Minute, env.WorkDir, bin, args...)
}
