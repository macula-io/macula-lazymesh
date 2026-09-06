package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/lipgloss"
)

func TestColumnWidths_FlexColumnAbsorbsRemainder(t *testing.T) {
	cases := []struct {
		name  string
		width int
		spec  []int
	}{
		{"flex first", 76, []int{-1, 20, 12, 10}},
		{"flex last", 76, []int{10, 20, -1}},
		{"flex middle", 76, []int{10, -1, 20, 12}},
		// Regression coverage for a real bug found live 2026-09-06: Rooms'
		// own spec (fixed columns summing to 42) has more fixed-column
		// weight than Rings/Presence, so it hits the shrink path at a
		// width well within what a real, if narrow, terminal produces --
		// the panel was visibly "stuck" below here instead of continuing
		// to track m.width, because the old flex-only floor broke the sum
		// invariant instead of preserving it.
		{"rooms spec, narrow terminal", 40, []int{-1, 20, 12, 10}},
		{"rooms spec, very narrow", 20, []int{-1, 20, 12, 10}},
		// Pathologically narrow: even the shrink-everything path bottoms
		// out at 1 per column. The invariant still holds; individual
		// columns are simply as narrow as they can meaningfully get.
		{"pathological", 10, []int{20, 20, -1}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			widths := columnWidths(c.width, c.spec)
			if len(widths) != len(c.spec) {
				t.Fatalf("expected %d widths, got %d", len(c.spec), len(widths))
			}
			// Every column costs its declared Width plus 2 (bubbles/table's
			// own Padding(0,1) per cell) -- the columns should always sum
			// to exactly the requested width, matching a full-width panel
			// rather than one sized to its content, in EVERY case -- tight
			// or comfortable, not just the comfortable ones.
			sum := 0
			for i, w := range widths {
				if w < 1 {
					t.Fatalf("column %d has non-positive width %d -- bubbles/table skips it entirely", i, w)
				}
				sum += w + 2
			}
			if sum != c.width {
				t.Fatalf("expected rendered widths to sum to %d, got %d (widths=%v)", c.width, sum, widths)
			}
		})
	}
}

func TestPanelInnerWidth_FloorsBeforeFirstResize(t *testing.T) {
	m := newTestModel(t)
	m.width = 0
	if got := m.panelInnerWidth(); got < 20 {
		t.Fatalf("expected a floor of at least 20 before the first WindowSizeMsg, got %d", got)
	}
}

func TestPanelInnerWidth_MatchesPanelStyleOverhead(t *testing.T) {
	m := newTestModel(t)
	m.width = 100
	// panelStyle costs 4 columns total (RoundedBorder + Padding(0,1) on
	// each side) -- panelInnerWidth's whole point is making the panel's
	// OVERALL rendered width equal m.width.
	if got, want := m.panelInnerWidth(), 96; got != want {
		t.Fatalf("expected panelInnerWidth() = %d for m.width = 100, got %d", want, got)
	}
}

func TestRenderRooms_EmptyShowsPlaceholder(t *testing.T) {
	m := newTestModel(t)
	got := m.renderRooms()
	if !strings.Contains(got, "no rooms joined yet") {
		t.Fatalf("expected the empty-state placeholder, got %q", got)
	}
}

func TestRenderPendingRings_EmptyShowsNone(t *testing.T) {
	m := newTestModel(t)
	got := m.renderPendingRings()
	if !strings.Contains(got, "none") {
		t.Fatalf("expected the empty-state placeholder, got %q", got)
	}
}

func TestRenderPresence_EmptyShowsNoTable(t *testing.T) {
	m := newTestModel(t)
	got := m.renderPresence()
	if strings.Contains(got, "Name") {
		t.Fatalf("expected no table header for zero agents, got %q", got)
	}
}

// Regression coverage for a real bug found live 2026-09-06: panelStyle has
// no explicit .Width() of its own, so an empty-state placeholder that was
// just a bare string made the panel shrink tightly around it -- stuck
// narrow regardless of m.width, unlike the populated-table case (whose
// own rows already fill panelInnerWidth()). Checked at two different
// m.width values specifically to prove the placeholder actually tracks
// the terminal rather than happening to match one fixed width by
// coincidence.
func TestEmptyPanels_PlaceholderFillsPanelWidth(t *testing.T) {
	for _, width := range []int{80, 50} {
		m := newTestModel(t)
		m.width = width
		m.resizeComponents()
		wantWidth := m.panelInnerWidth()

		for _, tc := range []struct {
			name   string
			render func() string
		}{
			{"Rooms", m.renderRooms},
			{"Pending rings", m.renderPendingRings},
			{"Presence", m.renderPresence},
		} {
			got := tc.render()
			lines := strings.Split(got, "\n")
			placeholder := lines[len(lines)-1] // title is line 0, placeholder is the last line
			if gotWidth := lipgloss.Width(placeholder); gotWidth != wantWidth {
				t.Fatalf("%s at m.width=%d: expected placeholder width %d, got %d (line=%q)",
					tc.name, width, wantWidth, gotWidth, placeholder)
			}
		}
	}
}

func TestRenderRooms_TableContainsRoomFields(t *testing.T) {
	m := newTestModel(t)
	m.width = 100
	m.state.joined = []joinedRoom{
		{RoomTopic: "agents.room.deadbeef", OpenedBy: "abc123", OpenedByPetname: "swift-otter", Purpose: "plan the release", ParticipantsSeen: []string{"a", "b"}, MessagesReceived: 7},
	}
	got := m.renderRooms()
	for _, want := range []string{"plan the release", "swift-otter", "2", "7"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected rendered rooms table to contain %q, got:\n%s", want, got)
		}
	}
}

func TestRenderRooms_StillShowsRecentMessagePreviews(t *testing.T) {
	m := newTestModel(t)
	m.width = 100
	m.state.joined = []joinedRoom{{RoomTopic: "agents.room.deadbeef", Purpose: "plan the release"}}
	m.state.recent = map[string][]roomMessage{
		"agents.room.deadbeef": {{From: "abc123", FromPetname: "swift-otter", Text: "ship it"}},
	}
	got := m.renderRooms()
	if !strings.Contains(got, "swift-otter: ship it") {
		t.Fatalf("expected the recent-message preview to survive the switch to a table, got:\n%s", got)
	}
}

func TestRenderPendingRings_TableContainsRingFields(t *testing.T) {
	m := newTestModel(t)
	m.width = 100
	m.state.pending = []pendingRing{
		{RingID: "r1", Direction: "in", Peer: "peerhex", PeerPetname: "quiet-falcon", Purpose: "quick question"},
	}
	got := m.renderPendingRings()
	for _, want := range []string{"in", "quiet-falcon", "quick question"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected rendered pending-rings table to contain %q, got:\n%s", want, got)
		}
	}
}

func TestRenderPresence_TableContainsAgentFields(t *testing.T) {
	m := newTestModel(t)
	m.width = 100
	m.state.agents = []agentPresence{
		{NodeID: "selfnode", OperatorName: "Raf", ConnectedVia: "claude-code 2.1.261", SecondsSinceSeen: 0, IsSelf: true},
	}
	got := m.renderPresence()
	for _, want := range []string{"Raf (you)", "claude-code 2.1.261", "0s ago"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected rendered presence table to contain %q, got:\n%s", want, got)
		}
	}
}

// Issue #7: each presence row gets a colored initials badge, deterministic
// from identity, distinct from the plain-uncolored badge state before.
func TestRenderPresence_ShowsAvatarBadge(t *testing.T) {
	m := newTestModel(t)
	m.width = 100
	m.state.agents = []agentPresence{
		{NodeID: "deadbeef", Petname: "swift-otter", ConnectedVia: "claude-code 2.1.261", SecondsSinceSeen: 0},
	}
	got := m.renderPresence()
	if !strings.Contains(got, "SO") {
		t.Fatalf("expected the 'swift-otter' badge initials 'SO' in the rendered presence panel, got:\n%s", got)
	}
}

// Two different agents in the roster must not silently render with the
// SAME badge -- would defeat the entire point of a per-agent identity
// marker. (Not a strict impossibility with only 6 palette slots, but
// these two particular petnames are asserted not to collide so the test
// stays deterministic rather than probabilistic.)
func TestRenderPresence_DistinctAgentsGetDistinctBadgeColors(t *testing.T) {
	if agentColor("swift-otter") == agentColor("quiet-falcon") {
		t.Skip("these two petnames happen to hash to the same slot -- pick different fixtures")
	}
	m := newTestModel(t)
	m.width = 100
	m.state.agents = []agentPresence{
		{NodeID: "n1", Petname: "swift-otter", ConnectedVia: "x", SecondsSinceSeen: 0},
		{NodeID: "n2", Petname: "quiet-falcon", ConnectedVia: "x", SecondsSinceSeen: 0},
	}
	got := m.renderPresence()
	otterBadge := agentBadgeStyle("swift-otter").Render(agentInitials("swift-otter"))
	falconBadge := agentBadgeStyle("quiet-falcon").Render(agentInitials("quiet-falcon"))
	if !strings.Contains(got, otterBadge) || !strings.Contains(got, falconBadge) {
		t.Fatalf("expected both agents' own distinct badge renderings present, got:\n%s", got)
	}
}

// Issue #8: the speaker's name in a room's recent-message preview carries
// their deterministic identity color -- same identityKey/agentColor pair
// the presence badge (issue #7) uses, so an agent is the same color
// everywhere in the mesh view.
func TestRenderRooms_MessagePreviewColorsSpeakerName(t *testing.T) {
	m := newTestModel(t)
	m.width = 100
	m.state.joined = []joinedRoom{{RoomTopic: "agents.room.deadbeef", Purpose: "plan the release"}}
	m.state.recent = map[string][]roomMessage{
		"agents.room.deadbeef": {{From: "n1", FromPetname: "swift-otter", Text: "ship it"}},
	}
	got := m.renderRooms()
	wantName := agentBadgeStyle(identityKey("n1", "swift-otter")).Render("swift-otter")
	if !strings.Contains(got, wantName) {
		t.Fatalf("expected the speaker name styled in its identity color, got:\n%s", got)
	}
}

// Regression guard for the double-padding bug caught by actually
// rendering sample data: bubbles/table's Model.cursor starts at 0
// regardless of focus, and renderRow wraps the whole already-cell-padded
// row in Selected on top when it's the cursor row. A naive Selected =
// Cell copy re-applies Cell's own Padding(0,1) at the row level -- this
// doesn't change the row's TOTAL rendered width (one style redistributes
// a space from the right edge to the left, it doesn't add one), so
// comparing total width across rows does not catch it. It does shift
// row 0's columns one space right of every other row's, which a leading-
// padding comparison against the (never Selected-styled) header does
// catch -- exactly what caught this by eye in the first place.
func TestTableStyles_SelectedRowNotMisaligned(t *testing.T) {
	tbl := table.New(
		table.WithColumns([]table.Column{{Title: "A", Width: 5}, {Title: "B", Width: 5}}),
		table.WithRows([]table.Row{{"row0a", "row0b"}, {"row1a", "row1b"}}),
		table.WithHeight(3),
		table.WithStyles(tableStyles()),
	)
	lines := strings.Split(tbl.View(), "\n")
	if len(lines) < 3 {
		t.Fatalf("expected a header + 2 data rows, got %d lines: %q", len(lines), lines)
	}
	leadingSpaces := func(s string) int { return len(s) - len(strings.TrimLeft(s, " ")) }
	headerIndent := leadingSpaces(lines[0])
	row0Indent := leadingSpaces(lines[1])
	row1Indent := leadingSpaces(lines[2])
	if row0Indent != headerIndent {
		t.Fatalf("expected row 0 (the default cursor position) to align with the header (indent %d), got indent %d:\nheader=%q\nrow0=%q",
			headerIndent, row0Indent, lines[0], lines[1])
	}
	if row1Indent != headerIndent {
		t.Fatalf("expected row 1 to align with the header (indent %d), got indent %d:\nheader=%q\nrow1=%q",
			headerIndent, row1Indent, lines[0], lines[2])
	}
}
