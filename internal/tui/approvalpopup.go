package tui

import (
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

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
func (m Model) handleApprovalResolved(msg approvalResolvedMsg) (Model, tea.Cmd) {
	closed := false
	if m.pendingApproval != nil && m.pendingApproval.id == msg.ID {
		m.pendingApproval = nil
		m.approvalArm = popupArm{}
		m.mode = m.returnTo
		m.resizeComponents()
		closed = true
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
	if closed && msg.Note == "" {
		text += " (this popup's answer was not the one used)"
	}
	m.chatEntries = append(m.chatEntries, chatEntry{kind: chatSystem, at: time.Now(), text: text})

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

// showApproval surfaces one approval prompt (G9): it takes over the
// screen exactly like a ring popup does — the tool call waits, nothing
// else proceeds until a decision lands.
func (m Model) showApproval(ev ApprovalRequestEvent) (Model, tea.Cmd) {
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
		m.mode = m.returnTo
		return m, nil
	}
	// Whether this key may answer is decided from the state BEFORE it, and
	// then every key that reaches the pop-up counts as typing and restarts
	// the guard (#59): the answer has to follow a pause, not land inside a
	// sentence someone was already writing.
	armed := m.approvalArm.armedAt(m.clock())
	m.approvalArm.lastKeyAt = m.clock()

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
	m.mode = m.returnTo
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
		"tool: " + approvalTitleStyle.Render(m.pendingApproval.tool),
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
	return wrapCells(visibleControls(m.pendingApproval.args), width)
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

// visibleControls shows as text every rune that could change what the
// operator reads without being seen itself: C0 and C1 controls and DEL
// as \xNN, and every format character (the Cf class, which holds the
// bidi overrides and isolates U+202A-202E and U+2066-2069 and the
// zero-width U+200B-200F and U+FEFF), line and paragraph separators, and
// any other rune that takes no cell on screen as \u{XXXX}. A shell
// command reordered by a bidi override (Trojan Source) then reads as
// written, not as displayed.
func visibleControls(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f):
			fmt.Fprintf(&b, "\\x%02x", r)
		case unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) || r == utf8.RuneError || runewidth.RuneWidth(r) == 0:
			fmt.Fprintf(&b, "\\u{%04X}", r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// wrapCells hard-wraps s so no line is wider than width terminal cells:
// arguments are machine text, and a word wrap could move a short tail out
// of sight just as a cut would. It counts cells, not runes, so a line of
// wide runes still fits the screen. s has been through visibleControls,
// so every rune takes at least one cell.
func wrapCells(s string, width int) []string {
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
