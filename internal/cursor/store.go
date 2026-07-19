package cursor

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func nowMs() int64 { return time.Now().UnixMilli() }

func jsonBody(s string) io.Reader { return strings.NewReader(s) }

func splitDots(s string) []string { return strings.Split(s, ".") }

// randomUUID returns an RFC-4122 v4 UUID string.
func randomUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// DefaultStorePath is where Cursor credentials live.
func DefaultStorePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".jurig", "cursor-auth.json")
}

// Save writes credentials to path (0600).
func Save(path string, c Credentials) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

// Load reads credentials from path.
func Load(path string) (Credentials, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Credentials{}, err
	}
	var c Credentials
	if err := json.Unmarshal(b, &c); err != nil {
		return Credentials{}, err
	}
	return c, nil
}

// ValidToken loads stored creds, refreshes if expired, persists any refresh,
// and returns a usable access token. This is what the (future) chat client
// calls before every request.
func ValidToken(path string) (string, error) {
	c, err := Load(path)
	if err != nil {
		return "", fmt.Errorf("not logged in to Cursor (run `jurig cursor login`): %w", err)
	}
	if c.Access != "" && !c.Expired() {
		return c.Access, nil
	}
	if c.Refresh == "" {
		return "", fmt.Errorf("cursor token expired and no refresh token; run `jurig cursor login`")
	}
	fresh, err := Refresh(c.Refresh)
	if err != nil {
		return "", err
	}
	_ = Save(path, fresh)
	return fresh.Access, nil
}

// LoggedIn reports whether a credential file exists.
func LoggedIn(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
