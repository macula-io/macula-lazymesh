package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/lipgloss"

	"github.com/macula-io/macula-lazymesh/internal/meshservices"
	"github.com/macula-io/macula-lazymesh/internal/realmjoin"
)

// panelInnerWidth is the content width available inside a panelStyle-
// wrapped panel: RoundedBorder (1 col each side) plus Padding(0,1) (1 col
// each side) costs 4 columns total, so subtracting them here is what makes
// the panel's OVERALL rendered width equal m.width -- the same full-width
// goal chatViewport already achieves directly by setting its own width to
// m.width with no wrapper. There is no floor: a floor wider than the
// terminal is exactly how a panel ends up wider than the screen it is shown
// on, which the terminal then wraps (#113 review of #86's class). A width
// of zero (before the first resize) wraps nothing.
func (m Model) panelInnerWidth() int {
	if m.width <= 0 {
		return 1 << 20
	}
	return max(m.width-4, 1)
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
	// Rounding every column UP to one cell can still leave the total above
	// the budget at an extremely narrow width -- found at nine cells, where
	// three columns rendered ten and the panel was one cell wider than the
	// terminal (#113). Take the difference back off the widest columns,
	// never below the same floor of one.
	for {
		total, widest := 0, -1
		for i, w := range widths {
			total += w
			if w > 1 && (widest < 0 || w > widths[widest]) {
				widest = i
			}
		}
		if total <= budget || widest < 0 {
			break
		}
		widths[widest]--
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
			termTextOf(roomLabel(r.RoomTopic, r.Purpose)).String(),
			termTextOf(displayName(r.OpenedBy, r.OpenedByPetname)).String(),
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
		b.WriteString("\n" + dimStyle.Render(termTextOf(roomLabel(r.RoomTopic, r.Purpose)+":").String()))
		for _, msg := range recent {
			// Issue #8: the speaker's name carries their deterministic
			// identity color (same principle as the presence badge below,
			// IRC-nick-style); the message body stays dim -- coloring the
			// whole line would fight legibility for exactly the busy
			// multi-agent conversation this is meant to help read.
			who := agentBadgeStyle(identityKey(msg.From, msg.FromPetname)).Render(termTextOf(displayName(msg.From, msg.FromPetname)).String())
			b.WriteString("\n    " + who + dimStyle.Render(termTextOf(": "+truncate(msg.Text, 80)).String()))
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
			termTextOf(r.Direction).String(),
			termTextOf(displayName(r.Peer, r.PeerPetname)).String(),
			termTextOf(r.Purpose).String(),
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
	// Session (14) and Model (16) are fixed-width like Connected via/Last
	// seen -- both are short, low-cardinality self-reported strings, not
	// identifying content worth flexing for. Name is the one flex column,
	// same "one flex column absorbs the rest" shape as the sibling panels
	// -- via presenceColumnWidths, NOT columnWidths (see its own doc
	// comment: reusing columnWidths here under-budgeted every row by 2*n
	// columns, issue #11). On a narrow terminal distributeColumnWidths
	// shrinks every column (fixed ones included) proportionally, so this
	// degrades the same way the 4-column layout did.
	widths := presenceColumnWidths(inner, []int{4, -1, 14, 16, 20, 12})
	header := lipgloss.JoinHorizontal(lipgloss.Top,
		presenceCol("", widths[0], true),
		presenceCol("Name", widths[1], true),
		presenceCol("Session", widths[2], true),
		presenceCol("Model", widths[3], true),
		presenceCol("Connected via", widths[4], true),
		presenceCol("Last seen", widths[5], true),
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
			presenceCol(termTextOf(name).String(), widths[1], false),
			presenceCol(termTextOf(a.SessionName).String(), widths[2], false),
			presenceCol(termTextOf(a.Model).String(), widths[3], false),
			presenceCol(termTextOf(a.ConnectedVia).String(), widths[4], false),
			presenceCol(fmt.Sprintf("%ds ago", a.SecondsSinceSeen), widths[5], false),
		))
	}
	b.WriteString(strings.Join(rows, "\n"))
	return b.String()
}

// meshServiceStatusLabel is one curated procedure's row-level status, in
// the same rendered form regardless of WHY it's that way -- disabled,
// not-yet-discovered, and discovered-but-absent all read differently to
// an operator (see renderMeshServices' own doc on why they must not
// collapse into one "not live" reading).
func meshServiceStatusLabel(enabled, discovered, live bool) string {
	switch {
	case !enabled:
		return dimStyle.Render("inactive")
	case !discovered:
		return dimStyle.Render("checking...")
	case live:
		return statusStripStyle.Render("live")
	default:
		return dimStyle.Render("not live")
	}
}

// renderMeshServices is the `s` panel: every curated mesh-service
// procedure (internal/meshservices.Curated -- always all 16, static data
// regardless of anything below), each tagged with its own current
// standing. Three distinct states, never collapsed into one:
//   - m.meshServices == nil: cfg.MeshServicesEnabled is false (the
//     default, see config.MeshServicesEnabled's own doc comment on why)
//     -- the catalog is shown anyway, as reference, every row "inactive",
//     with a note on how to turn it on. Never a blank panel just because
//     the feature happens to be off.
//   - enabled but !discovered: discovery is lazy, resolved once on the
//     agent's own first tool call (see meshservices.go's Discovery doc
//     comment) -- a human can open this panel before that has happened.
//     "checking..." is not the same claim as "confirmed not live" and
//     must not be presented as one.
//   - enabled and discovered: each entry's real Live/not-live standing,
//     from the EXACT Source instance the agent's own tool calls go
//     through (see Options.MeshServices' own doc comment on why this
//     isn't a second, independently-polled query).
//
// meshServiceEntries returns the same list renderMeshServices renders,
// pulled out so Up/Down cursor clamping (handleKey) and direct invocation
// (starting in ModeMeshServiceCall) see exactly the row the operator is
// actually looking at -- computing this twice with subtly different logic
// is how a cursor ends up pointing at a different row than the one drawn.
func (m Model) meshServiceEntries() (entries []meshservices.ServiceStatus, discovered bool) {
	if m.meshServices == nil {
		entries = make([]meshservices.ServiceStatus, len(meshservices.Curated))
		for i, cp := range meshservices.Curated {
			entries[i] = meshservices.ServiceStatus{CuratedProcedure: cp}
		}
		return entries, false
	}
	return m.meshServices.Snapshot()
}

func (m Model) renderMeshServices() string {
	if m.mode == ModeMeshServiceCall {
		return m.renderMeshServiceCallInput()
	}

	enabled := m.meshServices != nil
	entries, discovered := m.meshServiceEntries()

	liveCount := 0
	for _, e := range entries {
		if e.Live {
			liveCount++
		}
	}

	var b strings.Builder
	inner := m.panelInnerWidth()
	// Every line this panel writes itself wraps to the room the panel
	// border and padding leave. A fixed-width line here is what made the
	// whole box 72 cells wide on any terminal (#113): the box sizes to its
	// widest line, so one long sentence pushed every other line past the
	// edge with it.
	for _, line := range wrapCells(termTextOf(fmt.Sprintf("MeshServices (%d curated, %d live)", len(entries), liveCount)), inner) {
		b.WriteString(titleStyle.Render(line) + "\n")
	}
	if !enabled {
		for _, line := range wrapCells(termTextOf("disabled -- set mesh_services_enabled: true in config.yaml to enable"), inner) {
			b.WriteString(dimStyle.Render(line) + "\n")
		}
	}
	// Procedure, not Description, is the flex column here -- unlike the
	// sibling panels (Rooms' own flex column is likewise its identifying
	// content), an operator scanning this list needs to know WHICH exact
	// procedure a row is about more than the full text of its
	// description; a truncated description is still useful, a truncated
	// procedure name can hide which of several similarly-prefixed
	// procedures (e.g. mcl-rag/search_chunks_semantic vs.
	// mcl-rag/get_source_by_id) a row actually is.
	//
	// Which of the columns are shown at all follows the width (#113): the
	// description goes first, then the status, and on a terminal too narrow
	// for even one table column the rows are written as plain lines. A table
	// that cannot fit is a table the terminal wraps, one row taller per
	// line.
	cols, widths := meshServiceColumns(inner)
	if cols == nil {
		b.WriteString(meshServicePlainRows(entries, inner, enabled, discovered))
		return strings.TrimRight(b.String(), "\n")
	}
	for i := range cols {
		cols[i].Width = widths[i]
	}
	t := table.New(
		table.WithColumns(cols),
		table.WithWidth(inner),
		table.WithHeight(len(entries)+1),
		// meshServiceTableStyles, not the shared tableStyles() every OTHER
		// panel uses -- see that function's own comment: renderRow applies
		// Selected to the cursor row UNCONDITIONALLY (cursor defaults to 0
		// regardless of table.Focused, and nothing in this app ever calls
		// a table's own Update -- every panel rebuilds its table fresh per
		// render, so Focused has no behavioral effect here either way).
		// tableStyles() blanks Selected so that default cursor=0 doesn't
		// highlight row 0 of every read-only panel for no reason a viewer
		// could make sense of; this is the one panel with a row an
		// operator actually picks (see ModeMeshServiceCall), so it needs
		// the real thing back.
		table.WithStyles(meshServiceTableStyles()),
	)
	rows := make([]table.Row, 0, len(entries))
	for _, e := range entries {
		rows = append(rows, meshServiceRow(e, len(cols), enabled, discovered))
	}
	t.SetRows(rows)
	if len(entries) > 0 {
		t.SetCursor(m.meshServicesCursor)
	}
	b.WriteString(t.View())
	return strings.TrimRight(b.String(), "\n")
}

// meshServiceColumns is the table this panel can show at inner cells of
// content width, with the widths already distributed across them. It is a
// list of what FITS, not of what fits ideally: bubbles/table spends two
// cells of padding per column on top of the column's own width, so three
// columns need nine cells, two need six, one needs three, and below three
// there is no table at all (#113). The order the columns go in is the order
// an operator reads them in reverse: the description is dropped first, the
// status second, and the procedure's name is the last thing to go.
func meshServiceColumns(inner int) ([]table.Column, []int) {
	switch {
	case inner >= 9:
		return []table.Column{{Title: "Procedure"}, {Title: "Status"}, {Title: "Description"}},
			columnWidths(inner, []int{-1, 11, 40})
	case inner >= 6:
		return []table.Column{{Title: "Procedure"}, {Title: "Status"}},
			columnWidths(inner, []int{-1, 11})
	case inner >= 3:
		return []table.Column{{Title: "Procedure"}}, columnWidths(inner, []int{-1})
	}
	return nil, nil
}

// meshServiceRow is one table row with as many cells as the panel is
// showing columns (#113).
func meshServiceRow(e meshservices.ServiceStatus, columns int, enabled, discovered bool) table.Row {
	row := table.Row{e.Procedure()}
	if columns > 1 {
		row = append(row, meshServiceStatusLabel(enabled, discovered, e.Live))
	}
	if columns > 2 {
		row = append(row, e.Description)
	}
	return row
}

// meshServicePlainRows is the list at a width where no table fits: each
// entry is its procedure's name, wrapped, with its status on the line
// after. Nothing is dropped that the panel can still show -- the
// description is, because a description cut to one or two cells says
// nothing (#113).
func meshServicePlainRows(entries []meshservices.ServiceStatus, inner int, enabled, discovered bool) string {
	var b strings.Builder
	for _, e := range entries {
		for _, line := range wrapCells(termTextOf(e.Procedure()), inner) {
			b.WriteString(line + "\n")
		}
		b.WriteString(fitCells(meshServiceStatusLabel(enabled, discovered, e.Live), inner) + "\n")
	}
	return b.String()
}

// meshServiceTableStyles is tableStyles() with a real Selected style --
// see the table.New call site in renderMeshServices for why this table,
// alone among this app's panels, needs one.
func meshServiceTableStyles() table.Styles {
	s := tableStyles()
	s.Selected = lipgloss.NewStyle().Reverse(true)
	return s
}

// renderMeshServiceCallInput mirrors renderRealmJoinInput's own shape --
// same reasoning, see ModeMeshServiceCall's own doc comment (model.go).
func (m Model) renderMeshServiceCallInput() string {
	var b strings.Builder
	// The procedure's name is the panel's own text and can be long: it
	// wraps to the panel's inner width like every other line here, or the
	// box grows past the terminal with it (#113).
	for _, line := range wrapCells(termTextOf("MeshServices -- call "+m.meshServiceCallProcedure), m.panelInnerWidth()) {
		b.WriteString(titleStyle.Render(line) + "\n")
	}
	b.WriteString("\n")
	// The field is one row that has to fit the panel: its prompt and its
	// cursor can together take a cell more than the width it was given
	// (#113). Cutting the last cell is the difference between a box the
	// terminal wraps and one it does not.
	b.WriteString(fitCells(m.meshServiceCallInput.View(), m.panelInnerWidth()))
	return b.String()
}

// renderRealms is the `r` panel: exactly one of three things at a time,
// never stacked -- typing a realm name to join (ModeRealmJoin), an
// in-flight or just-finished join's own status (m.realmJoinLatest), or
// the plain membership list. Found live rendering this with real state
// before committing: the ModeRealmJoin case was originally missing
// entirely -- pressing `i` silently left the list showing with no visible
// sign typing was even happening, since realmJoinInput was never
// rendered anywhere. Same reasoning as the ring pop-up taking over
// rather than sitting alongside the mesh view.
func (m Model) renderRealms() string {
	if m.mode == ModeRealmJoin {
		return m.renderRealmJoinInput()
	}
	if m.realmJoinLatest != nil {
		return m.renderRealmJoinProgress(*m.realmJoinLatest)
	}

	var b strings.Builder
	b.WriteString(titleStyle.Render(fmt.Sprintf("Realms (%d joined)", len(m.state.realms))) + "\n")
	if len(m.state.realms) == 0 {
		b.WriteString(m.panelPlaceholder("no realms joined yet -- " + keyHint(DefaultKeyMap.Submit, "join one")))
		return b.String()
	}

	inner := m.panelInnerWidth()
	// Realm is the flex column, same reasoning as MeshServices' own
	// Procedure column: a truncated handle/tier/date is still useful, a
	// truncated realm name can hide which of several similarly-prefixed
	// realms (net.beam-campus vs. net.beam-campus.sales) a row is.
	widths := columnWidths(inner, []int{-1, 18, 9, 20})
	t := table.New(
		table.WithColumns([]table.Column{
			{Title: "Realm", Width: widths[0]},
			{Title: "Handle", Width: widths[1]},
			{Title: "Tier", Width: widths[2]},
			{Title: "Joined", Width: widths[3]},
		}),
		table.WithWidth(inner),
		table.WithHeight(len(m.state.realms)+1),
		table.WithStyles(tableStyles()),
	)
	rows := make([]table.Row, 0, len(m.state.realms))
	for _, r := range m.state.realms {
		rows = append(rows, table.Row{
			termTextOf(r.Realm).String(),
			termTextOf(r.Handle).String(),
			termTextOf(r.Tier).String(),
			termTextOf(r.JoinedAt).String(),
		})
	}
	t.SetRows(rows)
	b.WriteString(t.View())
	return strings.TrimRight(b.String(), "\n")
}

// realmJoinStatusLine renders one Event as its own human-readable line --
// pulled out from renderRealmJoinProgress so each kind's own wording has
// a direct test independent of the panel's surrounding layout.
func realmJoinStatusLine(ev realmjoin.Event) string {
	// Every field here is the realm server's, not lazymesh's (#95): the
	// realm name, the handle it hands out, its message and its timestamps
	// are all peer-controlled text, so each goes through termTextOf before
	// it is styled or joined into the line.
	realm := termTextOf(ev.Realm)
	switch ev.Kind {
	case "starting":
		return dimStyle.Render(fmt.Sprintf("starting join for %s...", realm))
	case "already_joined":
		return fmt.Sprintf("already joined %s as %s (joined %s)", realm, displayHandle(ev), termTextOf(ev.JoinedAt))
	case "confirmed":
		return statusStripStyle.Render(fmt.Sprintf("✓ joined %s as %s", realm, displayHandle(ev)))
	case "expired":
		return errStyle.Render(fmt.Sprintf("session for %s expired before it was confirmed -- %s", realm, realmRetryHint()))
	case "timeout":
		return errStyle.Render(fmt.Sprintf("gave up waiting for %s -- %s", realm, realmRetryHint()))
	case "error", "spawn_error":
		return errStyle.Render(fmt.Sprintf("error joining %s: %s", realm, termTextOf(ev.Message)))
	default: // "session" -- interim, still polling
		return dimStyle.Render(fmt.Sprintf("waiting for confirmation (session expires %s)...", termTextOf(ev.ExpiresAt)))
	}
}

// realmRetryHint is how a join is tried again: a finished join's status
// holds the panel, so esc dismisses it and enter opens the join input
// again. Written from the bindings, like every other hint on screen (#74).
func realmRetryHint() string {
	return keyHint(DefaultKeyMap.Normal, "dismiss") + ", then " + keyHint(DefaultKeyMap.Submit, "try again")
}

func displayHandle(ev realmjoin.Event) termText {
	if ev.Handle != "" {
		return termTextOf(ev.Handle)
	}
	return termTextOf(ev.OrgIdentity)
}

// renderRealmJoinProgress shows one join's own status -- the link and
// QR while a session is pending, then whatever the terminal outcome was
// once it resolves. The link, the QR and the realm name are the realm
// server's text: macula-mcp's realm.ts qrTerminal is expected to be
// ANSI-free, and termTextOf is what makes that expectation true rather
// than assumed (#95) -- a control the server did slip in is shown, not
// obeyed.
// Esc (handleKey's own ToggleRealm-closing and Normal-mode-within-the-
// panel paths) clears m.realmJoinLatest to get back to the plain list.
func (m Model) renderRealmJoinProgress(ev realmjoin.Event) string {
	var b strings.Builder
	b.WriteString(titleStyle.Render(fmt.Sprintf("Realms -- joining %s", termTextOf(ev.Realm))) + "\n")
	if ev.Kind == "session" {
		if ev.JoinURL != "" {
			b.WriteString(termTextOf(ev.JoinURL).String() + "\n\n")
		}
		if ev.QRTerminal != "" {
			b.WriteString(termTextOf(ev.QRTerminal).String() + "\n\n")
		}
	}
	b.WriteString(realmJoinStatusLine(ev))
	return b.String()
}

// renderRealmJoinInput is ModeRealmJoin's own view -- the text input a
// human types a realm name into. Deliberately no live preview of the
// resolved host or validation-as-you-type here: an invalid name's
// rejection reason (macula-mcp-realm's own parseRealmName, server side)
// surfaces as this join's own "error" event once submitted, the same
// path any other failure takes (realmJoinStatusLine), rather than
// duplicating that grammar client-side.
func (m Model) renderRealmJoinInput() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("Realms -- join which realm?") + "\n\n")
	b.WriteString(m.realmJoinInput.View())
	return b.String()
}
