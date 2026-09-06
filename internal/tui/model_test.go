package tui

import (
	"fmt"
	"os"
	"strings"
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
	m := New(nil, Options{UserInputCh: userInputCh, StatusBarPosition: "bottom"})
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
	m := New(nil, Options{UserInputCh: userInputCh, StatusBarPosition: "bottom"})
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

// Regression guard added alongside the ring pop-up: adding a third mode
// must not accidentally exempt it from the universal quit key.
func TestForceQuit_WorksDuringRingPopup(t *testing.T) {
	m := newTestModel(t)
	ring := pendingRing{RingID: "r1", Peer: "peer1"}
	m.pendingRingPopup = &ring
	m.mode = ModeRingPopup

	if _, cmd := m.Update(typeKey(tea.KeyCtrlC)); cmd == nil {
		t.Fatalf("expected ctrl+c to quit even while the ring pop-up is showing")
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

func TestRenderStatusStrip_OmitsAgentModelWhenNoAgentRunning(t *testing.T) {
	m := newTestModel(t)
	got := m.renderStatusStrip()
	if strings.Contains(got, "deepseek") {
		t.Fatalf("expected no model label with no --room agent running, got %q", got)
	}
}

func TestRenderStatusStrip_ShowsAgentModelWhenSet(t *testing.T) {
	userInputCh := make(chan string, 8)
	m := New(nil, Options{UserInputCh: userInputCh, StatusBarPosition: "bottom", AgentModel: "deepseek/deepseek-v4-flash"})
	m.width, m.height = 80, 24
	m.resizeComponents()
	got := m.renderStatusStrip()
	if !strings.Contains(got, "deepseek/deepseek-v4-flash") {
		t.Fatalf("expected the configured provider/model in the status strip, got %q", got)
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

// Issue #2: mode indicator, vim's own "-- MODE --" convention.
func TestRenderStatusStrip_ShowsModeIndicator(t *testing.T) {
	m := newTestModel(t)
	if !strings.Contains(m.renderStatusStrip(), "-- NORMAL --") {
		t.Fatalf("expected a NORMAL mode indicator, got %q", m.renderStatusStrip())
	}
	updated, _ := m.Update(runeKey('i'))
	m = updated.(Model)
	if !strings.Contains(m.renderStatusStrip(), "-- INSERT --") {
		t.Fatalf("expected an INSERT mode indicator after 'i', got %q", m.renderStatusStrip())
	}
}

// Issue #4: the compose line stays visible (and keeps its draft) after
// Esc back to Normal mode, instead of being replaced by a static hint.
func TestRenderInputLine_AlwaysVisibleWithRetainedDraft(t *testing.T) {
	m := newTestModel(t)
	updated, _ := m.Update(runeKey('i'))
	m = updated.(Model)
	for _, r := range "draft" {
		updated, _ = m.Update(runeKey(r))
		m = updated.(Model)
	}
	updated, _ = m.Update(typeKey(tea.KeyEsc))
	m = updated.(Model)
	if m.mode != ModeNormal {
		t.Fatalf("expected normal mode after Esc, got %v", m.mode)
	}
	if !strings.Contains(m.renderInputLine(), "draft") {
		t.Fatalf("expected the retained draft still visible in normal mode, got %q", m.renderInputLine())
	}
}

// Issue #2: routine tool-call activity is relocated out of the
// conversation pane by default.
func TestHandleAgentEvent_ToolCallRoutedToChatterLineByDefault(t *testing.T) {
	m := newTestModel(t)
	m.agentEvents = make(chan agent.Event)

	updated, _ := m.Update(agentEventMsg(agent.Event{Kind: agent.EventToolCall, ToolName: "mesh_call", Text: "{}"}))
	m = updated.(Model)
	if len(m.chatEntries) != 0 {
		t.Fatalf("expected no chat entry for a tool call by default, got %+v", m.chatEntries)
	}
	if !strings.Contains(m.lastChatter, "mesh_call") {
		t.Fatalf("expected the tool call to land in lastChatter, got %q", m.lastChatter)
	}
	if !strings.Contains(m.renderStatusStrip(), "mesh_call") {
		t.Fatalf("expected the status strip to surface the last tool call, got %q", m.renderStatusStrip())
	}
}

// Errors and system notices are never "chatter" -- they stay in the chat
// pane regardless of showChatter.
func TestHandleAgentEvent_ErrorsNeverRelocated(t *testing.T) {
	m := newTestModel(t)
	m.agentEvents = make(chan agent.Event)

	updated, _ := m.Update(agentEventMsg(agent.Event{Kind: agent.EventError, ToolName: "mesh_call", Err: fmt.Errorf("boom")}))
	m = updated.(Model)
	if len(m.chatEntries) != 1 || m.chatEntries[0].kind != chatError {
		t.Fatalf("expected the error inline in chat, got %+v", m.chatEntries)
	}
}

func TestHandleAgentEvent_ToolCallInlineWhenVerbose(t *testing.T) {
	m := newTestModel(t)
	m.agentEvents = make(chan agent.Event)
	m.showChatter = true

	updated, _ := m.Update(agentEventMsg(agent.Event{Kind: agent.EventToolCall, ToolName: "mesh_call", Text: "{}"}))
	m = updated.(Model)
	if len(m.chatEntries) != 1 || m.chatEntries[0].kind != chatToolCall {
		t.Fatalf("expected the tool call inline in chat when verbose, got %+v", m.chatEntries)
	}
}

func TestNormalMode_VTogglesChatter(t *testing.T) {
	m := newTestModel(t)
	if m.showChatter {
		t.Fatalf("expected chatter relocated by default")
	}
	updated, _ := m.Update(runeKey('v'))
	m = updated.(Model)
	if !m.showChatter {
		t.Fatalf("expected 'v' to enable verbose chatter")
	}
}

func TestInsertMode_VDoesNotToggleChatter_TypesInstead(t *testing.T) {
	m := newTestModel(t)
	updated, _ := m.Update(runeKey('i'))
	m = updated.(Model)
	updated, _ = m.Update(runeKey('v'))
	m = updated.(Model)
	if m.showChatter {
		t.Fatalf("'v' while composing must not toggle chatter mode")
	}
	if m.input.Value() != "v" {
		t.Fatalf("expected 'v' to be typed into the input, got %q", m.input.Value())
	}
}

// Issue #2: editor-based composition, from either mode.
func TestOpenEditor_WorksFromNormalAndInsertMode(t *testing.T) {
	for _, start := range []Mode{ModeNormal, ModeInsert} {
		m := newTestModel(t)
		m.mode = start
		_, cmd := m.Update(typeKey(tea.KeyCtrlE))
		if cmd == nil {
			t.Fatalf("expected ctrl+e to return a command from mode %v", start)
		}
	}
}

func TestHandleEditorFinished_LoadsContentAndEntersInsert(t *testing.T) {
	m := newTestModel(t)
	f, err := os.CreateTemp("", "lazymesh-test-*.md")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString("composed in $EDITOR"); err != nil {
		t.Fatalf("WriteString: %v", err)
	}
	f.Close()

	updated, cmd := m.Update(editorFinishedMsg{path: f.Name()})
	m = updated.(Model)
	if m.mode != ModeInsert {
		t.Fatalf("expected insert mode after the editor round-trip, got %v", m.mode)
	}
	if m.input.Value() != "composed in $EDITOR" {
		t.Fatalf("expected the edited content loaded into input, got %q", m.input.Value())
	}
	if cmd == nil {
		t.Fatalf("expected a focus command")
	}
	if _, err := os.Stat(f.Name()); !os.IsNotExist(err) {
		t.Fatalf("expected the temp file to be cleaned up, stat err: %v", err)
	}
}

func TestHandleEditorFinished_ErrorSurfacesAsChatEntry(t *testing.T) {
	m := newTestModel(t)
	updated, _ := m.Update(editorFinishedMsg{err: fmt.Errorf("boom")})
	m = updated.(Model)
	if len(m.chatEntries) != 1 || m.chatEntries[0].kind != chatError {
		t.Fatalf("expected a chatError entry on editor failure, got %+v", m.chatEntries)
	}
}

// Regression guard, found live 2026-09-06: a tool result containing
// pretty-printed JSON (raw newlines) landed in the chatter line and
// secretly rendered as more than one terminal row, so
// resizeComponents' `len(m.statusLines()) + 2` reserved-height math
// (one slice element assumed to be one row) undercounted -- the chat
// viewport visibly jumped on every update. statusLines()'s own line
// count must always match what actually prints as one row each.
func TestStatusLines_ChatterLineNeverContainsEmbeddedNewlines(t *testing.T) {
	m := newTestModel(t)
	m.agentEvents = make(chan agent.Event)

	prettyJSON := "{\n  \"rooms\": [\n    \"agents.room.deadbeef\"\n  ]\n}"
	updated, _ := m.Update(agentEventMsg(agent.Event{Kind: agent.EventToolResult, ToolName: "mesh_rooms", Text: prettyJSON}))
	m = updated.(Model)

	for i, line := range m.statusLines() {
		if strings.Contains(line, "\n") {
			t.Fatalf("statusLines()[%d] contains an embedded newline, breaking the one-element-one-row assumption: %q", i, line)
		}
	}
	if got, want := len(strings.Split(m.renderStatusStrip(), "\n")), len(m.statusLines()); got != want {
		t.Fatalf("renderStatusStrip rendered as %d rows, but resizeComponents counted %d via statusLines()", got, want)
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
