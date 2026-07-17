package agent

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/imtaqin/jurig/internal/llm"
)

// Session is the persisted state of a run: the full model conversation plus
// the user's prompt history, so a later launch can resume where it left off.
type Session struct {
	History []llm.Message `json:"history"`
	Prompts []string      `json:"prompts"`
}

// Snapshot returns the current conversation for persistence.
func (a *Agent) Snapshot() []llm.Message { return a.history }

// Restore loads a prior conversation into the agent.
func (a *Agent) Restore(h []llm.Message) { a.history = h }

// Turns reports how many messages are in the conversation.
func (a *Agent) Turns() int { return len(a.history) }

// SaveSession writes the conversation + prompt history to path.
func (a *Agent) SaveSession(path string, prompts []string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(Session{History: a.history, Prompts: prompts}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// LoadSession reads a persisted session (returns zero values if absent).
func LoadSession(path string) (Session, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Session{}, false
	}
	var s Session
	if json.Unmarshal(b, &s) != nil {
		return Session{}, false
	}
	return s, len(s.History) > 0 || len(s.Prompts) > 0
}
