package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// messagesClient POSTs Anthropic Messages requests. Both the Anthropic
// direct API and OpenRouter's Anthropic skin accept this exact shape;
// only base URL + auth headers differ.
type messagesClient struct {
	name    string
	baseURL string
	headers map[string]string
	hc      *http.Client
}

func newMessagesClient(name, baseURL string, headers map[string]string) *messagesClient {
	return &messagesClient{
		name:    name,
		baseURL: baseURL,
		headers: headers,
		hc:      &http.Client{Timeout: 5 * time.Minute},
	}
}

func (c *messagesClient) Name() string { return c.name }

func (c *messagesClient) Complete(ctx context.Context, req Request) (*Response, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	url := c.baseURL + "/v1/messages"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	for k, v := range c.headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := c.hc.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("%s request: %w", c.name, err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s http %d: %s", c.name, resp.StatusCode, truncate(string(raw), 500))
	}
	var out Response
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("%s decode: %w (%s)", c.name, err, truncate(string(raw), 300))
	}
	return &out, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
