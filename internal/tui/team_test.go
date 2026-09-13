package tui

import (
	"strings"
	"testing"
)

func msg(id, kind, inReplyTo, from, petname, text string, sentAt int64) roomMessage {
	return roomMessage{
		MessageID: id, Kind: kind, InReplyTo: inReplyTo,
		From: from, FromPetname: petname, Text: text, SentAt: sentAt,
	}
}

// TestOpenLanes_PairsClaimsWithReleases pins the lane derivation: a
// claimed lane stays open until a lane_released names that claim in its
// in_reply_to; an unrelated release does not close it.
func TestOpenLanes_PairsClaimsWithReleases(t *testing.T) {
	recent := map[string][]roomMessage{
		"agents.room.aaaaaaaa": {
			msg("c1", "lane_claimed", "", "n1", "alice", "taking the editor", 100),
			msg("r1", "lane_released", "c1", "n1", "alice", "", 200),
			msg("c2", "lane_claimed", "", "n2", "bob", "taking the termkeys", 300),
		},
		"agents.room.bbbbbbbb": {
			msg("r2", "lane_released", "c9", "n3", "carol", "", 400), // replies to a claim we never saw
		},
	}
	lanes := openLanes(recent)
	if len(lanes) != 1 {
		t.Fatalf("open lanes = %d, want 1", len(lanes))
	}
	if lanes[0].who != "n2" || lanes[0].room != "aaaaaaaa" {
		t.Fatalf("lane = %+v, want bob in aaaaaaaa", lanes[0])
	}
}

// TestOpenHandoffs_ResultsCloseThem pins the handoff derivation, same
// pairing rule.
func TestOpenHandoffs_ResultsCloseThem(t *testing.T) {
	recent := map[string][]roomMessage{
		"agents.room.aaaaaaaa": {
			msg("h1", "task_handed_over", "", "n1", "alice", "please review", 100),
			msg("d1", "result_reported", "h1", "n2", "bob", "reviewed", 200),
			msg("h2", "task_handed_over", "", "n1", "alice", "please test", 300),
		},
	}
	handoffs := openHandoffs(recent)
	if len(handoffs) != 1 || handoffs[0].text != "please test" {
		t.Fatalf("open handoffs = %+v, want the untested handoff", handoffs)
	}
}

// TestRecentResults_NewestFirstCapped pins ordering and the cap.
func TestRecentResults_NewestFirstCapped(t *testing.T) {
	recent := map[string][]roomMessage{
		"agents.room.aaaaaaaa": {
			msg("1", "result_reported", "", "n1", "alice", "oldest", 100),
			msg("2", "result_reported", "", "n2", "bob", "middle", 200),
			msg("3", "result_reported", "", "n3", "carol", "newest", 300),
		},
	}
	got := recentResults(recent, 2)
	if len(got) != 2 {
		t.Fatalf("results = %d, want 2 (capped)", len(got))
	}
	if got[0].text != "middle" || got[1].text != "newest" {
		t.Fatalf("order = %q, %q -- want newest last", got[0].text, got[1].text)
	}
}

// TestHelpBroadcasts_FiltersKinds pins the lobby filter: only
// help_requested/help_offered surface, newest last, capped.
func TestHelpBroadcasts_FiltersKinds(t *testing.T) {
	central := []roomMessage{
		msg("1", "remark_made", "", "n1", "alice", "not help", 100),
		msg("2", "help_requested", "", "n2", "bob", "need a review", 200),
		msg("3", "help_offered", "", "n3", "carol", "I can review", 300),
	}
	got := helpBroadcasts(central, 5)
	if len(got) != 2 {
		t.Fatalf("help broadcasts = %d, want 2", len(got))
	}
	if got[0].text != "need a review" || got[1].text != "I can review" {
		t.Fatalf("help order = %q, %q", got[0].text, got[1].text)
	}
}

// TestAgo_RendersCompact pins the age formatting.
func TestAgo_RendersCompact(t *testing.T) {
	cases := []struct {
		seconds int
		want    string
	}{
		{-1, "now"},
		{5, "5s ago"},
		{90, "1m ago"},
		{7200, "2h ago"},
	}
	for _, c := range cases {
		if got := ago(c.seconds); got != c.want {
			t.Fatalf("ago(%d) = %q, want %q", c.seconds, got, c.want)
		}
	}
}

// TestTeamTabIsFullScreen pins the full-screen contract: no chat content,
// no compose box on the teams tab -- the panel is the whole body.
func TestTeamTabIsFullScreen(t *testing.T) {
	m := newTestModel(t)
	m.width, m.height = 90, 24
	m.resizeComponents()
	for i := 0; i < 5; i++ {
		m.chatEntries = append(m.chatEntries, youChatEntry("chat line never shown here"))
	}
	m.syncViewport()
	m.tab = tabTeam

	view := m.View()
	if strings.Contains(view, "chat line never shown here") {
		t.Fatalf("chat content leaked onto the teams tab:\n%s", view)
	}
	if strings.Contains(view, "message the agent") {
		t.Fatalf("the compose box rendered on the teams tab:\n%s", view)
	}
	if !strings.Contains(view, "Team") {
		t.Fatalf("the team board is missing:\n%s", view)
	}
}
