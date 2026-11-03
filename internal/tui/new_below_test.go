package tui

// #78 gap 7: scrolled up in the history, a reader could not tell that
// output had arrived below. Follow mode stays as it is (nobody is yanked
// to the bottom); the chat says that something is there, and names the
// key that takes you to it.

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

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
