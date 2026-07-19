package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestProxyCapturesFlow(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, "hello-jurig")
	}))
	defer target.Close()

	m := New(t.TempDir())
	addr, ca, err := m.Start(18899)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer m.Stop()
	if !m.Running() {
		t.Fatal("proxy not running")
	}
	if _, err := os.Stat(ca); err != nil {
		t.Fatalf("CA not exported: %v", err)
	}

	// wait for the listener to accept
	proxyURL, _ := url.Parse("http://" + strings.Replace(addr, "0.0.0.0", "127.0.0.1", 1))
	client := &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)},
		Timeout:   5 * time.Second,
	}
	var resp *http.Response
	for i := 0; i < 20; i++ {
		resp, err = client.Get(target.URL + "/api/login")
		if err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("request through proxy: %v", err)
	}
	io.ReadAll(resp.Body)
	resp.Body.Close()

	if m.Count() == 0 {
		t.Fatal("no flow captured")
	}
	txt := m.FlowsText(10)
	if !strings.Contains(txt, "/api/login") || !strings.Contains(txt, "200") {
		t.Fatalf("flow not recorded correctly: %s", txt)
	}
	if !strings.Contains(txt, "hello-jurig") {
		t.Fatalf("response body not captured: %s", txt)
	}
}
