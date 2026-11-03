package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/macula-io/macula-lazymesh/internal/agent"
)

// mesh_answer_ring's own answer values (mirrors the mesh tool's own
// contract -- 1 accept, 2 decline).
const (
	answerAccept  = 1
	answerDecline = 2
)

type ringAnsweredMsg struct {
	peer      string
	accepted  bool
	alsoTrust bool
	err       error
}

// answerRingCmd calls mesh_answer_ring (and, if alsoTrust, mesh_trust_agent
// right after a successful accept) through the same MCP connection the
// mesh-state polling uses. peerNodeID is only needed when alsoTrust is
// set -- mesh_trust_agent is keyed by node_id, never by ring_id.
//
// An accept that joined the ring's room is reported to roomChanged before
// anything else (#97): the answer made this node a member, whatever the
// trust call does next, and the room-waiter must watch the room for the
// caller's next message to wake the agent.
func answerRingCmd(mcp toolCaller, roomChanged func(agent.Event), ringID string, answer int, alsoTrust bool, peerNodeID string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		result, err := mcp.CallTool(ctx, "mesh_answer_ring", map[string]any{"ring_id": ringID, "answer": answer})
		if ev, joined := agent.RoomChangeOf("mesh_answer_ring", result); err == nil && joined && roomChanged != nil {
			roomChanged(ev)
		}
		if err == nil && alsoTrust {
			if _, trustErr := mcp.CallTool(ctx, "mesh_trust_agent", map[string]any{"node_id": peerNodeID}); trustErr != nil {
				err = fmt.Errorf("answered but failed to trust: %w", trustErr)
			}
		}
		return ringAnsweredMsg{peer: peerNodeID, accepted: answer == answerAccept, alsoTrust: alsoTrust, err: err}
	}
}

// handleRingPopupKey is the phone-metaphor pop-up's own key handling:
// Answer, Decline, Answer + Trust, or Esc to leave it for later (the ring
// stays pending -- runAgent's own per-cycle ring-checking, if an agent is
// running, can still pick it up; Esc only suppresses the human-facing
// pop-up for this specific ring, not the whole mechanism).
func (m Model) handleRingPopupKey(msg tea.KeyMsg) (Model, tea.Cmd) {
	r := m.pendingRingPopup
	if r == nil {
		m = m.refocus(m.ringReturnTo)
		m.resizeComponents() // hint row goes from 1 line (Ring) to 2 (Normal)
		return m, nil
	}

	// The same guard the approval pop-up has (#59), for the same reason: this
	// one opens under the cursor too, and `a` is a letter a person types. Esc
	// is not an answer here -- it leaves the ring pending -- so it stays
	// instant, and every key still restarts the guard.
	armed := m.ringArm.armedAt(m.clock())
	m.ringArm.lastKeyAt = m.clock()
	if msg.Paste {
		return m, nil // a paste is typing, not an answer
	}
	if !armed && !key.Matches(msg, DefaultKeyMap.Normal) {
		return m, nil
	}

	switch {
	case key.Matches(msg, DefaultKeyMap.Answer):
		m.seenRingIDs[r.RingID] = true
		m.pendingRingPopup = nil
		m.ringArm = popupArm{}
		m = m.refocus(m.ringReturnTo)
		m.resizeComponents() // hint row goes from 1 line (Ring) to 2 (Normal)
		return m, answerRingCmd(m.mcp, m.roomChanged, r.RingID, answerAccept, false, "")
	case key.Matches(msg, DefaultKeyMap.Decline):
		m.seenRingIDs[r.RingID] = true
		m.pendingRingPopup = nil
		m.ringArm = popupArm{}
		m = m.refocus(m.ringReturnTo)
		m.resizeComponents() // hint row goes from 1 line (Ring) to 2 (Normal)
		return m, answerRingCmd(m.mcp, m.roomChanged, r.RingID, answerDecline, false, "")
	case key.Matches(msg, DefaultKeyMap.Trust):
		m.seenRingIDs[r.RingID] = true
		m.pendingRingPopup = nil
		m.ringArm = popupArm{}
		m = m.refocus(m.ringReturnTo)
		m.resizeComponents() // hint row goes from 1 line (Ring) to 2 (Normal)
		return m, answerRingCmd(m.mcp, m.roomChanged, r.RingID, answerAccept, true, r.Peer)
	case key.Matches(msg, DefaultKeyMap.Normal): // Esc
		m.seenRingIDs[r.RingID] = true
		m.pendingRingPopup = nil
		m.ringArm = popupArm{}
		m = m.refocus(m.ringReturnTo)
		m.resizeComponents() // hint row goes from 1 line (Ring) to 2 (Normal)
		return m, nil
	}
	return m, nil
}

// handleRingAnswered logs the outcome as a chat entry -- there is no
// pop-up left to update by this point, it was already cleared the moment
// the key was pressed, so the operator doesn't wait on the network round
// trip to get their screen back.
func (m Model) handleRingAnswered(msg ringAnsweredMsg) (Model, tea.Cmd) {
	text := "declined a ring"
	if msg.accepted {
		text = "accepted a ring"
		if msg.alsoTrust {
			text += " and added the peer to your trusted contacts"
		}
	}
	if msg.err != nil {
		text = fmt.Sprintf("ring answer failed: %s", msg.err)
	}
	m.chatEntries = append(m.chatEntries, chatEntry{kind: chatSystem, at: time.Now(), text: termTextOf(text)})
	m.syncViewport()
	return m, nil
}

var (
	popupBorderStyle = lipgloss.NewStyle().
				Border(lipgloss.ThickBorder()).
				BorderForeground(lipgloss.Color("#FB923C")).
				Padding(1, 2)
	popupTitleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FB923C"))
	popupHintStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
)

// renderRingPopup is the phone-call-style modal: who's ringing (petname
// over raw hex, same principle as everywhere else), why, and the three
// phone-style actions plus a way to leave it for later.
//
// Everything in it is peer-authored text, and a peer chooses how long its
// purpose is, so the box is laid out for the terminal it is shown on
// (#112): every line of it, the title included, wraps to the room the
// frame leaves, the actions row drops whole actions rather than running
// past the edge, and the box never grows past the rows the screen has --
// the actions are the last thing it would lose. A box wider than the
// terminal is wrapped by the terminal itself, which pushes the screen off
// by a row -- the class #86 closed for the status strip.
func (m Model) renderRingPopup() string {
	r := m.pendingRingPopup
	if r == nil {
		return ""
	}
	rows := m.ringPopupRows()
	inner, frame, verticalChrome := ringPopupFrame(m.width, rows)
	title := wrapCells(termTextOf("☎ Incoming ring"), inner)
	for i := range title {
		title[i] = popupTitleStyle.Render(title[i])
	}
	// Both are the peer's own text (#81): shown, never acted on.
	name := wrapCells(termTextOf("Incoming ring from "+collapseNewlines(displayName(r.Peer, r.PeerPetname))), inner)
	purpose := termTextOf(r.Purpose)
	if purpose == "" {
		purpose = "(no purpose given)"
	}
	purposeLines := wrapCells(purpose, inner)
	hint := m.ringPopupHint(inner)
	if hint != "" {
		hint = popupHintStyle.Render(hint)
	}

	avail := 0 // no limit before the first resize
	if rows >= 0 {
		avail = max(rows-verticalChrome, 1)
	}
	lines := ringPopupLines(title, name, purposeLines, hint, inner, avail)
	for i, line := range lines {
		// A control shown as <U+XXXX> is wider than the last, absurdly
		// narrow widths can hold. Nothing at that width is readable anyway;
		// what must not happen is a row wider than the terminal.
		lines[i] = fitCells(line, inner)
	}
	return frame.Render(strings.Join(lines, "\n"))
}

// The ring pop-up's own chrome: a thick border, one cell on each side and
// one row top and bottom, and one row/column of padding.
const (
	ringPopupHorizontalChrome = 6
	ringPopupVerticalChrome   = 4
)

// ringPopupFrame is what the pop-up is drawn in on a terminal width cells
// wide with rows rows for it (-1 when the terminal has not said how tall it
// is): the thick border with its padding when the screen can hold them, the
// bare cells when it cannot. The old inner floor of ten cells made a box
// sixteen wide, so below sixteen columns the box was wider than the terminal
// and the terminal wrapped it -- and a border that does not fit vertically
// scrolls the screen just the same (#112 r=1). A width of zero (before the
// first resize) wraps nothing.
func ringPopupFrame(width, rows int) (inner int, frame lipgloss.Style, verticalChrome int) {
	if width <= 0 {
		return 1 << 20, popupBorderStyle, ringPopupVerticalChrome
	}
	roomForFrame := rows < 0 || rows > ringPopupVerticalChrome+1
	if inner := width - ringPopupHorizontalChrome; roomForFrame && inner >= 1 {
		return inner, popupBorderStyle, ringPopupVerticalChrome
	}
	return width, lipgloss.NewStyle(), 0
}

// ringPopupRows is how many screen rows the pop-up may take: the terminal's
// height less the status strip that stays visible beside it. -1 means the
// terminal has not said (before the first resize), which is "no limit", not
// "no room".
func (m Model) ringPopupRows() int {
	if m.height <= 0 {
		return -1
	}
	return max(m.height-len(m.statusLines()), 0)
}

// ringPopupHint is the actions row. While the guard sleeps it names the
// guard and the way out; once armed it offers the answers -- but never an
// answer without both refusals beside it (#112 r=1: with accept first, at
// 1..43 columns the row kept "[a] Answer" alone, so the screen showed how
// to accept and not how to refuse).
func (m Model) ringPopupHint(inner int) string {
	if !m.ringArm.armedAt(m.clock()) {
		return fitHintSegments([]hintSegment{
			{armHint(), 0},
			{"[esc] Later", 1},
		}, "   ", inner)
	}
	armed := fitHintSegments([]hintSegment{
		{"[a] Answer", 2},
		{"[d] Decline", 1},
		{"[t] Answer + Trust", 3},
		{"[esc] Later", 0},
	}, "   ", inner)
	if ringHintShowsARefusal(armed) {
		return armed
	}
	return fitHintSegments([]hintSegment{
		{"[d] Decline", 1},
		{"[esc] Later", 0},
	}, "   ", inner)
}

// ringHintShowsARefusal reports whether an armed hint row still names both
// ways to refuse, which is the only state an answer may be offered in.
func ringHintShowsARefusal(hint string) bool {
	return strings.Contains(hint, "[d]") && strings.Contains(hint, "[esc]")
}

// ringPopupLines lays the pop-up's content out in the rows it has. The
// blank separators go first -- they are the only decoration here -- then
// the purpose, whose length a peer chose, and only then the name; the
// title and the actions row are the last things dropped. Shortened parts
// end in a marker saying how many rows are not shown (#112 r=1: a
// sixty-line purpose made a 73-row view on a 24-row terminal, and the
// actions row was off screen). avail <= 0 means no limit.
func ringPopupLines(title, name, purpose []string, hint string, inner, avail int) []string {
	hintRows := 0
	if hint != "" {
		hintRows = 1
	}
	compose := func(title, name, purpose []string, blanks bool) []string {
		out := append([]string{}, title...)
		if blanks {
			out = append(out, "")
		}
		out = append(out, name...)
		if len(purpose) > 0 {
			if blanks {
				out = append(out, "")
			}
			out = append(out, purpose...)
		}
		if hintRows > 0 {
			if blanks {
				out = append(out, "")
			}
			out = append(out, hint)
		}
		return out
	}

	if avail <= 0 {
		return compose(title, name, purpose, true)
	}
	if out := compose(title, name, purpose, true); len(out) <= avail {
		return out
	}
	if out := compose(title, name, purpose, false); len(out) <= avail {
		return out
	}
	if room := avail - len(title) - len(name) - hintRows; room > 0 {
		if out := compose(title, name, clampRows(purpose, room, inner), false); len(out) <= avail {
			return out
		}
	}
	if room := avail - len(title) - hintRows; room > 0 {
		if out := compose(title, clampRows(name, room, inner), nil, false); len(out) <= avail {
			return out
		}
	}
	out := compose(title, name, purpose, false)
	return out[len(out)-avail:]
}

// clampRows shortens lines to at most n rows, spending the last one on a
// marker that says how many are not shown. The marker is itself fitted to
// inner cells, so the row it stands in for is never the wider one.
func clampRows(lines []string, n, inner int) []string {
	if n <= 0 || len(lines) == 0 {
		return nil
	}
	if len(lines) <= n {
		return lines
	}
	marker := ringMoreLinesMarker(len(lines)-(n-1), inner)
	if marker == "" {
		return nil
	}
	return append(append([]string{}, lines[:n-1]...), marker)
}

// ringMoreLinesMarker names how many rows the box is not showing, in the
// widest spelling that fits inner cells.
func ringMoreLinesMarker(hidden, inner int) string {
	unit := "lines"
	if hidden == 1 {
		unit = "line"
	}
	for _, marker := range []string{
		fmt.Sprintf("… %d more %s", hidden, unit),
		fmt.Sprintf("… %d more", hidden),
		"…",
	} {
		if lipgloss.Width(marker) <= inner {
			return marker
		}
	}
	return ""
}
