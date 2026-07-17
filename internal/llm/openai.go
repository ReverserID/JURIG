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

// openAIProvider speaks the OpenAI Chat Completions protocol, which
// Ollama, OpenRouter, Moonshot (Kimi), and DashScope (Qwen) all implement.
// It translates Jurig's internal Anthropic-Messages format to/from the
// OpenAI shape, including tool calls.
type openAIProvider struct {
	name    string
	baseURL string
	apiKey  string
	hc      *http.Client
}

// NewOpenAI builds an OpenAI-compatible provider.
func NewOpenAI(name, baseURL, apiKey string) Provider {
	return &openAIProvider{
		name:    name,
		baseURL: baseURL,
		apiKey:  apiKey,
		hc:      &http.Client{Timeout: 5 * time.Minute},
	}
}

func (p *openAIProvider) Name() string { return p.name }

// ---- wire types (OpenAI) ----

type oaiTool struct {
	Type     string      `json:"type"`
	Function oaiToolFunc `json:"function"`
}
type oaiToolFunc struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}
type oaiToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}
type oaiMessage struct {
	Role       string        `json:"role"`
	Content    string        `json:"content,omitempty"`
	ToolCalls  []oaiToolCall `json:"tool_calls,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
}
type oaiRequest struct {
	Model     string       `json:"model"`
	Messages  []oaiMessage `json:"messages"`
	Tools     []oaiTool    `json:"tools,omitempty"`
	MaxTokens int          `json:"max_tokens,omitempty"`
}
type oaiResponse struct {
	Choices []struct {
		Message      oaiMessage `json:"message"`
		FinishReason string     `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (p *openAIProvider) Complete(ctx context.Context, req Request) (*Response, error) {
	body, err := json.Marshal(toOAI(req))
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	// OpenRouter attribution headers (harmless elsewhere).
	httpReq.Header.Set("HTTP-Referer", "https://github.com/imtaqin/jurig")
	httpReq.Header.Set("X-Title", "Jurig RE Agent")

	resp, err := p.hc.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("%s request: %w", p.name, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s http %d: %s", p.name, resp.StatusCode, truncate(string(raw), 500))
	}
	var out oaiResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("%s decode: %w (%s)", p.name, err, truncate(string(raw), 300))
	}
	if out.Error != nil {
		return nil, fmt.Errorf("%s: %s", p.name, out.Error.Message)
	}
	return fromOAI(req.Model, &out), nil
}

// toOAI translates a Messages request into OpenAI chat format.
func toOAI(req Request) oaiRequest {
	msgs := make([]oaiMessage, 0, len(req.Messages)+1)
	if req.System != "" {
		msgs = append(msgs, oaiMessage{Role: "system", Content: req.System})
	}
	for _, m := range req.Messages {
		switch m.Role {
		case RoleAssistant:
			om := oaiMessage{Role: "assistant"}
			for _, b := range m.Content {
				switch b.Type {
				case BlockText:
					om.Content += b.Text
				case BlockToolUse:
					tc := oaiToolCall{ID: b.ID, Type: "function"}
					tc.Function.Name = b.Name
					tc.Function.Arguments = string(b.Input)
					if tc.Function.Arguments == "" {
						tc.Function.Arguments = "{}"
					}
					om.ToolCalls = append(om.ToolCalls, tc)
				}
			}
			msgs = append(msgs, om)
		default: // user
			// tool_result blocks become separate "tool" messages; text stays user.
			var userText string
			for _, b := range m.Content {
				switch b.Type {
				case BlockText:
					userText += b.Text
				case BlockToolResult:
					msgs = append(msgs, oaiMessage{
						Role:       "tool",
						ToolCallID: b.ToolUseID,
						Content:    b.Content,
					})
				}
			}
			if userText != "" {
				msgs = append(msgs, oaiMessage{Role: "user", Content: userText})
			}
		}
	}

	var tools []oaiTool
	for _, t := range req.Tools {
		tools = append(tools, oaiTool{
			Type: "function",
			Function: oaiToolFunc{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.InputSchema,
			},
		})
	}
	return oaiRequest{Model: req.Model, Messages: msgs, Tools: tools, MaxTokens: req.MaxTokens}
}

// fromOAI translates an OpenAI response back into a Messages Response.
func fromOAI(model string, out *oaiResponse) *Response {
	r := &Response{
		Role:  RoleAssistant,
		Model: model,
		Usage: Usage{InputTokens: out.Usage.PromptTokens, OutputTokens: out.Usage.CompletionTokens},
	}
	if len(out.Choices) == 0 {
		return r
	}
	ch := out.Choices[0]
	if ch.Message.Content != "" {
		r.Content = append(r.Content, Text(ch.Message.Content))
	}
	for _, tc := range ch.Message.ToolCalls {
		args := tc.Function.Arguments
		if args == "" {
			args = "{}"
		}
		r.Content = append(r.Content, ContentBlock{
			Type:  BlockToolUse,
			ID:    tc.ID,
			Name:  tc.Function.Name,
			Input: json.RawMessage(args),
		})
	}
	// Normalize stop reason to Anthropic vocabulary the agent loop expects.
	switch ch.FinishReason {
	case "tool_calls":
		r.StopReason = "tool_use"
	case "length":
		r.StopReason = "max_tokens"
	default:
		r.StopReason = "end_turn"
	}
	return r
}
