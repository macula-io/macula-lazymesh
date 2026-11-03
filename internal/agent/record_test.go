package agent

import (
	"context"
	"testing"

	"github.com/macula-io/macula-lazymesh/internal/mcpclient"
	"github.com/macula-io/macula-lazymesh/internal/provider"
)

// recordingTools checks, at the moment a tool runs, what the loop has
// already recorded: the call must be on record before it can have any
// effect, so a crash during it cannot leave it unknown (#99).
type recordingTools struct {
	recorded *[]provider.Message
	seen     []int // how many messages were recorded when each call ran
}

func (r *recordingTools) ListTools(context.Context) ([]mcpclient.Tool, error) { return nil, nil }

func (r *recordingTools) CallToolRaw(context.Context, string, string) (string, error) {
	r.seen = append(r.seen, len(*r.recorded))
	return "sent", nil
}

// #99: every message is recorded as it is added, not after the turn: the
// user's message first, the assistant's tool call before the tool runs,
// the tool's result after it, then the reply.
func TestLoop_RecordsEachMessageAsItHappens(t *testing.T) {
	var recorded []provider.Message
	tools := &recordingTools{recorded: &recorded}
	p := &scriptedProvider{responses: []provider.ChatResponse{
		{Message: provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "c1", Name: "mesh_say", Arguments: `{"text":"hi"}`}}}},
		{Message: provider.Message{Role: provider.RoleAssistant, Content: "done"}},
	}}
	loop := NewLoop(p, tools, "system prompt")
	loop.OnRecord(func(m provider.Message) { recorded = append(recorded, m) })

	if err := loop.Say(context.Background(), "tell the room", nil); err != nil {
		t.Fatalf("Say: %v", err)
	}
	if len(tools.seen) != 1 || tools.seen[0] != 2 {
		t.Fatalf("when mesh_say ran, %v messages were on record; want 2 (the prompt and the call)", tools.seen)
	}
	want := []provider.Role{provider.RoleUser, provider.RoleAssistant, provider.RoleTool, provider.RoleAssistant}
	if len(recorded) != len(want) {
		t.Fatalf("recorded %d messages, want %d", len(recorded), len(want))
	}
	for i, role := range want {
		if recorded[i].Role != role {
			t.Fatalf("message %d is %s, want %s", i, recorded[i].Role, role)
		}
	}
}
