package tools

import (
	"archive/zip"
	"context"
	"debug/elf"
	"debug/pe"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ElfInfoTool parses an ELF binary (native .so/executable) using Go's
// debug/elf — arch, type, needed libs, imported/exported symbols. No external
// tool needed.
type ElfInfoTool struct{}

func (t *ElfInfoTool) Name() string { return "elf_info" }
func (t *ElfInfoTool) Description() string {
	return "Parse an ELF binary (.so / Linux/Android executable): architecture, type, NEEDED libraries, imported + exported (dynamic) symbols. Native, no external tool."
}
func (t *ElfInfoTool) Schema() map[string]any {
	return schema(map[string]any{"file": strProp("path to the ELF file")}, "file")
}
func (t *ElfInfoTool) Run(_ context.Context, input json.RawMessage, env *Env) (string, error) {
	var in struct {
		File string `json:"file"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", err
	}
	f, err := elf.Open(resolvePath(env, in.File))
	if err != nil {
		return "", err
	}
	defer f.Close()

	var b strings.Builder
	fmt.Fprintf(&b, "## ELF %s\n", filepath.Base(in.File))
	fmt.Fprintf(&b, "- class/machine: %s / %s\n", f.Class, f.Machine)
	fmt.Fprintf(&b, "- type: %s, byteorder: %s\n", f.Type, f.ByteOrder)
	if libs, e := f.ImportedLibraries(); e == nil && len(libs) > 0 {
		fmt.Fprintf(&b, "- NEEDED libs (%d): %s\n", len(libs), strings.Join(libs, ", "))
	}
	if syms, e := f.ImportedSymbols(); e == nil {
		fmt.Fprintf(&b, "\n### imported symbols (%d)\n", len(syms))
		for i, s := range syms {
			if i >= 60 {
				fmt.Fprintf(&b, "… (+%d more)\n", len(syms)-60)
				break
			}
			fmt.Fprintf(&b, "%s %s\n", s.Library, s.Name)
		}
	}
	if dyn, e := f.DynamicSymbols(); e == nil {
		var exp []string
		for _, s := range dyn {
			if s.Section != elf.SHN_UNDEF && s.Name != "" {
				exp = append(exp, s.Name)
			}
		}
		fmt.Fprintf(&b, "\n### exported symbols: %d", len(exp))
		if len(exp) > 0 {
			sort.Strings(exp)
			if len(exp) > 60 {
				exp = exp[:60]
			}
			fmt.Fprintf(&b, "\n%s\n", strings.Join(exp, ", "))
		}
	}
	return b.String(), nil
}

// PeInfoTool parses a PE binary (Windows .exe/.dll) via debug/pe.
type PeInfoTool struct{}

func (t *PeInfoTool) Name() string { return "pe_info" }
func (t *PeInfoTool) Description() string {
	return "Parse a Windows PE binary (.exe/.dll): machine, sections, imported symbols. Native, no external tool."
}
func (t *PeInfoTool) Schema() map[string]any {
	return schema(map[string]any{"file": strProp("path to the PE file")}, "file")
}
func (t *PeInfoTool) Run(_ context.Context, input json.RawMessage, env *Env) (string, error) {
	var in struct {
		File string `json:"file"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", err
	}
	f, err := pe.Open(resolvePath(env, in.File))
	if err != nil {
		return "", err
	}
	defer f.Close()

	var b strings.Builder
	fmt.Fprintf(&b, "## PE %s\n- machine: 0x%x, sections: %d\n", filepath.Base(in.File), f.Machine, len(f.Sections))
	for _, s := range f.Sections {
		fmt.Fprintf(&b, "  %-8s vsize=%d rawsize=%d\n", s.Name, s.VirtualSize, s.Size)
	}
	if syms, e := f.ImportedSymbols(); e == nil {
		fmt.Fprintf(&b, "\n### imported symbols (%d)\n", len(syms))
		for i, s := range syms {
			if i >= 80 {
				fmt.Fprintf(&b, "… (+%d more)\n", len(syms)-80)
				break
			}
			b.WriteString(s + "\n")
		}
	}
	return b.String(), nil
}

// HexdumpTool dumps a file region as hex + ASCII.
type HexdumpTool struct{}

func (t *HexdumpTool) Name() string { return "hexdump" }
func (t *HexdumpTool) Description() string {
	return "Hex + ASCII dump of a file region. Useful for headers, magic bytes, embedded blobs."
}
func (t *HexdumpTool) Schema() map[string]any {
	return schema(map[string]any{
		"file":   strProp("path to the file"),
		"offset": map[string]any{"type": "integer", "description": "start offset (default 0)"},
		"length": map[string]any{"type": "integer", "description": "bytes to dump (default 256, max 4096)"},
	}, "file")
}
func (t *HexdumpTool) Run(_ context.Context, input json.RawMessage, env *Env) (string, error) {
	var in struct {
		File   string `json:"file"`
		Offset int64  `json:"offset"`
		Length int    `json:"length"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", err
	}
	if in.Length <= 0 {
		in.Length = 256
	}
	if in.Length > 4096 {
		in.Length = 4096
	}
	f, err := os.Open(resolvePath(env, in.File))
	if err != nil {
		return "", err
	}
	defer f.Close()
	buf := make([]byte, in.Length)
	n, _ := f.ReadAt(buf, in.Offset)
	buf = buf[:n]

	var b strings.Builder
	for i := 0; i < len(buf); i += 16 {
		end := i + 16
		if end > len(buf) {
			end = len(buf)
		}
		row := buf[i:end]
		fmt.Fprintf(&b, "%08x  ", in.Offset+int64(i))
		for j := 0; j < 16; j++ {
			if j < len(row) {
				fmt.Fprintf(&b, "%02x ", row[j])
			} else {
				b.WriteString("   ")
			}
		}
		b.WriteString(" |")
		for _, c := range row {
			if c >= 0x20 && c < 0x7f {
				b.WriteByte(c)
			} else {
				b.WriteByte('.')
			}
		}
		b.WriteString("|\n")
	}
	return b.String(), nil
}

// NativeLibsTool lists native .so libraries (with ABI + size) in an APK or an
// extracted directory.
type NativeLibsTool struct{}

func (t *NativeLibsTool) Name() string { return "native_libs" }
func (t *NativeLibsTool) Description() string {
	return "List native libraries (lib/<abi>/*.so with size) inside an APK, XAPK base, or an extracted directory. Reveals the JNI attack surface + supported ABIs."
}
func (t *NativeLibsTool) Schema() map[string]any {
	return schema(map[string]any{"path": strProp("path to an .apk or a directory")}, "path")
}
func (t *NativeLibsTool) Run(_ context.Context, input json.RawMessage, env *Env) (string, error) {
	var in struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", err
	}
	p := resolvePath(env, in.Path)
	byAbi := map[string][]string{}

	fi, err := os.Stat(p)
	if err != nil {
		return "", err
	}
	if !fi.IsDir() && strings.HasSuffix(strings.ToLower(p), ".apk") {
		r, e := zip.OpenReader(p)
		if e != nil {
			return "", e
		}
		defer r.Close()
		for _, f := range r.File {
			if strings.HasPrefix(f.Name, "lib/") && strings.HasSuffix(f.Name, ".so") {
				parts := strings.Split(f.Name, "/")
				if len(parts) >= 3 {
					byAbi[parts[1]] = append(byAbi[parts[1]], fmt.Sprintf("%s (%dKB)", parts[len(parts)-1], f.UncompressedSize64/1024))
				}
			}
		}
	} else {
		_ = filepath.Walk(p, func(fp string, info os.FileInfo, werr error) error {
			if werr != nil || info.IsDir() || !strings.HasSuffix(fp, ".so") {
				return nil
			}
			abi := filepath.Base(filepath.Dir(fp))
			byAbi[abi] = append(byAbi[abi], fmt.Sprintf("%s (%dKB)", filepath.Base(fp), info.Size()/1024))
			return nil
		})
	}
	if len(byAbi) == 0 {
		return "no native .so libraries found in " + p, nil
	}
	var b strings.Builder
	b.WriteString("## Native libraries\n")
	abis := make([]string, 0, len(byAbi))
	for a := range byAbi {
		abis = append(abis, a)
	}
	sort.Strings(abis)
	for _, a := range abis {
		libs := byAbi[a]
		sort.Strings(libs)
		fmt.Fprintf(&b, "\n### %s (%d)\n%s\n", a, len(libs), strings.Join(libs, "\n"))
	}
	return b.String(), nil
}
