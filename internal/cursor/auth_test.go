package cursor

import (
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"testing"
)

func TestPKCE(t *testing.T) {
	v, c, err := pkce()
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(v))
	want := base64.RawURLEncoding.EncodeToString(sum[:])
	if c != want {
		t.Fatalf("challenge mismatch")
	}
	if strings.ContainsAny(v, "+/=") {
		t.Fatal("verifier not base64url")
	}
}
func TestAuthParams(t *testing.T) {
	p, err := GenerateAuthParams()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(p.LoginURL, "https://cursor.com/loginDeepControl?") {
		t.Fatal("bad login url")
	}
	if !strings.Contains(p.LoginURL, "redirectTarget=cli") {
		t.Fatal("missing redirectTarget")
	}
}
func TestExpiry(t *testing.T) {
	// exp far future JWT: header.payload.sig
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"exp":9999999999}`))
	e := tokenExpiry("h." + payload + ".s")
	if e < nowMs() {
		t.Fatal("expiry should be future")
	}
}
