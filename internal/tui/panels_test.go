package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/table"
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
			// rather than one sized to its content.
			sum := 0
			for _, w := range widths {
				sum += w + 2
			}
			if sum != c.width {
				t.Fatalf("expected rendered widths to sum to %d, got %d (widths=%v)", c.width, sum, widths)
			}
		})
	}
}

func TestColumnWidths_FloorsFlexColumnOnNarrowWidth(t *testing.T) {
	// A pathologically narrow width would otherwise drive the flex
	// column negative -- floors at 8 instead of producing a column that
	// can't even hold an ellipsis.
	widths := columnWidths(10, []int{20, 20, -1})
	if widths[2] != 8 {
		t.Fatalf("expected flex column to floor at 8, got %d", widths[2])
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
