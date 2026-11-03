package tui

import (
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/macula-io/macula-lazymesh/internal/prompthistory"
)

// Prompt recall (#78 gap 5): up in an empty composer puts the last prompt
// sent back in it, further ups walk back through the earlier ones, and down
// walks forward again, past the newest to an empty composer. A recalled
// prompt keeps the walk going only while it is unchanged: the moment it is
// edited it is a draft like any other, and up and down move inside it. A
// draft the operator typed is never replaced.
//
// Recall only ever fills the composer. A recalled prompt reaches the agent
// when the operator sends it, like typed text, and never otherwise.

var (
	recallOlderKey = key.NewBinding(key.WithKeys("up"), key.WithHelp("↑", "earlier prompt"))
	recallNewerKey = key.NewBinding(key.WithKeys("down"), key.WithHelp("↓", "later prompt"))
)

// loadPrompts reads the history at start. A failure is shown like any
// other error and leaves recall empty; it never stops the TUI.
func (m *Model) loadPrompts() {
	if m.promptHistoryPath == "" {
		return
	}
	prompts, err := prompthistory.Load(m.promptHistoryPath)
	m.prompts = prompts
	m.recallAt = len(m.prompts)
	if err != nil {
		m.lastErr = err
		m.recordError(err, time.Now())
	}
}

// recall handles up and down while the composer has the keyboard. handled
// is false when the key is the draft's own (a cursor move inside it).
func (m Model) recall(msg tea.KeyMsg) (Model, bool) {
	older := key.Matches(msg, recallOlderKey)
	if !older && !key.Matches(msg, recallNewerKey) {
		return m, false
	}
	if !m.walkingPrompts() {
		if !older || m.input.Value() != "" || len(m.prompts) == 0 {
			return m, false
		}
		m.recallAt = len(m.prompts)
	}
	switch {
	case older && m.recallAt > 0:
		m.recallAt--
	case !older:
		m.recallAt++
	}
	if m.recallAt >= len(m.prompts) {
		m.recallAt = len(m.prompts)
		m.input.Reset()
	} else {
		m.input.SetValue(m.prompts[m.recallAt])
	}
	m.resizeComponents()
	return m, true
}

// walkingPrompts is true while the composer holds a recalled prompt
// exactly as it was recalled.
func (m Model) walkingPrompts() bool {
	return m.recallAt < len(m.prompts) && m.input.Value() == m.prompts[m.recallAt]
}

// rememberPrompt records a sent prompt for recall, in memory and on disk,
// unless it repeats the newest one. A write that fails is shown and costs
// only the record: the prompt itself has been sent.
func (m *Model) rememberPrompt(text string) {
	defer func() { m.recallAt = len(m.prompts) }()
	if m.promptHistoryPath == "" {
		return
	}
	if n := len(m.prompts); n > 0 && m.prompts[n-1] == text {
		return
	}
	m.prompts = append(m.prompts, text)
	if len(m.prompts) > prompthistory.Max {
		m.prompts = m.prompts[len(m.prompts)-prompthistory.Max:]
	}
	if err := prompthistory.Append(m.promptHistoryPath, text); err != nil {
		m.lastErr = err
		m.recordError(err, time.Now())
	}
}
