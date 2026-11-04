package tui

// macula-io/macula-lazymesh#115: the answer the operator gave to an
// approval that is no longer waiting is shown as a notice, and the notice
// does not end the turn -- an EventError would have said the turn failed,
// which is not what happened.

import (
	"strings"
	"testing"
	"time"

	"github.com/macula-io/macula-lazymesh/internal/agent"
)

func TestAnswerUnused_ShowsANoticeAndKeepsTheTurnRunning(t *testing.T) {
	m, now, _ := turnClockModel(t)
	m = submit(t, m, "push it")
	*now = now.Add(2 * time.Second)
	m = feed(t, m,
		agent.Event{Kind: agent.EventToolCall, ToolName: "git_push", Text: `{}`},
		agent.Event{Kind: agent.EventAnswerUnused, ID: "approve-1"},
	)

	got := chatJoined(m)
	if !strings.Contains(got, "answer not used: the question is no longer waiting") {
		t.Fatalf("the operator is not told the answer reached nothing:\n%s", got)
	}
	if strings.Contains(got, "· failed") {
		t.Fatalf("a notice ended the turn as failed:\n%s", got)
	}
	if s := summary(m); !strings.Contains(s, "working") {
		t.Fatalf("the turn stopped running over a notice: %q", s)
	}
	if strings.Contains(got, "error") {
		t.Fatalf("the notice is rendered as an error:\n%s", got)
	}
}
