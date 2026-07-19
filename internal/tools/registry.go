package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/imtaqin/jurig/internal/llm"
)

// Registry holds the active tool set.
type Registry struct {
	byName map[string]Tool
	order  []string
}

// NewRegistry builds the default Jurig tool set.
func NewRegistry() *Registry {
	r := &Registry{byName: map[string]Tool{}}
	r.Add(
		&ShellTool{},
		&ReadFileTool{},
		&WriteFileTool{},
		&ListDirTool{},
		&StringsTool{},
		&SearchCodeTool{},
		&SecretScanTool{},
		&UrlExtractTool{},
		&ManifestTool{},
		&AskUserTool{},
		&UnzipTool{},
		&HexdumpTool{},
		&ElfInfoTool{},
		&PeInfoTool{},
		&NativeLibsTool{},
		&Radare2Tool{},
		&GhidraTool{},
		&JadxTool{},
		&ApktoolTool{},
		&AdbTool{},
		&FridaTool{},
		&FridaPresetTool{},
		&FridaPsTool{},
		&ProxyTool{},
		&HTTPRequestTool{},
		&DownloadTool{},
	)
	return r
}

// Add registers tools.
func (r *Registry) Add(ts ...Tool) {
	for _, t := range ts {
		if _, dup := r.byName[t.Name()]; !dup {
			r.order = append(r.order, t.Name())
		}
		r.byName[t.Name()] = t
	}
}

// Defs returns the tool definitions in llm wire format.
func (r *Registry) Defs() []llm.Tool {
	out := make([]llm.Tool, 0, len(r.order))
	for _, name := range r.order {
		t := r.byName[name]
		out = append(out, llm.Tool{
			Name:        t.Name(),
			Description: t.Description(),
			InputSchema: t.Schema(),
		})
	}
	return out
}

// Dispatch runs a named tool with raw JSON input.
func (r *Registry) Dispatch(ctx context.Context, name string, input json.RawMessage, env *Env) (string, error) {
	t, ok := r.byName[name]
	if !ok {
		return "", fmt.Errorf("unknown tool %q", name)
	}
	return t.Run(ctx, input, env)
}

// Names lists registered tool names.
func (r *Registry) Names() []string { return r.order }
