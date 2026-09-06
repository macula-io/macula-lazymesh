package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/macula-io/macula-lazymesh/internal/agent"
)

func TestChatEntryFromAgentEvent_ToolCallCollapsedByDefault(t *testing.T) {
	ev := agent.Event{Kind: agent.EventToolCall, ToolName: "mesh_say", Text: `{"room_topic":"agents.room.abc","text":"a very long message that should get truncated in the collapsed form"}`}
	entry := chatEntryFromAgentEvent(ev)
	if entry.kind != chatToolCall {
		t.Fatalf("expected chatToolCall, got %v", entry.kind)
	}
	rendered := entry.render(false)
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

	collapsed := entry.render(false)
	expanded := entry.render(true)
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
	if !strings.Contains(entry.render(false), "boom") {
		t.Fatalf("expected error text to appear in the render, got %q", entry.render(false))
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

func TestYouChatEntry(t *testing.T) {
	entry := youChatEntry("hi agent")
	if entry.kind != chatYou || entry.text != "hi agent" {
		t.Fatalf("unexpected entry: %+v", entry)
	}
	if !strings.Contains(entry.render(false), "hi agent") {
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
