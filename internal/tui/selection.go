package tui

import (
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/atotto/clipboard"
)

// sgrStripper removes SGR escape sequences, for turning the rendered
// chat lines into the plain text a clipboard wants.
var sgrStripper = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// selectionState is an in-progress shift+drag text selection over the
// chat pane, in terminal row coordinates (0 = the top screen row). The
// terminal does NOT select for an app that has mouse reporting enabled —
// the shift modifier arrives as a flag on the mouse events themselves —
// so selection is the app's job (found live 2026-09-12).
type selectionState struct {
	anchorRow int
	endRow    int
	active    bool
}

// rows returns the selection's row range, ordered.
func (s selectionState) rows() (int, int) {
	if s.endRow < s.anchorRow {
		return s.endRow, s.anchorRow
	}
	return s.anchorRow, s.endRow
}

// chatPaneTop is the first screen row of the chat pane: below the status
// block when the bar sits on top, row 0 otherwise.
func (m Model) chatPaneTop() int {
	if m.statusBarPosition == "top" {
		return len(m.statusLines())
	}
	return 0
}

// handleMouse routes mouse events: a left-button drag begins, extends
// and ends a chat-pane selection (copied to the clipboard on release);
// the wheel scrolls the chat when it is the surface on screen. Overlays
// own the screen while open, and popup modes answer keys, not mice.
//
// A PLAIN drag selects, not just shift+drag (2026-09-12, live): some
// terminals (kitty) consume shift+drag for their own native selection
// and never hand it to the app, while others hand everything over — so
// the app must select on the plain events it is guaranteed to receive,
// and the shift convention degrades gracefully either way.
func (m Model) handleMouse(msg tea.MouseMsg) (Model, tea.Cmd) {
	// Debug-level trace of every mouse event, because a live "selection
	// does not work" report can only be diagnosed against what the
	// terminal ACTUALLY sent (action/button/shift/x/y) — these lines
	// land in agent.log via cmd/lazymesh's default slog wiring, never on
	// the screen.
	slog.Debug("tui: mouse event", "action", int(msg.Action), "button", int(msg.Button), "shift", msg.Shift, "x", msg.X, "y", msg.Y, "selection_active", m.sel.active)
	switch {
	case msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft:
		if m.chatOnScreen() {
			m.sel = selectionState{anchorRow: msg.Y, endRow: msg.Y, active: true}
		}
		return m, nil
	case m.sel.active && msg.Action == tea.MouseActionMotion:
		m.sel.endRow = msg.Y
		return m, nil
	case m.sel.active && msg.Action == tea.MouseActionRelease:
		return m.finishSelection()
	case m.sel.active:
		// A wheel or other button while a selection is in flight: the
		// selection owns the mouse until it is released.
		return m, nil
	}

	if !m.chatOnScreen() {
		return m, nil
	}
	var cmd tea.Cmd
	m.chatViewport, cmd = m.chatViewport.Update(msg)
	return m, cmd
}

// chatOnScreen reports whether the plain chat pane is the surface being
// shown — the only surface a selection may start on.
func (m Model) chatOnScreen() bool {
	if m.meshExpanded || m.realmExpanded || m.meshServicesExpanded {
		return false
	}
	return m.mode == ModeNormal || m.mode == ModeInsert
}

// finishSelection copies the selected visible rows to the clipboard and
// clears the selection, reporting the outcome in the chat — a copy that
// silently fails is worse than one that names its reason (the clipboard
// helper is legitimately absent over a bare SSH session).
func (m Model) finishSelection() (Model, tea.Cmd) {
	top, bottom := m.sel.rows()
	m.sel = selectionState{}

	text, ok := m.selectedVisibleText(top, bottom)
	if !ok {
		m.chatEntries = append(m.chatEntries, chatEntry{kind: chatSystem, at: time.Now(), text: "selection is outside the chat pane"})
		m.syncViewport()
		return m, nil
	}
	if err := clipboard.WriteAll(text); err != nil {
		m.chatEntries = append(m.chatEntries, chatEntry{kind: chatError, at: time.Now(), text: fmt.Sprintf("could not copy selection to clipboard: %v", err)})
		m.syncViewport()
		return m, nil
	}
	m.chatEntries = append(m.chatEntries, chatEntry{kind: chatSystem, at: time.Now(), text: fmt.Sprintf("copied %d selected line(s) to the clipboard", strings.Count(text, "\n")+1)})
	m.syncViewport()
	return m, nil
}

// selectedVisibleText maps a screen-row range onto the chat viewport's
// visible lines and returns them as plain text (ANSI stripped): what the
// operator selected is what they saw.
func (m Model) selectedVisibleText(topRow, bottomRow int) (string, bool) {
	paneTop := m.chatPaneTop()
	topRow -= paneTop
	bottomRow -= paneTop
	if bottomRow < 0 || topRow >= m.chatViewport.Height {
		return "", false
	}
	if topRow < 0 {
		topRow = 0
	}
	if bottomRow >= m.chatViewport.Height {
		bottomRow = m.chatViewport.Height - 1
	}
	lines := m.chatContentLines()
	start := m.chatViewport.YOffset + topRow
	end := m.chatViewport.YOffset + bottomRow
	if start >= len(lines) {
		return "", false
	}
	if end >= len(lines) {
		end = len(lines) - 1
	}
	visible := make([]string, 0, end-start+1)
	for i := start; i <= end; i++ {
		visible = append(visible, sgrStripper.ReplaceAllString(lines[i], ""))
	}
	return strings.Join(visible, "\n"), true
}

// overlaySelection returns the rendered screen with the selected rows
// reversed, so a drag shows what it is about to copy. Rows outside the
// chat pane are never highlighted.
func (m Model) overlaySelection(screen string) string {
	if !m.sel.active {
		return screen
	}
	top, bottom := m.sel.rows()
	paneTop := m.chatPaneTop()
	paneBottom := paneTop + m.chatViewport.Height - 1
	if bottom < paneTop || top > paneBottom {
		return screen
	}
	if top < paneTop {
		top = paneTop
	}
	if bottom > paneBottom {
		bottom = paneBottom
	}
	lines := strings.Split(screen, "\n")
	for row := top; row <= bottom && row < len(lines); row++ {
		lines[row] = selectionStyle.Render(lines[row])
	}
	return strings.Join(lines, "\n")
}

// selectionStyle is the inverse-video highlight for the in-progress
// selection.
var selectionStyle = lipgloss.NewStyle().Reverse(true)
