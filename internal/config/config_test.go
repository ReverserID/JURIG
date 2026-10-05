package config

import (
	"path/filepath"
	"testing"
)

func TestDefaultProviders(t *testing.T) {
	c := Default()
	for _, name := range []string{"custom", "openrouter", "anthropic", "ollama", "claude-cli"} {
		if _, ok := c.Providers[name]; !ok {
			t.Errorf("default config missing provider %q", name)
		}
	}
	// No hardcoded router presets — those are entered via the custom provider.
	for _, name := range []string{"9router", "omniroute", "cursor"} {
		if _, ok := c.Providers[name]; ok {
			t.Errorf("default config should not ship provider %q", name)
		}
	}
	cu := c.Providers["custom"]
	if cu.BaseURL != "" || !cu.AutoModels {
		t.Errorf("custom provider = %+v, want empty base_url + auto_models", cu)
	}
}

func TestCustomLocalReadyWithoutKey(t *testing.T) {
	c := Default()
	p := c.Providers["custom"]
	p.BaseURL = "http://localhost:20128/v1"
	c.Providers["custom"] = p
	c.Active = Selection{Provider: "custom", Model: "cu/gpt-5"}
	if !c.ActiveReady() {
		t.Error("custom local gateway should be ready without a key")
	}
}

func TestActiveReady(t *testing.T) {
	c := Default()

	c.Active = Selection{Provider: "anthropic", Model: "x"}
	if c.ActiveReady() {
		t.Error("anthropic with no key should not be ready")
	}
	p := c.Providers["anthropic"]
	p.APIKey = "sk-test"
	c.Providers["anthropic"] = p
	if !c.ActiveReady() {
		t.Error("anthropic with key should be ready")
	}

	c.Active = Selection{Provider: "claude-cli", Model: "default"}
	if !c.ActiveReady() {
		t.Error("claude-cli (keyless subscription) should be ready")
	}

	c.Active = Selection{Provider: "nope"}
	if c.ActiveReady() {
		t.Error("unknown provider should not be ready")
	}
}

func TestCustomBaseURLEnvOverride(t *testing.T) {
	t.Setenv("CUSTOM_BASE_URL", "http://127.0.0.1:9999/v1")
	c, err := Load(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Providers["custom"].BaseURL; got != "http://127.0.0.1:9999/v1" {
		t.Errorf("CUSTOM_BASE_URL override = %q, want the env value", got)
	}
}

func TestKeyEnvOverlay(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "or-key")
	c, err := Load(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Providers["openrouter"].APIKey != "or-key" {
		t.Error("OPENROUTER_API_KEY not overlaid onto provider")
	}
}
