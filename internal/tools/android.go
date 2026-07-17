package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// JadxTool decompiles an APK/DEX to Java sources using the jadx CLI.
type JadxTool struct{}

func (t *JadxTool) Name() string { return "jadx" }
func (t *JadxTool) Description() string {
	return "Decompile an APK or DEX to Java source with jadx. Output goes to <workdir>/jadx by default. Returns the output dir + top-level listing."
}
func (t *JadxTool) Schema() map[string]any {
	return schema(map[string]any{
		"apk":    strProp("path to .apk/.dex/.jar"),
		"out":    strProp("output dir (optional, default <workdir>/jadx)"),
		"no_res": boolProp("skip resource decoding (faster)"),
	}, "apk")
}
func (t *JadxTool) Run(ctx context.Context, input json.RawMessage, env *Env) (string, error) {
	var in struct {
		APK   string `json:"apk"`
		Out   string `json:"out"`
		NoRes bool   `json:"no_res"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", err
	}
	apk := resolvePath(env, in.APK)

	// .xapk/.apks/.zip bundle → extract natively and locate the base apk.
	if isBundle(apk) {
		env.emit("cmd", "xapk bundle detected → extracting "+filepath.Base(apk))
		exDir := filepath.Join(env.WorkDir, "bundle_extracted")
		if _, err := extractZip(apk, exDir); err != nil {
			return "", fmt.Errorf("extract bundle: %w", err)
		}
		base, err := findBaseAPK(exDir)
		if err != nil {
			return "", err
		}
		env.emit("cmd", "base apk → "+base)
		apk = base
	}

	bin, err := env.ResolveBin("jadx")
	if err != nil {
		return "", err
	}
	out := in.Out
	if out == "" {
		out = filepath.Join(env.WorkDir, "jadx")
	}
	args := []string{"-d", out}
	if in.NoRes {
		args = append(args, "--no-res")
	}
	args = append(args, apk)
	log, err := runCmd(ctx, env, 15*time.Minute, env.WorkDir, bin, args...)
	// jadx often exits non-zero on partial-decompile warnings; if it produced
	// sources, treat as success and summarize.
	srcDir := filepath.Join(out, "sources")
	if _, e := os.Stat(srcDir); e != nil {
		if err != nil {
			return log, err
		}
	}
	return summarizeDecompile(apk, out), nil
}

// summarizeDecompile walks the jadx output and returns a hacker-style
// Markdown report: counts, top packages, and suspicious-string hits.
func summarizeDecompile(apk, out string) string {
	srcDir := filepath.Join(out, "sources")
	var javaFiles int
	pkgCount := map[string]int{}
	// suspicious surface: category -> hit files (bounded)
	sig := map[string][]string{}
	needles := map[string][]string{
		"crypto":    {"javax.crypto", "AES", "Cipher", "MessageDigest", "SecretKey"},
		"network":   {"okhttp", "Retrofit", "HttpURLConnection", "https://", "api."},
		"secrets":   {"api_key", "apikey", "secret", "token", "password", "Authorization"},
		"native":    {"System.loadLibrary", "JNI", "native "},
		"root/anti": {"su", "isDebuggerConnected", "RootBeer", "frida", "magisk"},
	}

	_ = filepath.Walk(srcDir, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() || !strings.HasSuffix(p, ".java") {
			return nil
		}
		javaFiles++
		rel, _ := filepath.Rel(srcDir, p)
		if pkg := filepath.Dir(rel); pkg != "." {
			pkgCount[filepath.ToSlash(pkg)]++
		}
		// scan a bounded number of files for signals to keep it fast
		if javaFiles <= 4000 {
			if b, e := os.ReadFile(p); e == nil {
				s := string(b)
				for cat, ns := range needles {
					for _, n := range ns {
						if strings.Contains(s, n) {
							if len(sig[cat]) < 12 {
								sig[cat] = appendUniq(sig[cat], rel)
							}
							break
						}
					}
				}
			}
		}
		return nil
	})

	// top packages by file count
	type kv struct {
		k string
		v int
	}
	var pkgs []kv
	for k, v := range pkgCount {
		pkgs = append(pkgs, kv{k, v})
	}
	sort.Slice(pkgs, func(i, j int) bool { return pkgs[i].v > pkgs[j].v })

	var b strings.Builder
	fmt.Fprintf(&b, "## Decompile complete\n\n")
	fmt.Fprintf(&b, "- **apk:** `%s`\n", filepath.Base(apk))
	fmt.Fprintf(&b, "- **output:** `%s`\n", out)
	fmt.Fprintf(&b, "- **classes (.java):** %d across %d packages\n\n", javaFiles, len(pkgCount))

	b.WriteString("### Top packages\n")
	for i, p := range pkgs {
		if i >= 12 {
			break
		}
		fmt.Fprintf(&b, "- `%s` — %d\n", p.k, p.v)
	}

	b.WriteString("\n### Suspicious surface\n")
	if len(sig) == 0 {
		b.WriteString("_no obvious signals in scanned sources_\n")
	}
	for _, cat := range []string{"secrets", "crypto", "network", "native", "root/anti"} {
		hits := sig[cat]
		if len(hits) == 0 {
			continue
		}
		fmt.Fprintf(&b, "- **%s** (%d): %s\n", cat, len(hits), strings.Join(hits, ", "))
	}
	b.WriteString("\nNext: inspect flagged classes with read_file, pull the manifest, then hook with frida.")
	return b.String()
}

func appendUniq(s []string, v string) []string {
	for _, x := range s {
		if x == v {
			return s
		}
	}
	return append(s, v)
}

// ApktoolTool decodes an APK (manifest, smali, resources).
type ApktoolTool struct{}

func (t *ApktoolTool) Name() string { return "apktool" }
func (t *ApktoolTool) Description() string {
	return "Decode an APK to smali + decoded AndroidManifest.xml + resources using apktool."
}
func (t *ApktoolTool) Schema() map[string]any {
	return schema(map[string]any{
		"apk": strProp("path to .apk"),
		"out": strProp("output dir (optional, default <workdir>/apktool)"),
	}, "apk")
}
func (t *ApktoolTool) Run(ctx context.Context, input json.RawMessage, env *Env) (string, error) {
	var in struct {
		APK string `json:"apk"`
		Out string `json:"out"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", err
	}
	bin, err := env.ResolveBin("apktool")
	if err != nil {
		return "", err
	}
	out := in.Out
	if out == "" {
		out = filepath.Join(env.WorkDir, "apktool")
	}
	return runCmd(ctx, env, 10*time.Minute, env.WorkDir, bin, "d", "-f", "-o", out, resolvePath(env, in.APK))
}

// AdbTool runs an adb command against a connected device/emulator.
type AdbTool struct{}

func (t *AdbTool) Name() string { return "adb" }
func (t *AdbTool) Description() string {
	return "Run an adb command (device shell, install, pull, logcat, etc). Args are passed verbatim, e.g. [\"shell\",\"pm\",\"list\",\"packages\"]."
}
func (t *AdbTool) Schema() map[string]any {
	return schema(map[string]any{
		"args":    arrProp("adb arguments"),
		"timeout": map[string]any{"type": "integer", "description": "timeout seconds (default 60)"},
	}, "args")
}
func (t *AdbTool) Run(ctx context.Context, input json.RawMessage, env *Env) (string, error) {
	var in struct {
		Args    []string `json:"args"`
		Timeout int      `json:"timeout"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", err
	}
	bin, err := env.ResolveBin("adb")
	if err != nil {
		return "", err
	}
	if in.Timeout == 0 {
		in.Timeout = 60
	}
	return runCmd(ctx, env, time.Duration(in.Timeout)*time.Second, env.WorkDir, bin, in.Args...)
}

// FridaTool runs a Frida script against a target (USB device by default).
// It writes the script to disk then invokes the frida CLI. spawn=true uses
// -f <target> (launch); otherwise -n/-p to attach.
type FridaTool struct{}

func (t *FridaTool) Name() string { return "frida" }
func (t *FridaTool) Description() string {
	return "Dynamic instrumentation. Injects a JS Frida script into an app on a USB device. Provide the script source; set spawn=true to launch the package fresh, or attach by process name. Runs for `run_seconds` then detaches and returns captured console output. Requires frida-server running on the device."
}
func (t *FridaTool) Schema() map[string]any {
	return schema(map[string]any{
		"target":      strProp("package name (spawn) or process name/pid (attach)"),
		"script":      strProp("Frida JS script source"),
		"spawn":       boolProp("launch target fresh with -f (default attach)"),
		"run_seconds": map[string]any{"type": "integer", "description": "how long to run before detach (default 15)"},
	}, "target", "script")
}
func (t *FridaTool) Run(ctx context.Context, input json.RawMessage, env *Env) (string, error) {
	var in struct {
		Target     string `json:"target"`
		Script     string `json:"script"`
		Spawn      bool   `json:"spawn"`
		RunSeconds int    `json:"run_seconds"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", err
	}
	bin, err := env.ResolveBin("frida")
	if err != nil {
		return "", err
	}
	if in.RunSeconds == 0 {
		in.RunSeconds = 15
	}
	scriptPath := filepath.Join(env.WorkDir, "frida_script.js")
	if err := os.MkdirAll(env.WorkDir, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(scriptPath, []byte(in.Script), 0o644); err != nil {
		return "", err
	}

	// -U USB, -q quiet, --runtime=v8, -l script; auto-exit via timeout wrapper.
	args := []string{"-U", "-q", "-l", scriptPath}
	if in.Spawn {
		args = append(args, "-f", in.Target)
	} else {
		args = append(args, "-n", in.Target)
	}
	// frida REPL blocks; the runCmd timeout doubles as the run window.
	out, err := runCmd(ctx, env, time.Duration(in.RunSeconds+10)*time.Second, env.WorkDir, bin, args...)
	// A timeout here is expected (we intentionally cap the run), so treat
	// timeout as success and return whatever the script printed.
	if err != nil && strings.Contains(err.Error(), "timeout") {
		return out, nil
	}
	return out, err
}
