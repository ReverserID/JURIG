package llm

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/imtaqin/jurig/internal/config"
)

// Provider is a single LLM backend.
type Provider interface {
	Name() string
	Complete(ctx context.Context, req Request) (*Response, error)
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
		// literal "ollama") as ready.
		return NewOpenAI(name, pc.BaseURL, pc.APIKey), pc.APIKey != "", nil
	case config.KindClaudeCLI:
		return NewClaudeCLI("claude", ""), true, nil
	default:
		return nil, false, fmt.Errorf("provider %q: unknown kind %q", name, pc.Kind)
	}
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
