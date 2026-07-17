package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestReconLive runs the recon tools against a real decompiled app if present
// (skips in CI). Verifies manifest/url_extract/secret_scan produce real output.
func TestReconLive(t *testing.T) {
	work := `C:\Users\Administrator\.jurig\work\uangme`
	if _, err := os.Stat(filepath.Join(work, "jadx", "sources")); err != nil {
		t.Skip("no decompiled uangme sample")
	}
	env := &Env{WorkDir: work, ResolveBin: func(n string) (string, error) { return n, nil }}
	ctx := context.Background()
	run := func(name string, in map[string]any) string {
		raw, _ := json.Marshal(in)
		reg := NewRegistry()
		out, err := reg.Dispatch(ctx, name, raw, env)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return out
	}

	if out := run("manifest", map[string]any{}); !strings.Contains(out, "com.cmcm.uangme") {
		t.Fatalf("manifest missing package: %.200s", out)
	}
	if out := run("url_extract", map[string]any{}); !strings.Contains(out, "uangme") {
		t.Fatalf("url_extract missing uangme host: %.200s", out)
	}
	if out := run("secret_scan", map[string]any{}); !strings.Contains(strings.ToLower(out), "secret") {
		t.Fatalf("secret_scan produced no report: %.200s", out)
	}
	t.Log("recon tools OK on live sample")
}
