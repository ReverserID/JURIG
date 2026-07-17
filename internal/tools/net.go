package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// HTTPRequestTool makes an HTTP call — for testing/replaying API endpoints
// discovered during analysis.
type HTTPRequestTool struct{}

func (t *HTTPRequestTool) Name() string { return "http_request" }
func (t *HTTPRequestTool) Description() string {
	return "Make an HTTP request to test/replay an API endpoint found during analysis. Returns status, response headers, and body (truncated). Use to verify endpoints, probe auth, or reproduce app traffic."
}
func (t *HTTPRequestTool) Schema() map[string]any {
	return schema(map[string]any{
		"url":     strProp("full URL"),
		"method":  strProp("HTTP method (default GET)"),
		"headers": map[string]any{"type": "object", "description": "request headers", "additionalProperties": map[string]any{"type": "string"}},
		"body":    strProp("request body (optional)"),
		"timeout": map[string]any{"type": "integer", "description": "timeout seconds (default 30)"},
	}, "url")
}
func (t *HTTPRequestTool) Run(ctx context.Context, input json.RawMessage, env *Env) (string, error) {
	var in struct {
		URL     string            `json:"url"`
		Method  string            `json:"method"`
		Headers map[string]string `json:"headers"`
		Body    string            `json:"body"`
		Timeout int               `json:"timeout"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", err
	}
	if in.Method == "" {
		in.Method = "GET"
	}
	if in.Timeout == 0 {
		in.Timeout = 30
	}
	cctx, cancel := context.WithTimeout(ctx, time.Duration(in.Timeout)*time.Second)
	defer cancel()

	var body io.Reader
	if in.Body != "" {
		body = strings.NewReader(in.Body)
	}
	req, err := http.NewRequestWithContext(cctx, strings.ToUpper(in.Method), in.URL, body)
	if err != nil {
		return "", err
	}
	for k, v := range in.Headers {
		req.Header.Set(k, v)
	}
	env.emit("cmd", in.Method+" "+in.URL)
	resp, err := (&http.Client{Timeout: time.Duration(in.Timeout) * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 32*1024))

	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", resp.Status)
	for k, v := range resp.Header {
		fmt.Fprintf(&b, "%s: %s\n", k, strings.Join(v, ", "))
	}
	b.WriteString("\n")
	b.Write(raw)
	if resp.ContentLength > 32*1024 {
		b.WriteString("\n…[body truncated]")
	}
	return b.String(), nil
}

// DownloadTool fetches a URL to the work dir.
type DownloadTool struct{}

func (t *DownloadTool) Name() string { return "download" }
func (t *DownloadTool) Description() string {
	return "Download a URL to a file in the work dir (e.g. pull an APK, config, or asset). Returns the saved path + size."
}
func (t *DownloadTool) Schema() map[string]any {
	return schema(map[string]any{
		"url": strProp("URL to download"),
		"out": strProp("output filename (optional)"),
	}, "url")
}
func (t *DownloadTool) Run(ctx context.Context, input json.RawMessage, env *Env) (string, error) {
	var in struct {
		URL string `json:"url"`
		Out string `json:"out"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", err
	}
	name := in.Out
	if name == "" {
		name = filepath.Base(in.URL)
		if name == "" || name == "/" || name == "." {
			name = "download.bin"
		}
	}
	dest := resolvePath(env, name)
	env.emit("cmd", "download "+in.URL)
	resp, err := (&http.Client{Timeout: 15 * time.Minute}).Get(in.URL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("http %d", resp.StatusCode)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", err
	}
	f, err := os.Create(dest)
	if err != nil {
		return "", err
	}
	defer f.Close()
	n, err := io.Copy(f, resp.Body)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("saved %s (%d bytes)", dest, n), nil
}

// FridaPsTool lists processes/apps on a USB device via frida-ps.
type FridaPsTool struct{}

func (t *FridaPsTool) Name() string { return "frida_ps" }
func (t *FridaPsTool) Description() string {
	return "List apps/processes on the connected USB device via frida-ps (needs frida-server running). Use to find the exact package/process name to hook."
}
func (t *FridaPsTool) Schema() map[string]any {
	return schema(map[string]any{
		"apps": boolProp("list installed applications (-ai) instead of running processes"),
	})
}
func (t *FridaPsTool) Run(ctx context.Context, input json.RawMessage, env *Env) (string, error) {
	var in struct {
		Apps bool `json:"apps"`
	}
	_ = json.Unmarshal(input, &in)
	bin, err := env.ResolveBin("frida-ps")
	if err != nil {
		return "", err
	}
	args := []string{"-U"}
	if in.Apps {
		args = append(args, "-ai")
	}
	return runCmd(ctx, env, 60*time.Second, env.WorkDir, bin, args...)
}
