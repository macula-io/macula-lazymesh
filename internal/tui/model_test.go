package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/macula-io/macula-lazymesh/internal/agent"
)

func runeKey(r rune) tea.KeyMsg {
	return tea.KeyMsg(tea.Key{Type: tea.KeyRunes, Runes: []rune{r}})
}

func typeKey(t tea.KeyType) tea.KeyMsg {
	return tea.KeyMsg(tea.Key{Type: t})
}

func newTestModel(t *testing.T) Model {
	t.Helper()
	userInputCh := make(chan string, 8)
	m := New(nil, nil, userInputCh, "bottom")
	m.width, m.height = 80, 24
	m.resizeComponents()
	return m
}

// These are exactly the modal-state bugs a reviewer would expect to find
// (cf's own words when delegating this): "q" must never quit while
// composing a message, and Esc/Enter must actually change mode.

func TestNormalMode_QQuits(t *testing.T) {
	m := newTestModel(t)
	_, cmd := m.Update(runeKey('q'))
	if cmd == nil {
		t.Fatalf("expected a quit command from 'q' in normal mode")
	}
}

func TestInsertMode_QDoesNotQuit_TypesInstead(t *testing.T) {
	m := newTestModel(t)
	updated, _ := m.Update(runeKey('i')) // enter insert mode
	m = updated.(Model)
	if m.mode != ModeInsert {
		t.Fatalf("expected insert mode after 'i', got %v", m.mode)
	}

	// tea.Cmd values aren't comparable, so the real assertion that 'q'
	// didn't quit is behavioral: still in insert mode, and the character
	// landed in the input rather than being consumed as a command.
	updated, _ = m.Update(runeKey('q'))
	m = updated.(Model)
	if m.mode != ModeInsert {
		t.Fatalf("'q' while composing must not leave insert mode, got %v", m.mode)
	}
	if m.input.Value() != "q" {
		t.Fatalf("expected 'q' to be typed into the input, got %q", m.input.Value())
	}
}

func TestInsertMode_EscReturnsToNormal(t *testing.T) {
	m := newTestModel(t)
	updated, _ := m.Update(runeKey('i'))
	m = updated.(Model)
	updated, _ = m.Update(runeKey('h'))
	m = updated.(Model)
	if m.input.Value() != "h" {
		t.Fatalf("expected 'h' typed, got %q", m.input.Value())
	}

	updated, _ = m.Update(typeKey(tea.KeyEsc))
	m = updated.(Model)
	if m.mode != ModeNormal {
		t.Fatalf("expected normal mode after Esc, got %v", m.mode)
	}
}

func TestInsertMode_EnterSubmitsAndReturnsToNormal(t *testing.T) {
	userInputCh := make(chan string, 8)
	m := New(nil, nil, userInputCh, "bottom")
	m.width, m.height = 80, 24
	m.resizeComponents()

	updated, _ := m.Update(runeKey('i'))
	m = updated.(Model)
	for _, r := range "hello agent" {
		updated, _ = m.Update(runeKey(r))
		m = updated.(Model)
	}

	updated, cmd := m.Update(typeKey(tea.KeyEnter))
	m = updated.(Model)
	if m.mode != ModeNormal {
		t.Fatalf("expected normal mode after Enter, got %v", m.mode)
	}
	if m.input.Value() != "" {
		t.Fatalf("expected input to be cleared after submit, got %q", m.input.Value())
	}
	if len(m.chatEntries) != 1 || m.chatEntries[0].kind != chatYou || m.chatEntries[0].text != "hello agent" {
		t.Fatalf("expected one chatYou entry with the typed text, got %+v", m.chatEntries)
	}
	if cmd == nil {
		t.Fatalf("expected a command to deliver the message to userInputCh")
	}
	cmd() // execute the returned tea.Cmd synchronously

	select {
	case got := <-userInputCh:
		if got != "hello agent" {
			t.Fatalf("expected %q on userInputCh, got %q", "hello agent", got)
		}
	default:
		t.Fatalf("expected the submitted message to be sent on userInputCh")
	}
}

func TestInsertMode_SubmittingEmptyInputDoesNothing(t *testing.T) {
	m := newTestModel(t)
	updated, _ := m.Update(runeKey('i'))
	m = updated.(Model)

	updated, _ = m.Update(typeKey(tea.KeyEnter))
	m = updated.(Model)
	if len(m.chatEntries) != 0 {
		t.Fatalf("expected no chat entry for an empty submit, got %+v", m.chatEntries)
	}
	if m.mode != ModeNormal {
		t.Fatalf("expected normal mode after submitting empty input, got %v", m.mode)
	}
}

func TestNormalMode_MTogglesMeshExpanded(t *testing.T) {
	m := newTestModel(t)
	if m.meshExpanded {
		t.Fatalf("expected mesh view collapsed by default")
	}
	updated, _ := m.Update(runeKey('m'))
	m = updated.(Model)
	if !m.meshExpanded {
		t.Fatalf("expected 'm' to expand the mesh view")
	}
	updated, _ = m.Update(runeKey('m'))
	m = updated.(Model)
	if m.meshExpanded {
		t.Fatalf("expected a second 'm' to collapse it again")
	}
}

func TestInsertMode_MDoesNotToggleMeshView_TypesInstead(t *testing.T) {
	m := newTestModel(t)
	updated, _ := m.Update(runeKey('i'))
	m = updated.(Model)

	updated, _ = m.Update(runeKey('m'))
	m = updated.(Model)
	if m.meshExpanded {
		t.Fatalf("'m' while composing must not toggle the mesh view")
	}
	if m.input.Value() != "m" {
		t.Fatalf("expected 'm' to be typed into the input, got %q", m.input.Value())
	}
}

func TestForceQuit_WorksInEitherMode(t *testing.T) {
	m := newTestModel(t)
	if _, cmd := m.Update(typeKey(tea.KeyCtrlC)); cmd == nil {
		t.Fatalf("expected ctrl+c to quit in normal mode")
	}

	updated, _ := m.Update(runeKey('i'))
	m = updated.(Model)
	if _, cmd := m.Update(typeKey(tea.KeyCtrlC)); cmd == nil {
		t.Fatalf("expected ctrl+c to quit even while composing")
	}
}

func TestNormalMode_ToggleMute(t *testing.T) {
	m := newTestModel(t)
	if m.muted {
		t.Fatalf("expected bell enabled by default")
	}
	updated, _ := m.Update(runeKey('b'))
	m = updated.(Model)
	if !m.muted {
		t.Fatalf("expected 'b' to mute")
	}
}

func TestHandleAgentEvent_AppendsChatEntryAndReArms(t *testing.T) {
	events := make(chan agent.Event, 1)
	m := newTestModel(t)
	m.agentEvents = events

	events <- agent.Event{Kind: agent.EventAssistantMessage, Text: "hi from agent"}
	ev := <-events // simulate what waitForAgentEvent would have received
	updated, cmd := m.Update(agentEventMsg(ev))
	m = updated.(Model)
	if len(m.chatEntries) != 1 || m.chatEntries[0].kind != chatAssistant {
		t.Fatalf("expected one chatAssistant entry, got %+v", m.chatEntries)
	}
	if cmd == nil {
		t.Fatalf("expected handleAgentEvent to return a command (bell + re-arm listen)")
	}
}

func TestHandleAgentEvent_BackoffQueuesTripleBell(t *testing.T) {
	m := newTestModel(t)
	m.agentEvents = make(chan agent.Event) // never fires again, fine for this assertion

	// We can't directly observe ringBell's side effect without executing
	// it against a real stdout, but we CAN confirm the entry lands as a
	// system message -- the observable, testable half of this behavior.
	updated, _ := m.Update(agentEventMsg(agent.Event{Kind: agent.EventBackoff}))
	m = updated.(Model)
	if len(m.chatEntries) != 1 || m.chatEntries[0].kind != chatSystem {
		t.Fatalf("expected a chatSystem entry for EventBackoff, got %+v", m.chatEntries)
	}
}
