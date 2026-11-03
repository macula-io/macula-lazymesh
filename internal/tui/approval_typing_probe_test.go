package tui

import (
	"testing"
	"time"

	"github.com/macula-io/macula-lazymesh/internal/agent"
)

// TestApprovalTypingProbeIsClosed is the probe in macula-lazymesh#59, kept as
// filed: it is the scenario, not a unit of the fix, and it fails on main
// 17f6699 where typing " yes" into a message approves a shell_exec.
//
// Since #62 the composer is always focused and the prompt does not open
// mid-word at all, so the probe now runs in two steps: type the sentence
// while the call waits (it must neither answer nor appear), then let the
// composer go idle and check the prompt is there and still unanswered.
//
// Deliberately free of this package's test helpers apart from driving the
// clock by hand, which the deferral makes unavoidable.
func TestApprovalTypingProbeIsClosed(t *testing.T) {
	clock := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	approvalCh := make(chan ApprovalAnswer, 4)
	m := New(nil, Options{UserInputCh: make(chan string, 8), ApprovalCh: approvalCh})
	m.now = func() time.Time { return clock }

	typeText := func(s string) {
		for _, r := range s {
			updated, _ := m.Update(runeKey(r))
			m = updated.(Model)
		}
	}

	typeText("ok so")
	updated, _ := m.Update(agentEventMsg(agent.Event{Kind: agent.EventApprovalRequested, ToolName: "shell_exec", ID: "p1", Text: `{"cmd":"rm -rf build"}`}))
	m = updated.(Model)

	typeText(" yes")

	select {
	case answer := <-approvalCh:
		t.Fatalf("typing \" yes\" answered the prompt: %+v", answer)
	default:
	}
	if m.pendingApproval != nil {
		t.Fatal("the approval took the screen in the middle of a word")
	}

	// The person stops typing: now the prompt may appear, and the sentence
	// already in the draft still answers nothing.
	clock = clock.Add(popupIdleDelay)
	m, _ = m.maybeShowDeferredApproval()
	if m.pendingApproval == nil {
		t.Fatal("the prompt never appeared after the composer went idle")
	}
	select {
	case answer := <-approvalCh:
		t.Fatalf("showing the prompt answered it by itself: %+v", answer)
	default:
	}
	if got := m.input.Value(); got != "ok so yes" {
		t.Fatalf("the sentence did not reach the draft: %q", got)
	}
}
