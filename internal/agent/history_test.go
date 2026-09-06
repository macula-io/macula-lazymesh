package agent

import (
	"testing"

	"github.com/macula-io/macula-lazymesh/internal/provider"
)

func TestTrimHistory_NoOpUnderLimit(t *testing.T) {
	l := &Loop{messages: []provider.Message{
		{Role: provider.RoleSystem, Content: "sys"},
		{Role: provider.RoleUser, Content: "hi"},
		{Role: provider.RoleAssistant, Content: "hello"},
	}}
	l.trimHistory()
	if len(l.messages) != 3 {
		t.Fatalf("expected no trimming under the limit, got %d messages", len(l.messages))
	}
}

func TestTrimHistory_KeepsSystemMessageAndDropsOldestTurns(t *testing.T) {
	l := &Loop{}
	l.messages = append(l.messages, provider.Message{Role: provider.RoleSystem, Content: "sys"})
	// Build maxHistoryMessages+20 messages as many 2-message "turns"
	// (user, assistant) so trimming has clearly identifiable boundaries.
	for i := 0; i < (maxHistoryMessages+20)/2; i++ {
		l.messages = append(l.messages,
			provider.Message{Role: provider.RoleUser, Content: "turn"},
			provider.Message{Role: provider.RoleAssistant, Content: "reply"},
		)
	}
	before := len(l.messages)

	l.trimHistory()

	if len(l.messages) >= before {
		t.Fatalf("expected trimming to reduce message count, was %d, now %d", before, len(l.messages))
	}
	if len(l.messages) > maxHistoryMessages {
		t.Fatalf("expected at most %d messages after trimming, got %d", maxHistoryMessages, len(l.messages))
	}
	if l.messages[0].Role != provider.RoleSystem || l.messages[0].Content != "sys" {
		t.Fatalf("expected the system message to survive trimming, got %+v", l.messages[0])
	}
	// Every remaining non-system message must start with a user turn --
	// confirms cutting happened at a turn boundary, not mid-turn.
	if l.messages[1].Role != provider.RoleUser {
		t.Fatalf("expected the oldest surviving message to be a user turn, got role %q", l.messages[1].Role)
	}
}

func TestTrimHistory_NeverSeparatesToolCallFromItsResult(t *testing.T) {
	l := &Loop{}
	l.messages = append(l.messages, provider.Message{Role: provider.RoleSystem, Content: "sys"})
	for i := 0; i < (maxHistoryMessages+30)/3; i++ {
		l.messages = append(l.messages,
			provider.Message{Role: provider.RoleUser, Content: "do something"},
			provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "c1", Name: "mesh_say", Arguments: "{}"}}},
			provider.Message{Role: provider.RoleTool, ToolCallID: "c1", Name: "mesh_say", Content: "ok"},
		)
	}

	l.trimHistory()

	for i, m := range l.messages {
		if m.Role == provider.RoleTool && i > 0 {
			prev := l.messages[i-1]
			if prev.Role != provider.RoleAssistant || len(prev.ToolCalls) == 0 {
				t.Fatalf("found a tool-result message at index %d with no preceding tool-calling assistant message: %+v then %+v", i, prev, m)
			}
		}
	}
}
