package tui

import (
	"fmt"
	"strings"

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
	// offset is the first args line on screen; the args are shown whole,
	// wrapped and scrolled, never cut (#39).
	offset int
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
// sharp tool is no. y counts only once the call's last line has been on
// screen (#39); j/k, space/b and g/G scroll a call taller than the
// screen. The answer travels on ApprovalCh (never blocking), and the
// pending prompt clears either way.
func (m Model) handleApprovalPopupKey(msg tea.KeyMsg) (Model, tea.Cmd) {
	if m.pendingApproval == nil {
		m.mode = ModeNormal
		return m, nil
	}
	if m.scrollApproval(msg.String()) {
		return m, nil
	}
	var answer *ApprovalAnswer
	switch {
	case msg.String() == "y":
		if !m.approvalEndShown() {
			return m, nil
		}
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

// renderApprovalPopup renders the prompt: the tool asking, the whole
// call wrapped to the screen width and scrolled when it is taller than
// the screen, and the choices. Control characters in the call are shown
// as text, so an escape sequence cannot erase or overwrite what the
// operator reads.
func (m Model) renderApprovalPopup(width int) []string {
	if m.pendingApproval == nil {
		return nil
	}
	args := m.approvalArgLines(width)
	first, last := m.pendingApproval.offset, m.pendingApproval.offset+m.approvalVisibleLines()
	if last > len(args) {
		last = len(args)
	}
	lines := []string{
		approvalTitleStyle.Render("approval required"),
		"tool: " + approvalTitleStyle.Render(m.pendingApproval.tool),
		fmt.Sprintf("args (lines %d-%d of %d):", first+1, last, len(args)),
	}
	for _, line := range args[first:last] {
		lines = append(lines, approvalArgsStyle.Render(line))
	}
	hint := "y allow    n deny    esc deny"
	if !m.approvalEndShown() {
		hint = "scroll to the end (G, or j/space) before y can allow    n deny    esc deny"
	}
	return append(lines, "", approvalHintStyle.Render(hint))
}

// approvalChromeLines is the popup's own lines around the args, plus the
// status bar.
const approvalChromeLines = 6

// approvalVisibleLines is how many args lines fit on screen.
func (m Model) approvalVisibleLines() int {
	if n := m.height - approvalChromeLines; n > 3 {
		return n
	}
	return 3
}

// approvalArgLines wraps the call to width, after making every control
// character visible.
func (m Model) approvalArgLines(width int) []string {
	if width < 20 {
		width = 20
	}
	return wrapRunes(visibleControls(m.pendingApproval.args), width)
}

// approvalEndShown reports whether the call's last line is on screen.
func (m Model) approvalEndShown() bool {
	return m.pendingApproval.offset+m.approvalVisibleLines() >= len(m.approvalArgLines(m.width))
}

// scrollApproval moves the args view for a scroll key and reports
// whether key was one.
func (m Model) scrollApproval(key string) bool {
	page := m.approvalVisibleLines()
	maxOffset := len(m.approvalArgLines(m.width)) - page
	if maxOffset < 0 {
		maxOffset = 0
	}
	moves := map[string]int{"j": 1, "down": 1, "k": -1, "up": -1, " ": page, "pgdown": page, "b": -page, "pgup": -page}
	offset := m.pendingApproval.offset
	switch key {
	case "G", "end":
		offset = maxOffset
	case "g", "home":
		offset = 0
	default:
		delta, ok := moves[key]
		if !ok {
			return false
		}
		offset += delta
	}
	m.pendingApproval.offset = min(max(offset, 0), maxOffset)
	return true
}

// visibleControls replaces C0 and C1 control characters and DEL with a
// visible \xNN.
func visibleControls(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			fmt.Fprintf(&b, "\\x%02x", r)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// wrapRunes hard-wraps s every width runes: arguments are machine text,
// and a word wrap could move a short tail out of sight just as a cut
// would.
func wrapRunes(s string, width int) []string {
	runes := []rune(s)
	var lines []string
	for len(runes) > width {
		lines = append(lines, string(runes[:width]))
		runes = runes[width:]
	}
	return append(lines, string(runes))
}

// ApprovalRequestEvent carries what showApproval needs, decoupled from
// agent.Event so the popup tests don't import the agent package's wire.
type ApprovalRequestEvent struct {
	Tool string
	ID   string
	Args string
}
