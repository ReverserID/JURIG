// Package agent implements Jurig's autonomous plan/act/observe loop.
package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/imtaqin/jurig/internal/config"
	"github.com/imtaqin/jurig/internal/llm"
	"github.com/imtaqin/jurig/internal/tools"
)

// Agent drives an autonomous reverse-engineering session.
type Agent struct {
	router   *llm.Router
	reg      *tools.Registry
	env      *tools.Env
	maxSteps int
	system   string
	history  []llm.Message
}

// New builds an agent bound to a target work dir.
func New(cfg *config.Config, router *llm.Router, reg *tools.Registry, env *tools.Env) *Agent {
	return &Agent{
		router:   router,
		reg:      reg,
		env:      env,
		maxSteps: cfg.MaxSteps,
		system:   systemPrompt(env.WorkDir, reg.Names()),
	}
}

// Reset clears conversation history (new target/session).
func (a *Agent) Reset() { a.history = nil }

// Run executes the loop for one task, streaming events to sink. It returns
// the final assistant text.
func (a *Agent) Run(ctx context.Context, task string, sink Sink) (string, error) {
	// Forward subprocess command lines from tools to the UI.
	a.env.Emit = func(kind, msg string) {
		if kind == "cmd" {
			sink.emit(Event{Kind: EvCmd, Text: msg})
		}
	}

	a.history = append(a.history, llm.Message{
		Role:    llm.RoleUser,
		Content: []llm.ContentBlock{llm.Text(task)},
	})

	defs := a.reg.Defs()
	final := ""

	for step := 1; step <= a.maxSteps; step++ {
		sink.emit(Event{Kind: EvStatus, Step: step, Text: fmt.Sprintf("step %d/%d · %s", step, a.maxSteps, a.router.ProviderName())})

		resp, err := a.router.Complete(ctx, llm.Request{
			Tier:     "act",
			System:   a.system,
			Messages: a.history,
			Tools:    defs,
		})
		if err != nil {
			sink.emit(Event{Kind: EvError, Text: err.Error()})
			return final, err
		}

		if resp.Usage.InputTokens+resp.Usage.OutputTokens > 0 {
			sink.emit(Event{Kind: EvUsage, In: resp.Usage.InputTokens, Out: resp.Usage.OutputTokens})
		}
		a.history = append(a.history, llm.Message{Role: llm.RoleAssistant, Content: resp.Content})

		if txt := resp.TextParts(); strings.TrimSpace(txt) != "" {
			final = txt
			sink.emit(Event{Kind: EvText, Text: txt, Step: step})
		}

		calls := resp.ToolCalls()
		if len(calls) == 0 {
			// No tools requested → model considers the task done.
			sink.emit(Event{Kind: EvDone, Text: final, Step: step})
			return final, nil
		}

		// Execute every requested tool, collect results as a user turn.
		results := make([]llm.ContentBlock, 0, len(calls))
		for _, c := range calls {
			sink.emit(Event{Kind: EvToolCall, Tool: c.Name, Text: string(c.Input), Step: step})
			out, rerr := a.reg.Dispatch(ctx, c.Name, c.Input, a.env)
			isErr := rerr != nil
			payload := out
			if isErr {
				payload = "ERROR: " + rerr.Error()
				if out != "" {
					payload += "\n" + out
				}
			}
			payload = clamp(payload, 24*1024)
			sink.emit(Event{Kind: EvToolResult, Tool: c.Name, Text: payload, Step: step})
			results = append(results, llm.ToolResult(c.ID, payload, isErr))
		}
		a.history = append(a.history, llm.Message{Role: llm.RoleUser, Content: results})
	}

	sink.emit(Event{Kind: EvError, Text: fmt.Sprintf("hit max steps (%d) without finishing", a.maxSteps)})
	return final, fmt.Errorf("max steps reached")
}

// clamp bounds a tool result so a single huge dump can't blow the context.
func clamp(s string, max int) string {
	if len(s) <= max {
		return s
	}
	head := s[:max*3/4]
	tail := s[len(s)-max/4:]
	return head + fmt.Sprintf("\n…[%d bytes elided]…\n", len(s)-max) + tail
}
