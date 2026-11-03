package tui

import (
	"testing"

	"github.com/macula-io/macula-lazymesh/internal/agent"
)

// TestApprovalTypingProbeIsClosed is the probe in macula-lazymesh#59, kept
// exactly as filed and deliberately free of this package's newer test helpers:
// it compiles against the unfixed tree too, where it fails.
//
// On main 17f6699: compose "ok so", a shell_exec approval arrives for
// `{"cmd":"rm -rf build"}`, type " yes" -- and the y of "yes" sends
// {ID:p1 Allow:true}, approving the call. With the arming guard (#59) no key
// of the sentence answers, and the prompt is still waiting when the typing
// stops.
func TestApprovalTypingProbeIsClosed(t *testing.T) {
	approvalCh := make(chan ApprovalAnswer, 4)
	m := New(nil, Options{UserInputCh: make(chan string, 8), ApprovalCh: approvalCh})

	updated, _ := m.Update(runeKey('i'))
	m = updated.(Model)
	for _, r := range "ok so" {
		updated, _ = m.Update(runeKey(r))
		m = updated.(Model)
	}

	updated, _ = m.Update(agentEventMsg(agent.Event{Kind: agent.EventApprovalRequested, ToolName: "shell_exec", ID: "p1", Text: `{"cmd":"rm -rf build"}`}))
	m = updated.(Model)

	for _, r := range " yes" {
		updated, _ = m.Update(runeKey(r))
		m = updated.(Model)
	}

	select {
	case answer := <-approvalCh:
		t.Fatalf("typing \" yes\" answered the prompt: %+v", answer)
	default:
	}
	if m.pendingApproval == nil {
		t.Fatal("the prompt closed on keys meant for the message")
	}
}
