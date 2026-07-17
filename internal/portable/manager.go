// Package portable resolves and installs the portable RE toolchain
// (jadx, ghidra, apktool, radare2, adb, frida) without MCP.
package portable

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Manager resolves logical tool names to executable paths.
type Manager struct {
	toolsDir string
}

// New builds a Manager rooted at toolsDir (bundled binaries live here).
func New(toolsDir string) *Manager { return &Manager{toolsDir: toolsDir} }

// candidates lists the executable filenames a tool may present as,
// most-preferred first. Extensions are matched with platform awareness.
var candidates = map[string][]string{
	"radare2":  {"radare2", "r2"},
	"jadx":     {"jadx"},
	"apktool":  {"apktool"},
	"adb":      {"adb"},
	"frida":    {"frida"},
	"frida-ps": {"frida-ps"},
	"ghidra":   {"analyzeHeadless"},
	"objdump":  {"objdump"},
}

// Resolve returns an executable path for name, searching the bundled
// tools dir first, then PATH.
func (m *Manager) Resolve(name string) (string, error) {
	names := candidates[name]
	if len(names) == 0 {
		names = []string{name}
	}

	// 1) bundled: toolsDir/<name>/bin/<exe>, toolsDir/<name>/<exe>, toolsDir/<exe>
	for _, base := range names {
		for _, exe := range execNames(base) {
			for _, cand := range []string{
				filepath.Join(m.toolsDir, name, "bin", exe),
				filepath.Join(m.toolsDir, name, exe),
				filepath.Join(m.toolsDir, exe),
			} {
				if isExecFile(cand) {
					return cand, nil
				}
			}
		}
	}

	// 2) PATH
	for _, base := range names {
		if p, err := exec.LookPath(base); err == nil {
			return p, nil
		}
	}

	return "", fmt.Errorf("tool %q not found (looked in %s and PATH); run `jurig install %s`", name, m.toolsDir, name)
}

// Status reports resolved path or a not-found marker for each known tool.
func (m *Manager) Status() map[string]string {
	out := map[string]string{}
	for name := range candidates {
		if p, err := m.Resolve(name); err == nil {
			out[name] = p
		} else {
			out[name] = "MISSING"
		}
	}
	return out
}

// execNames expands a base name to platform executable filenames.
func execNames(base string) []string {
	if runtime.GOOS == "windows" {
		return []string{base + ".exe", base + ".bat", base + ".cmd", base}
	}
	return []string{base}
}

func isExecFile(p string) bool {
	if _, err := exec.LookPath(p); err == nil {
		return true
	}
	// LookPath needs PATHEXT/x bit; fall back to a plain existence check for
	// absolute bundled paths with a known extension.
	if strings.ContainsAny(p, `/\`) {
		if fileExists(p) {
			return true
		}
	}
	return false
}
