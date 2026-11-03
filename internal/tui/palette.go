package tui

// The command palette (#78 gap 6): the leader's commands, found by name
// rather than remembered by letter. It lists leaderCommands -- local
// commands only; anything that acts on the mesh waits for #75 -- and runs
// the chosen one through runLeader, exactly as its key would, so a command
// keeps its own guards whichever way it was reached.

import (
	"sort"
	"strings"
	"unicode"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
)

// paletteState is what the operator has typed and which match is selected.
type paletteState struct {
	query  string
	cursor int
}

// openPalette gives the keyboard to the palette. It is reached from the
// leader, which a pop-up or a focused panel never lets through, so it cannot
// open over a prompt (#59).
func (m Model) openPalette() (tea.Model, tea.Cmd) {
	m.mode = ModePalette
	m.palette = paletteState{}
	m.resizeComponents()
	return m, nil
}

func (m Model) closePalette() Model {
	m.mode = ModeCompose
	m.palette = paletteState{}
	m.resizeComponents()
	return m
}

// paletteMatches is every leader command whose name holds the query's
// letters in order, ignoring case. A name holding the query as one piece
// ranks first, so "cop" puts "copy last answer" at the top. The palette is
// not offered inside itself.
func paletteMatches(query string) []key.Binding {
	q := strings.ToLower(query)
	type match struct {
		b    key.Binding
		rank int
	}
	var found []match
	for _, b := range leaderCommands() {
		if b.Help().Key == DefaultKeyMap.Palette.Help().Key {
			continue
		}
		name := strings.ToLower(b.Help().Desc)
		switch {
		case strings.HasPrefix(name, q):
			found = append(found, match{b, 0})
		case strings.Contains(name, q):
			found = append(found, match{b, 1})
		case inOrder(name, q):
			found = append(found, match{b, 2})
		}
	}
	sort.SliceStable(found, func(i, j int) bool { return found[i].rank < found[j].rank })
	out := make([]key.Binding, 0, len(found))
	for _, f := range found {
		out = append(out, f.b)
	}
	return out
}

// inOrder reports whether every rune of q appears in s, in order.
func inOrder(s, q string) bool {
	rest := []rune(q)
	for _, r := range s {
		if len(rest) == 0 {
			break
		}
		if r == rest[0] {
			rest = rest[1:]
		}
	}
	return len(rest) == 0
}

// handlePaletteKey types into the search, moves the selection, runs the
// selected command or closes the palette.
func (m Model) handlePaletteKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	matches := paletteMatches(m.palette.query)
	switch {
	case key.Matches(msg, DefaultKeyMap.Normal):
		return m.closePalette(), nil
	case key.Matches(msg, DefaultKeyMap.Submit):
		if len(matches) == 0 {
			return m, nil
		}
		chosen := matches[m.palette.cursor]
		return m.closePalette().runLeader(paletteKeyPress(chosen))
	}
	switch msg.Type {
	case tea.KeyUp:
		if m.palette.cursor > 0 {
			m.palette.cursor--
		}
	case tea.KeyDown:
		if m.palette.cursor < len(matches)-1 {
			m.palette.cursor++
		}
	case tea.KeyBackspace:
		if q := []rune(m.palette.query); len(q) > 0 {
			m.palette.query = string(q[:len(q)-1])
			m.palette.cursor = 0
		}
	case tea.KeyRunes:
		m.palette.query += searchable(msg.Runes)
		m.palette.cursor = 0
	case tea.KeySpace:
		m.palette.query += " "
		m.palette.cursor = 0
	}
	return m, nil
}

// searchable keeps the runes a command name can hold. The search is one
// line of letters, so a control character -- a paste can carry a whole
// escape sequence -- has no use in it and is never kept.
func searchable(runes []rune) string {
	var b strings.Builder
	for _, r := range runes {
		if unicode.IsPrint(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// paletteKeyPress is the key press that runs b after the leader. Every
// leader command is one printable key.
func paletteKeyPress(b key.Binding) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(b.Keys()[0])}
}

// renderPalette is the palette's own view: the search as typed, then each
// match with the key that runs it without the palette.
func (m Model) renderPalette() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("Commands") + "\n")
	// The query goes out through neutralise like any other text that came
	// from outside the program (#81): searchable keeps controls out of it,
	// and this keeps them off the terminal whatever reaches the field.
	b.WriteString("> " + neutralise(m.palette.query) + "\n")
	matches := paletteMatches(m.palette.query)
	if len(matches) == 0 {
		b.WriteString(dimStyle.Render("no command matches"))
		return b.String()
	}
	for i, c := range matches {
		row := c.Help().Desc + "  " + dimStyle.Render(m.leaderLabel()+" "+c.Help().Key)
		if i == m.palette.cursor {
			row = statusStripStyle.Render("› "+c.Help().Desc) + "  " + dimStyle.Render(m.leaderLabel()+" "+c.Help().Key)
		} else {
			row = "  " + row
		}
		b.WriteString(row + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
