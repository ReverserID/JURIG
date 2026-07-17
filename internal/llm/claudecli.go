package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// claudeCLI drives the Claude Code subscription by shelling out to the
// `claude` binary in headless print mode. Anthropic blocks subscription
// auth in third-party HTTP harnesses (April 2026), so the supported way
// to use a subscription is to run *inside* Claude Code — that is exactly
// what this provider does.
//
// Limitation: the CLI print mode returns assistant text, not structured
// tool_use blocks, so native tool-calling is unavailable on this path.
// Use it for advisory / chat runs; use anthropic|openrouter for the full
// autonomous tool loop.
type claudeCLI struct {
	binary string
	model  string
}

// NewClaudeCLI builds the subscription provider.
func NewClaudeCLI(binary, model string) Provider {
	if binary == "" {
		binary = "claude"
	}
	return &claudeCLI{binary: binary, model: model}
}

func (c *claudeCLI) Name() string { return "claude-cli" }

func (c *claudeCLI) Complete(ctx context.Context, req Request) (*Response, error) {
	prompt := flattenForCLI(req)

	args := []string{"-p", "--output-format", "json"}
	if c.model != "" {
		args = append(args, "--model", c.model)
	}
	cmd := exec.CommandContext(ctx, c.binary, args...)
	cmd.Stdin = strings.NewReader(prompt)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("claude cli: %w", err)
	}

	// `claude -p --output-format json` emits {"result": "...", ...}.
	var parsed struct {
		Result string `json:"result"`
	}
	text := string(out)
	if json.Unmarshal(out, &parsed) == nil && parsed.Result != "" {
		text = parsed.Result
	}
	return &Response{
		Role:       RoleAssistant,
		Content:    []ContentBlock{Text(text)},
		StopReason: "end_turn",
		Model:      "claude-cli",
	}, nil
}

// flattenForCLI collapses a Messages request into a single text prompt,
// since the CLI has no structured message input.
func flattenForCLI(req Request) string {
	var b strings.Builder
	if req.System != "" {
		b.WriteString(req.System)
		b.WriteString("\n\n")
	}
	for _, m := range req.Messages {
		b.WriteString(strings.ToUpper(string(m.Role)))
		b.WriteString(": ")
		for _, blk := range m.Content {
			switch blk.Type {
			case BlockText:
				b.WriteString(blk.Text)
			case BlockToolResult:
				b.WriteString("[tool result] ")
				b.WriteString(blk.Content)
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}
