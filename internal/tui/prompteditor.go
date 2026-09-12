package tui

import (
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// PromptEditor is the chatbox: a bordered multi-line textarea with
// TEXT-EDITOR semantics — enter inserts a newline (and shift+enter,
// which bubbletea cannot distinguish from enter, therefore also inserts
// a newline: the trap this component exists to close), alt+enter
// signals a submit. The component owns its key handling entirely, so
// the model never has to route compose keys around the textarea (the
// bug farm the plain-textarea approach turned into, 2026-09-12).
//
// The submit is reported out of band: Update swallows the send key and
// sets a flag the owner reads with Submitted() — a bubbletea component
// has no upward message channel, and a flag read right after Update is
// the honest equivalent.
type PromptEditor struct {
	ta        textarea.Model
	submitted bool
}

var (
	// promptSendKey submits: plain enter, the standard chat binding.
	promptSendKey = key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "send"))
	// promptNewlineKey inserts a newline: shift+enter arrives as the
	// ctrl+j byte via the termkeys wrapper (kitty protocol, flag 4), and
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
// is swallowed (the textarea must never see it as a newline); every
// other message goes to the textarea, whose own keymap already turns
// enter into a newline.
func (e PromptEditor) Update(msg tea.Msg) (PromptEditor, tea.Cmd) {
	if kmsg, ok := msg.(tea.KeyMsg); ok && key.Matches(kmsg, promptSendKey) {
		e.submitted = true
		return e, nil
	}
	var cmd tea.Cmd
	e.ta, cmd = e.ta.Update(msg)
	return e, cmd
}

// Submitted reports whether the send key was pressed since the last
// call, clearing the flag as it reads.
func (e *PromptEditor) Submitted() bool {
	s := e.submitted
	e.submitted = false
	return s
}

// View renders the chatbox.
func (e PromptEditor) View() string { return e.ta.View() }

// Value returns the composed text.
func (e PromptEditor) Value() string { return e.ta.Value() }

// SetValue replaces the composed text (the $EDITOR round-trip).
func (e *PromptEditor) SetValue(s string) { e.ta.SetValue(s) }

// Reset clears the composed text.
func (e *PromptEditor) Reset() { e.ta.Reset() }

// Focus routes keystrokes into the box; Blur stops them.
func (e *PromptEditor) Focus() tea.Cmd { return e.ta.Focus() }
func (e *PromptEditor) Blur()          { e.ta.Blur() }

// CursorEnd moves the cursor to the end of the composed text.
func (e *PromptEditor) CursorEnd() { e.ta.CursorEnd() }

// SetWidth sizes the box to the pane.
func (e *PromptEditor) SetWidth(w int) { e.ta.SetWidth(w) }

// RenderedHeight is the box's actual rendered height (text rows plus
// border), measured — the layout reserves this exactly.
func (e PromptEditor) RenderedHeight() int { return lipgloss.Height(e.ta.View()) }
