// Package proxy is a native Go MITM HTTP(S) proxy (goproxy) used for dynamic
// network capture. It records flows into a ring buffer the TUI renders live
// and the agent can query.
package proxy

import (
	"bytes"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/elazarl/goproxy"
)

// Flow is one captured request/response pair (bounded preview).
type Flow struct {
	ID          int
	Method      string
	Host        string
	Path        string
	Status      int
	ReqCT       string
	RespCT      string
	ReqPreview  string
	RespPreview string
}

// Manager owns the MITM proxy lifecycle + captured flows.
type Manager struct {
	caDir string

	mu      sync.RWMutex
	flows   []Flow
	nextID  int
	running bool
	addr    string
	caPath  string
	srv     *http.Server
}

const (
	maxFlows   = 300
	maxPreview = 2048
)

// New builds a Manager; CA + captures are written under caDir.
func New(caDir string) *Manager { return &Manager{caDir: caDir} }

// Running reports whether the proxy is live.
func (m *Manager) Running() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.running
}

// Addr returns the listen address (empty if stopped).
func (m *Manager) Addr() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.addr
}

// CAPath returns the exported CA cert path.
func (m *Manager) CAPath() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.caPath
}

// Count returns the number of captured flows.
func (m *Manager) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.flows)
}

// Recent returns up to n most-recent flows (newest last).
func (m *Manager) Recent(n int) []Flow {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if n <= 0 || n > len(m.flows) {
		n = len(m.flows)
	}
	out := make([]Flow, n)
	copy(out, m.flows[len(m.flows)-n:])
	return out
}

// Start launches the MITM proxy on the given port (0 → 8888). Returns the
// listen address and the exported CA path (install on the device to decrypt
// TLS). Idempotent while running.
func (m *Manager) Start(port int) (addr, caPath string, err error) {
	m.mu.Lock()
	if m.running {
		a, c := m.addr, m.caPath
		m.mu.Unlock()
		return a, c, nil
	}
	m.mu.Unlock()

	if port == 0 {
		port = 8888
	}
	caPath, err = m.exportCA()
	if err != nil {
		return "", "", fmt.Errorf("export CA: %w", err)
	}

	px := goproxy.NewProxyHttpServer()
	px.OnRequest().HandleConnect(goproxy.AlwaysMitm)
	px.OnResponse().DoFunc(func(resp *http.Response, ctx *goproxy.ProxyCtx) *http.Response {
		m.record(ctx.Req, resp)
		return resp
	})

	listenAddr := fmt.Sprintf("0.0.0.0:%d", port)
	srv := &http.Server{Addr: listenAddr, Handler: px}

	m.mu.Lock()
	m.running = true
	m.addr = listenAddr
	m.caPath = caPath
	m.srv = srv
	m.mu.Unlock()

	go func() {
		if e := srv.ListenAndServe(); e != nil && e != http.ErrServerClosed {
			m.mu.Lock()
			m.running = false
			m.mu.Unlock()
		}
	}()
	return listenAddr, caPath, nil
}

// Stop shuts the proxy down (flows are retained).
func (m *Manager) Stop() error {
	m.mu.Lock()
	srv := m.srv
	m.running = false
	m.addr = ""
	m.srv = nil
	m.mu.Unlock()
	if srv != nil {
		return srv.Close()
	}
	return nil
}

// Clear drops captured flows.
func (m *Manager) Clear() {
	m.mu.Lock()
	m.flows = nil
	m.mu.Unlock()
}

func (m *Manager) record(req *http.Request, resp *http.Response) {
	f := Flow{Method: req.Method}
	if req.URL != nil {
		f.Host = req.URL.Host
		f.Path = req.URL.Path
		if req.URL.RawQuery != "" {
			f.Path += "?" + req.URL.RawQuery
		}
	}
	if f.Host == "" {
		f.Host = req.Host
	}
	f.ReqCT = req.Header.Get("Content-Type")
	if req.Body != nil {
		f.ReqPreview, req.Body = teePreview(req.Body)
	}
	if resp != nil {
		f.Status = resp.StatusCode
		f.RespCT = resp.Header.Get("Content-Type")
		if resp.Body != nil {
			f.RespPreview, resp.Body = teePreview(resp.Body)
		}
	}

	m.mu.Lock()
	m.nextID++
	f.ID = m.nextID
	m.flows = append(m.flows, f)
	if len(m.flows) > maxFlows {
		m.flows = m.flows[len(m.flows)-maxFlows:]
	}
	m.mu.Unlock()
}

// teePreview reads a bounded preview from a body and returns a fresh body that
// still yields the full content downstream.
func teePreview(body io.ReadCloser) (string, io.ReadCloser) {
	defer body.Close()
	full, err := io.ReadAll(io.LimitReader(body, 1<<20)) // cap 1MB
	if err != nil {
		return "", io.NopCloser(bytes.NewReader(nil))
	}
	prev := full
	if len(prev) > maxPreview {
		prev = prev[:maxPreview]
	}
	return string(prev), io.NopCloser(bytes.NewReader(full))
}

// FlowsText renders the n most-recent flows as text for the agent.
func (m *Manager) FlowsText(n int) string {
	flows := m.Recent(n)
	if len(flows) == 0 {
		return "no flows captured yet (is the device pointed at the proxy + CA installed + traffic generated?)"
	}
	var b bytes.Buffer
	for _, f := range flows {
		fmt.Fprintf(&b, "#%d %s %s%s -> %d %s\n", f.ID, f.Method, f.Host, f.Path, f.Status, f.RespCT)
		if f.ReqPreview != "" {
			fmt.Fprintf(&b, "   req: %s\n", clip(f.ReqPreview, 400))
		}
		if f.RespPreview != "" {
			fmt.Fprintf(&b, "   resp: %s\n", clip(f.RespPreview, 600))
		}
	}
	return b.String()
}

func clip(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// exportCA writes goproxy's CA certificate as PEM for device installation.
func (m *Manager) exportCA() (string, error) {
	if err := os.MkdirAll(m.caDir, 0o755); err != nil {
		return "", err
	}
	p := filepath.Join(m.caDir, "jurig-ca.pem")
	if _, err := os.Stat(p); err == nil {
		return p, nil
	}
	der := goproxy.GoproxyCa.Certificate[0]
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(p, pemBytes, 0o644); err != nil {
		return "", err
	}
	return p, nil
}
