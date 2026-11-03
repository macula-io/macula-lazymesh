package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// macula-lazymesh#61: a person running lazymesh can see at a glance that
// macula-mcp is connected and which release it is, and sees at once when it
// disconnects. The version comes from macula-mcp's own handshake
// (mcpclient.Client.ServerVersion), never from config or npm.

// mcpTestModel is a model that reached macula-mcp: the reported release is
// set, so the summary line carries the segment.
func mcpTestModel(t *testing.T, pinned string) Model {
	t.Helper()
	m := New(nil, Options{
		UserInputCh:              make(chan string, 8),
		StatusBarPosition:        "bottom",
		AgentModel:               "deepseek/deepseek-v4-flash",
		MaculaMCPVersion:         pinned,
		MaculaMCPReportedVersion: "0.51.1",
	})
	m.width, m.height = 80, 24
	m.resizeComponents()
	return m
}

func TestSummaryLine_ShowsTheConnectedMaculaMCPAndItsVersion(t *testing.T) {
	m := mcpTestModel(t, "")
	got := m.renderSummaryLine()
	if !strings.Contains(got, "mcp 0.51.1 ●") {
		t.Fatalf("expected the summary line to name the connected macula-mcp and its version, got %q", got)
	}
	if strings.Contains(got, "disconnected") {
		t.Fatalf("expected a connected segment, got %q", got)
	}
	if strings.Contains(got, "(pinned)") {
		t.Fatalf("expected no (pinned) for a floating release, got %q", got)
	}
}

func TestSummaryLine_MarksAPinnedRelease(t *testing.T) {
	m := mcpTestModel(t, "0.51.1")
	got := m.renderSummaryLine()
	if !strings.Contains(got, "mcp 0.51.1 ●") || !strings.Contains(got, "(pinned)") {
		t.Fatalf("expected the segment to say the release is pinned, got %q", got)
	}
}

func TestSummaryLine_ShowsDisconnectedAfterAFailedRefresh(t *testing.T) {
	m := mcpTestModel(t, "")
	if !strings.Contains(m.renderSummaryLine(), "mcp 0.51.1 ●") {
		t.Fatalf("expected to start connected, got %q", m.renderSummaryLine())
	}

	m, _ = m.handleRefresh(refreshFailure("mesh_agents: connection refused"))
	got := m.renderSummaryLine()
	if !strings.Contains(got, "mcp ✗ disconnected") {
		t.Fatalf("expected the segment to report the connection is gone, got %q", got)
	}
	if strings.Contains(got, "0.51.1") {
		t.Fatalf("expected no version on a disconnected segment -- it would read as still connected, got %q", got)
	}
}

func TestSummaryLine_ReconnectsOnTheNextGoodRefresh(t *testing.T) {
	m := mcpTestModel(t, "")
	m, _ = m.handleRefresh(refreshFailure("mesh_agents: connection refused"))
	m, _ = m.handleRefresh(refreshMsg{state: meshState{}})
	if got := m.renderSummaryLine(); !strings.Contains(got, "mcp 0.51.1 ●") {
		t.Fatalf("expected the segment to go back to connected, got %q", got)
	}
}

// An attached TUI has no macula-mcp of its own, so it must not claim one.
func TestSummaryLine_NoMCPSegmentWithoutAMaculaMCPOfItsOwn(t *testing.T) {
	m := New(nil, Options{UserInputCh: make(chan string, 8), StatusBarPosition: "bottom", Attached: true})
	m.width, m.height = 80, 24
	m.resizeComponents()
	if got := m.renderSummaryLine(); strings.Contains(got, "mcp") {
		t.Fatalf("expected no macula-mcp segment in an attached TUI, got %q", got)
	}
}

// The segment is one more thing on a line that already carries counts, the
// model, the identity and the listening tail: at a narrow terminal it still
// has to be ONE row, or the reserved-height math in resizeComponents drifts
// from what is on screen.
func TestSummaryLine_StillFitsOneLineAtEightyColumns(t *testing.T) {
	m := mcpTestModel(t, "0.51.1")
	m.state = meshState{
		joined:  []joinedRoom{{}, {}},
		pending: []pendingRing{{RingID: "r1", Peer: "peer1"}},
		agents: []agentPresence{
			{NodeID: strings.Repeat("ab", 32), Petname: "wise_scarlet_dolphin_3829"},
			{NodeID: strings.Repeat("cd", 32), Petname: "tranquil_azure_badger_2009"},
			{NodeID: strings.Repeat("ef", 32), Petname: "cheerful_amber_gecko_9441"},
		},
	}
	m.lastListeningAt = m.lastListeningAt.Add(0) // zero: no listening tail, the common start state
	got := m.renderSummaryLine()
	if w := lipgloss.Width(got); w > 80 {
		t.Fatalf("expected the summary line to fit 80 columns, got width %d: %q", w, got)
	}
	// The cut takes the tail, never the segment: what was added here is the
	// part a person needs at a glance.
	if !strings.Contains(got, "mcp 0.51.1 ●") {
		t.Fatalf("expected the mcp segment to survive the cut at 80 columns, got %q", got)
	}
	if rows := len(strings.Split(m.renderStatusStrip(), "\n")); rows != len(m.statusLines()) {
		t.Fatalf("expected %d rows, got %d", len(m.statusLines()), rows)
	}
}

// One line in the chat at start says the same thing the segment says at a
// glance: which macula-mcp this session is on.
func TestNew_AnnouncesTheConnectedMaculaMCP(t *testing.T) {
	m := mcpTestModel(t, "")
	var found string
	for _, e := range m.chatEntries {
		if e.kind == chatSystem && strings.Contains(e.text.String(), "macula-mcp") {
			found = e.text.String()
		}
	}
	if want := "connected to macula-mcp 0.51.1 over stdio"; found != want {
		t.Fatalf("expected the start line %q, got %q", want, found)
	}
}

func TestNew_NoStartLineWithoutAMaculaMCPOfItsOwn(t *testing.T) {
	m := New(nil, Options{UserInputCh: make(chan string, 8), StatusBarPosition: "bottom", Attached: true})
	for _, e := range m.chatEntries {
		if strings.Contains(e.text.String(), "macula-mcp") {
			t.Fatalf("expected no macula-mcp start line in an attached TUI, got %q", e.text)
		}
	}
}

// Jupiter's finding 1 (#61 review): the version was read once into
// Options at start, so a transparent respawn -- which an unpinned
// macula-mcp takes on every spawn, floating to npm's latest -- left the
// segment green and naming a release nobody was running any more. The
// refresh re-reads it, and a change is announced in the chat.
func TestRefresh_FollowsARespawnToAnotherRelease(t *testing.T) {
	m := mcpTestModel(t, "")
	before := len(m.chatEntries)

	m, _ = m.handleRefresh(refreshMsg{state: meshState{}, serverVersion: "0.52.0"})
	got := m.renderSummaryLine()
	if !strings.Contains(got, "mcp 0.52.0 ●") {
		t.Fatalf("expected the segment to follow the respawned release, got %q", got)
	}
	if strings.Contains(got, "0.51.1") {
		t.Fatalf("expected no trace of the release that is no longer running, got %q", got)
	}
	if len(m.chatEntries) != before+1 {
		t.Fatalf("expected one chat line saying the release changed, got %d new entries", len(m.chatEntries)-before)
	}
	if last := m.chatEntries[len(m.chatEntries)-1]; !strings.Contains(last.text.String(), "macula-mcp is now 0.52.0 (was 0.51.1)") {
		t.Fatalf("expected the change to name both releases, got %q", last.text)
	}
}

// The same release, every two seconds, must not fill the chat.
func TestRefresh_DoesNotRepeatAnUnchangedRelease(t *testing.T) {
	m := mcpTestModel(t, "")
	before := len(m.chatEntries)
	for i := 0; i < 3; i++ {
		m, _ = m.handleRefresh(refreshMsg{state: meshState{}, serverVersion: "0.51.1"})
	}
	if len(m.chatEntries) != before {
		t.Fatalf("expected no chat lines for an unchanged release, got %d", len(m.chatEntries)-before)
	}
}

// The refresh itself re-reads the release every tick: what a respawn did
// during a refresh is already visible in the message it returns.
func TestRefreshCmd_CarriesTheReleaseTheSessionIsRunningNow(t *testing.T) {
	client := &versionedToolCaller{
		fakeToolCaller: &fakeToolCaller{responses: map[string]string{
			"mesh_rooms":       fixtureMeshRooms,
			"mesh_read_inbox":  fixtureMeshReadInbox,
			"mesh_agents":      fixtureMeshAgents,
			"mesh_list_realms": fixtureMeshListRealms,
		}},
		version: "0.52.0",
	}
	m := New(client, Options{UserInputCh: make(chan string, 8), StatusBarPosition: "bottom"})
	m.width, m.height = 80, 24
	m.resizeComponents()

	msg, ok := m.refreshCmd()().(refreshMsg)
	if !ok {
		t.Fatalf("expected a refreshMsg")
	}
	if msg.serverVersion != "0.52.0" {
		t.Fatalf("expected the refresh to carry the running release, got %q", msg.serverVersion)
	}
}

// `(pinned)` describes the release that is running, not what config asked
// for: a pin npm did not honour must not be labelled as if it were.
func TestSummaryLine_PinnedOnlyWhenTheRunningReleaseIsThePinnedOne(t *testing.T) {
	m := mcpTestModel(t, "0.50.0")
	if got := m.renderSummaryLine(); strings.Contains(got, "(pinned)") {
		t.Fatalf("expected no (pinned) when the running release is not the pinned one, got %q", got)
	}
}

func TestSummaryLine_APinWrittenWithAVPrefixStillMatches(t *testing.T) {
	m := mcpTestModel(t, "v0.51.1")
	if got := m.renderSummaryLine(); !strings.Contains(got, "(pinned)") {
		t.Fatalf("expected (pinned) for a v-prefixed pin of the running release, got %q", got)
	}
}

// The segment leads the line so a narrow terminal's cut never takes it: at
// 60 columns with a long petname, "disconnected" is what has to survive.
func TestSummaryLine_SegmentSurvivesANarrowCut(t *testing.T) {
	m := mcpTestModel(t, "")
	m.width = 60
	m.state = meshState{agents: []agentPresence{
		{NodeID: strings.Repeat("ab", 32), Petname: "wise_scarlet_dolphin_3829"},
	}}
	m, _ = m.handleRefresh(refreshMsg{err: errors.New("mesh_agents: connection refused")})

	got := m.renderSummaryLine()
	if !strings.Contains(got, "mcp ✗ disconnected") {
		t.Fatalf("expected the disconnected segment to survive a 60-column cut, got %q", got)
	}
	if w := lipgloss.Width(got); w > 60 {
		t.Fatalf("expected the line to fit 60 columns, got width %d: %q", w, got)
	}
}

// versionedToolCaller is fakeToolCaller plus the release a real client
// reports in its handshake.
type versionedToolCaller struct {
	*fakeToolCaller
	version string
}

func (v *versionedToolCaller) ServerVersion() string { return v.version }
