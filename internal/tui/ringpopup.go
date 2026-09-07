package tui

import (
	"context"
	"fmt"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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
func answerRingCmd(mcp toolCaller, ringID string, answer int, alsoTrust bool, peerNodeID string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		_, err := mcp.CallTool(ctx, "mesh_answer_ring", map[string]any{"ring_id": ringID, "answer": answer})
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
		m.mode = ModeNormal
		m.resizeComponents() // hint row goes from 1 line (Ring) to 2 (Normal)
		return m, nil
	}

	switch {
	case key.Matches(msg, DefaultKeyMap.Answer):
		m.seenRingIDs[r.RingID] = true
		m.pendingRingPopup = nil
		m.mode = ModeNormal
		m.resizeComponents() // hint row goes from 1 line (Ring) to 2 (Normal)
		return m, answerRingCmd(m.mcp, r.RingID, answerAccept, false, "")
	case key.Matches(msg, DefaultKeyMap.Decline):
		m.seenRingIDs[r.RingID] = true
		m.pendingRingPopup = nil
		m.mode = ModeNormal
		m.resizeComponents() // hint row goes from 1 line (Ring) to 2 (Normal)
		return m, answerRingCmd(m.mcp, r.RingID, answerDecline, false, "")
	case key.Matches(msg, DefaultKeyMap.Trust):
		m.seenRingIDs[r.RingID] = true
		m.pendingRingPopup = nil
		m.mode = ModeNormal
		m.resizeComponents() // hint row goes from 1 line (Ring) to 2 (Normal)
		return m, answerRingCmd(m.mcp, r.RingID, answerAccept, true, r.Peer)
	case key.Matches(msg, DefaultKeyMap.Normal): // Esc
		m.seenRingIDs[r.RingID] = true
		m.pendingRingPopup = nil
		m.mode = ModeNormal
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
	m.chatEntries = append(m.chatEntries, chatEntry{kind: chatSystem, at: time.Now(), text: text})
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
func (m Model) renderRingPopup() string {
	r := m.pendingRingPopup
	if r == nil {
		return ""
	}
	name := displayName(r.Peer, r.PeerPetname)
	purpose := r.Purpose
	if purpose == "" {
		purpose = "(no purpose given)"
	}
	body := fmt.Sprintf(
		"%s\n\nIncoming ring from %s\n\n%s\n\n%s",
		popupTitleStyle.Render("☎ Incoming ring"),
		name,
		purpose,
		popupHintStyle.Render("[a] Answer   [d] Decline   [t] Answer + Trust   [esc] Later"),
	)
	return popupBorderStyle.Render(body)
}
