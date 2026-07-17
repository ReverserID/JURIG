package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestStringsAndRadare2 exercises the real subprocess path end-to-end.
func TestStringsAndRadare2(t *testing.T) {
	reg := NewRegistry()
	if len(reg.Defs()) < 8 {
		t.Fatalf("expected full tool set, got %d", len(reg.Defs()))
	}

	env := &Env{
		WorkDir: t.TempDir(),
		ResolveBin: func(name string) (string, error) {
			// only radare2 is expected present in CI env
			return name, nil
		},
	}

	ctx := context.Background()

	// write_file then strings, round-tripping through the registry.
	wf, _ := json.Marshal(map[string]any{"path": "sample.bin", "content": "MODULE_secret_key=abcd\x00\x01garbage"})
	if _, err := reg.Dispatch(ctx, "write_file", wf, env); err != nil {
		t.Fatalf("write_file: %v", err)
	}
	in, _ := json.Marshal(map[string]any{"path": "sample.bin", "min": 4})
	out, err := reg.Dispatch(ctx, "strings", in, env)
	if err != nil {
		t.Fatalf("strings dispatch: %v", err)
	}
	if !strings.Contains(out, "MODULE_secret_key") {
		t.Fatalf("strings output missing marker: %q", out)
	}
}

// TestSchemasHaveArrayRequired guards against `"required": null`, which some
// providers (Kimi/Moonshot) reject.
func TestSchemasHaveArrayRequired(t *testing.T) {
	for _, d := range NewRegistry().Defs() {
		r, ok := d.InputSchema["required"]
		if !ok {
			t.Fatalf("%s: schema missing 'required'", d.Name)
		}
		if _, isArr := r.([]string); !isArr {
			t.Fatalf("%s: 'required' must be []string, got %T", d.Name, r)
		}
	}
}
