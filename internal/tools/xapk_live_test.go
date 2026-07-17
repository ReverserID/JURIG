package tools

import (
	"os"
	"path/filepath"
	"testing"
)

// TestXapkExtractLive runs against the real sample xapk if present, proving
// the native extract + base-apk detection works without any shell.
func TestXapkExtractLive(t *testing.T) {
	// locate the sample at repo root (../../)
	sample := ""
	root := filepath.Join("..", "..")
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".xapk" {
			sample = filepath.Join(root, e.Name())
			break
		}
	}
	if sample == "" {
		t.Skip("no sample .xapk at repo root")
	}
	dest := t.TempDir()
	n, err := extractZip(sample, dest)
	if err != nil {
		t.Fatalf("extractZip: %v", err)
	}
	if n == 0 {
		t.Fatal("extracted 0 entries")
	}
	base, err := findBaseAPK(dest)
	if err != nil {
		t.Fatalf("findBaseAPK: %v", err)
	}
	fi, _ := os.Stat(base)
	t.Logf("entries=%d base=%s size=%d", n, filepath.Base(base), fi.Size())
	if fi == nil || fi.Size() == 0 {
		t.Fatal("base apk empty")
	}
}
