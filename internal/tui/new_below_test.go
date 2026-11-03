package tui

// #78 gap 7: scrolled up in the history, a reader could not tell that
// output had arrived below. Follow mode stays as it is (nobody is yanked
// to the bottom); the chat says that something is there, and names the
// key that takes you to it.

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/macula-io/macula-lazymesh/internal/agent"
)

// deliverAnswer sends one finished assistant message through Update, the
// way the agent's stream reaches the model.
func deliverAnswer(t *testing.T, m Model, text string) Model {
	t.Helper()
	next, _ := m.handleAgentEvent(agentEventMsg(agent.Event{Kind: agent.EventAssistantMessage, Text: text}))
	return next
}

// TestNewBelowMarker_ShownWhenScrolledUpClearedByBottomKey is the issue's
// check, under a leader that is not the default, so a marker that spells
// the key out instead of reading it from the keymap fails here.
func TestNewBelowMarker_ShownWhenScrolledUpClearedByBottomKey(t *testing.T) {
	m := modelWithLeader(t, "ctrl+g")
	m = fillChat(t, m, 30)
	m.chatViewport.GotoTop()

	m = deliverAnswer(t, m, "an answer that arrived below")
	view := m.View()
	if !strings.Contains(view, "new below") {
		t.Fatalf("scrolled up, a delivered answer left no \"new below\" marker:\n%s", view)
	}
	want := m.leaderLabel() + " " + DefaultKeyMap.ScrollBottom.Help().Key
	if !strings.Contains(view, want) {
		t.Fatalf("the marker does not name the bottom key as %q:\n%s", want, view)
	}

	m, _ = pressLeader(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(DefaultKeyMap.ScrollBottom.Keys()[0])})
	if !m.chatViewport.AtBottom() {
		t.Fatal("the bottom key did not reach the bottom")
	}
	if view := m.View(); strings.Contains(view, "new below") {
		t.Fatalf("the bottom key left the marker on screen:\n%s", view)
	}
}

// TestNewBelowMarker_NotShownWithoutNewOutput: scrolling up alone, or a
// re-render that adds nothing (a resize), is not news.
func TestNewBelowMarker_NotShownWithoutNewOutput(t *testing.T) {
	m := modelWithLeader(t, "")
	m = fillChat(t, m, 30)
	m.chatViewport.GotoTop()
	m.syncViewport()
	if view := m.View(); strings.Contains(view, "new below") {
		t.Fatalf("nothing arrived, yet the marker is on screen:\n%s", view)
	}
}

// TestNewBelowMarker_SeenOnceScrolledToTheBottom: reaching the bottom by
// any route counts as having seen it, so scrolling back up does not bring
// back a marker for output already read.
func TestNewBelowMarker_SeenOnceScrolledToTheBottom(t *testing.T) {
	m := modelWithLeader(t, "")
	m = fillChat(t, m, 30)
	m.chatViewport.GotoTop()
	m = deliverAnswer(t, m, "an answer that arrived below")

	for i := 0; i < 50 && !m.chatViewport.AtBottom(); i++ {
		updated, _ := m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown})
		m = updated.(Model)
	}
	if !m.chatViewport.AtBottom() {
		t.Fatal("test setup: the wheel never reached the bottom")
	}
	updated, _ := m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp})
	m = updated.(Model)
	if view := m.View(); strings.Contains(view, "new below") {
		t.Fatalf("output already scrolled past came back as new:\n%s", view)
	}
}

// TestNewBelowMarker_NotShownAtBottom: a reader at the bottom follows the
// output and sees it; there is nothing to point at.
func TestNewBelowMarker_NotShownAtBottom(t *testing.T) {
	m := modelWithLeader(t, "")
	m = fillChat(t, m, 30)
	m.chatViewport.GotoBottom()
	m = deliverAnswer(t, m, "an answer in plain sight")
	if view := m.View(); strings.Contains(view, "new below") {
		t.Fatalf("at the bottom, the marker is on screen:\n%s", view)
	}
}

// TestNewBelowMarker_ShownWhenAToolResultFolds: a tool result folds into
// its call's entry, which changes neither the entry count nor the last
// entry, and is still news (r2 of #78 gap 7, Terra's probe A).
func TestNewBelowMarker_ShownWhenAToolResultFolds(t *testing.T) {
	m := modelWithLeader(t, "")
	m.agentEvents = make(chan agent.Event)
	m = fillChat(t, m, 30)
	m = sendAgentEvents(t, m, agent.Event{Kind: agent.EventToolCall, ToolName: "shell_exec", Text: `{"command":"go test ./..."}`})
	m.chatViewport.GotoTop()
	n := len(m.chatEntries)
	m = sendAgentEvents(t, m, agent.Event{Kind: agent.EventToolResult, ToolName: "shell_exec", Text: shellResult(t, 1, "FAIL pkg\n")})
	if len(m.chatEntries) != n {
		t.Fatalf("test setup: the result did not fold into its call (%d -> %d entries)", n, len(m.chatEntries))
	}
	if view := m.View(); !strings.Contains(view, "new below") {
		t.Fatalf("a tool result folded in below a reader scrolled up left no marker:\n%s", view)
	}
}

// TestNewBelowMarker_NotShownForAResizeOrAToggle: a re-render that brings
// nothing new is not news, however much the text on screen moved.
func TestNewBelowMarker_NotShownForAResizeOrAToggle(t *testing.T) {
	m := modelWithLeader(t, "")
	m = fillChat(t, m, 30)
	m.chatViewport.GotoTop()

	m = send(t, m, tea.WindowSizeMsg{Width: 60, Height: 24})
	m.syncViewport()
	if view := m.View(); strings.Contains(view, "new below") {
		t.Fatalf("a resize put the marker up:\n%s", view)
	}
	m.chatViewport.GotoTop()
	m, _ = pressLeader(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(DefaultKeyMap.ToggleDetails.Keys()[0])})
	if view := m.View(); strings.Contains(view, "new below") {
		t.Fatalf("expanding tool detail put the marker up:\n%s", view)
	}
}

// TestNewBelowMarker_FitsANarrowTerminal: the marker never wraps, and keeps
// the key for as long as the width allows (Terra's probe B).
func TestNewBelowMarker_FitsANarrowTerminal(t *testing.T) {
	for _, width := range []int{30, 16, 8} {
		m := modelWithLeader(t, "")
		m = send(t, m, tea.WindowSizeMsg{Width: width, Height: 20})
		m = fillChat(t, m, 30)
		m.chatViewport.GotoTop()
		m = deliverAnswer(t, m, "an answer that arrived below")

		lines := strings.Split(m.chatBody(), "\n")
		marker := lines[len(lines)-1]
		if !strings.Contains(marker, "↓") {
			t.Fatalf("width %d: no marker on the chat's bottom line: %q", width, marker)
		}
		if w := lipgloss.Width(marker); w > width {
			t.Fatalf("width %d: the marker is %d cells wide: %q", width, w, marker)
		}
		chord := m.leaderLabel() + " " + DefaultKeyMap.ScrollBottom.Help().Key
		if width >= len([]rune("↓ "+chord)) && !strings.Contains(marker, chord) {
			t.Fatalf("width %d: the marker dropped the key %q though it fits: %q", width, chord, marker)
		}
	}
}
