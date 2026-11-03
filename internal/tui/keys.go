package tui

import (
	"fmt"
	"slices"
	"strings"

	"github.com/charmbracelet/bubbles/key"
)

// KeyMap is the TUI's own key bindings. The composer is focused by default,
// so almost nothing is a bare key: a bare key is a letter someone meant to
// type. Commands sit behind the leader (ctrl+x unless leader_key says
// otherwise, #63), the keys a person needs while reading sit on modifier or
// navigation keys the composer already leaves free, and the pop-ups keep their
// own single keys, guarded (#59).
type KeyMap struct {
	// Leader arms the leader: the next key is a command, never a letter.
	Leader key.Binding

	// Normal is esc. It closes what is open, cancels a pending leader, or
	// interrupts a running turn -- in that order. It never types and never
	// leaves a mode behind.
	Normal key.Binding

	Submit     key.Binding // enter -- send from the composer
	ForceQuit  key.Binding // ctrl+c -- works in every state
	OpenEditor key.Binding // ctrl+e -- compose in $EDITOR

	// Chat scrolling, on keys the composer leaves free. A line, a page, and
	// the two ends behind the leader -- the composer keeps home/end and the
	// plain arrows for the draft.
	ScrollLineUp   key.Binding
	ScrollLineDown key.Binding
	ScrollPageUp   key.Binding
	ScrollPageDown key.Binding

	// Commands after the leader.
	Quit               key.Binding
	ToggleMesh         key.Binding
	ToggleMeshServices key.Binding
	ToggleRealm        key.Binding
	ToggleNotes        key.Binding
	ToggleLogs         key.Binding
	ShowError          key.Binding
	ToggleDetails      key.Binding
	ToggleChatter      key.Binding
	ToggleQuiet        key.Binding
	CopyChat           key.Binding
	Interrupt          key.Binding
	ScrollTop          key.Binding
	ScrollBottom       key.Binding
	LeaderHelp         key.Binding

	// Ring pop-up only, phone metaphor: Answer/Decline/Answer+Trust, esc to
	// leave it for later.
	Answer  key.Binding
	Decline key.Binding
	Trust   key.Binding

	// Error pop-up. Copy, OlderError and NewerError only mean anything once
	// it is open.
	Copy       key.Binding
	OlderError key.Binding
	NewerError key.Binding
}

var DefaultKeyMap = KeyMap{
	Leader:     key.NewBinding(key.WithKeys("ctrl+x"), key.WithHelp("ctrl+x", "leader")),
	Normal:     key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "close, cancel or interrupt")),
	Submit:     key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "send")),
	ForceQuit:  key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("ctrl+c", "quit")),
	OpenEditor: key.NewBinding(key.WithKeys("ctrl+e"), key.WithHelp("ctrl+e", "compose in $EDITOR")),

	ScrollLineUp:   key.NewBinding(key.WithKeys("shift+up"), key.WithHelp("shift+↑", "scroll a line")),
	ScrollLineDown: key.NewBinding(key.WithKeys("shift+down"), key.WithHelp("shift+↓", "scroll a line")),
	ScrollPageUp:   key.NewBinding(key.WithKeys("pgup"), key.WithHelp("pgup", "scroll a page")),
	ScrollPageDown: key.NewBinding(key.WithKeys("pgdown"), key.WithHelp("pgdn", "scroll a page")),

	Quit:               key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
	ToggleMesh:         key.NewBinding(key.WithKeys("m"), key.WithHelp("m", "mesh view")),
	ToggleMeshServices: key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "mesh services")),
	ToggleRealm:        key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "realms")),
	ToggleNotes:        key.NewBinding(key.WithKeys("N"), key.WithHelp("N", "checkout notes")),
	ToggleLogs:         key.NewBinding(key.WithKeys("l"), key.WithHelp("l", "logs in $EDITOR")),
	ShowError:          key.NewBinding(key.WithKeys("E"), key.WithHelp("E", "recent errors")),
	ToggleDetails:      key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "expand tool detail")),
	ToggleChatter:      key.NewBinding(key.WithKeys("v"), key.WithHelp("v", "verbose")),
	ToggleQuiet:        key.NewBinding(key.WithKeys("b"), key.WithHelp("b", "mute bell")),
	CopyChat:           key.NewBinding(key.WithKeys("y"), key.WithHelp("y", "copy last answer")),
	Interrupt:          key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "interrupt the turn")),
	ScrollTop:          key.NewBinding(key.WithKeys("g"), key.WithHelp("g", "chat top")),
	ScrollBottom:       key.NewBinding(key.WithKeys("G"), key.WithHelp("G", "chat bottom")),
	LeaderHelp:         key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "this list")),

	Answer:     key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "answer")),
	Decline:    key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "decline")),
	Trust:      key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "answer + trust")),
	Copy:       key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "copy")),
	OlderError: key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "older error")),
	NewerError: key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "newer error")),
}

// leaderHelp is the whole keymap behind the leader, in the order the hint row
// shows it, for the leader then `?`. The same list is what the hint row offers
// while the leader is pending.
func leaderHelp() []struct{ Key, Action string } {
	return []struct{ Key, Action string }{
		{"m", "mesh view"},
		{"s", "mesh services"},
		{"r", "realms"},
		{"N", "checkout notes"},
		{"l", "logs in $EDITOR"},
		{"E", "recent errors"},
		{"e", "expand tool detail"},
		{"v", "verbose"},
		{"b", "mute bell"},
		{"y", "copy last answer"},
		{"x", "interrupt the turn"},
		{"g", "chat top"},
		{"G", "chat bottom"},
		{"q", "quit"},
		{"?", "this list"},
	}
}

// composerKeys are the ctrl combinations the message box itself uses: the
// bubbles textarea v1.0.0 defaults plus lazymesh's own ctrl+e (compose in
// $EDITOR) and ctrl+j (newline). A leader on one of them would swallow a key
// someone typing needs, so it is refused rather than reassigned (#63).
var composerKeys = []string{
	"ctrl+a", "ctrl+b", "ctrl+d", "ctrl+e", "ctrl+f", "ctrl+h", "ctrl+j",
	"ctrl+k", "ctrl+m", "ctrl+n", "ctrl+p", "ctrl+t", "ctrl+u", "ctrl+v",
	"ctrl+w",
}

// LeaderBinding resolves the configured leader_key. Empty means the default,
// ctrl+x: an unset setting is not a choice. Everything else is either a
// binding or a refusal that names the key and why it cannot be the leader --
// a key the message box uses, the quit key, or anything that is not ctrl and
// one letter. It is called once at start, so a refusal reaches the operator
// before the TUI opens rather than after they find they cannot type.
func LeaderBinding(name string) (key.Binding, error) {
	if name == "" {
		return DefaultKeyMap.Leader, nil
	}
	if !isCtrlLetter(name) {
		return key.Binding{}, fmt.Errorf("leader_key %q: the leader must be ctrl and one lowercase letter, like ctrl+x", name)
	}
	if name == "ctrl+c" {
		return key.Binding{}, fmt.Errorf("leader_key %q: ctrl+c quits lazymesh, so it cannot also be the leader", name)
	}
	// The terminal names HT itself and bubbletea passes its name through: a
	// ctrl+i keystroke is reported as "tab", never as "ctrl+i". A leader on
	// it would be a key the hint row offers and no keystroke can arm.
	// (ctrl+m is the same class -- CR arrives as "enter" -- and is refused
	// above as one of the message box's own keys.)
	if name == "ctrl+i" {
		return key.Binding{}, fmt.Errorf("leader_key %q: your terminal sends it as tab, so no keystroke could ever arm the leader", name)
	}
	if slices.Contains(composerKeys, name) {
		return key.Binding{}, fmt.Errorf("leader_key %q: the message box uses it to type, edit or open $EDITOR, so the leader would swallow a key you need while writing", name)
	}
	return key.NewBinding(key.WithKeys(name), key.WithHelp(name, "leader")), nil
}

// isCtrlLetter is the shape the leader has to have: ctrl and exactly one
// lowercase letter. The key names here are bubbletea's own, which is what a
// terminal actually sends.
func isCtrlLetter(name string) bool {
	if !strings.HasPrefix(name, "ctrl+") || len(name) != len("ctrl+x") {
		return false
	}
	c := name[len("ctrl+")]
	return c >= 'a' && c <= 'z'
}
