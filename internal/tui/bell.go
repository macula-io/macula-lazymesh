package tui

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
)

// bellPattern differentiates audio cues by cadence, not by different sound
// files -- the plan's own "lean version, not an audio engine": plain
// terminal BEL only, every terminal supports it, zero dependencies.
type bellPattern int

const (
	bellNone   bellPattern = iota
	bellSingle             // an ordinary room message from someone else
	bellDouble             // a ring addressed to this agent
	bellTriple             // agent entered backoff / hit max consecutive failures
)

func (p bellPattern) sequence() string {
	switch p {
	case bellSingle:
		return "\a"
	case bellDouble:
		return "\a\a"
	case bellTriple:
		return "\a\a\a"
	default:
		return ""
	}
}

// ringBell is a tea.Cmd, not something baked into View() -- BEL is a real
// one-shot side effect (write it once, don't re-fire it on every re-render
// the way View()'s return value would). Writing "\a" directly to stdout
// alongside bubbletea's own alt-screen rendering is safe: BEL moves no
// cursor and draws no visible character, so it can't corrupt the display
// the way writing arbitrary text outside bubbletea's own render calls
// would. Muted or a pattern of bellNone both no-op without ever touching
// the file descriptor -- and even where the terminal ignores/reroutes BEL
// entirely (headless, SSH, visual-bell-only config), writing the byte
// itself never errors, so there is nothing here that needs "detecting
// whether the bell works" -- the safety comes from BEL being harmless to
// send, not from checking first.
func ringBell(pattern bellPattern, muted bool) tea.Cmd {
	return func() tea.Msg {
		if !muted && pattern != bellNone {
			fmt.Fprint(os.Stdout, pattern.sequence())
		}
		return nil
	}
}

// detectRingBell reports whether prev -> next's pending rings gained an
// entry (by ring_id) -- a new ring addressed to this agent.
func detectRingBell(prev, next meshState) bellPattern {
	seen := make(map[string]bool, len(prev.pending))
	for _, r := range prev.pending {
		seen[r.RingID] = true
	}
	for _, r := range next.pending {
		if !seen[r.RingID] {
			return bellDouble
		}
	}
	return bellNone
}

// detectRoomMessageBell reports whether prev -> next's joined rooms gained
// a message from someone other than selfNodeID. selfNodeID empty (self not
// yet identified in a presence refresh) means nothing is attributed as
// "self" yet -- conservatively treated as no new message to avoid a false
// bell storm on the very first refresh before presence data exists.
func detectRoomMessageBell(prev, next meshState, selfNodeID string) bellPattern {
	if selfNodeID == "" {
		return bellNone
	}
	prevCount := make(map[string]int, len(prev.recent))
	for room, msgs := range prev.recent {
		prevCount[room] = len(msgs)
	}
	for room, msgs := range next.recent {
		start := prevCount[room]
		for _, m := range msgs[minInt(start, len(msgs)):] {
			if m.From != selfNodeID {
				return bellSingle
			}
		}
	}
	return bellNone
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// selfAgent finds the presence entry marked is_self, if any have been
// fetched yet -- the one lookup selfNodeID/selfPetname/renderSummaryLine's
// own identity styling all build on, rather than three separate scans
// over the same slice.
func selfAgent(agents []agentPresence) (agentPresence, bool) {
	for _, a := range agents {
		if a.IsSelf {
			return a, true
		}
	}
	return agentPresence{}, false
}

// selfNodeID finds the presence entry marked is_self, if any have been
// fetched yet.
func selfNodeID(agents []agentPresence) string {
	self, ok := selfAgent(agents)
	if !ok {
		return ""
	}
	return self.NodeID
}

// selfPetname is this instance's own deterministic petname, the same
// displayName fallback (petname, else shortened node_id) the presence
// panel already uses for everyone else -- lazymesh never sets its own
// operator_name (no config option for it, no explicit mesh_hello call
// anywhere in this codebase), so petname is the only human-legible label
// a lazymesh instance's own presence entry actually has. Requires
// macula-mcp >= 0.25.2 (macula-io/macula-mcp#3/@a939b0b): mesh_agents
// didn't include petname for every roster entry, self included, before
// that. Empty until the first refreshCmd tick populates m.state.agents
// (see resizeComponents' own callers for the same "may not have data yet"
// shape elsewhere in this package).
func selfPetname(agents []agentPresence) string {
	self, ok := selfAgent(agents)
	if !ok {
		return ""
	}
	return displayName(self.NodeID, self.Petname)
}
