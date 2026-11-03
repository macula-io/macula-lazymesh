package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/macula-io/macula-lazymesh/internal/agent"
)

// #99: a turn the session crashed in ends as failed, and the operator sees
// the crash notice in the chat, not only in the status strip.
func TestTurnCrashed_EndsTheTurnAsFailedAndShowsTheNotice(t *testing.T) {
	m, now, _ := turnClockModel(t)
	m = submit(t, m, "tell pluto")
	*now = now.Add(2 * time.Second)
	notice := "the session crashed during this turn and was restarted; Steps that ran: 1) mesh_say {\"text\":\"hi\"};"
	m = feed(t, m,
		agent.Event{Kind: agent.EventToolCall, ToolName: "mesh_say", Text: `{"text":"hi"}`},
		agent.Event{Kind: agent.EventTurnCrashed, Text: notice},
		agent.Event{Kind: agent.EventListening},
	)
	got := chatJoined(m)
	if !strings.Contains(got, "session crashed during this turn") || !strings.Contains(got, "mesh_say") {
		t.Fatalf("the crash notice is not in the chat:\n%s", got)
	}
	if !strings.Contains(got, "· 2.0s · failed") {
		t.Fatalf("a crashed turn does not end as failed:\n%s", got)
	}
	if strings.Count(got, "deepseek/deepseek-v4-flash ·") != 1 {
		t.Fatalf("one turn, more than one end line:\n%s", got)
	}
	if s := summary(m); strings.Contains(s, "working") {
		t.Fatalf("still working after a crashed turn: %q", s)
	}
}
