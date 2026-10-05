package portable

import (
	"archive/zip"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// CatalogEntry describes a downloadable portable tool. URL is the default
// (OS-independent) archive; URLs overrides it per runtime.GOOS when a tool
// ships OS-specific binaries.
type CatalogEntry struct {
	Name string
	URL  string            // default zip/jar archive
	URLs map[string]string // per-GOOS override ("windows"/"linux"/"darwin")
	Note string
}

// resolveURL picks the archive URL for the current OS.
func (e CatalogEntry) resolveURL() string {
	if u, ok := e.URLs[runtime.GOOS]; ok && u != "" {
		return u
	}
	return e.URL
}

// Catalog holds known auto-installable tools.
var Catalog = map[string]CatalogEntry{
	"jadx": {
		Name: "jadx",
		// Pinned to 1.4.7: jadx 1.5.x has a plugin-loader NPE
		// (JadxPluginsTools.getEnabledPluginJars) on Java 21 / Windows.
		URL:  "https://github.com/skylot/jadx/releases/download/v1.4.7/jadx-1.4.7.zip",
		Note: "Android DEX/APK -> Java decompiler",
	},
	"apktool": {
		Name: "apktool",
		URL:  "https://github.com/iBotPeaches/Apktool/releases/download/v2.10.0/apktool_2.10.0.jar",
		Note: "APK resource+smali decoder (jar; wrapper script required)",
	},
	"radare2": {
		Name: "radare2",
		URLs: map[string]string{
			"windows": "https://github.com/radareorg/radare2/releases/download/6.0.0/radare2-6.0.0-w64.zip",
		},
		Note: "Native binary static analysis (ELF/PE/Mach-O). Windows auto-installs; on Linux/macOS install via package manager or `r2pm`.",
	},
	"ghidra": {
		Name: "ghidra",
		// Universal (Java) — one zip for every OS. Needs a JDK 21+ on PATH.
		URL:  "https://github.com/NationalSecurityAgency/ghidra/releases/download/Ghidra_11.3.2_build/ghidra_11.3.2_PUBLIC_20250415.zip",
		Note: "Ghidra headless (analyzeHeadless). ~450MB, needs JDK 21+.",
	},
	"objdump": {
		Name: "objdump",
		URLs: map[string]string{
			"windows": "https://github.com/mstorsjo/llvm-mingw/releases/download/20250114/llvm-mingw-20250114-ucrt-x86_64.zip",
			"linux":   "https://github.com/mstorsjo/llvm-mingw/releases/download/20250114/llvm-mingw-20250114-ucrt-ubuntu-20.04-x86_64.tar.xz",
		},
		Note: "GNU/LLVM objdump (disassembly + headers). Windows auto-installs llvm-objdump; on Linux/macOS use binutils.",
	},
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

// AutoInstallable reports whether a catalog tool can be fetched without
// manual steps (zip archive or runnable jar). Heavy/licensed tools return false.
func AutoInstallable(name string) bool {
	e, ok := Catalog[name]
	if !ok {
		return false
	}
	u := e.resolveURL()
	return strings.HasSuffix(u, ".zip") || strings.HasSuffix(u, ".jar")
}

// ResolveOrInstall resolves name, and if missing but auto-installable from
// the catalog, fetches it first, then resolves again. This is what makes
// the toolchain install itself on demand, driven by what the agent needs.
func (m *Manager) ResolveOrInstall(name string, progress func(string)) (string, error) {
	if p, err := m.Resolve(name); err == nil {
		return p, nil
	}
	if !AutoInstallable(name) {
		// Not auto-installable → surface the original resolve error + hint.
		_, err := m.Resolve(name)
		if e, ok := Catalog[name]; ok {
			return "", fmt.Errorf("%v (manual install: %s — %s)", err, e.resolveURL(), e.Note)
		}
		return "", err
	}
	if progress != nil {
		progress(fmt.Sprintf("tool %q missing → auto-installing from catalog", name))
	}
	if _, err := m.Install(name, progress); err != nil {
		return "", fmt.Errorf("auto-install %s: %w", name, err)
	}
	return m.Resolve(name)
}

// Install downloads and unpacks a catalog tool into toolsDir/<name>.
// Handles .zip archives and runnable .jar files (wrapped in a launcher).
func (m *Manager) Install(name string, progress func(string)) (string, error) {
	entry, ok := Catalog[name]
	if !ok {
		return "", fmt.Errorf("no catalog entry for %q", name)
	}
	url := entry.resolveURL()
	dest := filepath.Join(m.toolsDir, name)
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return "", err
	}
	log := func(s string) {
		if progress != nil {
			progress(s)
		}
	}

	switch {
	case strings.HasSuffix(url, ".zip"):
		log("downloading " + url + " …")
		tmp := filepath.Join(m.toolsDir, name+".zip")
		if err := download(url, tmp); err != nil {
			return "", err
		}
		defer os.Remove(tmp)
		log("unpacking …")
		if err := unzip(tmp, dest); err != nil {
			return "", err
		}

	case strings.HasSuffix(url, ".jar"):
		log("downloading " + url + " …")
		jar := filepath.Join(dest, name+".jar")
		if err := download(url, jar); err != nil {
			return "", err
		}
		if err := writeJarWrapper(dest, name, jar); err != nil {
			return "", err
		}

	default:
		return "", fmt.Errorf("%s must be installed manually: %s (%s)", name, url, entry.Note)
	}

	log("installed to " + dest)
	return dest, nil
}

// writeJarWrapper drops a launcher next to a jar so Resolve can exec it as
// `<name>` (java -jar). Requires java on PATH.
func writeJarWrapper(dir, name, jar string) error {
	base := filepath.Base(jar)
	if runtime.GOOS == "windows" {
		bat := "@echo off\r\njava -jar \"%~dp0" + base + "\" %*\r\n"
		return os.WriteFile(filepath.Join(dir, name+".bat"), []byte(bat), 0o755)
	}
	sh := "#!/bin/sh\nexec java -jar \"$(dirname \"$0\")/" + base + "\" \"$@\"\n"
	return os.WriteFile(filepath.Join(dir, name), []byte(sh), 0o755)
}

func download(url, dest string) error {
	hc := &http.Client{Timeout: 15 * time.Minute}
	resp, err := hc.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("download http %d", resp.StatusCode)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
}

func unzip(src, dest string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer r.Close()
	for _, f := range r.File {
		fp := filepath.Join(dest, f.Name)
		// zip-slip guard
		if !strings.HasPrefix(fp, filepath.Clean(dest)+string(os.PathSeparator)) {
			return fmt.Errorf("illegal path in zip: %s", f.Name)
		}
		if f.FileInfo().IsDir() {
			_ = os.MkdirAll(fp, 0o755)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(fp), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(fp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
		if err != nil {
			rc.Close()
			return err
		}
		_, err = io.Copy(out, rc)
		out.Close()
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}
