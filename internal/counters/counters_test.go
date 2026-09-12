package counters

import (
	"strings"
	"testing"

	"github.com/macula-io/macula-lazymesh/internal/agent"
)

// TestRecordCountsTurnBoundariesAndTools pins the counting contract:
// assistant messages count toward the turn only at the listening
// marker, tool activity counts per kind and per tool, and a snapshot is
// a copy (later records don't mutate it).
func TestRecordCountsTurnBoundariesAndTools(t *testing.T) {
	r := New()
	events := []agent.Event{
		{Kind: agent.EventAssistantDelta, Text: "a"},
		{Kind: agent.EventToolCall, ToolName: "mesh_say"},
		{Kind: agent.EventToolResult, ToolName: "mesh_say"},
		{Kind: agent.EventAssistantMessage, Text: "done"},
		{Kind: agent.EventToolCall, ToolName: "mesh_rooms"},
		{Kind: agent.EventToolResult, ToolName: "mesh_rooms"},
		{Kind: agent.EventError, Err: errBoom},
		{Kind: agent.EventApprovalRequested, ToolName: "shell_exec"},
		{Kind: agent.EventBackoff},
		{Kind: agent.EventListening},
	}
	for _, ev := range events {
		r.Record(ev)
	}

	s := r.Snapshot()
	if s.Turns != 1 {
		t.Fatalf("turns = %d, want 1", s.Turns)
	}
	if s.ToolCalls != 2 || s.ToolResults != 2 || s.Errors != 1 || s.Approvals != 1 || s.Backoffs != 1 {
		t.Fatalf("snapshot = %+v", s)
	}
	if s.ByTool["mesh_say"] != 1 || s.ByTool["mesh_rooms"] != 1 || s.ByTool["shell_exec"] != 1 {
		t.Fatalf("byTool = %v", s.ByTool)
	}

	// Snapshot is a copy: mutating the registry afterwards must not
	// change what was already read.
	r.Record(agent.Event{Kind: agent.EventToolCall, ToolName: "mesh_say"})
	if s.ByTool["mesh_say"] != 1 {
		t.Fatalf("snapshot mutated after Record: %v", s.ByTool)
	}
}

// TestLineRendersSortedCounters pins the agent.log line's shape.
func TestLineRendersSortedCounters(t *testing.T) {
	r := New()
	r.Record(agent.Event{Kind: agent.EventListening})
	r.Record(agent.Event{Kind: agent.EventToolCall, ToolName: "zeta"})
	r.Record(agent.Event{Kind: agent.EventToolCall, ToolName: "alpha"})
	line := r.Snapshot().Line()
	for _, want := range []string{"[counters]", "turns=1", "tool_calls=2", "alpha=1", "zeta=1"} {
		if !strings.Contains(line, want) {
			t.Fatalf("line %q missing %q", line, want)
		}
	}
	if strings.Index(line, "alpha=1") > strings.Index(line, "zeta=1") {
		t.Fatalf("tools not sorted in %q", line)
	}
}

var errBoom = &boomError{}

type boomError struct{}

func (b *boomError) Error() string { return "boom" }
