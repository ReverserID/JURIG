// Package config loads Jurig runtime configuration from JSON + environment.
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Kind is a provider wire protocol.
type Kind string

const (
	KindAnthropic Kind = "anthropic"  // native Messages API (Anthropic direct)
	KindOpenAI    Kind = "openai"     // OpenAI chat/completions (OpenRouter, Ollama, Kimi, Qwen…)
	KindClaudeCLI Kind = "claude-cli" // Claude Code subscription via `claude` binary
)

// ProviderCfg describes one backend and the models it exposes to the picker.
type ProviderCfg struct {
	Kind    Kind     `json:"kind"`
	BaseURL string   `json:"base_url"`
	APIKey  string   `json:"api_key"`
	Models  []string `json:"models"`
	// KeyEnv names the env var to pull the key from if APIKey is empty.
	KeyEnv string `json:"key_env,omitempty"`
}

// Selection is the currently active provider+model.
type Selection struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// Config is the whole app config.
type Config struct {
	Active    Selection              `json:"active"`
	Providers map[string]ProviderCfg `json:"providers"`

	ToolsDir string `json:"tools_dir"`
	WorkDir  string `json:"work_dir"`
	MaxSteps int    `json:"max_steps"`
}

// Default returns config with all known providers preset.
func Default() *Config {
	home, _ := os.UserHomeDir()
	root := filepath.Join(home, ".jurig")
	return &Config{
		Active: Selection{Provider: "openrouter", Model: "anthropic/claude-fable-5"},
		Providers: map[string]ProviderCfg{
			"anthropic": {
				Kind:    KindAnthropic,
				BaseURL: "https://api.anthropic.com",
				KeyEnv:  "ANTHROPIC_API_KEY",
				Models:  []string{"claude-fable-5", "claude-opus-4-8", "claude-sonnet-5", "claude-haiku-4-5"},
			},
			"openrouter": {
				Kind:    KindOpenAI,
				BaseURL: "https://openrouter.ai/api/v1",
				KeyEnv:  "OPENROUTER_API_KEY",
				Models: []string{
					"anthropic/claude-fable-5",
					"moonshotai/kimi-k2.5",
					"qwen/qwen3-max",
					"deepseek/deepseek-v3.2",
					"google/gemini-2.5-pro",
				},
			},
			"ollama": {
				Kind:    KindOpenAI,
				BaseURL: "http://localhost:11434/v1",
				APIKey:  "ollama",
				Models:  []string{"qwen2.5-coder:14b", "llama3.1:8b", "deepseek-r1:14b"},
			},
			"kimi": {
				// Kimi Code platform — keys start with sk-kimi-.
				Kind:    KindOpenAI,
				BaseURL: "https://api.kimi.com/coding/v1",
				KeyEnv:  "KIMI_API_KEY",
				Models:  []string{"kimi-k2.7-code", "kimi-k2.7-code-highspeed", "kimi-k2.6", "kimi-k2.5"},
			},
			"moonshot": {
				// Moonshot Open Platform — keys start with sk- (not sk-kimi-).
				Kind:    KindOpenAI,
				BaseURL: "https://api.moonshot.ai/v1",
				KeyEnv:  "MOONSHOT_API_KEY",
				Models:  []string{"kimi-k2.5", "moonshot-v1-128k", "moonshot-v1-32k"},
			},
			"dashscope": {
				Kind:    KindOpenAI,
				BaseURL: "https://dashscope-intl.aliyuncs.com/compatible-mode/v1",
				KeyEnv:  "DASHSCOPE_API_KEY",
				Models:  []string{"qwen3-max", "qwen3-coder-plus", "qwen-max", "qwen-plus"},
			},
			"cursor": {
				// Cursor subscription via a local Cursor->OpenAI bridge
				// (e.g. opencode-cursor). Run `jurig cursor login` for native
				// auth, start the bridge, then point base_url at it.
				Kind:    KindOpenAI,
				BaseURL: "http://127.0.0.1:8000/v1",
				APIKey:  "cursor",
				Models:  []string{"claude-4.5-sonnet", "gpt-5", "auto"},
			},
			"claude-cli": {
				Kind:   KindClaudeCLI,
				Models: []string{"default"},
			},
		},
		ToolsDir: filepath.Join(root, "tools"),
		WorkDir:  filepath.Join(root, "work"),
		MaxSteps: 60,
	}
}

// Load reads config from path (if present), then overlays environment.
func Load(path string) (*Config, error) {
	cfg := Default()
	if path != "" {
		if b, err := os.ReadFile(path); err == nil {
			if err := json.Unmarshal(b, cfg); err != nil {
				return nil, err
			}
		}
	}
	cfg.overlayEnv()
	for _, d := range []string{cfg.ToolsDir, cfg.WorkDir} {
		if d != "" {
			_ = os.MkdirAll(d, 0o755)
		}
	}
	return cfg, nil
}

// overlayEnv fills each provider's key from its KeyEnv, and honors a couple
// of global overrides.
func (c *Config) overlayEnv() {
	for name, p := range c.Providers {
		if p.APIKey == "" && p.KeyEnv != "" {
			if v := os.Getenv(p.KeyEnv); v != "" {
				p.APIKey = v
			}
		}
		c.Providers[name] = p
	}
	if v := os.Getenv("JURIG_PROVIDER"); v != "" {
		c.Active.Provider = v
	}
	if v := os.Getenv("JURIG_MODEL"); v != "" {
		c.Active.Model = v
	}
	if v := os.Getenv("JURIG_TOOLS_DIR"); v != "" {
		c.ToolsDir = v
	}
	// Let a running Cursor->OpenAI bridge advertise its port.
	if v := os.Getenv("CURSOR_BASE_URL"); v != "" {
		if p, ok := c.Providers["cursor"]; ok {
			p.BaseURL = v
			c.Providers["cursor"] = p
		}
	}
}

// DefaultPath is where Jurig looks for config.json.
func DefaultPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".jurig", "config.json")
}

// Exists reports whether a config file is present at path.
func Exists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir()
}

// Save writes the config as pretty JSON, creating parent dirs.
func (c *Config) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// ActiveReady reports whether the active provider can actually be called
// (has a key, or is a keyless local/subscription provider).
func (c *Config) ActiveReady() bool {
	p, ok := c.Providers[c.Active.Provider]
	if !ok {
		return false
	}
	if p.Kind == KindClaudeCLI {
		return true
	}
	return p.APIKey != ""
}
