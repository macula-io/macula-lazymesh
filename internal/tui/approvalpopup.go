package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"

	"github.com/macula-io/macula-lazymesh/internal/frontend"
)

// ApprovalAnswer is the operator's decision on one approval prompt (G9):
// the approval id the answer must carry, and whether the call may run.
type ApprovalAnswer struct {
	ID    string
	Allow bool
}

// approvalResolvedMsg carries a frontend.ApprovalResolved through the tea
// loop. The type is the control plane's rather than a copy of it: the
// in-process TUI and an attached one must read the same resolution, and
// two shapes for one fact is how they drift apart.
type approvalResolvedMsg frontend.ApprovalResolved

// handleApprovalResolved closes a popup that was answered by someone
// else, and records who decided it. The chat line matters more than the
// popup closing: a human who pressed a key must be able to see that
// another frontend's answer is the one that took effect.
//
// A request that was still HELD for later is dropped here too. It is the same
// question, and the session has already stopped waiting for an answer: showing
// it when the person next pauses would put a dead question in front of them
// and offer a decision that cannot be delivered (macula-lazymesh#62 review
// r=2, probe P1).
func (m Model) handleApprovalResolved(msg approvalResolvedMsg) (Model, tea.Cmd) {
	closed := false
	if m.pendingApproval != nil && m.pendingApproval.id == msg.ID {
		m.pendingApproval = nil
		m.approvalArm = popupArm{}
		m = m.refocus(m.approvalReturnTo)
		m.resizeComponents()
		closed = true
	}
	dropped := false
	if m.deferredApproval != nil && m.deferredApproval.ID == msg.ID {
		m.deferredApproval = nil
		dropped = true
	}

	var text string
	switch {
	case msg.Note != "":
		// This frontend's own answer was refused, and it says why.
		text = "approval " + msg.ID + ": " + msg.Note
	default:
		by := msg.By
		if by == "" {
			by = "another frontend"
		}
		text = "approval " + msg.ID + " was answered by " + by
	}
	switch {
	case closed && msg.Note == "":
		text += " (this popup's answer was not the one used)"
	case dropped:
		text += " (it was still waiting to be shown)"
	}
	m.chatEntries = append(m.chatEntries, chatEntry{kind: chatSystem, at: time.Now(), text: termTextOf(text)})

	var next tea.Cmd
	if m.approvalResolvedCh != nil {
		next = waitForApprovalResolved(m.approvalResolvedCh)
	}
	return m, next
}

// pendingApproval is the one approval prompt currently shown.
type pendingApproval struct {
	tool string
	id   string
	args string
	// offset is the first args line on screen; the args are shown whole,
	// wrapped and scrolled, never cut (#39).
	offset int
	// seen marks each args line that has been on screen, at seenWidth:
	// y allows only once every line has been, so G cannot skip the
	// middle of a long call. A new width rewraps the call and starts
	// the count again.
	seen      map[int]bool
	seenWidth int
}

// approvalStyle matches the other popups' palette: brand orange for the
// tool under question, dim text for the args preview.
var (
	approvalTitleStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#FB923C")).Bold(true)
	approvalArgsStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	approvalHintStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Italic(true)
)

// showApproval surfaces one approval prompt (G9) unless the person is busy or
// a prompt is already up: taking the screen mid-word is the situation #59
// exists to survive, and the better answer is not to appear. The request is
// held -- the newest one only -- and shown on the first tick or key that finds
// the person free. Nothing but approvalMayOpen decides this, so a request can
// never replace a prompt someone is already reading.
func (m Model) showApproval(ev ApprovalRequestEvent) (Model, tea.Cmd) {
	if !m.approvalMayOpen() {
		held := ev
		m.deferredApproval = &held
		return m, nil
	}
	return m.showApprovalNow(ev)
}

// showApprovalNow puts the prompt on screen.
func (m Model) showApprovalNow(ev ApprovalRequestEvent) (Model, tea.Cmd) {
	m = m.rememberFocus(ModeApprovalPopup)
	m.pendingApproval = &pendingApproval{tool: ev.Tool, id: ev.ID, args: ev.Args}
	// The call may arrive while the person is mid-word: the pop-up takes the
	// screen, but its answer keys sleep for the guard (#59) so the rest of
	// the word cannot answer it.
	m.approvalArm = popupArm{shownAt: m.clock()}
	m.mode = ModeApprovalPopup
	m.markApprovalSeen()
	return m, nil
}

// handleApprovalPopupKey answers the prompt: y allows, n denies, esc
// denies — a popup must never be a trap, and the safe default for a
// sharp tool is no. y counts only once every line of the call has been
// on screen (#39); j/k, space/b and g/G scroll a call taller than the
// screen. The answer travels on ApprovalCh (never blocking), and the
// pending prompt clears either way.
func (m Model) handleApprovalPopupKey(msg tea.KeyMsg) (Model, tea.Cmd) {
	if m.pendingApproval == nil {
		return m.refocus(m.approvalReturnTo), nil
	}
	// Whether this key may answer is decided from the state BEFORE it, and
	// then every key that reaches the pop-up counts as typing and restarts
	// the guard (#59): the answer has to follow a pause, not land inside a
	// sentence someone was already writing.
	armed := m.approvalArm.armedAt(m.clock())
	m.approvalArm.lastKeyAt = m.clock()
	// A paste is typing whatever it contains: a y pasted into a message must
	// not answer, even with the guard already passed.
	if msg.Paste {
		return m, nil
	}

	if m.scrollApproval(msg.String()) {
		return m, nil
	}
	if key.Matches(msg, DefaultKeyMap.Normal) { // esc denies at once: the safe answer may be quick
		return m.answerApproval(false)
	}
	if !armed {
		return m, nil
	}
	switch msg.String() {
	case "y":
		if m.approvalUnseenLines() > 0 {
			return m, nil
		}
		return m.answerApproval(true)
	case "n":
		return m.answerApproval(false)
	}
	return m, nil
}

// answerApproval sends the decision and puts focus back where the pop-up took
// it -- the draft is untouched, so a person who was composing carries on from
// the word they were on.
func (m Model) answerApproval(allow bool) (Model, tea.Cmd) {
	if m.approvalCh != nil {
		select {
		case m.approvalCh <- ApprovalAnswer{ID: m.pendingApproval.id, Allow: allow}:
		default:
		}
	}
	m.pendingApproval = nil
	m.approvalArm = popupArm{}
	m = m.refocus(m.approvalReturnTo)
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
	m.markApprovalSeen()
	args := m.approvalArgLines(width)
	first, last := m.pendingApproval.offset, m.pendingApproval.offset+m.approvalVisibleLines()
	if last > len(args) {
		last = len(args)
	}
	lines := []string{
		approvalTitleStyle.Render("approval required"),
		"tool: " + approvalTitleStyle.Render(termTextOf(m.pendingApproval.tool).String()),
		fmt.Sprintf("args (lines %d-%d of %d):", first+1, last, len(args)),
	}
	for _, line := range args[first:last] {
		lines = append(lines, approvalArgsStyle.Render(line))
	}
	hint := "y allow    n deny    esc deny"
	switch unseen, armed := m.approvalUnseenLines(), m.approvalArm.armedAt(m.clock()); {
	case unseen > 0:
		hint = fmt.Sprintf("%d lines unseen: page through them (space or j) before y can allow    n deny    esc deny", unseen)
		if !armed {
			hint += "    " + armHint()
		}
	case !armed:
		hint = armHint() + "    esc deny"
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
	return wrapCells(termTextOf(m.pendingApproval.args), width)
}

// markApprovalSeen records the args lines now on screen.
func (m Model) markApprovalSeen() {
	p := m.pendingApproval
	if p.seen == nil || p.seenWidth != m.width {
		p.seen = map[int]bool{}
		p.seenWidth = m.width
	}
	last := min(p.offset+m.approvalVisibleLines(), len(m.approvalArgLines(m.width)))
	for i := p.offset; i < last; i++ {
		p.seen[i] = true
	}
}

// approvalUnseenLines counts the args lines never yet on screen at the
// current width.
func (m Model) approvalUnseenLines() int {
	p := m.pendingApproval
	total := len(m.approvalArgLines(m.width))
	if p.seenWidth != m.width {
		return total
	}
	return total - len(p.seen)
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
	m.markApprovalSeen()
	return true
}

// wrapCells hard-wraps s so no line is wider than width terminal cells:
// arguments are machine text, and a word wrap could move a short tail out
// of sight just as a cut would. It counts cells, not runes, so a line of
// wide runes still fits the screen. t came from termTextOf, so every rune
// takes at least one cell.
func wrapCells(t termText, width int) []string {
	s := t.String()
	var lines []string
	var line strings.Builder
	cells := 0
	for _, r := range s {
		w := runewidth.RuneWidth(r)
		if cells+w > width && cells > 0 {
			lines = append(lines, line.String())
			line.Reset()
			cells = 0
		}
		line.WriteRune(r)
		cells += w
	}
	return append(lines, line.String())
}

// ApprovalRequestEvent carries what showApproval needs, decoupled from
// agent.Event so the popup tests don't import the agent package's wire.
type ApprovalRequestEvent struct {
	Tool string
	ID   string
	Args string
}
