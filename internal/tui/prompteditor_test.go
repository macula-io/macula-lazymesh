package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func keyMsg(t tea.KeyType, alt bool) tea.KeyMsg {
	return tea.KeyMsg(tea.Key{Type: t, Alt: alt})
}

// TestPromptEditor_EnterInsertsNewline pins the editor semantics: plain
// enter is a newline, never a submit.
func TestPromptEditor_EnterInsertsNewline(t *testing.T) {
	e := NewPromptEditor()
	e.Focus() // the textarea ignores keystrokes until focused (the model focuses it on insert mode)
	var cmd tea.Cmd
	e, cmd = e.Update(tea.KeyMsg(tea.Key{Type: tea.KeyRunes, Runes: []rune("a")}))
	_ = cmd
	e, _ = e.Update(keyMsg(tea.KeyEnter, false))
	e, _ = e.Update(tea.KeyMsg(tea.Key{Type: tea.KeyRunes, Runes: []rune("b")}))

	if e.Submitted() {
		t.Fatal("enter must never submit")
	}
	if got := e.Value(); got != "a\nb" {
		t.Fatalf("value = %q, want a\\nb", got)
	}
}

// TestPromptEditor_AltEnterSubmits pins the submit path: alt+enter sets
// the submitted flag exactly once and does not leak into the text.
func TestPromptEditor_AltEnterSubmits(t *testing.T) {
	e := NewPromptEditor()
	e.Focus()
	e, _ = e.Update(tea.KeyMsg(tea.Key{Type: tea.KeyRunes, Runes: []rune("hello")}))
	e, _ = e.Update(keyMsg(tea.KeyCtrlM, true)) // alt+enter

	if !e.Submitted() {
		t.Fatal("alt+enter did not set the submitted flag")
	}
	if e.Submitted() {
		t.Fatal("the submitted flag must clear on read")
	}
	if got := e.Value(); got != "hello" {
		t.Fatalf("the send key leaked into the text: %q", got)
	}
}

// TestPromptEditor_PasteSplitsLines pins the multi-line paste path: a
// KeyRunes paste with embedded newlines lands as rows.
func TestPromptEditor_PasteSplitsLines(t *testing.T) {
	e := NewPromptEditor()
	e.Focus()
	e, _ = e.Update(tea.KeyMsg(tea.Key{Type: tea.KeyRunes, Paste: true, Runes: []rune("x\ny\nz")}))
	if got := e.Value(); got != "x\ny\nz" {
		t.Fatalf("pasted value = %q", got)
	}
}
