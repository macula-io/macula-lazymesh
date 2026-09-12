package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func keyMsg(t tea.KeyType, alt bool) tea.KeyMsg {
	return tea.KeyMsg(tea.Key{Type: t, Alt: alt})
}

// TestPromptEditor_StandardChatSemantics pins the contract: enter
// submits; ctrl+j -- the byte shift+enter arrives as through the
// termkeys wrapper -- inserts a newline; the submit flag clears on read
// and never leaks into the text.
func TestPromptEditor_StandardChatSemantics(t *testing.T) {
	e := NewPromptEditor()
	e.Focus() // the textarea ignores keystrokes until focused (the model focuses it on insert mode)

	var cmd tea.Cmd
	e, cmd = e.Update(tea.KeyMsg(tea.Key{Type: tea.KeyRunes, Runes: []rune("a")}))
	_ = cmd
	e, _ = e.Update(keyMsg(tea.KeyCtrlJ, false)) // shift+enter's byte form
	e, _ = e.Update(tea.KeyMsg(tea.Key{Type: tea.KeyRunes, Runes: []rune("b")}))

	if e.Submitted() {
		t.Fatal("ctrl+j must never submit")
	}
	if got := e.Value(); got != "a\nb" {
		t.Fatalf("value = %q, want a\\nb", got)
	}

	e, _ = e.Update(keyMsg(tea.KeyEnter, false))
	if !e.Submitted() {
		t.Fatal("enter must submit")
	}
	if e.Submitted() {
		t.Fatal("the submitted flag must clear on read")
	}
	if got := e.Value(); got != "a\nb" {
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
