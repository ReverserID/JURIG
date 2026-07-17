// Package llm implements the Anthropic Messages protocol and a provider
// router so the agent can speak one wire format to Anthropic-direct,
// OpenRouter's Anthropic skin, or the `claude` subscription CLI.
package llm

import "encoding/json"

// Role of a message.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// BlockType discriminates content blocks.
type BlockType string

const (
	BlockText       BlockType = "text"
	BlockToolUse    BlockType = "tool_use"
	BlockToolResult BlockType = "tool_result"
)

// ContentBlock is one piece of a message. Fields are populated by Type.
type ContentBlock struct {
	Type BlockType `json:"type"`

	// text
	Text string `json:"text,omitempty"`

	// tool_use
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`

	// tool_result
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`
}

// Text builds a text block.
func Text(s string) ContentBlock { return ContentBlock{Type: BlockText, Text: s} }

// ToolResult builds a tool_result block referencing a prior tool_use id.
func ToolResult(useID, out string, isErr bool) ContentBlock {
	return ContentBlock{Type: BlockToolResult, ToolUseID: useID, Content: out, IsError: isErr}
}

// Message is a turn in the conversation.
type Message struct {
	Role    Role           `json:"role"`
	Content []ContentBlock `json:"content"`
}

// Tool is a function the model may call. InputSchema is JSON Schema.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

// Request is a completion request in Messages format.
type Request struct {
	Model     string    `json:"model"`
	System    string    `json:"system,omitempty"`
	Messages  []Message `json:"messages"`
	Tools     []Tool    `json:"tools,omitempty"`
	MaxTokens int       `json:"max_tokens"`
	// Tier is a logical hint ("plan"/"act"/"cheap"); the router maps it
	// to Model when Model is empty.
	Tier string `json:"-"`
}

// Response is the model's reply.
type Response struct {
	ID         string         `json:"id"`
	Role       Role           `json:"role"`
	Content    []ContentBlock `json:"content"`
	StopReason string         `json:"stop_reason"`
	Model      string         `json:"model"`
	Usage      Usage          `json:"usage"`
}

// Usage reports token counts.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// ToolCalls returns all tool_use blocks in the response.
func (r *Response) ToolCalls() []ContentBlock {
	var out []ContentBlock
	for _, b := range r.Content {
		if b.Type == BlockToolUse {
			out = append(out, b)
		}
	}
	return out
}

// TextParts concatenates text blocks.
func (r *Response) TextParts() string {
	s := ""
	for _, b := range r.Content {
		if b.Type == BlockText {
			s += b.Text
		}
	}
	return s
}
