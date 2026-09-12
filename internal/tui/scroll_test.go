package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/macula-io/macula-lazymesh/internal/agent"
)

// fillChat grows the chat pane past the viewport height, so scrolling
// has somewhere to go, and returns the model.
func fillChat(t *testing.T, m Model, entries int) Model {
	t.Helper()
	for i := 0; i < entries; i++ {
		m.chatEntries = append(m.chatEntries, chatEntry{kind: chatAssistant, at: time.Now(), text: strings.Repeat("line\n", 3)})
	}
	m.syncViewport()
	return m
}

// TestSyncViewportFollowsOnlyWhenAtBottom pins the follow-mode contract:
// new content re-anchors the bottom only for someone already reading the
// bottom; someone scrolled up in the history keeps their place while
// entries (and streaming deltas) arrive below.
func TestSyncViewportFollowsOnlyWhenAtBottom(t *testing.T) {
	m := newTestModel(t)
	m = fillChat(t, m, 30)

	// Scroll up into the history, then append + sync: the offset must
	// stay put.
	m.chatViewport.GotoTop()
	before := m.chatViewport.YOffset
	if before == 0 && m.chatViewport.AtBottom() {
		t.Fatal("test setup: chat did not overflow the viewport")
	}
	m.chatEntries = append(m.chatEntries, chatEntry{kind: chatYou, at: time.Now(), text: "new message below"})
	m.syncViewport()
	if m.chatViewport.YOffset != before {
		t.Fatalf("scrolled-up reader was yanked: offset %d -> %d", before, m.chatViewport.YOffset)
	}

	// At the bottom, new content follows.
	m.chatViewport.GotoBottom()
	m.chatEntries = append(m.chatEntries, chatEntry{kind: chatYou, at: time.Now(), text: "another one"})
	m.syncViewport()
	if !m.chatViewport.AtBottom() {
		t.Fatal("bottom reader did not follow new content")
	}
}

// TestMouseWheelScrollsChat pins the mouse half: with the program
// running WithMouseCellMotion, a wheel press scrolls the chat viewport
// when the chat is the surface on screen.
func TestMouseWheelScrollsChat(t *testing.T) {
	m := newTestModel(t)
	m = fillChat(t, m, 30)
	m.chatViewport.GotoTop()

	updated, _ := m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown})
	m = updated.(Model)
	if m.chatViewport.YOffset == 0 {
		t.Fatal("wheel down did not scroll the chat")
	}
}

// TestMouseWheelIgnoredUnderOverlay pins the boundary: while an overlay
// owns the screen, the wheel must not move the chat hidden beneath it.
func TestMouseWheelIgnoredUnderOverlay(t *testing.T) {
	m := newTestModel(t)
	m = fillChat(t, m, 30)
	m.chatViewport.GotoTop()
	m.meshExpanded = true

	updated, _ := m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown})
	m = updated.(Model)
	if m.chatViewport.YOffset != 0 {
		t.Fatal("wheel moved the chat under an open overlay")
	}
}

// TestPageKeysScrollChat pins the page-key forwarding: pgdown scrolls
// the chat when no overlay is open.
func TestPageKeysScrollChat(t *testing.T) {
	m := newTestModel(t)
	m = fillChat(t, m, 30)
	m.chatViewport.GotoTop()

	updated, _ := m.Update(typeKey(tea.KeyPgDown))
	m = updated.(Model)
	if m.chatViewport.YOffset == 0 {
		t.Fatal("pgdown did not scroll the chat")
	}
}

// TestStreamingDeltaDoesNotYankScrolledUpReader pins the live symptom
// end to end: a streaming delta arriving while the reader is scrolled up
// leaves their position alone (this was the "can't scroll during
// conversation" behavior).
func TestStreamingDeltaDoesNotYankScrolledUpReader(t *testing.T) {
	m := newTestModel(t)
	m = fillChat(t, m, 30)
	m.chatViewport.GotoTop()
	before := m.chatViewport.YOffset

	update := func(ev agent.Event) Model {
		next, _ := m.Update(agentEventMsg(ev))
		return next.(Model)
	}
	m = update(agent.Event{Kind: agent.EventAssistantDelta, Text: "streaming chunk"})
	if m.chatViewport.YOffset != before {
		t.Fatalf("a delta yanked a scrolled-up reader: offset %d -> %d", before, m.chatViewport.YOffset)
	}
}
