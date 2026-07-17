package llm

import (
	"encoding/json"
	"testing"
)

func TestToOAITranslatesToolFlow(t *testing.T) {
	req := Request{
		Model:  "kimi-k2.5",
		System: "you are jurig",
		Tools: []Tool{{
			Name:        "radare2",
			Description: "static",
			InputSchema: map[string]any{"type": "object"},
		}},
		Messages: []Message{
			{Role: RoleUser, Content: []ContentBlock{Text("analyze app")}},
			{Role: RoleAssistant, Content: []ContentBlock{
				Text("running r2"),
				{Type: BlockToolUse, ID: "call_1", Name: "radare2", Input: json.RawMessage(`{"file":"a"}`)},
			}},
			{Role: RoleUser, Content: []ContentBlock{ToolResult("call_1", "0x1000 main", false)}},
		},
	}
	o := toOAI(req)

	// system + user + assistant(w/ tool_call) + tool = 4 messages
	if len(o.Messages) != 4 {
		t.Fatalf("want 4 msgs, got %d: %+v", len(o.Messages), o.Messages)
	}
	if o.Messages[0].Role != "system" {
		t.Fatalf("first msg not system: %s", o.Messages[0].Role)
	}
	asst := o.Messages[2]
	if asst.Role != "assistant" || len(asst.ToolCalls) != 1 || asst.ToolCalls[0].Function.Name != "radare2" {
		t.Fatalf("assistant tool_call missing: %+v", asst)
	}
	toolMsg := o.Messages[3]
	if toolMsg.Role != "tool" || toolMsg.ToolCallID != "call_1" || toolMsg.Content != "0x1000 main" {
		t.Fatalf("tool result mistranslated: %+v", toolMsg)
	}
	if len(o.Tools) != 1 || o.Tools[0].Function.Name != "radare2" {
		t.Fatalf("tools missing: %+v", o.Tools)
	}
}

func TestFromOAIParsesToolCalls(t *testing.T) {
	var resp oaiResponse
	raw := `{"choices":[{"message":{"role":"assistant","content":"ok","tool_calls":[{"id":"c1","type":"function","function":{"name":"jadx","arguments":"{\"apk\":\"x\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatal(err)
	}
	r := fromOAI("m", &resp)
	if r.StopReason != "tool_use" {
		t.Fatalf("stop reason: %s", r.StopReason)
	}
	calls := r.ToolCalls()
	if len(calls) != 1 || calls[0].Name != "jadx" {
		t.Fatalf("tool call parse: %+v", r.Content)
	}
	if r.Usage.InputTokens != 10 {
		t.Fatalf("usage: %+v", r.Usage)
	}
}
