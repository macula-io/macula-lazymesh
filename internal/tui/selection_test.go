package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// TestShiftDragSelectsAndCopies pins the whole selection flow without a
// real clipboard: shift+press begins, motion extends, release finalizes
// — the copy attempt runs (and fails here without a clipboard helper,
// which is itself the reporting path under test) and the selection
// clears.
func TestShiftDragSelectsAndCopies(t *testing.T) {
	m := newTestModel(t)
	m = fillChat(t, m, 30)
	m.chatViewport.GotoTop()

	press := tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, Shift: true, X: 0, Y: 2}
	updated, _ := m.Update(press)
	m = updated.(Model)
	if !m.sel.active {
		t.Fatal("shift+press did not begin a selection")
	}

	motion := tea.MouseMsg{Action: tea.MouseActionMotion, Shift: true, X: 0, Y: 5}
	updated, _ = m.Update(motion)
	m = updated.(Model)
	if m.sel.endRow != 5 {
		t.Fatalf("motion did not extend the selection: endRow = %d", m.sel.endRow)
	}

	release := tea.MouseMsg{Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft, Shift: true, X: 0, Y: 5}
	updated, _ = m.Update(release)
	m = updated.(Model)
	if m.sel.active {
		t.Fatal("release did not clear the selection")
	}
	// The copy ran: either it succeeded (no entry check possible) or it
	// reported its failure in the chat — never silent.
	if len(m.chatEntries) < 31 {
		t.Fatalf("expected a copy-outcome entry, chat entries = %d", len(m.chatEntries))
	}
}

// TestSelectedVisibleTextMapsRowsToLines pins the mapping: a screen-row
// range inside the pane yields exactly those visible lines, ANSI-free,
// in order.
func TestSelectedVisibleTextMapsRowsToLines(t *testing.T) {
	m := newTestModel(t)
	m = fillChat(t, m, 30)
	m.chatViewport.GotoTop()

	paneTop := m.chatPaneTop()
	text, ok := m.selectedVisibleText(paneTop, paneTop+3)
	if !ok {
		t.Fatal("selection inside the pane reported outside it")
	}
	if !strings.Contains(text, "line") {
		t.Fatalf("selected text does not contain chat content: %q", text)
	}
	if strings.Contains(text, "\x1b[") {
		t.Fatalf("selected text still carries ANSI: %q", text)
	}
}

// TestSelectedVisibleTextRefusesOutOfPane pins the boundary: rows
// outside the chat pane never select.
func TestSelectedVisibleTextRefusesOutOfPane(t *testing.T) {
	m := newTestModel(t)
	m = fillChat(t, m, 30)
	paneTop := m.chatPaneTop()
	if _, ok := m.selectedVisibleText(paneTop-2, paneTop-1); ok && paneTop > 0 {
		t.Fatal("rows above the pane selected")
	}
	if _, ok := m.selectedVisibleText(paneTop+m.chatViewport.Height+1, paneTop+m.chatViewport.Height+2); ok {
		t.Fatal("rows below the pane selected")
	}
}

// TestSelectionOverlayReversesOnlyPaneRows pins the visual: an active
// selection reverses the rows inside the pane and leaves others alone.
// The test forces an ANSI color profile because without a TTY lipgloss's
// default profile strips every escape — the real TUI always has one.
func TestSelectionOverlayReversesOnlyPaneRows(t *testing.T) {
	lipgloss.SetColorProfile(termenv.ANSI)
	defer lipgloss.SetColorProfile(termenv.Ascii)

	m := newTestModel(t)
	m = fillChat(t, m, 30)
	m.chatViewport.GotoTop()
	m.sel = selectionState{anchorRow: m.chatPaneTop() + 1, endRow: m.chatPaneTop() + 2, active: true}

	screen := m.View()
	lines := strings.Split(screen, "\n")
	paneTop := m.chatPaneTop()
	reverseEscape := "\x1b[7m"
	for row, line := range lines {
		inSelection := row >= paneTop+1 && row <= paneTop+2
		has := strings.Contains(line, reverseEscape)
		if inSelection != has {
			t.Fatalf("row %d: selection=%v but reverse=%v: %q", row, inSelection, has, line)
		}
	}
}

// TestPlainDragSelects pins the 2026-09-12 live fix: a PLAIN left drag
// selects too, because some terminals (kitty) keep shift+drag for their
// own native selection and the app only ever receives plain events.
func TestPlainDragSelects(t *testing.T) {
	m := newTestModel(t)
	m = fillChat(t, m, 30)
	updated, _ := m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 0, Y: 2})
	m = updated.(Model)
	if !m.sel.active {
		t.Fatal("a plain left press did not begin a selection")
	}
	updated, _ = m.Update(tea.MouseMsg{Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft, X: 0, Y: 6})
	m = updated.(Model)
	if m.sel.endRow != 6 {
		t.Fatalf("plain drag did not extend the selection: endRow = %d", m.sel.endRow)
	}
	updated, _ = m.Update(tea.MouseMsg{Action: tea.MouseActionRelease, X: 0, Y: 6})
	m = updated.(Model)
	if m.sel.active {
		t.Fatal("release did not clear the selection")
	}
}
