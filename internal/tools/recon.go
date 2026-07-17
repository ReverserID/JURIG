package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// secretPatterns are high-signal credential regexes scanned by secret_scan.
var secretPatterns = []struct {
	name string
	re   *regexp.Regexp
}{
	{"aws_access_key", regexp.MustCompile(`AKIA[0-9A-Z]{16}`)},
	{"google_api_key", regexp.MustCompile(`AIza[0-9A-Za-z_\-]{35}`)},
	{"firebase_url", regexp.MustCompile(`https://[a-z0-9\-]+\.firebaseio\.com`)},
	{"jwt", regexp.MustCompile(`eyJ[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{6,}`)},
	{"private_key", regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`)},
	{"slack_token", regexp.MustCompile(`xox[baprs]-[0-9A-Za-z\-]{10,}`)},
	{"stripe_key", regexp.MustCompile(`sk_live_[0-9A-Za-z]{16,}`)},
	{"github_token", regexp.MustCompile(`gh[pousr]_[0-9A-Za-z]{30,}`)},
	{"generic_secret", regexp.MustCompile(`(?i)(api[_-]?key|secret|passwd|password|token)\s*[:=]\s*["'][^"']{6,}["']`)},
	{"bearer", regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9_\-\.=]{16,}`)},
	{"aes_key_literal", regexp.MustCompile(`SecretKeySpec\(\s*"[^"]{8,}"`)},
}

// SecretScanTool scans decompiled sources for hardcoded credentials.
type SecretScanTool struct{}

func (t *SecretScanTool) Name() string { return "secret_scan" }
func (t *SecretScanTool) Description() string {
	return "Scan a directory (default jadx sources) for hardcoded secrets: AWS/Google/Stripe/GitHub/Slack keys, JWTs, private keys, bearer tokens, api_key/password literals, AES key literals. Returns categorized file:line hits."
}
func (t *SecretScanTool) Schema() map[string]any {
	return schema(map[string]any{
		"dir": strProp("directory to scan (default <workdir>/jadx/sources)"),
		"max": map[string]any{"type": "integer", "description": "max hits per category (default 15)"},
	})
}
func (t *SecretScanTool) Run(_ context.Context, input json.RawMessage, env *Env) (string, error) {
	var in struct {
		Dir string `json:"dir"`
		Max int    `json:"max"`
	}
	_ = json.Unmarshal(input, &in)
	dir := in.Dir
	if dir == "" {
		dir = filepath.Join(env.WorkDir, "jadx", "sources")
	} else {
		dir = resolvePath(env, dir)
	}
	if in.Max <= 0 {
		in.Max = 15
	}

	hits := map[string][]string{}
	_ = filepath.Walk(dir, func(p string, fi os.FileInfo, werr error) error {
		if werr != nil || fi.IsDir() {
			return nil
		}
		if !isTextExt(p) {
			return nil
		}
		f, e := os.Open(p)
		if e != nil {
			return nil
		}
		defer f.Close()
		rel, _ := filepath.Rel(env.WorkDir, p)
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		ln := 0
		for sc.Scan() {
			ln++
			line := sc.Text()
			for _, pat := range secretPatterns {
				if len(hits[pat.name]) >= in.Max {
					continue
				}
				if m := pat.re.FindString(line); m != "" {
					if len(m) > 80 {
						m = m[:80] + "…"
					}
					hits[pat.name] = append(hits[pat.name], fmt.Sprintf("%s:%d: %s", filepath.ToSlash(rel), ln, m))
				}
			}
		}
		return nil
	})

	if len(hits) == 0 {
		return "no secrets matched under " + dir, nil
	}
	var b strings.Builder
	b.WriteString("## Secret scan\n")
	names := make([]string, 0, len(hits))
	for n := range hits {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintf(&b, "\n### %s (%d)\n%s\n", n, len(hits[n]), strings.Join(hits[n], "\n"))
	}
	return b.String(), nil
}

// UrlExtractTool pulls URLs/endpoints from sources and groups them by host.
type UrlExtractTool struct{}

func (t *UrlExtractTool) Name() string { return "url_extract" }
func (t *UrlExtractTool) Description() string {
	return "Extract all http(s) URLs from a directory (default jadx sources), dedupe, and group by host — the app's network/API map. Great first step for endpoint discovery."
}
func (t *UrlExtractTool) Schema() map[string]any {
	return schema(map[string]any{
		"dir": strProp("directory to scan (default <workdir>/jadx/sources)"),
	})
}

var urlRe = regexp.MustCompile(`https?://[a-zA-Z0-9\.\-]+(?::\d+)?(?:/[^\s"'<>\\)]*)?`)

func (t *UrlExtractTool) Run(_ context.Context, input json.RawMessage, env *Env) (string, error) {
	var in struct {
		Dir string `json:"dir"`
	}
	_ = json.Unmarshal(input, &in)
	dir := in.Dir
	if dir == "" {
		dir = filepath.Join(env.WorkDir, "jadx", "sources")
	} else {
		dir = resolvePath(env, dir)
	}

	byHost := map[string]map[string]bool{}
	_ = filepath.Walk(dir, func(p string, fi os.FileInfo, werr error) error {
		if werr != nil || fi.IsDir() || !isTextExt(p) {
			return nil
		}
		b, e := os.ReadFile(p)
		if e != nil {
			return nil
		}
		for _, u := range urlRe.FindAllString(string(b), -1) {
			host := u
			if i := strings.Index(u[8:], "/"); i >= 0 {
				host = u[:8+i]
			}
			host = strings.TrimPrefix(strings.TrimPrefix(host, "https://"), "http://")
			if strings.HasPrefix(host, "schemas.android.com") || strings.HasPrefix(host, "www.w3.org") {
				continue // framework noise
			}
			if byHost[host] == nil {
				byHost[host] = map[string]bool{}
			}
			if len(byHost[host]) < 40 {
				byHost[host][u] = true
			}
		}
		return nil
	})

	if len(byHost) == 0 {
		return "no URLs found under " + dir, nil
	}
	hosts := make([]string, 0, len(byHost))
	for h := range byHost {
		hosts = append(hosts, h)
	}
	sort.Strings(hosts)
	var b strings.Builder
	fmt.Fprintf(&b, "## URL map (%d hosts)\n", len(hosts))
	for _, h := range hosts {
		urls := make([]string, 0, len(byHost[h]))
		for u := range byHost[h] {
			urls = append(urls, u)
		}
		sort.Strings(urls)
		fmt.Fprintf(&b, "\n### %s (%d)\n%s\n", h, len(urls), strings.Join(urls, "\n"))
	}
	return b.String(), nil
}

// ManifestTool summarizes a decoded AndroidManifest.xml (package, permissions,
// exported components).
type ManifestTool struct{}

func (t *ManifestTool) Name() string { return "manifest" }
func (t *ManifestTool) Description() string {
	return "Summarize the decoded AndroidManifest.xml (auto-located under jadx/resources or apktool output): package, version, permissions, and EXPORTED components (attack surface)."
}
func (t *ManifestTool) Schema() map[string]any {
	return schema(map[string]any{
		"path": strProp("optional explicit AndroidManifest.xml path"),
	})
}
func (t *ManifestTool) Run(_ context.Context, input json.RawMessage, env *Env) (string, error) {
	var in struct {
		Path string `json:"path"`
	}
	_ = json.Unmarshal(input, &in)

	path := in.Path
	if path != "" {
		path = resolvePath(env, path)
	} else {
		for _, c := range []string{
			filepath.Join(env.WorkDir, "jadx", "resources", "AndroidManifest.xml"),
			filepath.Join(env.WorkDir, "apktool", "AndroidManifest.xml"),
		} {
			if _, e := os.Stat(c); e == nil {
				path = c
				break
			}
		}
	}
	if path == "" {
		return "", fmt.Errorf("AndroidManifest.xml not found; decompile first (jadx/apktool)")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}

	type intentFilter struct{}
	type comp struct {
		Name     string         `xml:"http://schemas.android.com/apk/res/android name,attr"`
		Exported string         `xml:"http://schemas.android.com/apk/res/android exported,attr"`
		Filters  []intentFilter `xml:"intent-filter"`
	}
	var mf struct {
		Package     string `xml:"package,attr"`
		VersionName string `xml:"http://schemas.android.com/apk/res/android versionName,attr"`
		Uses        []struct {
			Name string `xml:"http://schemas.android.com/apk/res/android name,attr"`
		} `xml:"uses-permission"`
		App struct {
			Activities []comp `xml:"activity"`
			Services   []comp `xml:"service"`
			Receivers  []comp `xml:"receiver"`
			Providers  []comp `xml:"provider"`
		} `xml:"application"`
	}
	if err := xml.Unmarshal(data, &mf); err != nil {
		return "", fmt.Errorf("parse manifest: %w", err)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "## Manifest\n- package: `%s`  version: %s\n", mf.Package, mf.VersionName)
	fmt.Fprintf(&b, "\n### permissions (%d)\n", len(mf.Uses))
	for _, u := range mf.Uses {
		b.WriteString("- " + strings.TrimPrefix(u.Name, "android.permission.") + "\n")
	}
	exported := func(title string, cs []comp) {
		var ex []string
		for _, c := range cs {
			if c.Exported == "true" || (c.Exported == "" && len(c.Filters) > 0) {
				ex = append(ex, c.Name)
			}
		}
		if len(ex) > 0 {
			fmt.Fprintf(&b, "\n### exported %s (%d)\n%s\n", title, len(ex), strings.Join(ex, "\n"))
		}
	}
	exported("activities", mf.App.Activities)
	exported("services", mf.App.Services)
	exported("receivers", mf.App.Receivers)
	exported("providers", mf.App.Providers)
	return b.String(), nil
}

func isTextExt(p string) bool {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".java", ".kt", ".smali", ".xml", ".json", ".txt", ".properties", ".gradle", ".js", ".html", ".cfg", ".yml", ".yaml":
		return true
	}
	return false
}
