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
// (#112): the name and the purpose wrap to the room the border and padding
// leave, and the hint row drops whole actions rather than running past the
// edge. A box wider than the terminal is wrapped by the terminal itself,
// which pushes the screen off by a row -- the class #86 closed for the
// status strip.
func (m Model) renderRingPopup() string {
	r := m.pendingRingPopup
	if r == nil {
		return ""
	}
	inner := ringPopupInnerWidth(m.width)
	// Both are the peer's own text (#81): shown, never acted on.
	nameLines := wrapCells(termTextOf("Incoming ring from "+collapseNewlines(displayName(r.Peer, r.PeerPetname))), inner)
	purpose := termTextOf(r.Purpose)
	if purpose == "" {
		purpose = "(no purpose given)"
	}
	purposeLines := wrapCells(purpose, inner)
	var hint string
	if m.ringArm.armedAt(m.clock()) {
		hint = fitHintSegments([]hintSegment{
			{"[a] Answer", 0},
			{"[d] Decline", 1},
			{"[t] Answer + Trust", 3},
			{"[esc] Later", 2},
		}, "   ", inner)
	} else {
		hint = fitHintSegments([]hintSegment{
			{armHint(), 0},
			{"[esc] Later", 1},
		}, "   ", inner)
	}
	lines := append([]string{popupTitleStyle.Render("☎ Incoming ring"), ""}, nameLines...)
	lines = append(lines, "")
	lines = append(lines, purposeLines...)
	lines = append(lines, "", popupHintStyle.Render(hint))
	return popupBorderStyle.Render(strings.Join(lines, "\n"))
}

// ringPopupInnerWidth is the widest content line the ring pop-up may hold
// on a terminal width cells wide: its thick border takes one cell each
// side and its padding two, so six cells of the box are chrome (#112). An
// unknown width (before the first resize) wraps nothing; a very narrow one
// gets a floor rather than a nonsense width.
func ringPopupInnerWidth(width int) int {
	if width <= 0 {
		return 1 << 20
	}
	inner := width - 6
	if inner < 10 {
		return 10
	}
	return inner
}
