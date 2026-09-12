package tui

import (
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ApprovalAnswer is the operator's decision on one approval prompt (G9):
// the approval id the answer must carry, and whether the call may run.
type ApprovalAnswer struct {
	ID    string
	Allow bool
}

// pendingApproval is the one approval prompt currently shown.
type pendingApproval struct {
	tool string
	id   string
	args string
}

// approvalStyle matches the other popups' palette: brand orange for the
// tool under question, dim text for the args preview.
var (
	approvalTitleStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#FB923C")).Bold(true)
	approvalArgsStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	approvalHintStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Italic(true)
)

// showApproval surfaces one approval prompt (G9): it takes over the
// screen exactly like a ring popup does — the tool call waits, nothing
// else proceeds until a decision lands.
func (m Model) showApproval(ev ApprovalRequestEvent) (Model, tea.Cmd) {
	m.pendingApproval = &pendingApproval{tool: ev.Tool, id: ev.ID, args: ev.Args}
	m.mode = ModeApprovalPopup
	return m, nil
}

// handleApprovalPopupKey answers the prompt: y allows, n denies, esc
// denies — a popup must never be a trap, and the safe default for a
// sharp tool is no. The answer travels on ApprovalCh (never blocking),
// and the pending prompt clears either way.
func (m Model) handleApprovalPopupKey(msg tea.KeyMsg) (Model, tea.Cmd) {
	if m.pendingApproval == nil {
		m.mode = ModeNormal
		return m, nil
	}
	var answer *ApprovalAnswer
	switch {
	case msg.String() == "y":
		answer = &ApprovalAnswer{ID: m.pendingApproval.id, Allow: true}
	case msg.String() == "n" || key.Matches(msg, DefaultKeyMap.Normal):
		answer = &ApprovalAnswer{ID: m.pendingApproval.id, Allow: false}
	default:
		return m, nil
	}
	if m.approvalCh != nil {
		select {
		case m.approvalCh <- *answer:
		default:
		}
	}
	m.pendingApproval = nil
	m.mode = ModeNormal
	m.resizeComponents()
	return m, nil
}

// renderApprovalPopup renders the prompt: the tool asking, a truncated
// args preview, and the y/n choice.
func (m Model) renderApprovalPopup(width int) []string {
	if m.pendingApproval == nil {
		return nil
	}
	lines := []string{
		approvalTitleStyle.Render("approval required"),
		"",
		"tool: " + approvalTitleStyle.Render(m.pendingApproval.tool),
		"args: " + approvalArgsStyle.Render(m.pendingApproval.args),
		"",
		approvalHintStyle.Render("y allow    n deny    esc deny"),
	}
	return lines
}

// ApprovalRequestEvent carries what showApproval needs, decoupled from
// agent.Event so the popup tests don't import the agent package's wire.
type ApprovalRequestEvent struct {
	Tool string
	ID   string
	Args string
}
