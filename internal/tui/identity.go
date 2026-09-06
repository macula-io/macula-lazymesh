package tui

import (
	"hash/fnv"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// agentPalette is the fixed-order categorical hue set used to derive a
// per-agent identity color -- issue #7's avatar badge in the mesh view
// and issue #8's per-speaker room-message coloring both key off this same
// palette, per both issues' own "same derivation principle" requirement.
// Sourced from the workspace's validated categorical palette (dataviz
// skill's references/palette.md dark-mode slots, CVD-checked adjacent-
// pair safe -- not invented). Slots 1 (blue) and 2 (orange) are left out
// deliberately: those hues are already macula's own brand colors
// (macula-artwork, see chat.go's own comment), reserved for "you"
// (chatYouStyle, #38BDF8) and "agent" (chatAssistantStyl, #FB923C) -- a
// third party landing on either would read as one of those two fixed
// roles instead of its own identity.
var agentPalette = []string{
	"#199e70", // aqua
	"#c98500", // yellow
	"#d55181", // magenta
	"#008300", // green
	"#9085e9", // violet
	"#e66767", // red
}

// identityKey is what agentColor/agentInitials hash on: petname when set
// (deterministic from node_id, already the preferred human-legible label
// everywhere else in this package -- see displayName), the raw node_id
// otherwise. Same fallback order as displayName so a badge/color and its
// adjacent name label are always keyed on the same identity.
func identityKey(nodeID, petname string) string {
	if petname != "" {
		return petname
	}
	return nodeID
}

// agentColor deterministically maps identity onto one of agentPalette's
// hues via FNV-1a over the whole string -- not just a first-byte or
// length-based hash, since petnames share a common adjective-noun shape
// that would collide far more under a cheap hash than a real one does.
// Same identity always lands on the same color for the life of the
// process.
func agentColor(identity string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(identity))
	return agentPalette[h.Sum32()%uint32(len(agentPalette))]
}

// agentInitials is issue #7's badge content: 1-2 uppercase letters.
// macula-mcp's petnames are adjective-noun ("brave-falcon") -- initials
// take the first letter of each word for exactly that shape, falling
// back to the first two characters for anything without a word
// separator (a bare node_id, a single-word petname).
func agentInitials(identity string) string {
	runes := []rune(identity)
	if len(runes) == 0 {
		return "??"
	}
	fields := strings.FieldsFunc(identity, func(r rune) bool {
		return r == '-' || r == '_' || r == ' '
	})
	if len(fields) >= 2 {
		a := []rune(fields[0])
		b := []rune(fields[1])
		return strings.ToUpper(string(a[0]) + string(b[0]))
	}
	if len(runes) >= 2 {
		return strings.ToUpper(string(runes[:2]))
	}
	return strings.ToUpper(string(runes))
}

// agentBadgeStyle renders identity's deterministic color, bold -- shared
// by the mesh view's avatar badge (issue #7) and per-speaker room-message
// coloring (issue #8). This is the terminal-native "avatar" both issues
// settle on rather than a literal image: lazymesh is a bubbletea TUI with
// no image-protocol usage anywhere in this codebase, and Kitty graphics/
// Sixel/iTerm2 inline images would be a much bigger lift with real
// terminal-compatibility risk for comparatively little payoff here.
func agentBadgeStyle(identity string) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(lipgloss.Color(agentColor(identity))).Bold(true)
}
