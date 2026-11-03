package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/macula-io/macula-lazymesh/internal/checkoutnotes"
)

func chatTexts(m Model) string {
	var b strings.Builder
	for _, e := range m.chatEntries {
		b.WriteString(e.text)
		b.WriteString("\n")
	}
	return b.String()
}

func press(t *testing.T, m Model, msg tea.Msg) Model {
	t.Helper()
	updated, _ := m.Update(msg)
	next, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update returned %T", updated)
	}
	return next
}

// TestChangedCheckoutNoteSaysWhyInTheTUI is invariant 2's second check,
// TUI half: a file the operator trusted that has since changed is
// announced, with the reason, rather than quietly demoted.
func TestChangedCheckoutNoteSaysWhyInTheTUI(t *testing.T) {
	m := New(nil, Options{CheckoutNotes: []checkoutnotes.Note{{
		Name: "AGENTS.md", Path: "/work/checkout/AGENTS.md", Sum: "abc123", Trusted: false, Changed: true,
	}}})
	texts := chatTexts(m)
	for _, want := range []string{"AGENTS.md", "changed", "data"} {
		if !strings.Contains(texts, want) {
			t.Fatalf("the notice does not say %q: %q", want, texts)
		}
	}
}

// TestNotesPanelTrustsTheSelectedNote: the operator's own key press is
// what records trust, it records the exact content shown, and the TUI says
// the change applies at the next start -- the running session's prompt is
// fixed.
func TestNotesPanelTrustsTheSelectedNote(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENTS.md")
	if err := os.WriteFile(path, []byte("run the tests from ./system"), 0o644); err != nil {
		t.Fatal(err)
	}
	notes := checkoutnotes.Read(dir)
	record := filepath.Join(t.TempDir(), checkoutnotes.FileName)
	m := New(nil, Options{CheckoutNotes: notes, CheckoutTrustPath: record})
	m.width, m.height = 80, 24
	m.resizeComponents()

	m = press(t, m, runeKey('N'))
	if !m.notesExpanded {
		t.Fatal("N did not open the checkout-notes panel")
	}
	if view := m.View(); !strings.Contains(view, "AGENTS.md") {
		t.Fatalf("the panel does not list the checkout's notes:\n%s", view)
	}

	m = press(t, m, runeKey('t'))
	trust, err := checkoutnotes.LoadTrust(record)
	if err != nil {
		t.Fatalf("trust record: %v", err)
	}
	if !trust.Trusts(path, notes[0].Sum) {
		t.Fatalf("the record does not hold %s with the hash the operator saw", path)
	}
	if texts := chatTexts(m); !strings.Contains(texts, "next start") {
		t.Fatalf("the TUI does not say when trust applies: %q", texts)
	}
}

// TestTrustNoteWithoutAPanelIsANoOp: `t` in normal mode must not write
// anything, so the ring pop-up's own `t` and the panel's never collide.
func TestTrustNoteWithoutAPanelIsANoOp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENTS.md")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	notes := checkoutnotes.Read(dir)
	record := filepath.Join(t.TempDir(), checkoutnotes.FileName)
	m := New(nil, Options{CheckoutNotes: notes, CheckoutTrustPath: record})
	m.width, m.height = 80, 24
	m.resizeComponents()

	m = press(t, m, runeKey('t'))
	if _, err := os.Stat(record); err == nil {
		t.Fatal("t outside the panel wrote a trust record")
	}
}
