package agent

import (
	"context"
	"fmt"
	"sync"

	"github.com/macula-io/macula-lazymesh/internal/mcpclient"
)

// MultiSource composes several ToolSources into one, so Loop can stay
// unaware of how many tool sources actually exist. Phase 1 only ever
// needed macula-mcp; Phase 2 adds internal/localtools as a second,
// separately configurable source -- MultiSource is what lets main.go
// combine them without touching Loop at all.
type MultiSource struct {
	sources []ToolSource

	mu    sync.Mutex
	index map[string]ToolSource // built by the most recent ListTools call
}

// NewMultiSource combines the given sources, in order. A single source is
// a valid (if pointless) input -- callers with only macula-mcp can still
// use MultiSource uniformly, or just pass the source directly to NewLoop.
func NewMultiSource(sources ...ToolSource) *MultiSource {
	return &MultiSource{sources: sources}
}

// ListTools concatenates every source's tools. Two sources advertising the
// same tool name is a configuration error, not something to silently
// shadow -- it means whoever wired up MultiSource gave it overlapping
// sources, which would make CallToolRaw's routing ambiguous.
func (m *MultiSource) ListTools(ctx context.Context) ([]mcpclient.Tool, error) {
	var all []mcpclient.Tool
	index := make(map[string]ToolSource)
	for _, src := range m.sources {
		tools, err := src.ListTools(ctx)
		if err != nil {
			return nil, fmt.Errorf("list tools: %w", err)
		}
		for _, t := range tools {
			if _, exists := index[t.Name]; exists {
				return nil, fmt.Errorf("tool name collision: %q is advertised by more than one source", t.Name)
			}
			index[t.Name] = src
			all = append(all, t)
		}
	}
	m.mu.Lock()
	m.index = index
	m.mu.Unlock()
	return all, nil
}

// CallToolRaw routes to whichever source's most recent ListTools call
// advertised name. ListTools must have been called at least once first --
// Loop.Say always does this itself before ever calling CallToolRaw.
func (m *MultiSource) CallToolRaw(ctx context.Context, name string, argumentsJSON string) (string, error) {
	m.mu.Lock()
	src, ok := m.index[name]
	m.mu.Unlock()
	if !ok {
		return "", fmt.Errorf("no tool source currently advertises %q (call ListTools first)", name)
	}
	return src.CallToolRaw(ctx, name, argumentsJSON)
}
