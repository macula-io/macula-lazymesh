package tui

import "github.com/charmbracelet/bubbles/key"

// KeyMap is normal-mode's key bindings. Every binding accepts both a vim
// key and an arrow/plain key (per the plan: "not vim-only") using bubbles'
// own multi-key key.Binding support, not a hand-rolled key switch.
type KeyMap struct {
	Up                 key.Binding
	Down               key.Binding
	ToggleMesh         key.Binding
	ToggleMeshServices key.Binding
	ToggleRealm        key.Binding
	ToggleQuiet        key.Binding
	ToggleDetails      key.Binding
	Insert             key.Binding
	Normal             key.Binding
	Quit               key.Binding // "q" -- normal mode only, never steals a "q" typed while composing
	ForceQuit          key.Binding // ctrl+c -- works in either mode, the universal escape hatch
	Submit             key.Binding
	ToggleChatter      key.Binding // routine tool-call activity: status line (default) vs inline in chat
	OpenEditor         key.Binding // ctrl+e -- works in either mode, always lands back in insert mode

	// Ring pop-up only, phone-metaphor: Answer/Decline/Answer+Trust.
	Answer  key.Binding
	Decline key.Binding
	Trust   key.Binding

	// Opens the agent log in $EDITOR. Not an overlay: the log is already a
	// file, and an editor already searches and scrolls it better.
	ToggleLogs key.Binding

	// Error pop-up. ShowError opens it from normal mode; Copy, OlderError
	// and NewerError only mean anything once it is open. Shift-E, not
	// "e", because "e" is already the chat detail toggle.
	ShowError  key.Binding
	Copy       key.Binding
	OlderError key.Binding // p -- step back through the error history
	NewerError key.Binding // n -- step forward again

	Interrupt key.Binding // x -- cancel the in-flight turn (normal mode)
}

var DefaultKeyMap = KeyMap{
	Up:                 key.NewBinding(key.WithKeys("k", "up"), key.WithHelp("k/↑", "scroll up")),
	Down:               key.NewBinding(key.WithKeys("j", "down"), key.WithHelp("j/↓", "scroll down")),
	ToggleMesh:         key.NewBinding(key.WithKeys("m"), key.WithHelp("m", "toggle mesh view")),
	ToggleMeshServices: key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "toggle mesh services view")),
	ToggleRealm:        key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "toggle realms view")),
	ToggleQuiet:        key.NewBinding(key.WithKeys("b"), key.WithHelp("b", "mute/unmute bell")),
	ToggleDetails:      key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "expand/collapse tool-call detail")),
	Insert:             key.NewBinding(key.WithKeys("i"), key.WithHelp("i", "compose message")),
	Normal:             key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "normal mode")),
	Quit:               key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
	ForceQuit:          key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("ctrl+c", "quit")),
	Submit:             key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "send")),
	ToggleChatter:      key.NewBinding(key.WithKeys("v"), key.WithHelp("v", "verbose (show tool activity in chat)")),
	OpenEditor:         key.NewBinding(key.WithKeys("ctrl+e"), key.WithHelp("ctrl+e", "compose in $EDITOR")),
	Answer:             key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "answer")),
	Decline:            key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "decline")),
	Trust:              key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "answer + trust")),
	ToggleLogs:         key.NewBinding(key.WithKeys("l"), key.WithHelp("l", "logs")),
	ShowError:          key.NewBinding(key.WithKeys("E"), key.WithHelp("E", "show recent errors")),
	Copy:               key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "copy")),
	OlderError:         key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "older error")),
	NewerError:         key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "newer error")),
	Interrupt:          key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "interrupt the running turn")),
}
