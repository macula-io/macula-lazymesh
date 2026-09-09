package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/lipgloss"

	"github.com/macula-io/macula-lazymesh/internal/meshservices"
	"github.com/macula-io/macula-lazymesh/internal/realmjoin"

	"crypto/sha256"
	"encoding/hex"
)

// ioMaculaRealm mirrors meshservices.go's own unexported pinnedRealm
// computation (sha256 of "io.macula", uppercase hex) -- the real value,
// not a copy-pasted literal, so a future realm-name change can't drift
// silently between the package and these tests.
func ioMaculaRealm() string {
	sum := sha256.Sum256([]byte("io.macula"))
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

// fakeMeshMCP satisfies meshservices' own (unexported) mcpCaller
// interface structurally -- same technique internal/meshservices' own
// tests use, needed here to construct a real *meshservices.Source
// (rather than reasoning about its private fields) for the `s` panel's
// enabled/discovered states.
type fakeMeshMCP struct {
	discoveryResponse string
}

func (f *fakeMeshMCP) CallTool(ctx context.Context, name string, args map[string]any) (string, error) {
	if name == "mesh_find_records_by_type" {
		if f.discoveryResponse == "" {
			return `{"records":[]}`, nil
		}
		return f.discoveryResponse, nil
	}
	return "", fmt.Errorf("fakeMeshMCP: no handler for %s", name)
}

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

// session_name exists specifically to tell apart two sessions run by the
// SAME operator (e.g. two Claude Code windows both self-reporting "Raf
// Lefever") -- it folds into the Name column rather than taking a column
// of its own.
func TestRenderPresence_FoldsSessionNameIntoNameColumn(t *testing.T) {
	m := newTestModel(t)
	m.width = 100
	m.state.agents = []agentPresence{
		{NodeID: "n1", OperatorName: "Raf Lefever", SessionName: "Jupiter", ConnectedVia: "claude-code 2.1.261", SecondsSinceSeen: 5, IsSelf: false},
		{NodeID: "n2", OperatorName: "Raf Lefever", SessionName: "Mercury", ConnectedVia: "claude-code 2.1.261", SecondsSinceSeen: 0, IsSelf: true},
	}
	got := m.renderPresence()
	for _, want := range []string{"Raf Lefever (Jupiter)", "Raf Lefever (Mercury) (you)"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected rendered presence table to contain %q, got:\n%s", want, got)
		}
	}
}

// No session_name (the common case, omitted from the JSON when unset) must
// render exactly as before -- no stray parens.
func TestRenderPresence_NoSessionNameOmitsParens(t *testing.T) {
	m := newTestModel(t)
	m.width = 100
	m.state.agents = []agentPresence{
		{NodeID: "n1", OperatorName: "goose", ConnectedVia: "goose-cli 1.48.0", SecondsSinceSeen: 0},
	}
	got := m.renderPresence()
	if !strings.Contains(got, "goose") {
		t.Fatalf("expected rendered presence table to contain %q, got:\n%s", "goose", got)
	}
	if strings.Contains(got, "goose (") {
		t.Fatalf("expected no parenthetical after 'goose' when session_name is unset, got:\n%s", got)
	}
}

// Issue #11: renderPresence originally reused columnWidths, which was
// written for bubbles/table's padding model (Padding(0,1) adds 2 columns
// of rendered width BEYOND declared Width). presenceCol's plain lipgloss
// cells work the opposite way (Width already includes Padding), so
// reusing that compensation silently rendered every row 2*n columns
// narrower than panelInnerWidth(). Measures ACTUAL lipgloss.Width(), not
// just content presence -- the previous presence tests would have passed
// unchanged even with this bug present.
func TestRenderPresence_RowsFillPanelInnerWidth(t *testing.T) {
	m := newTestModel(t)
	m.width = 100
	m.resizeComponents()
	m.state.agents = []agentPresence{
		{NodeID: "deadbeef", Petname: "swift-otter", ConnectedVia: "claude-code 2.1.261", SecondsSinceSeen: 5},
	}
	got := m.renderPresence()
	lines := strings.Split(got, "\n")
	if len(lines) < 3 {
		t.Fatalf("expected title + header + at least one row, got %d lines: %q", len(lines), lines)
	}
	want := m.panelInnerWidth()
	// lines[0] is the title (titleStyle text, not width-constrained by
	// design -- Rooms/Pending rings' titles aren't either); header and
	// data rows are the ones presenceColumnWidths actually budgets.
	if got := lipgloss.Width(lines[1]); got != want {
		t.Fatalf("header row: expected width %d (panelInnerWidth), got %d: %q", want, got, lines[1])
	}
	if got := lipgloss.Width(lines[2]); got != want {
		t.Fatalf("data row: expected width %d (panelInnerWidth), got %d: %q", want, got, lines[2])
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

// Three distinct states, per renderMeshServices' own doc comment -- a
// human must never see "not live" collapse "disabled" and "not yet
// checked" into the same claim.

func TestRenderMeshServices_DisabledShowsCuratedCatalogMarkedInactive(t *testing.T) {
	m := newTestModel(t) // meshServices is nil -- the default, cfg.MeshServicesEnabled false
	m.width = 200        // wide enough that the longest curated procedure name isn't truncated -- this test checks for the full name, not table-truncation behavior (that's covered elsewhere)
	got := m.renderMeshServices()

	if !strings.Contains(got, fmt.Sprintf("%d curated", len(meshservices.Curated))) {
		t.Fatalf("expected the title to report every curated entry even while disabled, got:\n%s", got)
	}
	if !strings.Contains(got, "disabled -- set mesh_services_enabled: true") {
		t.Fatalf("expected the enable hint, got:\n%s", got)
	}
	if !strings.Contains(got, meshservices.Curated[0].Procedure()) {
		t.Fatalf("expected the first curated procedure listed by name, got:\n%s", got)
	}
	if !strings.Contains(got, "inactive") {
		t.Fatalf("expected every row marked inactive while disabled, got:\n%s", got)
	}
	if strings.Contains(got, "not live") || strings.Contains(got, "checking") {
		t.Fatalf("expected the disabled state to read as its own thing, not 'not live'/'checking', got:\n%s", got)
	}
}

func TestRenderMeshServices_EnabledNotYetDiscoveredShowsChecking(t *testing.T) {
	m := newTestModel(t)
	// A freshly-constructed Source, Snapshot never preceded by ListTools --
	// exactly the shape of a human opening this panel before the agent's
	// own first tool call (Discovery is lazy, see meshservices.go).
	m.meshServices = meshservices.New(&fakeMeshMCP{})

	got := m.renderMeshServices()
	if strings.Contains(got, "disabled") {
		t.Fatalf("expected no disabled messaging once meshServices is set, got:\n%s", got)
	}
	if !strings.Contains(got, "checking...") {
		t.Fatalf("expected every row marked checking before discovery has resolved, got:\n%s", got)
	}
	if strings.Contains(got, "not live") {
		t.Fatalf("expected 'not yet checked' to never read as 'confirmed not live', got:\n%s", got)
	}
}

func TestRenderMeshServices_EnabledAndDiscoveredMarksOnlyTheDiscoveredProcedureLive(t *testing.T) {
	m := newTestModel(t)
	discovered := meshservices.Curated[0]
	fake := &fakeMeshMCP{discoveryResponse: fmt.Sprintf(
		`{"records":[{"procedure_advertisement":{"realm":%q,"procedure":%q}}]}`,
		ioMaculaRealm(), discovered.Procedure(),
	)}
	src := meshservices.New(fake)
	if _, err := src.ListTools(context.Background()); err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	m.meshServices = src

	got := m.renderMeshServices()
	if !strings.Contains(got, "live") {
		t.Fatalf("expected at least one row marked live, got:\n%s", got)
	}
	if !strings.Contains(got, "not live") {
		t.Fatalf("expected every other curated procedure marked not live, got:\n%s", got)
	}
	if strings.Contains(got, "checking") {
		t.Fatalf("expected no row still marked checking once discovery has resolved, got:\n%s", got)
	}
	if !strings.Contains(got, fmt.Sprintf("%d live", 1)) {
		t.Fatalf("expected the title's live count to be exactly 1, got:\n%s", got)
	}
}

func TestRenderRealms_EmptyShowsPlaceholder(t *testing.T) {
	m := newTestModel(t)
	got := m.renderRealms()
	if !strings.Contains(got, "no realms joined yet") {
		t.Fatalf("expected the empty-state placeholder, got %q", got)
	}
}

func TestRenderRealms_TableContainsMembershipFields(t *testing.T) {
	m := newTestModel(t)
	m.width = 100
	m.state.realms = []realmMembership{
		{Realm: "net.beam-campus.sales", Handle: "rgfaber", Tier: "citizen", JoinedAt: "2026-09-08T00:00:00Z"},
	}
	got := m.renderRealms()
	for _, want := range []string{"net.beam-campus.sales", "rgfaber", "citizen"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected rendered realms table to contain %q, got:\n%s", want, got)
		}
	}
}

// A join in progress replaces the plain list entirely, same as the ring
// pop-up takes over rather than sitting alongside other panels -- not
// both shown at once.
// Found live rendering this before committing: ModeRealmJoin originally
// had no branch in renderRealms at all, so pressing `i` silently left
// the membership list (or empty placeholder) showing with realmJoinInput
// typed into but never displayed anywhere -- a human would type a realm
// name and see nothing happen until pressing Enter.
func TestRenderRealms_ModeRealmJoinShowsTheInputBoxNotTheList(t *testing.T) {
	m := newTestModel(t)
	m.state.realms = []realmMembership{{Realm: "io.macula", Handle: "rgfaber"}}
	m.mode = ModeRealmJoin
	m.realmJoinInput.SetValue("net.beam-campus")

	got := m.renderRealms()
	if !strings.Contains(got, "net.beam-campus") {
		t.Fatalf("expected the in-progress typed realm name visible, got:\n%s", got)
	}
	if strings.Contains(got, "io.macula") {
		t.Fatalf("expected the membership list NOT shown while typing a new realm name, got:\n%s", got)
	}
}

func TestRenderRealms_AJoinInProgressReplacesTheListEntirely(t *testing.T) {
	m := newTestModel(t)
	m.state.realms = []realmMembership{{Realm: "io.macula", Handle: "rgfaber"}}
	ev := realmjoin.Event{Kind: "session", Realm: "net.beam-campus", JoinURL: "https://realm.beam-campus.net/join/s1"}
	m.realmJoinLatest = &ev

	got := m.renderRealms()
	if strings.Contains(got, "io.macula") {
		t.Fatalf("expected the plain membership list NOT shown while a join is in progress, got:\n%s", got)
	}
	if !strings.Contains(got, "net.beam-campus") || !strings.Contains(got, "https://realm.beam-campus.net/join/s1") {
		t.Fatalf("expected the in-progress join's own realm and link, got:\n%s", got)
	}
}

func TestRealmJoinStatusLine_EachEventKindGetsItsOwnWording(t *testing.T) {
	cases := []struct {
		name string
		ev   realmjoin.Event
		want string
	}{
		{"session", realmjoin.Event{Kind: "session", ExpiresAt: "2026-09-08T20:00:00Z"}, "waiting for confirmation"},
		{"starting", realmjoin.Event{Kind: "starting", Realm: "io.macula"}, "starting join for io.macula"},
		{"already_joined prefers handle", realmjoin.Event{Kind: "already_joined", Realm: "io.macula", Handle: "rgfaber"}, "already joined io.macula as rgfaber"},
		{"already_joined falls back to org_identity", realmjoin.Event{Kind: "already_joined", Realm: "io.macula", OrgIdentity: "mri:org:io.macula/rgfaber"}, "as mri:org:io.macula/rgfaber"},
		{"confirmed", realmjoin.Event{Kind: "confirmed", Realm: "io.macula", Handle: "rgfaber"}, "joined io.macula as rgfaber"},
		{"expired", realmjoin.Event{Kind: "expired", Realm: "io.macula"}, "expired"},
		{"timeout", realmjoin.Event{Kind: "timeout", Realm: "io.macula"}, "gave up waiting"},
		{"error", realmjoin.Event{Kind: "error", Realm: "io.macula", Message: "boom"}, "boom"},
		{"spawn_error", realmjoin.Event{Kind: "spawn_error", Realm: "io.macula", Message: "npx not found"}, "npx not found"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := realmJoinStatusLine(c.ev)
			if !strings.Contains(got, c.want) {
				t.Fatalf("expected %q to contain %q, got %q", got, c.want, got)
			}
		})
	}
}
