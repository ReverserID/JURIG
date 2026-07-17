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

// CatalogEntry describes a downloadable portable tool.
type CatalogEntry struct {
	Name string
	URL  string // zip archive
	Note string
}

// Catalog holds known auto-installable tools. Heavy tools (ghidra) are
// listed for reference but installed by the user due to size/licensing.
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
	"ghidra": {
		Name: "ghidra",
		URL:  "https://github.com/NationalSecurityAgency/ghidra/releases",
		Note: "Install manually (large). Point tools_dir/ghidra at the release, analyzeHeadless is auto-detected.",
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
	return strings.HasSuffix(e.URL, ".zip") || strings.HasSuffix(e.URL, ".jar")
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
			return "", fmt.Errorf("%v (manual install: %s — %s)", err, e.URL, e.Note)
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
	case strings.HasSuffix(entry.URL, ".zip"):
		log("downloading " + entry.URL + " …")
		tmp := filepath.Join(m.toolsDir, name+".zip")
		if err := download(entry.URL, tmp); err != nil {
			return "", err
		}
		defer os.Remove(tmp)
		log("unpacking …")
		if err := unzip(tmp, dest); err != nil {
			return "", err
		}

	case strings.HasSuffix(entry.URL, ".jar"):
		log("downloading " + entry.URL + " …")
		jar := filepath.Join(dest, name+".jar")
		if err := download(entry.URL, jar); err != nil {
			return "", err
		}
		if err := writeJarWrapper(dest, name, jar); err != nil {
			return "", err
		}

	default:
		return "", fmt.Errorf("%s must be installed manually: %s (%s)", name, entry.URL, entry.Note)
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
