package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// PromptEditor is the chatbox: a bordered multi-line textarea with
// STANDARD CHAT semantics — enter submits, ctrl+j (the byte shift+enter
// arrives as through the termkeys wrapper) inserts a newline. The
// component owns its key handling entirely, so the model never has to
// route compose keys around the textarea (the bug farm the
// plain-textarea approach turned into, 2026-09-12).
//
// Multi-line pastes are held out of the editor in compact form (the
// "[pasted N lines]" chip, same convention as OpenCode/Claude Code):
// the pasted text is never rendered into the box, it is appended to
// the typed text on submit, and backspace on the empty box discards it.
//
// The submit is reported out of band: Update swallows the send key and
// sets a flag the owner reads with Submitted() — a bubbletea component
// has no upward message channel, and a flag read right after Update is
// the honest equivalent.
type PromptEditor struct {
	ta        textarea.Model
	submitted bool
	// pasted holds a multi-line paste, kept out of the textarea until
	// submit (or discarded).
	pasted string
}

var (
	// promptSendKey submits: plain enter, the standard chat binding.
	promptSendKey = key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "send"))
	// promptNewlineKey inserts a newline: shift+enter arrives as the
	// ctrl+j byte via the termkeys wrapper (kitty protocol), and
	// ctrl+j is the universal fallback on terminals without it.
	promptNewlineKey = key.NewBinding(key.WithKeys("ctrl+j"), key.WithHelp("ctrl+j", "newline"))
)

// NewPromptEditor builds the chatbox: bordered (dim when idle, brand
// blue when focused), starts at 3 visible rows and grows to 8.
func NewPromptEditor() PromptEditor {
	ta := textarea.New()
	ta.Placeholder = "message the agent..."
	ta.CharLimit = 4000
	ta.Prompt = ""
	ta.SetHeight(3)
	ta.MaxHeight = 8
	ta.ShowLineNumbers = false
	ta.KeyMap.InsertNewline = promptNewlineKey

	focusedStyle, blurredStyle := textarea.DefaultStyles()
	blurredStyle.Base = chatboxBlurredStyle
	focusedStyle.Base = chatboxFocusedStyle
	ta.FocusedStyle = focusedStyle
	ta.BlurredStyle = blurredStyle
	// Blur() re-points the textarea's internal style reference at the
	// BlurredStyle above (New()'s own pointer targets its built-in
	// defaults, which would silently ignore these).
	ta.Blur()

	return PromptEditor{ta: ta}
}

// Update handles one message: the send key sets the submitted flag and
// is swallowed (the textarea must never see it as a newline); a
// multi-line paste is chipped instead of inserted; backspace on the
// empty box discards the chip; everything else goes to the textarea.
func (e PromptEditor) Update(msg tea.Msg) (PromptEditor, tea.Cmd) {
	if kmsg, ok := msg.(tea.KeyMsg); ok {
		if key.Matches(kmsg, promptSendKey) {
			e.submitted = true
			return e, nil
		}
		if kmsg.Paste {
			if strings.ContainsRune(string(kmsg.Runes), '\n') {
				e.pasted = string(kmsg.Runes)
				return e, nil
			}
			// A single-line paste is just text: let the textarea take it.
		} else if e.pasted != "" && e.ta.Value() == "" && key.Matches(kmsg, promptDiscardKey) {
			e.pasted = ""
			return e, nil
		}
	}
	var cmd tea.Cmd
	e.ta, cmd = e.ta.Update(msg)
	return e, cmd
}

// promptDiscardKey drops the paste chip: backspace on the empty box,
// the OpenCode convention.
var promptDiscardKey = key.NewBinding(key.WithKeys("backspace"))

// Submitted reports whether the send key was pressed since the last
// call, clearing the flag as it reads.
func (e *PromptEditor) Submitted() bool {
	s := e.submitted
	e.submitted = false
	return s
}

// View renders the chatbox, plus the paste chip line when a paste is
// held.
func (e PromptEditor) View() string {
	v := e.ta.View()
	if e.pasted == "" {
		return v
	}
	lines := strings.Count(e.pasted, "\n") + 1
	chip := chatboxBlurredStyle.Render(fmt.Sprintf("[pasted %d lines — backspace on empty box to discard]", lines))
	return v + "\n" + chip
}

// Value returns the full composed payload: the typed text plus any
// held paste.
func (e PromptEditor) Value() string {
	if e.pasted == "" {
		return e.ta.Value()
	}
	if e.ta.Value() == "" {
		return e.pasted
	}
	return e.ta.Value() + "\n" + e.pasted
}

// SetValue replaces the composed text (the $EDITOR round-trip). An
// external edit supersedes any held paste.
func (e *PromptEditor) SetValue(s string) {
	e.ta.SetValue(s)
	e.pasted = ""
}

// Reset clears the composed text and any held paste.
func (e *PromptEditor) Reset() {
	e.ta.Reset()
	e.pasted = ""
}

// Focus routes keystrokes into the box; Blur stops them.
func (e *PromptEditor) Focus() tea.Cmd { return e.ta.Focus() }
func (e *PromptEditor) Blur()          { e.ta.Blur() }

// CursorEnd moves the cursor to the end of the composed text.
func (e *PromptEditor) CursorEnd() { e.ta.CursorEnd() }

// SetWidth sizes the box to the pane.
func (e *PromptEditor) SetWidth(w int) { e.ta.SetWidth(w) }

// RenderedHeight is the box's actual rendered height (text rows plus
// border, plus the paste chip line when one is held), measured — the
// layout reserves this exactly.
func (e PromptEditor) RenderedHeight() int {
	h := lipgloss.Height(e.ta.View())
	if e.pasted != "" {
		h++
	}
	return h
}
