package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/lipgloss"
)

// panelInnerWidth is the content width available inside a panelStyle-
// wrapped panel: RoundedBorder (1 col each side) plus Padding(0,1) (1 col
// each side) costs 4 columns total, so subtracting them here is what makes
// the panel's OVERALL rendered width equal m.width -- the same full-width
// goal chatViewport already achieves directly by setting its own width to
// m.width with no wrapper. The floor guards the pre-first-WindowSizeMsg
// state (m.width == 0) and pathologically narrow terminals.
func (m Model) panelInnerWidth() int {
	w := m.width - 4
	if w < 20 {
		w = 20
	}
	return w
}

// columnWidths distributes width across len(spec) bubbles/table columns.
// Each entry in spec is either a fixed content width, or -1 for the one
// column that should absorb whatever's left. Accounts for bubbles/table's
// own Padding(0,1) per cell (an extra 2 columns of rendered width BEYOND
// each column's declared Width, added by table.DefaultStyles' Header/Cell
// styles) so the columns' rendered widths always sum to exactly width.
// The presence panel (its own hand-rolled lipgloss cells, not
// bubbles/table) needs a different overhead -- see presenceColumnWidths.
func columnWidths(width int, spec []int) []int {
	return distributeColumnWidths(width, spec, 2)
}

// presenceColumnWidths is columnWidths' counterpart for the presence
// panel's own presenceCol cells. Found live 2026-09-06 (issue #11):
// renderPresence originally reused columnWidths directly, which silently
// under-budgeted every row by 2*n columns. columnWidths' -2*n
// compensation exists because bubbles/table's Padding(0,1) adds 2 columns
// of rendered width BEYOND each column's declared Width -- confirmed
// empirically that presenceCol's plain lipgloss cells work the opposite
// way: Width(20).Padding(0,1).Render(...) measures to exactly 20, not 22,
// because lipgloss's Width sets the TOTAL rendered width and Padding is
// absorbed inside it. Reusing columnWidths' bubbles/table-shaped
// compensation here made every presence row render 2*n columns narrower
// than panelInnerWidth() actually allows.
func presenceColumnWidths(width int, spec []int) []int {
	return distributeColumnWidths(width, spec, 0)
}

// distributeColumnWidths is the shared "one flex column absorbs the
// remainder" algorithm; overhead is how many columns of rendered width
// per cell exist BEYOND its declared Width (2 for bubbles/table's
// Padding(0,1), 0 for a lipgloss cell whose Width already includes its
// own padding) -- see columnWidths and presenceColumnWidths above for
// which is which and why they differ.
func distributeColumnWidths(width int, spec []int, overhead int) []int {
	n := len(spec)
	widths := make([]int, n)
	fixedSum := 0
	flexIdx := -1
	for i, w := range spec {
		if w < 0 {
			flexIdx = i
			continue
		}
		widths[i] = w
		fixedSum += w
	}
	if flexIdx < 0 {
		return widths
	}

	const minFlex = 8
	budget := width - overhead*n
	if flexWant := budget - fixedSum; flexWant >= minFlex {
		widths[flexIdx] = flexWant
		return widths
	}

	// Not enough room for every fixed column at full size AND a
	// reasonably-sized flex column. Found live 2026-09-06: the old
	// version floored the flex column at a fixed 8 unconditionally, while
	// leaving every fixed column at full size -- so the table (and the
	// panel wrapping it, which has no explicit .Width() of its own and
	// just sizes to its widest line) rendered WIDER than `width` below
	// whatever point this floor first triggered, visibly "stuck" instead
	// of continuing to shrink with the terminal. Shrink every column
	// (fixed ones included) proportionally so the total still matches
	// `width` exactly, down to a hard floor of 1 per column -- bubbles/
	// table truncates any cell with an ellipsis regardless of width, so 1
	// is tight but never broken, just as narrow as it gets.
	widths[flexIdx] = minFlex
	total := fixedSum + minFlex
	assigned := 0
	for i, w := range widths {
		scaled := w * budget / total
		if scaled < 1 {
			scaled = 1
		}
		widths[i] = scaled
		assigned += scaled
	}
	widths[flexIdx] += budget - assigned // remainder from integer division, keeps the sum exact
	if widths[flexIdx] < 1 {
		widths[flexIdx] = 1
	}
	return widths
}

// tableStyles matches table.DefaultStyles' Header/Cell (bold header,
// Padding(0,1) on both) but makes Selected a no-op style. These tables are
// read-only mesh-state display -- never focused or navigated, no
// table.Focus() call anywhere in this package -- but bubbles/table's own
// Model.cursor starts at 0 regardless of focus state, and renderRow
// applies the Selected style to whichever row equals cursor unconditionally.
// Without this override, row 0 of every panel would render bold pink for
// no reason a viewer could make sense of.
//
// Selected can't just be set equal to Cell, either -- caught by actually
// rendering sample data before calling this done, not just reasoning about
// it: renderRow already applies Cell's Padding(0,1) once per cell, then
// wraps the WHOLE already-joined row in Selected on top when it's the
// cursor row. Cell's own Padding(0,1) applied a second time at the row
// level added one extra space of left/right padding to row 0 only,
// visibly misaligning it against every other row's columns. An empty
// lipgloss.Style{} wrapping the row is a true no-op; a copy of Cell is not.
func tableStyles() table.Styles {
	s := table.DefaultStyles()
	s.Selected = lipgloss.NewStyle()
	return s
}

// panelPlaceholder pads an empty-state message out to the panel's full
// inner width, matching how a populated table's own rows already do.
// Found live 2026-09-06: panelStyle has no explicit .Width() of its own,
// so without this it shrinks tightly around whatever short placeholder
// string it's given -- the panel visibly stopped tracking m.width
// whenever a list was empty (resizing/zooming did nothing to it), because
// nothing about the bare string depended on m.width at all.
func (m Model) panelPlaceholder(text string) string {
	return lipgloss.NewStyle().Width(m.panelInnerWidth()).Render(dimStyle.Render(text))
}

func (m Model) renderRooms() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render(fmt.Sprintf("Rooms (%d joined)", len(m.state.joined))) + "\n")
	if len(m.state.joined) == 0 {
		b.WriteString(m.panelPlaceholder("no rooms joined yet"))
		return b.String()
	}

	inner := m.panelInnerWidth()
	widths := columnWidths(inner, []int{-1, 20, 12, 10})
	t := table.New(
		table.WithColumns([]table.Column{
			{Title: "Room", Width: widths[0]},
			{Title: "Opened by", Width: widths[1]},
			{Title: "Participants", Width: widths[2]},
			{Title: "Messages", Width: widths[3]},
		}),
		table.WithWidth(inner),
		table.WithHeight(len(m.state.joined)+1),
		table.WithStyles(tableStyles()),
	)
	rows := make([]table.Row, 0, len(m.state.joined))
	for _, r := range m.state.joined {
		rows = append(rows, table.Row{
			roomLabel(r.RoomTopic, r.Purpose),
			displayName(r.OpenedBy, r.OpenedByPetname),
			fmt.Sprintf("%d", len(r.ParticipantsSeen)),
			fmt.Sprintf("%d", r.MessagesReceived),
		})
	}
	t.SetRows(rows)
	b.WriteString(t.View())

	// Recent-message previews, grouped by room below the aligned table --
	// unchanged in substance from the pre-table layout (still the last 3
	// messages per room, still displayName/truncate), just no longer
	// interleaved directly under each room's own row now that rows live
	// in a table together.
	for _, r := range m.state.joined {
		recent := lastN(m.state.recent[r.RoomTopic], 3)
		if len(recent) == 0 {
			continue
		}
		b.WriteString("\n" + dimStyle.Render(roomLabel(r.RoomTopic, r.Purpose)+":"))
		for _, msg := range recent {
			// Issue #8: the speaker's name carries their deterministic
			// identity color (same principle as the presence badge below,
			// IRC-nick-style); the message body stays dim -- coloring the
			// whole line would fight legibility for exactly the busy
			// multi-agent conversation this is meant to help read.
			who := agentBadgeStyle(identityKey(msg.From, msg.FromPetname)).Render(displayName(msg.From, msg.FromPetname))
			b.WriteString("\n    " + who + dimStyle.Render(": "+truncate(msg.Text, 80)))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m Model) renderPendingRings() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render(fmt.Sprintf("Pending rings (%d)", len(m.state.pending))) + "\n")
	if len(m.state.pending) == 0 {
		b.WriteString(m.panelPlaceholder("none"))
		return b.String()
	}

	inner := m.panelInnerWidth()
	widths := columnWidths(inner, []int{10, 20, -1})
	t := table.New(
		table.WithColumns([]table.Column{
			{Title: "Direction", Width: widths[0]},
			{Title: "From", Width: widths[1]},
			{Title: "Purpose", Width: widths[2]},
		}),
		table.WithWidth(inner),
		table.WithHeight(len(m.state.pending)+1),
		table.WithStyles(tableStyles()),
	)
	rows := make([]table.Row, 0, len(m.state.pending))
	for _, r := range m.state.pending {
		rows = append(rows, table.Row{
			r.Direction,
			displayName(r.Peer, r.PeerPetname),
			r.Purpose,
		})
	}
	t.SetRows(rows)
	b.WriteString(t.View())
	return strings.TrimRight(b.String(), "\n")
}

// presenceCol renders one presence-table cell to exactly width visual
// columns (ANSI-aware pad/truncate via lipgloss.Width, ignoring escape
// bytes), matching table.DefaultStyles' own Padding(0,1) per cell so this
// panel still lines up with Rooms/Pending rings alongside it. Deliberately
// NOT built on bubbles/table: its cell truncation
// (runewidth.Truncate(value, width, "...")) measures raw bytes, so an
// embedded ANSI color escape (issue #7's badge) would be sliced into and
// corrupted -- found by reading table.go's own renderRow, not live, but
// exactly the kind of thing this file's own columnWidths comment already
// warns about verifying by rendering rather than reasoning.
func presenceCol(content string, width int, bold bool) string {
	s := lipgloss.NewStyle().Width(width).MaxWidth(width).Padding(0, 1).Inline(true)
	if bold {
		s = s.Bold(true)
	}
	return s.Render(content)
}

func (m Model) renderPresence() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render(fmt.Sprintf("Presence (%d agents)", len(m.state.agents))) + "\n")
	if len(m.state.agents) == 0 {
		// Same latent shape as Rooms/Pending rings' empty states above --
		// unconfirmed live so far since "you" is always in the roster in
		// practice, but the panel would be just as stuck-narrow here if
		// it were ever hit without this.
		b.WriteString(m.panelPlaceholder("no agents seen yet"))
		return b.String()
	}

	inner := m.panelInnerWidth()
	// badge is a fixed 4-column slot (Padding(0,1) + 2-char initials);
	// the rest split the remaining width the same "one flex column"
	// shape as the sibling panels -- via presenceColumnWidths, NOT
	// columnWidths (see its own doc comment: reusing columnWidths here
	// under-budgeted every row by 2*n columns, issue #11).
	widths := presenceColumnWidths(inner, []int{4, -1, 20, 12})
	header := lipgloss.JoinHorizontal(lipgloss.Top,
		presenceCol("", widths[0], true),
		presenceCol("Name", widths[1], true),
		presenceCol("Connected via", widths[2], true),
		presenceCol("Last seen", widths[3], true),
	)
	b.WriteString(header + "\n")

	rows := make([]string, 0, len(m.state.agents))
	for _, a := range m.state.agents {
		// operator_name (a human-chosen self-description) wins when set;
		// petname (a deterministic, human-legible stand-in for the raw
		// node_id, never self-asserted) is next; the hex id is the last
		// resort, not the default -- per the same "don't make a human
		// read raw hex" principle driving the ring pop-up design.
		name := a.OperatorName
		if name == "" {
			name = a.Petname
		}
		if name == "" {
			name = shortID(a.NodeID)
		}
		if a.IsSelf {
			name += " (you)"
		}

		// Issue #7's avatar badge: 1-2 initials in this agent's
		// deterministic identity color -- same identityKey/agentColor
		// pairing the Rooms panel's message previews use, so an agent
		// reads as the same color everywhere it shows up in the mesh view.
		identity := identityKey(a.NodeID, a.Petname)
		badge := agentBadgeStyle(identity).Render(agentInitials(identity))

		rows = append(rows, lipgloss.JoinHorizontal(lipgloss.Top,
			presenceCol(badge, widths[0], false),
			presenceCol(name, widths[1], false),
			presenceCol(a.ConnectedVia, widths[2], false),
			presenceCol(fmt.Sprintf("%ds ago", a.SecondsSinceSeen), widths[3], false),
		))
	}
	b.WriteString(strings.Join(rows, "\n"))
	return b.String()
}
