package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/macula-io/macula-lazymesh/internal/agent"
	"github.com/macula-io/macula-lazymesh/internal/provider"
)

func TestChatEntryFromAgentEvent_ToolCallCollapsedByDefault(t *testing.T) {
	ev := agent.Event{Kind: agent.EventToolCall, ToolName: "mesh_say", Text: `{"room_topic":"agents.room.abc","text":"a very long message that should get truncated in the collapsed form"}`}
	entry := chatEntryFromAgentEvent(ev)
	if entry.kind != chatToolCall {
		t.Fatalf("expected chatToolCall, got %v", entry.kind)
	}
	rendered := entry.render(false, 80)
	if strings.Contains(rendered, "should get truncated") {
		t.Fatalf("expected collapsed render to truncate the arguments, got %q", rendered)
	}
	if !strings.Contains(rendered, "mesh_say") {
		t.Fatalf("expected the tool name to appear, got %q", rendered)
	}
}

func TestChatEntry_ExpandedShowsFullDetail(t *testing.T) {
	ev := agent.Event{Kind: agent.EventToolResult, ToolName: "mesh_rooms", Text: strings.Repeat("x", 200)}
	entry := chatEntryFromAgentEvent(ev)

	collapsed := entry.render(false, 80)
	expanded := entry.render(true, 80)
	if len(expanded) <= len(collapsed) {
		t.Fatalf("expected expanded render to be longer than collapsed, collapsed=%d expanded=%d", len(collapsed), len(expanded))
	}
	if !strings.Contains(expanded, strings.Repeat("x", 200)) {
		t.Fatalf("expected expanded render to contain the full untruncated result")
	}
}

func TestChatEntryFromAgentEvent_AssistantMessage(t *testing.T) {
	entry := chatEntryFromAgentEvent(agent.Event{Kind: agent.EventAssistantMessage, Text: "hello room"})
	if entry.kind != chatAssistant || entry.text != "hello room" {
		t.Fatalf("unexpected entry: %+v", entry)
	}
}

func TestChatEntryFromAgentEvent_Error(t *testing.T) {
	entry := chatEntryFromAgentEvent(agent.Event{Kind: agent.EventError, ToolName: "shell_exec", Err: errors.New("boom")})
	if entry.kind != chatError {
		t.Fatalf("expected chatError, got %v", entry.kind)
	}
	if !strings.Contains(entry.render(false, 80), "boom") {
		t.Fatalf("expected error text to appear in the render, got %q", entry.render(false, 80))
	}
}

func TestChatEntryFromAgentEvent_BackoffAndMaxFailures(t *testing.T) {
	backoff := chatEntryFromAgentEvent(agent.Event{Kind: agent.EventBackoff})
	if backoff.kind != chatSystem {
		t.Fatalf("expected chatSystem for EventBackoff, got %v", backoff.kind)
	}
	stopped := chatEntryFromAgentEvent(agent.Event{Kind: agent.EventMaxFailuresReached})
	if stopped.kind != chatSystem {
		t.Fatalf("expected chatSystem for EventMaxFailuresReached, got %v", stopped.kind)
	}
}

// Covers macula-io/macula-lazymesh#13/#15: EventListening is the
// liveness signal replacing the old design's incidental mesh_say
// tool-call traffic. Must render as chatter (routed to the status
// strip, see isChatter in model.go) and must be routine, not alarming.
func TestChatEntryFromAgentEvent_Listening(t *testing.T) {
	entry := chatEntryFromAgentEvent(agent.Event{Kind: agent.EventListening})
	if entry.kind != chatSystem {
		t.Fatalf("expected chatSystem, got %v", entry.kind)
	}
	if !strings.Contains(entry.render(false, 80), "listening") {
		t.Fatalf("expected the rendered text to say it's listening, got %q", entry.render(false, 80))
	}
	if !isChatter(agent.EventListening) {
		t.Fatalf("expected EventListening to be routed as chatter (status strip, not the main chat pane)")
	}
}

func TestYouChatEntry(t *testing.T) {
	entry := youChatEntry("hi agent")
	if entry.kind != chatYou || entry.text != "hi agent" {
		t.Fatalf("unexpected entry: %+v", entry)
	}
	if !strings.Contains(entry.render(false, 80), "hi agent") {
		t.Fatalf("expected rendered text to include the message")
	}
}

func TestTruncateForChat(t *testing.T) {
	if got := truncateForChat("short", 10); got != "short" {
		t.Fatalf("expected no truncation, got %q", got)
	}
	got := truncateForChat(strings.Repeat("x", 20), 5)
	if got != strings.Repeat("x", 5)+"..." {
		t.Fatalf("unexpected truncation result: %q", got)
	}
}

// Regression guard, found live 2026-09-06: pretty-printed JSON (a
// mesh_read_inbox/mesh_rooms tool result) still containing a raw newline
// after truncation silently rendered as more than one terminal row,
// breaking statusLines()'s "one slice element = one row" assumption --
// the status block's chatter line (issue #2) visibly wrapped and made the
// chat viewport jump every update.
func TestTruncateForChat_StripsEmbeddedNewlines(t *testing.T) {
	got := truncateForChat("{\n  \"a\": 1,\n  \"b\": 2\n}", 100)
	if strings.Contains(got, "\n") {
		t.Fatalf("expected no embedded newline in a chat-collapsed string, got %q", got)
	}
}

func TestCollapseNewlines_HandlesAllLineEndings(t *testing.T) {
	got := collapseNewlines("a\nb\r\nc\rd")
	if strings.Contains(got, "\n") || strings.Contains(got, "\r") {
		t.Fatalf("expected all line endings collapsed to spaces, got %q", got)
	}
	if got != "a b c d" {
		t.Fatalf("expected %q, got %q", "a b c d", got)
	}
}

// TestChatEntry_ApplyPatchShowsTheDiffWithoutExpanding pins #46's rule
// that an applied patch is visible in the chat stream: the operator sees
// what changed without having to toggle details, because a change they
// did not see is not a review.
func TestChatEntry_ApplyPatchShowsTheDiffWithoutExpanding(t *testing.T) {
	diff := "applied 1 hunk(s) to 1 file: a.txt\n\n--- a/a.txt\n+++ b/a.txt\n@@ -1,2 +1,2 @@\n one\n-two\n+three\n"
	entry := chatEntryFromAgentEvent(agent.Event{Kind: agent.EventToolResult, ToolName: "apply_patch", Text: diff})

	rendered := entry.render(false, 80)
	for _, want := range []string{"+three", "-two", "a.txt"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("apply_patch's diff is hidden without expanding details (missing %q):\n%s", want, rendered)
		}
	}

	// Other tools keep the collapsed-by-default behaviour.
	other := chatEntryFromAgentEvent(agent.Event{Kind: agent.EventToolResult, ToolName: "mesh_rooms", Text: diff})
	if strings.Contains(other.render(false, 80), "+three") {
		t.Fatalf("an ordinary tool result expanded itself")
	}
}

// TestChatEntryFromAgentEvent_CutReplyIsMarked: a reply the backend cut short
// must say so under the answer, with the answer's own text left whole so
// scrollback and copy return what the model actually said (#77).
func TestChatEntryFromAgentEvent_CutReplyIsMarked(t *testing.T) {
	cases := []struct {
		stop   provider.Stop
		marker string
	}{
		{provider.Stop{Reason: provider.StopOutputLimit}, "reply cut: output limit reached"},
		{provider.Stop{Reason: provider.StopStreamEndedEarly}, "reply cut: stream ended early"},
		{provider.Stop{Reason: provider.StopOther, Detail: "content_filter"}, "reply cut: stopped early (content_filter)"},
	}
	for _, c := range cases {
		entry := chatEntryFromAgentEvent(agent.Event{Kind: agent.EventAssistantMessage, Text: "the answer stops mid", Stop: c.stop})
		rendered := entry.render(false, 80)
		if !strings.Contains(rendered, c.marker) {
			t.Errorf("render for %+v = %q, want marker %q", c.stop, rendered, c.marker)
		}
		if entry.text != "the answer stops mid" {
			t.Errorf("entry text = %q, want the cut text kept whole (the marker is rendered, not stored)", entry.text)
		}
	}
}

// TestChatEntryFromAgentEvent_NormalReplyHasNoMarker: a finished turn, and a
// backend that reported nothing at all, both render exactly as before --
// only a positively named cut earns a marker.
func TestChatEntryFromAgentEvent_NormalReplyHasNoMarker(t *testing.T) {
	for _, stop := range []provider.Stop{{}, {Reason: provider.StopNormal}} {
		entry := chatEntryFromAgentEvent(agent.Event{Kind: agent.EventAssistantMessage, Text: "hello room", Stop: stop})
		if rendered := entry.render(false, 80); strings.Contains(rendered, "reply cut") {
			t.Errorf("render for %+v marked a reply that was not cut: %q", stop, rendered)
		}
	}
}
