package tui

import "github.com/charmbracelet/bubbles/key"

// KeyMap is normal-mode's key bindings. Every binding accepts both a vim
// key and an arrow/plain key (per the plan: "not vim-only") using bubbles'
// own multi-key key.Binding support, not a hand-rolled key switch.
type KeyMap struct {
	Up            key.Binding
	Down          key.Binding
	ToggleMesh    key.Binding
	ToggleQuiet   key.Binding
	ToggleDetails key.Binding
	Insert        key.Binding
	Normal        key.Binding
	Quit          key.Binding // "q" -- normal mode only, never steals a "q" typed while composing
	ForceQuit     key.Binding // ctrl+c -- works in either mode, the universal escape hatch
	Submit        key.Binding

	// Ring pop-up only, phone-metaphor: Answer/Decline/Answer+Trust.
	Answer  key.Binding
	Decline key.Binding
	Trust   key.Binding
}

var DefaultKeyMap = KeyMap{
	Up:            key.NewBinding(key.WithKeys("k", "up"), key.WithHelp("k/↑", "scroll up")),
	Down:          key.NewBinding(key.WithKeys("j", "down"), key.WithHelp("j/↓", "scroll down")),
	ToggleMesh:    key.NewBinding(key.WithKeys("m"), key.WithHelp("m", "toggle mesh view")),
	ToggleQuiet:   key.NewBinding(key.WithKeys("b"), key.WithHelp("b", "mute/unmute bell")),
	ToggleDetails: key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "expand/collapse tool-call detail")),
	Insert:        key.NewBinding(key.WithKeys("i"), key.WithHelp("i", "compose message")),
	Normal:        key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "normal mode")),
	Quit:          key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
	ForceQuit:     key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("ctrl+c", "quit")),
	Submit:        key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "send")),
	Answer:        key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "answer")),
	Decline:       key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "decline")),
	Trust:         key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "answer + trust")),
}
