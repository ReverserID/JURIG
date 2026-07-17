package tools

import (
	"strings"
	"testing"
)

func TestFridaScripts(t *testing.T) {
	cases := []struct {
		preset, arg, want string
	}{
		{"ssl_unpin", "", "CertificatePinner"},
		{"list_classes", "uangme", `"uangme"`},
		{"dump_class", "com.x.Y", `"com.x.Y"`},
		{"hook", "com.x.Y!doSign", `"doSign"`},
		{"trace_http", "", "okhttp"},
	}
	for _, c := range cases {
		js, err := fridaScript(c.preset, c.arg)
		if err != nil {
			t.Fatalf("%s: %v", c.preset, err)
		}
		if !strings.Contains(js, "Java.perform") {
			t.Fatalf("%s: missing Java.perform", c.preset)
		}
		if c.want != "" && !strings.Contains(js, c.want) {
			t.Fatalf("%s: want %q in script", c.preset, c.want)
		}
	}
	// bad inputs
	if _, err := fridaScript("hook", "noBang"); err == nil {
		t.Fatal("hook without ! should error")
	}
	if _, err := fridaScript("nope", ""); err == nil {
		t.Fatal("unknown preset should error")
	}
}
