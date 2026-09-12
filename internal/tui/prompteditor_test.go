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

// TestPromptEditor_PasteChips pins the compact-paste contract: a
// multi-line paste is held out of the editor as a chip (never rendered
// into the box), joins the typed text on submit, and is discarded by
// backspace on the empty box. Single-line pastes insert normally.
func TestPromptEditor_PasteChips(t *testing.T) {
	e := NewPromptEditor()
	e.Focus()

	e, _ = e.Update(tea.KeyMsg(tea.Key{Type: tea.KeyRunes, Paste: true, Runes: []rune("x\ny\nz")}))
	if e.pasted != "x\ny\nz" {
		t.Fatalf("paste was not chipped: %q", e.pasted)
	}
	if got := e.ta.Value(); got != "" {
		t.Fatalf("the paste leaked into the textarea: %q", got)
	}
	if got := e.Value(); got != "x\ny\nz" {
		t.Fatalf("payload without typed text = %q", got)
	}
	if h, h0 := e.RenderedHeight(), NewPromptEditor().RenderedHeight(); h != h0+1 {
		t.Fatalf("chip must reserve one extra line: %d vs %d", h, h0)
	}

	e, _ = e.Update(tea.KeyMsg(tea.Key{Type: tea.KeyRunes, Runes: []rune("typed")}))
	if e.pasted == "" {
		t.Fatal("typing must not discard the chip")
	}
	if got := e.Value(); got != "typed\nx\ny\nz" {
		t.Fatalf("payload with typed text = %q", got)
	}

	// Backspace with text in the box is normal editing, not a discard.
	e, _ = e.Update(keyMsg(tea.KeyBackspace, false))
	if e.pasted == "" {
		t.Fatal("backspace with text present must not discard the chip")
	}

	e, _ = e.Update(keyMsg(tea.KeyEnter, false))
	if !e.Submitted() {
		t.Fatal("enter must submit with a chip held")
	}
	e.Reset()
	if e.pasted != "" || e.Value() != "" {
		t.Fatal("reset must clear the chip and the text")
	}
}

// TestPromptEditor_SingleLinePasteInsertsNormally pins the other paste
// path: a paste without newlines is ordinary text.
func TestPromptEditor_SingleLinePasteInsertsNormally(t *testing.T) {
	e := NewPromptEditor()
	e.Focus()
	e, _ = e.Update(tea.KeyMsg(tea.Key{Type: tea.KeyRunes, Paste: true, Runes: []rune("one line")}))
	if e.pasted != "" {
		t.Fatal("a single-line paste must not chip")
	}
	if got := e.Value(); got != "one line" {
		t.Fatalf("single-line paste value = %q", got)
	}
}
