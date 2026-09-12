// Package counters holds lazymesh's process-wide runtime counters (G16):
// everything an operator would otherwise learn the hard way — how many
// turns ran, how many tool calls were made (per tool), how many errors
// and approvals happened. The event bridge records every loop event as it
// forwards it, the control plane's status query reports a snapshot, and
// agent.log carries a counters line at every turn boundary.
//
// This is deliberately counters, not a metrics library: the fix
// direction G16 names is structured counters in agent.log plus the
// socket query, with Prometheus export as an explicit later step — a
// dependency-free registry gets the observability value today.
package counters

import (
	"fmt"
	"sort"
	"sync"

	"github.com/macula-io/macula-lazymesh/internal/agent"
)

// Snapshot is one read of the registry, shaped for the control plane's
// status query and the agent.log line.
type Snapshot struct {
	Turns       uint64            `json:"turns"`
	ToolCalls   uint64            `json:"tool_calls"`
	ToolResults uint64            `json:"tool_results"`
	Errors      uint64            `json:"errors"`
	Backoffs    uint64            `json:"backoffs"`
	Approvals   uint64            `json:"approvals"`
	ByTool      map[string]uint64 `json:"by_tool,omitempty"`
}

// Registry accumulates counters. Record is called from the event bridge
// (one goroutine), Snapshot from the control plane's query handler
// (others) — hence the mutex.
type Registry struct {
	mu          sync.Mutex
	turns       uint64
	toolCalls   uint64
	toolResults uint64
	errors      uint64
	backoffs    uint64
	approvals   uint64
	byTool      map[string]uint64
}

// New returns an empty registry.
func New() *Registry {
	return &Registry{byTool: make(map[string]uint64)}
}

// Record folds one loop event into the counters. Turns are counted at
// the listening marker (the turn boundary), tool activity per kind.
func (r *Registry) Record(ev agent.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch ev.Kind {
	case agent.EventAssistantMessage:
		// Counted at EventListening instead — the turn boundary — so a
		// multi-round turn counts once.
	case agent.EventListening:
		r.turns++
	case agent.EventToolCall:
		r.toolCalls++
		r.byTool[ev.ToolName]++
	case agent.EventToolResult:
		r.toolResults++
	case agent.EventError:
		r.errors++
	case agent.EventBackoff:
		r.backoffs++
	case agent.EventApprovalRequested:
		r.approvals++
		// An approval is about a specific tool — worth seeing WHICH
		// tool keeps asking, in the same byTool view as the calls.
		r.byTool[ev.ToolName]++
	}
}

// Snapshot returns a copy of the current counts.
func (r *Registry) Snapshot() Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	byTool := make(map[string]uint64, len(r.byTool))
	for name, count := range r.byTool {
		byTool[name] = count
	}
	return Snapshot{
		Turns:       r.turns,
		ToolCalls:   r.toolCalls,
		ToolResults: r.toolResults,
		Errors:      r.errors,
		Backoffs:    r.backoffs,
		Approvals:   r.approvals,
		ByTool:      byTool,
	}
}

// Line renders the snapshot as the one-line structured counters record
// agent.log gets at every turn boundary.
func (s Snapshot) Line() string {
	top := make([]string, 0, len(s.ByTool))
	for name := range s.ByTool {
		top = append(top, name)
	}
	sort.Strings(top)
	line := fmt.Sprintf("[counters] turns=%d tool_calls=%d tool_results=%d errors=%d backoffs=%d approvals=%d",
		s.Turns, s.ToolCalls, s.ToolResults, s.Errors, s.Backoffs, s.Approvals)
	for _, name := range top {
		line += fmt.Sprintf(" %s=%d", name, s.ByTool[name])
	}
	return line + ")"
}
