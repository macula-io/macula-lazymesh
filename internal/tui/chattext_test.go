package tui

import (
	"strings"
	"testing"

	"github.com/macula-io/macula-lazymesh/internal/agent"
)

// hostileText carries what a model, or a peer through the model, could
// write to make the operator's terminal act: an OSC 52 clipboard write,
// an OSC 0 title write, a CSI clear screen, a C1 CSI, and a carriage
// return that would let "rm -rf ~" hide behind "ls" on the same row.
const hostileText = "fine " +
	"\x1b]52;c;cm0gLXJmIH4=\x07" +
	"\x1b]0;owned\x1b\\" +
	"\x1b[2J" +
	"\u009b2J" +
	"rm -rf ~\rls and the rest"

var hostileBytes = []string{"]52;", "]0;", "\x07", "\r", "[2J", "\u009b", "cm0gLXJmIH4=", "owned"}

func assertNoTerminalControl(t *testing.T, what, got string) {
	t.Helper()
	for _, banned := range hostileBytes {
		if strings.Contains(got, banned) {
			t.Fatalf("%s: %q reached the terminal:\n%q", what, banned, got)
		}
	}
	// Visible text is what is left once our own colour codes are gone and
	// the renderer's wrapping is folded back to single spaces.
	visible := strings.Join(strings.Fields(sgrStripper.ReplaceAllString(got, "")), " ")
	for _, want := range []string{"fine", "rm -rf ~", "ls and the rest"} {
		if !strings.Contains(visible, want) {
			t.Fatalf("%s: visible text %q was lost:\n%q", what, want, got)
		}
	}
}

// #81: the model's answer is not trusted text -- peer room text flows into
// its context -- so an answer, finished or still streaming, prints none of
// the sequences in it.
func TestChatText_AssistantAnswerCannotDriveTheTerminal(t *testing.T) {
	finished := chatEntryFromAgentEvent(agent.Event{Kind: agent.EventAssistantMessage, Text: hostileText})
	assertNoTerminalControl(t, "finished answer", finished.render(false, 80))

	m := newTestModelWithEvents(t)
	m = sendAgentEvents(t, m, agent.Event{Kind: agent.EventAssistantDelta, Text: hostileText})
	assertNoTerminalControl(t, "streaming answer", chatText(m))
}

// Every other entry kind renders through the same door: a system line can
// carry a peer's ring purpose, an error a tool's or a provider's message.
func TestChatText_EveryEntryKindIsNeutralised(t *testing.T) {
	for name, entry := range map[string]chatEntry{
		"you":    youChatEntry(hostileText),
		"system": {kind: chatSystem, text: hostileText},
		"error":  {kind: chatError, text: hostileText},
		"result": {kind: chatToolResult, tool: "mesh_rooms", text: hostileText, detail: hostileText},
	} {
		for _, expanded := range []bool{false, true} {
			assertNoTerminalControl(t, name, entry.render(expanded, 80))
		}
	}
}

// A sequence split across streaming deltas -- ESC at the end of one chunk,
// "]52;..." at the start of the next -- is neutralised too: the whole
// accumulated answer is what gets neutralised, never one chunk alone.
func TestChatText_SequenceSplitAcrossDeltas(t *testing.T) {
	m := newTestModelWithEvents(t)
	m = sendAgentEvents(t, m,
		agent.Event{Kind: agent.EventAssistantDelta, Text: "fine \x1b"},
		agent.Event{Kind: agent.EventAssistantDelta, Text: "]52;c;cm0gLXJmIH4=\x07"},
		agent.Event{Kind: agent.EventAssistantDelta, Text: "rm -rf ~\r"},
		agent.Event{Kind: agent.EventAssistantDelta, Text: "ls and the rest"},
	)
	assertNoTerminalControl(t, "split deltas", chatText(m))
}

// An attached TUI (lazymesh attach) renders the session's events through
// the same model: its chat is as clean as a local one.
func TestChatText_AttachedPathIsNeutralised(t *testing.T) {
	events := make(chan agent.Event)
	m := New(nil, Options{UserInputCh: make(chan string, 8), AgentEvents: events, Attached: true, StatusBarPosition: "bottom"})
	m.agentEvents = events
	m = sendAgentEvents(t, m,
		agent.Event{Kind: agent.EventAssistantMessage, Text: hostileText},
		agent.Event{Kind: agent.EventError, Err: errString(hostileText)},
	)
	assertNoTerminalControl(t, "attached chat", chatText(m))
}

type errString string

func (e errString) Error() string { return string(e) }

// What reaches the clipboard is clean too: a selection copies rendered
// rows, and "copy last answer" copies the answer's text -- pasted into a
// terminal, an ESC byte in either would act there.
func TestChatText_ClipboardCopiesAreNeutralised(t *testing.T) {
	m := newTestModelWithEvents(t)
	m = sendAgentEvents(t, m, agent.Event{Kind: agent.EventAssistantMessage, Text: hostileText})
	m.chatViewport.GotoTop()

	paneTop := m.chatPaneTop()
	selected, ok := m.selectedVisibleText(paneTop, paneTop+m.chatViewport.Height-1)
	if !ok {
		t.Fatal("selection inside the pane reported outside it")
	}
	assertNoTerminalControl(t, "selection", selected)

	answer, ok := lastAssistantText(m.chatEntries)
	if !ok {
		t.Fatal("no answer to copy")
	}
	assertNoTerminalControl(t, "copy last answer", answer)
}
