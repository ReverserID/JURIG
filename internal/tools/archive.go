package tools

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// UnzipTool extracts a zip/apk/xapk/apks/jar archive natively (pure Go), so
// the agent never has to fight cmd/powershell/7z quoting on Windows.
type UnzipTool struct{}

func (t *UnzipTool) Name() string { return "unzip" }
func (t *UnzipTool) Description() string {
	return "Extract a ZIP-family archive (.zip/.apk/.xapk/.apks/.jar) to a directory. Native, cross-platform, no shell needed. Returns the output dir + file listing."
}
func (t *UnzipTool) Schema() map[string]any {
	return schema(map[string]any{
		"archive": strProp("path to the archive"),
		"out":     strProp("output dir (optional, default <workdir>/<archive-name>_extracted)"),
	}, "archive")
}
func (t *UnzipTool) Run(_ context.Context, input json.RawMessage, env *Env) (string, error) {
	var in struct {
		Archive string `json:"archive"`
		Out     string `json:"out"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", err
	}
	src := resolvePath(env, in.Archive)
	out := in.Out
	if out == "" {
		base := strings.TrimSuffix(filepath.Base(src), filepath.Ext(src))
		out = filepath.Join(env.WorkDir, base+"_extracted")
	} else {
		out = resolvePath(env, out)
	}
	env.emit("cmd", "unzip "+filepath.Base(src)+" -> "+out)
	n, err := extractZip(src, out)
	if err != nil {
		return "", err
	}
	listing := listTree(out, 40)
	return fmt.Sprintf("extracted %d entries to %s\n\n%s", n, out, listing), nil
}

// extractZip unpacks src into dest, returning the entry count. Guards zip-slip.
func extractZip(src, dest string) (int, error) {
	r, err := zip.OpenReader(src)
	if err != nil {
		return 0, err
	}
	defer r.Close()
	clean := filepath.Clean(dest)
	n := 0
	for _, f := range r.File {
		fp := filepath.Join(dest, f.Name)
		if !strings.HasPrefix(fp, clean+string(os.PathSeparator)) && fp != clean {
			return n, fmt.Errorf("illegal path in archive: %s", f.Name)
		}
		if f.FileInfo().IsDir() {
			_ = os.MkdirAll(fp, 0o755)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(fp), 0o755); err != nil {
			return n, err
		}
		rc, err := f.Open()
		if err != nil {
			return n, err
		}
		w, err := os.OpenFile(fp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
		if err != nil {
			rc.Close()
			return n, err
		}
		_, cerr := io.Copy(w, rc)
		w.Close()
		rc.Close()
		if cerr != nil {
			return n, cerr
		}
		n++
	}
	return n, nil
}

// findBaseAPK picks the primary APK inside an extracted xapk/apks bundle:
// prefer a file literally named base.apk, else the largest non-split apk.
func findBaseAPK(dir string) (string, error) {
	type cand struct {
		path string
		size int64
	}
	var apks []cand
	_ = filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return nil
		}
		if strings.EqualFold(filepath.Ext(p), ".apk") {
			apks = append(apks, cand{p, fi.Size()})
		}
		return nil
	})
	if len(apks) == 0 {
		return "", fmt.Errorf("no .apk found inside bundle %s", dir)
	}
	for _, c := range apks {
		if strings.EqualFold(filepath.Base(c.path), "base.apk") {
			return c.path, nil
		}
	}
	// heuristic: drop obvious splits/config apks, take the largest remainder
	sort.Slice(apks, func(i, j int) bool { return apks[i].size > apks[j].size })
	for _, c := range apks {
		name := strings.ToLower(filepath.Base(c.path))
		if !strings.Contains(name, "config.") && !strings.HasPrefix(name, "split") {
			return c.path, nil
		}
	}
	return apks[0].path, nil
}

// isBundle reports whether an archive is a multi-apk container (not a plain apk).
func isBundle(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".xapk", ".apks", ".zip":
		return true
	}
	return false
}

// listTree renders up to max relative paths under root, sorted.
func listTree(root string, max int) string {
	var paths []string
	_ = filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		paths = append(paths, rel)
		return nil
	})
	sort.Strings(paths)
	var b strings.Builder
	for i, p := range paths {
		if i >= max {
			fmt.Fprintf(&b, "… (+%d more)\n", len(paths)-max)
			break
		}
		b.WriteString(p + "\n")
	}
	return b.String()
}
