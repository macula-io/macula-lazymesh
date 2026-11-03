package tui

import (
	"strings"
	"testing"
)

// #95's other half. The chat's entries and the popups take a termText, so
// the compiler is the guard there; a panel row and a table cell still take
// a plain string, because bubbles/table does, so the guard for the panels
// is this sweep: every field a peer or the model fills in, with the
// hostile text of the #81 tests, rendered through every panel that shows
// it, and not one control byte on the way out.
func TestTermText_PanelsShowHostileText(t *testing.T) {
	m := newTestModel(t)
	m.width = 140
	m.state.joined = []joinedRoom{{
		RoomTopic:       "agents.room.deadbeef",
		OpenedBy:        "peerhex",
		OpenedByPetname: hostileText,
		Purpose:         hostileText,
	}}
	m.state.recent = map[string][]roomMessage{
		"agents.room.deadbeef": {{From: "peerhex", FromPetname: hostileText, Text: hostileText}},
	}
	m.state.pending = []pendingRing{{
		RingID:      "r1",
		Direction:   "in",
		Peer:        "peerhex",
		PeerPetname: hostileText,
		Purpose:     hostileText,
	}}
	m.state.agents = []agentPresence{{
		NodeID:       "peerhex",
		Petname:      hostileText,
		OperatorName: hostileText,
		SessionName:  hostileText,
		Model:        hostileText,
		ConnectedVia: hostileText,
	}}
	m.state.realms = []realmMembership{{
		Realm:       hostileText,
		Handle:      hostileText,
		Tier:        hostileText,
		JoinedAt:    hostileText,
		Account:     hostileText,
		OrgIdentity: hostileText,
	}}

	for name, render := range map[string]func() string{
		"rooms panel":         m.renderRooms,
		"pending rings panel": m.renderPendingRings,
		"presence panel":      m.renderPresence,
		"realms panel":        m.renderRealms,
	} {
		assertShowsControlsAsText(t, name, render())
	}
}

// assertShowsControlsAsText is assertNoTerminalControl without its
// whole-text requirement: a panel's columns truncate a long hostile
// string, so only the first cells survive -- but the ESC must still be
// SHOWN (as ␛), because a control that was deleted takes what it carried
// with it (#83 review).
func assertShowsControlsAsText(t *testing.T, what, got string) {
	t.Helper()
	for _, banned := range hostileBytes {
		if strings.Contains(got, banned) {
			t.Fatalf("%s: %q reached the terminal:\n%q", what, banned, got)
		}
	}
	if !strings.Contains(got, "␛") {
		t.Fatalf("%s: the ESC is not shown as ␛, so it was deleted rather than shown:\n%s", what, got)
	}
}

// A ring popup and an approval popup are the two places the operator is
// asked to decide something on text a peer or the model wrote, so their
// fields are swept the same way.
func TestTermText_PopupsShowHostileText(t *testing.T) {
	m := newTestModel(t)
	m.width = 140
	m.pendingRingPopup = &pendingRing{RingID: "r1", PeerPetname: hostileText, Purpose: hostileText}
	assertNoTerminalControl(t, "ring popup", m.renderRingPopup())

	approval, _ := approvalModel(t, 120, 40, `{"command":"`+strings.ReplaceAll(hostileText, `"`, `\"`)+`"}`)
	assertNoTerminalControl(t, "approval popup", approval.View())

	m.recordError(errString(hostileText), m.clock())
	assertNoTerminalControl(t, "error popup", m.openErrorPopup(0).renderErrorPopup())
}
