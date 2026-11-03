package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/macula-io/macula-lazymesh/internal/checkoutnotes"
)

// The checkout-notes panel (macula-io/macula-lazymesh#44, #38 slice B) is
// the operator's half of threat model invariant 2: the model is given the
// working directory's AGENTS.md and CLAUDE.md as data, and only an
// operator's own key press -- on the exact content shown here -- makes one
// of them the operator's instructions. Nothing the model can call reaches
// this panel's writer.
//
// The content is what carries the decision, so the panel shows it whole:
// the same escaped text the model was given at start, wrapped and scrolled,
// and t records nothing until every line of it has been on screen. A row of
// names with a hash prefix would ask the operator to vouch for bytes that
// were never in front of them.
//
// Deliberately not re-read while running: the text listed is the content
// the model was actually given at start, so t records what the operator
// read. If the file has changed since, t refuses -- those bytes are no
// longer on disk, and trusting them would be a claim nobody can check.

// notesPanel is the panel's own state, nil when it is closed.
type notesPanel struct {
	note int // which of m.checkoutNotes lines were built from
	// width is the wrap the lines were built at; a resize rebuilds them,
	// because "every line seen" is measured in the lines actually shown.
	width  int
	lines  []string
	offset int
	seen   map[int]bool
}

// notesWidth is the content width: the terminal minus the border's two
// columns and the padding's two.
func (m Model) notesWidth() int {
	if w := m.width - 6; w > 20 {
		return w
	}
	return 20
}

// notesVisibleLines is how many content lines fit: what is left of the
// terminal once the panel's own lines, its border and the status strip are
// accounted for. Counted from the lines this render will actually emit, so
// a long path or a second refused file cannot push the tail off the
// bottom.
func (m Model) notesVisibleLines() int {
	if m.notesPanel == nil {
		return 3
	}
	fixed := len(m.notesHeaderLines(0)) + len(m.notesHintLines()) + 1 /*blank before the hint*/ + 2 /*border*/ + 2 /*status strip*/
	if v := m.height - fixed; v > 3 {
		return v
	}
	return 3
}

// openNotes shows the panel over the first note, with its content already
// on screen -- and therefore already counted as seen.
func (m *Model) openNotes() {
	m.notesCursor = 0
	m.notesPanel = &notesPanel{note: -1}
	m.mode = ModeNotesPanel
	m.meshExpanded = false
	m.meshServicesExpanded = false
	m.realmExpanded = false
	m.ensureNotesPanel()
	m.markNotesSeen()
	m.resizeComponents()
}

func (m *Model) closeNotes() {
	m.notesPanel = nil
	m.mode = ModeCompose
	m.resizeComponents()
}

// ensureNotesPanel rebuilds the wrapped lines when the panel is new, the
// selection moved, or the terminal was resized.
func (m *Model) ensureNotesPanel() {
	p := m.notesPanel
	if p == nil {
		return
	}
	width := m.notesWidth()
	if p.lines != nil && p.note == m.notesCursor && p.width == width {
		return
	}
	p.note = m.notesCursor
	p.width = width
	p.offset = 0
	p.seen = map[int]bool{}
	p.lines = nil
	if m.notesCursor < len(m.checkoutNotes) {
		// Spaces for tabs: Escape keeps a tab because the model's copy
		// wants one, but the panel's line widths are counted in cells and a
		// tab's width depends on where it lands.
		content := strings.ReplaceAll(checkoutnotes.Escape(m.checkoutNotes[m.notesCursor].Content), "\t", "    ")
		p.lines = wrapBlock(content, width)
	}
}

// wrapBlock wraps each of s's own lines to width, keeping the note's line
// structure: a note is prose, and its blank lines and breaks are part of
// what the operator is reading.
func wrapBlock(s string, width int) []string {
	var lines []string
	for _, line := range strings.Split(s, "\n") {
		lines = append(lines, wrapCells(line, width)...)
	}
	return lines
}

// markNotesSeen records the lines now on screen, so t can require the
// whole note to have passed in front of the operator.
func (m Model) markNotesSeen() {
	p := m.notesPanel
	if p == nil {
		return
	}
	last := min(p.offset+m.notesVisibleLines(), len(p.lines))
	for i := p.offset; i < last; i++ {
		p.seen[i] = true
	}
}

// notesUnseenLines counts the lines of the selected note never yet shown.
func (m Model) notesUnseenLines() int {
	p := m.notesPanel
	if p == nil {
		return 0
	}
	return len(p.lines) - len(p.seen)
}

// selectNote moves the panel to another note, starting it at its top.
func (m Model) selectNote(i int) Model {
	if i < 0 || i >= len(m.checkoutNotes) || m.notesPanel == nil {
		return m
	}
	m.notesCursor = i
	m.notesPanel.note = -1 // forces ensureNotesPanel to rebuild
	return m
}

// scrollNotes moves the content view for a scroll key and reports whether
// key was one.
func (m Model) scrollNotes(key string) bool {
	p := m.notesPanel
	page := m.notesVisibleLines()
	maxOffset := len(p.lines) - page
	if maxOffset < 0 {
		maxOffset = 0
	}
	moves := map[string]int{"j": 1, "down": 1, "k": -1, "up": -1, " ": page, "pgdown": page, "b": -page, "pgup": -page}
	offset := p.offset
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
	p.offset = min(max(offset, 0), maxOffset)
	m.markNotesSeen()
	return true
}

// handleNotesPanelKey scrolls, switches note, trusts, or closes.
func (m Model) handleNotesPanelKey(msg tea.KeyMsg) (Model, tea.Cmd) {
	if m.notesPanel == nil {
		m.mode = ModeCompose
		return m, nil
	}
	m.ensureNotesPanel()
	m.markNotesSeen()
	switch {
	case key.Matches(msg, DefaultKeyMap.Normal), msg.String() == "N":
		m.closeNotes()
		return m, nil
	case msg.String() == "tab":
		return m.selectNote(m.notesCursor + 1), nil
	case msg.String() == "shift+tab":
		return m.selectNote(m.notesCursor - 1), nil
	case msg.String() == "t":
		return m.trustSelectedNote()
	}
	if m.scrollNotes(msg.String()) {
		return m, nil
	}
	return m, nil
}

// trustSelectedNote records trust for the highlighted note.
func (m Model) trustSelectedNote() (Model, tea.Cmd) {
	switch {
	case m.mode != ModeNotesPanel || m.notesPanel == nil:
		return m, nil // t outside the panel is not a trust action
	case len(m.checkoutNotes) == 0:
		return m, nil
	case m.checkoutTrustPath == "":
		// The mesh profile: those files are already the operator's own.
		return m, nil
	}
	m.ensureNotesPanel()
	note := m.checkoutNotes[m.notesCursor]
	if note.Trusted {
		return m.saySystem(fmt.Sprintf("%s is already trusted at this content", note.Name))
	}
	if unseen := m.notesUnseenLines(); unseen > 0 {
		return m.saySystem(fmt.Sprintf("%d lines of %s have not been on screen yet: page to its end (space) before trusting it", unseen, note.Name))
	}
	if problem := checkoutnotes.RecordProblem(m.checkoutDir, m.checkoutTrustPath); problem != "" {
		return m.saySystem(problem)
	}
	// The panel holds what the model was given at start. Trust is given to
	// those bytes, so they must still be the file's: otherwise the operator
	// is vouching for text that is no longer on disk, and the record would
	// pin a version nobody can read.
	fresh := checkoutnotes.Read(m.checkoutDir)
	if !sameContent(fresh.Notes, note) {
		return m.saySystem(fmt.Sprintf("%s changed since this session started, so the text above is no longer the file's: restart to read the new version", note.Name))
	}
	trust, err := checkoutnotes.LoadTrust(m.checkoutTrustPath)
	if err != nil {
		return m.saySystem(fmt.Sprintf("could not read the trust record: %s", err))
	}
	if err := trust.TrustNote(note.Path, note.Sum); err != nil {
		return m.saySystem(fmt.Sprintf("could not trust %s: %s", note.Name, err))
	}
	// Says when, not that it is done: the running session's system prompt
	// is fixed, so this takes effect at the next start and the operator
	// must not think otherwise.
	return m.saySystem(fmt.Sprintf("trusted %s (sha256 %s…): it becomes the operator's instructions at the next start", note.Name, shortSum(note.Sum)))
}

// sameContent reports whether the notes read now still hold exactly the
// bytes the panel shows.
func sameContent(fresh []checkoutnotes.Note, shown checkoutnotes.Note) bool {
	for _, n := range fresh {
		if n.Name == shown.Name {
			return n.Sum == shown.Sum
		}
	}
	return false
}

// saySystem appends one system line to the conversation pane.
func (m Model) saySystem(text string) (Model, tea.Cmd) {
	m.chatEntries = append(m.chatEntries, chatEntry{kind: chatSystem, at: time.Now(), text: text})
	m.syncViewport()
	return m, nil
}

func shortSum(sum string) string {
	if len(sum) > 12 {
		return sum[:12]
	}
	return sum
}

// noteState is the one word the panel shows for a note.
func noteState(n checkoutnotes.Note) string {
	switch {
	case n.Trusted:
		return "trusted"
	case n.Changed:
		return "changed -- data again"
	default:
		return "data"
	}
}

var notesPanelStyle = lipgloss.NewStyle().
	Border(lipgloss.RoundedBorder()).
	BorderForeground(lipgloss.Color("240")).
	Padding(0, 1)

// notesHeaderLines is everything the panel shows above the note's
// content. visible is the number of content lines that fit; it fills in the
// line ruler and never changes how many rows the header itself has.
func (m Model) notesHeaderLines(visible int) []string {
	width := m.notesWidth()
	var lines []string
	title := "Checkout notes"
	if len(m.checkoutNotes) > 0 {
		title = fmt.Sprintf("Checkout notes %d of %d", m.notesCursor+1, len(m.checkoutNotes))
	}
	lines = append(lines, titleStyle.Render(title))
	if m.checkoutTrustPath == "" {
		// The mesh-only profile reads those files as the operator's own
		// instructions (G8), so there is nothing here to trust.
		return append(lines, dimStyle.Render("the coding profile only: this session reads them as the operator's own instructions"))
	}
	for _, r := range m.refusedNotes {
		for _, line := range wrapBlock(fmt.Sprintf("%s   not delivered: %s", r.Name, r.Reason), width) {
			lines = append(lines, dimStyle.Render(line))
		}
	}
	if len(m.checkoutNotes) == 0 {
		if len(m.refusedNotes) == 0 {
			lines = append(lines, dimStyle.Render("this working directory has no AGENTS.md or CLAUDE.md"))
		}
		return lines
	}
	note := m.checkoutNotes[m.notesCursor]
	_, first, last := m.panelContentLines(visible)
	lines = append(lines, fmt.Sprintf("%s   %s   sha256 %s   lines %d-%d of %d",
		note.Name, noteState(note), shortSum(note.Sum), first+1, last, len(m.notesPanel.lines)))
	for _, line := range wrapBlock(note.Path, width) {
		lines = append(lines, dimStyle.Render(line))
	}
	return append(lines, dimStyle.Render("The model reads this as the checkout's own notes, never as orders, until you trust it here."))
}

// notesHintLines is the guidance under the content.
func (m Model) notesHintLines() []string {
	var lines []string
	if unseen := m.notesUnseenLines(); unseen > 0 {
		for _, line := range wrapBlock(fmt.Sprintf("%d lines not yet on screen -- page to the end before t can trust", unseen), m.notesWidth()) {
			lines = append(lines, dimStyle.Render(line))
		}
	}
	hint := "j/k \u2191\u2193 scroll \u00b7 space/b page \u00b7 g/G top/end \u00b7 tab next note \u00b7 t trust \u00b7 esc close"
	for _, line := range wrapBlock(hint, m.notesWidth()) {
		lines = append(lines, dimStyle.Render(line))
	}
	return lines
}

// renderNotesPanel is the `N` panel: the content the model was given, what
// it ranks as, and what the operator can do about it.
func (m Model) renderNotesPanel() string {
	m.ensureNotesPanel()
	visible := m.notesVisibleLines()
	m.markNotesSeen()

	lines := m.notesHeaderLines(visible)
	content, _, _ := m.panelContentLines(visible)
	lines = append(lines, content...)
	lines = append(lines, "")
	lines = append(lines, m.notesHintLines()...)
	return notesPanelStyle.Render(strings.Join(lines, "\n"))
}

// panelContentLines is the slice of the selected note now on screen, with
// the 1-based first and last line numbers it covers (0, 0 when there is
// nothing to show).
func (m Model) panelContentLines(visible int) ([]string, int, int) {
	p := m.notesPanel
	if p == nil || len(p.lines) == 0 {
		return nil, 0, 0
	}
	first := min(p.offset, len(p.lines)-1)
	last := min(first+visible, len(p.lines))
	return p.lines[first:last], first, last
}
