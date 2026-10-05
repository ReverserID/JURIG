package llm

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/imtaqin/jurig/internal/config"
)

// Provider is a single LLM backend.
type Provider interface {
	Name() string
	Complete(ctx context.Context, req Request) (*Response, error)
}

// ModelLister is an optional Provider capability: fetching the model catalog
// from the backend (OpenAI-compatible GET /models).
type ModelLister interface {
	ListModels(ctx context.Context) ([]string, error)
}

// MaxOutputProvider is an optional Provider capability: the detected max output
// token budget for a model (from the endpoint's /models metadata). 0 = unknown.
type MaxOutputProvider interface {
	MaxOutput(model string) int
}

// Choice is one selectable provider+model for the picker.
type Choice struct {
	Provider string
	Model    string
	Ready    bool // credentials present (or local)
}

func (c Choice) Label() string {
	mark := "  "
	if !c.Ready {
		mark = "× "
	}
	return fmt.Sprintf("%s%s / %s", mark, c.Provider, c.Model)
}

// Router holds every configured provider and the active selection. It is
// safe for concurrent use so the TUI can switch models mid-session.
type Router struct {
	cfg       *config.Config
	providers map[string]Provider
	ready     map[string]bool
	mu        sync.RWMutex
	active    config.Selection
}

// NewRouter builds providers from config and validates the active selection.
func NewRouter(cfg *config.Config) (*Router, error) {
	r := &Router{
		cfg:       cfg,
		providers: map[string]Provider{},
		ready:     map[string]bool{},
		active:    cfg.Active,
	}
	for name, pc := range cfg.Providers {
		p, ready, err := build(name, pc)
		if err != nil {
			return nil, err
		}
		r.providers[name] = p
		r.ready[name] = ready
	}
	if _, ok := r.providers[r.active.Provider]; !ok {
		return nil, fmt.Errorf("active provider %q not configured", r.active.Provider)
	}
	return r, nil
}

func build(name string, pc config.ProviderCfg) (Provider, bool, error) {
	switch pc.Kind {
	case config.KindAnthropic:
		return NewAnthropic(pc.BaseURL, pc.APIKey), pc.APIKey != "", nil
	case config.KindOpenAI:
		// Ollama is local and needs no real key; treat any key (incl. the
		// literal "ollama") as ready. A local gateway (localhost/127.0.0.1) is
		// also usable keyless — ready as soon as it has a base URL.
		ready := pc.APIKey != "" || (pc.BaseURL != "" && isLocalURL(pc.BaseURL))
		return NewOpenAI(name, pc.BaseURL, pc.APIKey), ready, nil
	case config.KindClaudeCLI:
		return NewClaudeCLI("claude", ""), true, nil
	default:
		return nil, false, fmt.Errorf("provider %q: unknown kind %q", name, pc.Kind)
	}
}

// isLocalURL reports whether a base URL points at the local machine.
func isLocalURL(u string) bool {
	return strings.Contains(u, "localhost") ||
		strings.Contains(u, "127.0.0.1") ||
		strings.Contains(u, "0.0.0.0") ||
		strings.Contains(u, "[::1]")
}

// Complete dispatches to the active provider with the active model.
func (r *Router) Complete(ctx context.Context, req Request) (*Response, error) {
	r.mu.RLock()
	sel := r.active
	p := r.providers[sel.Provider]
	ready := r.ready[sel.Provider]
	r.mu.RUnlock()

	if p == nil {
		return nil, fmt.Errorf("no active provider")
	}
	if !ready {
		return nil, fmt.Errorf("provider %q has no API key set", sel.Provider)
	}
	if req.Model == "" {
		req.Model = sel.Model
	}
	if req.MaxTokens == 0 {
		req.MaxTokens = 8192
		// Auto-detected per-model output cap (from the endpoint's /models) wins.
		if mo, ok := p.(MaxOutputProvider); ok {
			if n := mo.MaxOutput(req.Model); n > 0 {
				req.MaxTokens = n
			}
		}
	}
	return p.Complete(ctx, req)
}

// SetSelection switches the active provider+model.
func (r *Router) SetSelection(provider, model string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.providers[provider]; !ok {
		return fmt.Errorf("unknown provider %q", provider)
	}
	r.active = config.Selection{Provider: provider, Model: model}
	return nil
}

// Active returns the current selection.
func (r *Router) Active() config.Selection {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.active
}

// ProviderName reports the active provider name.
func (r *Router) ProviderName() string { return r.Active().Provider }

// ActiveLabel is a compact "provider/model" for the status bar.
func (r *Router) ActiveLabel() string {
	s := r.Active()
	return s.Provider + "/" + s.Model
}

// Catalog lists every provider+model for the picker, ready ones first.
func (r *Router) Catalog() []Choice {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Choice
	names := make([]string, 0, len(r.cfg.Providers))
	for n := range r.cfg.Providers {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		pc := r.cfg.Providers[n]
		for _, m := range pc.Models {
			out = append(out, Choice{Provider: n, Model: m, Ready: r.ready[n]})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Ready != out[j].Ready {
			return out[i].Ready // ready first
		}
		return false
	})
	return out
}

// CanListModels reports whether a provider supports fetching its catalog.
func (r *Router) CanListModels(name string) bool {
	r.mu.RLock()
	p := r.providers[name]
	ready := r.ready[name]
	r.mu.RUnlock()
	_, ok := p.(ModelLister)
	return ok && ready
}

// FetchModels pulls the live model list from a provider and merges it into the
// config (union with any presets, sorted). Returns the merged list.
func (r *Router) FetchModels(ctx context.Context, name string) ([]string, error) {
	r.mu.RLock()
	p := r.providers[name]
	r.mu.RUnlock()
	lister, ok := p.(ModelLister)
	if !ok {
		return nil, fmt.Errorf("provider %q cannot list models", name)
	}
	ids, err := lister.ListModels(ctx)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	pc := r.cfg.Providers[name]
	seen := map[string]bool{}
	merged := make([]string, 0, len(pc.Models)+len(ids))
	for _, m := range append(append([]string{}, pc.Models...), ids...) {
		if m != "" && !seen[m] {
			seen[m] = true
			merged = append(merged, m)
		}
	}
	sort.Strings(merged)
	pc.Models = merged
	r.cfg.Providers[name] = pc
	return merged, nil
}

// AutoModelProviders lists configured providers marked auto_models that can
// currently list their catalog (ready + OpenAI-compatible).
func (r *Router) AutoModelProviders() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []string
	for name, pc := range r.cfg.Providers {
		if !pc.AutoModels {
			continue
		}
		if _, ok := r.providers[name].(ModelLister); ok && r.ready[name] {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// ReplaceProviders swaps in a rebuilt router's providers + readiness without
// copying the mutex. Used when an API key is set from the TUI at runtime.
func (r *Router) ReplaceProviders(new *Router) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers = new.providers
	r.ready = new.ready
	r.cfg = new.cfg
}
