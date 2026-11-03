package tui

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/macula-io/macula-lazymesh/internal/agent"
)

// The two pop-ups can be open at once -- a ring over an approval, or an
// approval over a ring -- and these are the r=1 holes in macula-lazymesh#59
// that stacking opened. Each was proved against the first version of the fix
// with a test; they stay here so the next change to either pop-up has to keep
// them shut.

// send applies one message through Update.
func send(t *testing.T, m Model, msg tea.Msg) Model {
	t.Helper()
	updated, _ := m.Update(msg)
	next, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update returned %T, not a Model", updated)
	}
	return next
}

// TestRingOverApprovalDoesNotReturnItArmed is the first hole: a ring opening
// over an approval took the keys, so the approval's own guard never saw them
// and came back already armed when the ring closed. One y after the last typed
// key allowed a shell_exec.
func TestRingOverApprovalDoesNotReturnItArmed(t *testing.T) {
	m, clock, approvalCh := typingApprovalModel(t)
	m.width, m.height = 80, 24

	m = typeText(t, m, "ok so")
	m = send(t, m, agentEventMsg(agent.Event{
		Kind: agent.EventApprovalRequested, ToolName: "shell_exec", ID: "p1", Text: `{"cmd":"rm -rf build"}`,
	}))
	// The prompt waits for the person to stop typing (#62), then takes the
	// screen -- the stack below is what #59's arming guards have to survive.
	clock.advance(popupIdleDelay)
	m, _ = m.maybeShowDeferredApproval()
	if m.mode != ModeApprovalPopup {
		t.Fatalf("the approval pop-up did not take the screen: mode %v", m.mode)
	}

	clock.advance(50 * time.Millisecond)
	m.state.pending = []pendingRing{{RingID: "r1", Peer: "stranger", Purpose: "hi"}}
	m.processPendingRings()
	if m.mode != ModeRingPopup {
		t.Fatalf("the ring did not open over the approval: mode %v", m.mode)
	}

	for _, r := range " and then" {
		clock.advance(100 * time.Millisecond)
		m = send(t, m, runeKey(r))
	}

	// Esc is a habit, not an answer: it leaves the ring at once.
	clock.advance(100 * time.Millisecond)
	m = send(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.mode != ModeApprovalPopup || m.pendingApproval == nil {
		t.Fatalf("esc did not hand focus back to the approval still pending: mode %v pending %v", m.mode, m.pendingApproval)
	}

	// 100 ms after the last typed key: the approval's guard starts again, so
	// this y is not an answer.
	clock.advance(100 * time.Millisecond)
	m = send(t, m, runeKey('y'))
	select {
	case answer := <-approvalCh:
		t.Fatalf("a y 100 ms after the last typed key allowed the call: %+v", answer)
	default:
	}
	if m.pendingApproval == nil {
		t.Fatal("the approval closed on a key inside the guard")
	}

	// And it is still answerable after a real pause, back where the person was.
	clock.advance(time.Second)
	m = send(t, m, runeKey('y'))
	select {
	case answer := <-approvalCh:
		if !answer.Allow || answer.ID != "p1" {
			t.Fatalf("answer = %+v", answer)
		}
	default:
		t.Fatal("y after a pause did not answer the approval")
	}
	if m.mode != ModeCompose {
		t.Fatalf("focus did not go back to the composer: mode %v", m.mode)
	}
}

// TestClosingBothPopupsIsNeverStranded is the second hole: with one shared
// return slot a ring over an approval overwrote it, and when both closed the
// mode was an approval pop-up with nothing pending -- every key re-set the
// same mode, and only ctrl+c got out.
func TestClosingBothPopupsIsNeverStranded(t *testing.T) {
	m, clock, _ := typingApprovalModel(t)
	m.width, m.height = 80, 24
	m = typeText(t, m, "ok so")
	m = send(t, m, agentEventMsg(agent.Event{
		Kind: agent.EventApprovalRequested, ToolName: "shell_exec", ID: "p1", Text: `{"cmd":"ls"}`,
	}))
	clock.advance(popupIdleDelay)
	m, _ = m.maybeShowDeferredApproval()
	if m.mode != ModeApprovalPopup {
		t.Fatalf("the approval pop-up did not take the screen: mode %v", m.mode)
	}

	clock.advance(50 * time.Millisecond)
	m.state.pending = []pendingRing{{RingID: "r1", Peer: "stranger", Purpose: "hi"}}
	m.processPendingRings()

	// Close the ring, then answer the approval.
	clock.advance(time.Second)
	m = send(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	clock.advance(time.Second)
	m = send(t, m, runeKey('n'))

	if m.pendingApproval != nil || m.pendingRingPopup != nil {
		t.Fatalf("a pop-up is still open: approval %v ring %v", m.pendingApproval, m.pendingRingPopup)
	}
	if m.mode == ModeApprovalPopup || m.mode == ModeRingPopup {
		t.Fatalf("focus is stranded in a pop-up with nothing pending: mode %v", m.mode)
	}
	if m.mode != ModeCompose {
		t.Fatalf("focus did not go back to the composer: mode %v", m.mode)
	}
	m = typeText(t, m, "!")
	if m.input.Value() != "ok so!" {
		t.Fatalf("keys do not reach the draft after both pop-ups closed: %q", m.input.Value())
	}
}

// TestApprovalOverRingDoesNotReturnItArmed is the third hole: a ring left up
// for a while had armed keys, an approval took the screen, and when the
// approval closed the ring was still armed -- so the next letter answered it.
func TestApprovalOverRingDoesNotReturnItArmed(t *testing.T) {
	fake := &fakeToolCaller{responses: map[string]string{"mesh_answer_ring": "{}"}}
	m, clock := ringPopupModel(t, fake)

	clock.advance(2 * time.Second) // the ring has been up a while, nobody typed
	m = send(t, m, agentEventMsg(agent.Event{
		Kind: agent.EventApprovalRequested, ToolName: "shell_exec", ID: "p1", Text: `{"cmd":"ls"}`,
	}))
	if m.mode != ModeApprovalPopup {
		t.Fatalf("the approval did not take the screen from the ring: mode %v", m.mode)
	}

	clock.advance(time.Second)
	m = send(t, m, runeKey('n'))
	if m.pendingApproval != nil {
		t.Fatal("n did not answer the approval")
	}
	if m.mode != ModeRingPopup || m.pendingRingPopup == nil {
		t.Fatalf("focus did not go back to the ring still pending: mode %v", m.mode)
	}

	// 50 ms after the key that closed the approval: the ring's guard starts
	// again, so this a is not an answer.
	clock.advance(50 * time.Millisecond)
	m = send(t, m, runeKey('a'))
	if m.pendingRingPopup == nil {
		t.Fatal("an a 50 ms after the approval closed answered the ring")
	}
	if len(fake.calls) != 0 {
		t.Fatalf("the ring was answered: %v", fake.calls)
	}

	// After a pause the ring is answerable again.
	clock.advance(time.Second)
	m = send(t, m, runeKey('a'))
	if m.pendingRingPopup != nil {
		t.Fatal("a after a pause did not answer the ring")
	}
}

// TestPastedYDoesNotAnswer: a paste is typing whatever it contains. A "y"
// pasted into a message must not allow a call, even with the guard passed.
func TestPastedYDoesNotAnswer(t *testing.T) {
	m, clock, approvalCh := typingApprovalModel(t)
	m.width, m.height = 80, 24
	m = send(t, m, agentEventMsg(agent.Event{
		Kind: agent.EventApprovalRequested, ToolName: "shell_exec", ID: "p1", Text: `{"cmd":"ls"}`,
	}))

	clock.advance(time.Second) // the guard has passed; this is not about timing
	m = send(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y"), Paste: true})

	select {
	case answer := <-approvalCh:
		t.Fatalf("a pasted y answered the prompt: %+v", answer)
	default:
	}
	if m.pendingApproval == nil {
		t.Fatal("a pasted y closed the prompt")
	}

	// A pasted y also restarts the guard: the person is still typing.
	clock.advance(100 * time.Millisecond)
	m = send(t, m, runeKey('y'))
	select {
	case answer := <-approvalCh:
		t.Fatalf("a y 100 ms after a paste answered: %+v", answer)
	default:
	}
}
