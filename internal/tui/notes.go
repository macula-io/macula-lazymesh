package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/macula-io/macula-lazymesh/internal/checkoutnotes"
)

// The checkout-notes panel (macula-io/macula-lazymesh#44, #38 slice B) is
// the operator's half of threat model invariant 2: the model is given the
// working directory's AGENTS.md and CLAUDE.md as data, and only an
// operator's own key press -- on the exact content shown here -- makes one
// of them the operator's instructions. Nothing the model can call reaches
// this panel's writer.
//
// Deliberately not re-read while running: the row listed is the content the
// model was actually given at start, so t records what the operator read,
// not whatever the file says by the time they press it.

// trustSelectedNote records trust for the highlighted note.
func (m Model) trustSelectedNote() (Model, tea.Cmd) {
	switch {
	case !m.notesExpanded:
		return m, nil // t in normal mode is not a trust action
	case len(m.checkoutNotes) == 0:
		return m, nil
	case m.checkoutTrustPath == "":
		// The mesh profile: those files are already the operator's own.
		return m, nil
	}
	note := m.checkoutNotes[m.notesCursor]
	if note.Trusted {
		return m.saySystem(fmt.Sprintf("%s is already trusted at this content", note.Name))
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

// renderNotes is the `N` panel: what the model was given, and what the
// operator can do about it.
func (m Model) renderNotes() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render(fmt.Sprintf("Checkout notes (%d)", len(m.checkoutNotes))) + "\n")
	if m.checkoutTrustPath == "" {
		// The mesh-only profile reads those files as the operator's own
		// instructions (G8), so there is nothing here to trust.
		b.WriteString(dimStyle.Render("the coding profile only: this session reads them as the operator's own instructions") + "\n")
		return b.String()
	}
	if len(m.checkoutNotes) == 0 {
		b.WriteString(dimStyle.Render("this working directory has no AGENTS.md or CLAUDE.md") + "\n")
		return b.String()
	}
	b.WriteString(dimStyle.Render("The model reads these as the checkout's own notes, never as orders, until you trust one here.") + "\n\n")
	for i, n := range m.checkoutNotes {
		cursor := "  "
		if i == m.notesCursor {
			cursor = "> "
		}
		line := fmt.Sprintf("%s%-12s %-20s sha256 %s", cursor, n.Name, noteState(n), shortSum(n.Sum))
		if i == m.notesCursor {
			line = statusStripStyle.Render(line)
		} else {
			line = dimStyle.Render(line)
		}
		b.WriteString(line + "\n")
		if i == m.notesCursor {
			b.WriteString(dimStyle.Render(fmt.Sprintf("    %s", n.Path)) + "\n")
		}
	}
	b.WriteString("\n" + dimStyle.Render("t: trust the selected note (applies at the next start)   ↑↓: select   esc/N: close"))
	return b.String()
}
