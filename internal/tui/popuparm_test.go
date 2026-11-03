package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/macula-io/macula-lazymesh/internal/agent"
)

// fakeClock is a clock the test moves by hand, so a 600 ms guard is proved
// without sleeping through it and a "one second later" case costs nothing.
type fakeClock struct{ at time.Time }

func (c *fakeClock) now() time.Time          { return c.at }
func (c *fakeClock) advance(d time.Duration) { c.at = c.at.Add(d) }

// withClock installs the fake clock on a model, at the instant the fake holds.
func withClock(m Model, c *fakeClock) Model {
	m.now = c.now
	return m
}

// armPopups makes both pop-ups' answer keys live, as a person who has stopped
// typing does by waiting. Tests that are about what an answer DOES use this so
// they say what they mean; the tests below are the ones about the waiting.
func armPopups(m Model) Model {
	past := m.clock().Add(-2 * popupArmDelay)
	m.approvalArm = popupArm{shownAt: past, lastKeyAt: past}
	m.ringArm = popupArm{shownAt: past, lastKeyAt: past}
	return m
}

// typingApprovalModel is the #59 probe's own setup: a person composing a
// message, a shell_exec approval that arrives mid-word, and a model clock the
// test drives.
func typingApprovalModel(t *testing.T) (Model, *fakeClock, chan ApprovalAnswer) {
	t.Helper()
	clock := &fakeClock{at: time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)}
	approvalCh := make(chan ApprovalAnswer, 4)
	m := New(nil, Options{UserInputCh: make(chan string, 8), ApprovalCh: approvalCh})
	m = withClock(m, clock)

	return m, clock, approvalCh
}

// typeText sends each rune the way the terminal delivers it.
func typeText(t *testing.T, m Model, s string) Model {
	t.Helper()
	for _, r := range s {
		updated, _ := m.Update(runeKey(r))
		var ok bool
		m, ok = updated.(Model)
		if !ok {
			t.Fatalf("Update returned %T, not a Model", updated)
		}
	}
	return m
}

// TestApprovalTypedIntoAMessageDoesNotAnswer is #59 itself: a person
// composing "ok so" keeps typing " yes" while a shell_exec approval arrives,
// and no key of it answers -- the call is neither allowed nor denied, and the
// pop-up is still waiting when the typing stops.
func TestApprovalTypedIntoAMessageDoesNotAnswer(t *testing.T) {
	m, clock, approvalCh := typingApprovalModel(t)

	updated, _ := m.Update(runeKey('i'))
	m = updated.(Model)
	m = typeText(t, m, "ok so")
	if m.mode != ModeInsert {
		t.Fatalf("compose did not take: mode %v", m.mode)
	}

	updated, _ = m.Update(agentEventMsg(agent.Event{
		Kind: agent.EventApprovalRequested, ToolName: "shell_exec", ID: "p1", Text: `{"cmd":"rm -rf build"}`,
	}))
	m = updated.(Model)
	if m.pendingApproval == nil {
		t.Fatal("the approval prompt did not appear")
	}

	// The rest of the word, a keystroke at a time, at typing speed.
	for _, r := range " yes" {
		m = typeText(t, m, string(r))
		clock.advance(80 * time.Millisecond)
	}

	select {
	case answer := <-approvalCh:
		t.Fatalf("typing \" yes\" answered the prompt: %+v", answer)
	default:
	}
	if m.pendingApproval == nil {
		t.Fatal("the prompt closed on keys that were meant for the message")
	}

	// The pause does not answer either: waiting is not consent.
	clock.advance(time.Second)
	select {
	case answer := <-approvalCh:
		t.Fatalf("an unpressed prompt answered itself: %+v", answer)
	default:
	}
	if m.pendingApproval == nil {
		t.Fatal("the prompt closed while nobody pressed anything")
	}
}

// TestApprovalYAfterAPauseStillAnswers: the guard delays the answer, it does
// not stop it. A y pressed one second after the prompt appeared, with nothing
// typed in between, allows the call.
func TestApprovalYAfterAPauseStillAnswers(t *testing.T) {
	m, clock, approvalCh := typingApprovalModel(t)
	updated, _ := m.Update(agentEventMsg(agent.Event{
		Kind: agent.EventApprovalRequested, ToolName: "shell_exec", ID: "p1", Text: `{"cmd":"true"}`,
	}))
	m = updated.(Model)

	clock.advance(time.Second)
	updated, _ = m.Update(runeKey('y'))
	m = updated.(Model)

	select {
	case answer := <-approvalCh:
		if !answer.Allow || answer.ID != "p1" {
			t.Fatalf("answer = %+v", answer)
		}
	default:
		t.Fatal("y one second after the prompt appeared did not allow")
	}
	if m.pendingApproval != nil {
		t.Fatal("the prompt stayed open after an answer")
	}
}

// TestApprovalTypingRestartsTheGuard: the guard is measured from the last
// keystroke, not from the prompt appearing, so a person who types again after
// the guard had almost passed still cannot answer by accident.
func TestApprovalTypingRestartsTheGuard(t *testing.T) {
	m, clock, approvalCh := typingApprovalModel(t)
	updated, _ := m.Update(agentEventMsg(agent.Event{
		Kind: agent.EventApprovalRequested, ToolName: "shell_exec", ID: "p1", Text: `{"cmd":"true"}`,
	}))
	m = updated.(Model)

	clock.advance(500 * time.Millisecond) // almost armed
	m = typeText(t, m, "s")               // ...and the word is not finished
	clock.advance(200 * time.Millisecond)
	m = typeText(t, m, "y") // 200 ms after the s: still asleep

	select {
	case answer := <-approvalCh:
		t.Fatalf("a key answered 200 ms after typing: %+v", answer)
	default:
	}

	clock.advance(time.Second)
	m = typeText(t, m, "y") // a real pause, then a real y
	select {
	case answer := <-approvalCh:
		if !answer.Allow {
			t.Fatalf("answer = %+v", answer)
		}
	default:
		t.Fatal("y after a pause following the typing did not allow")
	}
}

// TestApprovalEscDeniesAtOnce: the safe answer is never delayed. esc is not a
// letter anyone types into a message, so it denies the moment it is pressed.
func TestApprovalEscDeniesAtOnce(t *testing.T) {
	m, _, approvalCh := typingApprovalModel(t)
	updated, _ := m.Update(agentEventMsg(agent.Event{
		Kind: agent.EventApprovalRequested, ToolName: "shell_exec", ID: "p1", Text: `{"cmd":"true"}`,
	}))
	m = updated.(Model)

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(Model)

	select {
	case answer := <-approvalCh:
		if answer.Allow {
			t.Fatalf("esc allowed the call: %+v", answer)
		}
	default:
		t.Fatal("esc did not deny at once")
	}
}

// TestApprovalDenyNAfterTheGuardIsAlsoDelayed: n is guarded too. A "no" typed
// into a message must not deny a call either -- the agent would be told the
// person refused something they never read.
func TestApprovalDenyNAfterTheGuardIsAlsoDelayed(t *testing.T) {
	m, clock, approvalCh := typingApprovalModel(t)
	updated, _ := m.Update(agentEventMsg(agent.Event{
		Kind: agent.EventApprovalRequested, ToolName: "shell_exec", ID: "p1", Text: `{"cmd":"true"}`,
	}))
	m = updated.(Model)

	m = typeText(t, m, "n")
	select {
	case answer := <-approvalCh:
		t.Fatalf("n answered inside the guard: %+v", answer)
	default:
	}

	clock.advance(time.Second)
	m = typeText(t, m, "n")
	select {
	case answer := <-approvalCh:
		if answer.Allow {
			t.Fatalf("answer = %+v", answer)
		}
	default:
		t.Fatal("n after a pause did not deny")
	}
}

// TestApprovalKeepsTheDraftAndGivesFocusBack: answering a prompt does not
// cost the operator the message they were writing. The draft is untouched and
// the composer is focused again, so the next key is the next letter.
func TestApprovalKeepsTheDraftAndGivesFocusBack(t *testing.T) {
	m, clock, _ := typingApprovalModel(t)
	updated, _ := m.Update(runeKey('i'))
	m = updated.(Model)
	m = typeText(t, m, "ok so")

	updated, _ = m.Update(agentEventMsg(agent.Event{
		Kind: agent.EventApprovalRequested, ToolName: "shell_exec", ID: "p1", Text: `{"cmd":"true"}`,
	}))
	m = updated.(Model)
	if m.input.Value() != "ok so" {
		t.Fatalf("the draft did not survive the prompt appearing: %q", m.input.Value())
	}

	clock.advance(time.Second)
	m = typeText(t, m, "n")
	if m.mode != ModeInsert {
		t.Fatalf("focus did not come back to the composer: mode %v", m.mode)
	}
	if m.input.Value() != "ok so" {
		t.Fatalf("the draft did not survive the answer: %q", m.input.Value())
	}
	m = typeText(t, m, "!")
	if m.input.Value() != "ok so!" {
		t.Fatalf("the next key did not reach the draft: %q", m.input.Value())
	}
}

// TestApprovalHintSaysTheKeysAreAsleep: a key that does nothing has to say
// why, or the pop-up reads as broken.
func TestApprovalHintSaysTheKeysAreAsleep(t *testing.T) {
	m, clock, _ := typingApprovalModel(t)
	updated, _ := m.Update(agentEventMsg(agent.Event{
		Kind: agent.EventApprovalRequested, ToolName: "shell_exec", ID: "p1", Text: `{"cmd":"true"}`,
	}))
	m = updated.(Model)

	if view := strings.ReplaceAll(m.View(), "\n", ""); !strings.Contains(view, armHint()) {
		t.Fatalf("the pop-up does not say its keys are asleep:\n%s", m.View())
	}
	clock.advance(time.Second)
	if view := strings.ReplaceAll(m.View(), "\n", ""); strings.Contains(view, armHint()) {
		t.Fatalf("the pop-up still says its keys are asleep after the guard:\n%s", m.View())
	}
}

// ringPopupModel is a model with one ring pop-up open, opened the way the real
// one opens, and a clock the test drives.
func ringPopupModel(t *testing.T, mcp toolCaller) (Model, *fakeClock) {
	t.Helper()
	clock := &fakeClock{at: time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)}
	m := New(mcp, Options{UserInputCh: make(chan string, 8)})
	m = withClock(m, clock)
	m.width, m.height = 80, 24

	m.pendingRingPopup = &pendingRing{RingID: "r1", Peer: "peer1", Purpose: "hello"}
	m.ringArm = popupArm{shownAt: clock.now()}
	m.mode = ModeRingPopup
	m.resizeComponents()
	return m, clock
}

// TestRingTypedIntoAMessageDoesNotAnswer is the ring half of #59: the letters
// of a sentence must not accept a ring.
func TestRingTypedIntoAMessageDoesNotAnswer(t *testing.T) {
	fake := &fakeToolCaller{responses: map[string]string{"mesh_answer_ring": "{}"}}
	m, clock := ringPopupModel(t, fake)

	for _, r := range "add the tests" {
		var cmd tea.Cmd
		m, cmd = m.handleRingPopupKey(runeKey(r))
		if cmd != nil {
			t.Fatalf("the letter %q answered the ring", r)
		}
		clock.advance(80 * time.Millisecond)
	}
	if m.pendingRingPopup == nil {
		t.Fatal("the ring pop-up closed on letters meant for a message")
	}
	if len(fake.calls) != 0 {
		t.Fatalf("a ring was answered: %v", fake.calls)
	}
}

// TestRingAnswerAfterAPauseStillWorks: the guard delays the answer, it does
// not stop it.
func TestRingAnswerAfterAPauseStillWorks(t *testing.T) {
	fake := &fakeToolCaller{responses: map[string]string{"mesh_answer_ring": "{}"}}
	m, clock := ringPopupModel(t, fake)

	clock.advance(time.Second)
	m, cmd := m.handleRingPopupKey(runeKey('a'))
	if m.pendingRingPopup != nil {
		t.Fatal("a after the guard did not answer the ring")
	}
	if cmd == nil {
		t.Fatal("a after the guard answered nothing")
	}
	if m.mode != ModeNormal {
		t.Fatalf("focus did not go back: mode %v", m.mode)
	}
}

// TestRingEscLeavesAtOnce: esc does not answer a ring, it leaves it pending,
// so it is never delayed.
func TestRingEscLeavesAtOnce(t *testing.T) {
	fake := &fakeToolCaller{responses: map[string]string{"mesh_answer_ring": "{}"}}
	m, _ := ringPopupModel(t, fake)

	m, cmd := m.handleRingPopupKey(tea.KeyMsg{Type: tea.KeyEsc})
	if m.pendingRingPopup != nil {
		t.Fatal("esc did not dismiss the pop-up")
	}
	if cmd != nil || len(fake.calls) != 0 {
		t.Fatal("esc answered a ring instead of leaving it for later")
	}
	if m.mode != ModeNormal {
		t.Fatalf("focus did not go back: mode %v", m.mode)
	}
}

// TestRingHintSaysTheKeysAreAsleep: the ring pop-up says the same thing the
// approval one does while its keys are asleep.
func TestRingHintSaysTheKeysAreAsleep(t *testing.T) {
	fake := &fakeToolCaller{responses: map[string]string{"mesh_answer_ring": "{}"}}
	m, clock := ringPopupModel(t, fake)

	if view := strings.ReplaceAll(m.View(), "\n", ""); !strings.Contains(view, armHint()) {
		t.Fatalf("the ring pop-up does not say its keys are asleep:\n%s", m.View())
	}
	clock.advance(time.Second)
	if view := strings.ReplaceAll(m.View(), "\n", ""); strings.Contains(view, armHint()) {
		t.Fatalf("the ring pop-up still says its keys are asleep after the guard:\n%s", m.View())
	}
}
