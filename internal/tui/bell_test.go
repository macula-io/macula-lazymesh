package tui

import "testing"

func TestBellPattern_Sequences(t *testing.T) {
	cases := []struct {
		p    bellPattern
		want string
	}{
		{bellNone, ""},
		{bellSingle, "\a"},
		{bellDouble, "\a\a"},
		{bellTriple, "\a\a\a"},
	}
	for _, c := range cases {
		if got := c.p.sequence(); got != c.want {
			t.Fatalf("pattern %d: expected %q, got %q", c.p, c.want, got)
		}
	}
}

func TestDetectRingBell_NewRingTriggersDoubleBell(t *testing.T) {
	prev := meshState{}
	next := meshState{pending: []pendingRing{{RingID: "abc"}}}
	if got := detectRingBell(prev, next); got != bellDouble {
		t.Fatalf("expected bellDouble for a new pending ring, got %v", got)
	}
}

func TestDetectRingBell_SameRingIsNotNew(t *testing.T) {
	prev := meshState{pending: []pendingRing{{RingID: "abc"}}}
	next := meshState{pending: []pendingRing{{RingID: "abc"}}}
	if got := detectRingBell(prev, next); got != bellNone {
		t.Fatalf("expected bellNone for an already-seen ring, got %v", got)
	}
}

func TestDetectRingBell_NoRingsIsNone(t *testing.T) {
	if got := detectRingBell(meshState{}, meshState{}); got != bellNone {
		t.Fatalf("expected bellNone with no rings at all, got %v", got)
	}
}

func TestDetectRoomMessageBell_NewMessageFromOtherTriggersSingleBell(t *testing.T) {
	prev := meshState{recent: map[string][]roomMessage{
		"room1": {{From: "self", Text: "hi"}},
	}}
	next := meshState{recent: map[string][]roomMessage{
		"room1": {{From: "self", Text: "hi"}, {From: "peer", Text: "hello back"}},
	}}
	if got := detectRoomMessageBell(prev, next, "self"); got != bellSingle {
		t.Fatalf("expected bellSingle for a new message from someone else, got %v", got)
	}
}

func TestDetectRoomMessageBell_OwnOutgoingMessageIsSilent(t *testing.T) {
	prev := meshState{recent: map[string][]roomMessage{"room1": {}}}
	next := meshState{recent: map[string][]roomMessage{
		"room1": {{From: "self", Text: "my own message"}},
	}}
	if got := detectRoomMessageBell(prev, next, "self"); got != bellNone {
		t.Fatalf("expected silence for the agent's own outgoing message, got %v", got)
	}
}

func TestDetectRoomMessageBell_EmptySelfNodeIDIsConservativelySilent(t *testing.T) {
	prev := meshState{recent: map[string][]roomMessage{"room1": {}}}
	next := meshState{recent: map[string][]roomMessage{
		"room1": {{From: "someone", Text: "hi"}},
	}}
	if got := detectRoomMessageBell(prev, next, ""); got != bellNone {
		t.Fatalf("expected bellNone when self isn't identified yet, got %v", got)
	}
}

func TestDetectRoomMessageBell_NoNewMessagesIsNone(t *testing.T) {
	state := meshState{recent: map[string][]roomMessage{
		"room1": {{From: "peer", Text: "hi"}},
	}}
	if got := detectRoomMessageBell(state, state, "self"); got != bellNone {
		t.Fatalf("expected bellNone when nothing new arrived, got %v", got)
	}
}

func TestSelfNodeID_FindsIsSelfEntry(t *testing.T) {
	agents := []agentPresence{
		{NodeID: "a", IsSelf: false},
		{NodeID: "b", IsSelf: true},
	}
	if got := selfNodeID(agents); got != "b" {
		t.Fatalf("expected b, got %q", got)
	}
}

func TestSelfNodeID_EmptyWhenNoneMarkedSelf(t *testing.T) {
	agents := []agentPresence{{NodeID: "a", IsSelf: false}}
	if got := selfNodeID(agents); got != "" {
		t.Fatalf("expected empty string, got %q", got)
	}
}

func TestSelfPetname_PrefersPetnameOverShortID(t *testing.T) {
	agents := []agentPresence{
		{NodeID: "a", IsSelf: false, Petname: "other-agent"},
		{NodeID: "deadbeefcafe", IsSelf: true, Petname: "swift-otter"},
	}
	if got := selfPetname(agents); got != "swift-otter" {
		t.Fatalf("expected swift-otter, got %q", got)
	}
}

func TestSelfPetname_FallsBackToShortIDWhenPetnameEmpty(t *testing.T) {
	agents := []agentPresence{{NodeID: "deadbeefcafe", IsSelf: true}}
	if got := selfPetname(agents); got != "deadbeef" {
		t.Fatalf("expected shortened node_id fallback, got %q", got)
	}
}

func TestSelfPetname_EmptyWhenNoSelfEntryYet(t *testing.T) {
	if got := selfPetname(nil); got != "" {
		t.Fatalf("expected empty string before the first refresh populates agents, got %q", got)
	}
}

func TestRingBell_MutedNeverWrites(t *testing.T) {
	// ringBell's Cmd body is only observably safe to call directly in a
	// test if muted is true (unmuted would actually write \a to this
	// process's real stdout) -- exercise exactly the code path that
	// matters for correctness without doing that.
	cmd := ringBell(bellTriple, true)
	if cmd == nil {
		t.Fatalf("expected a non-nil Cmd even when muted")
	}
	if msg := cmd(); msg != nil {
		t.Fatalf("expected ringBell's Cmd to return a nil Msg, got %v", msg)
	}
}

func TestRingBell_NonePatternIsANoOpEvenUnmuted(t *testing.T) {
	cmd := ringBell(bellNone, false)
	if msg := cmd(); msg != nil {
		t.Fatalf("expected nil Msg for bellNone, got %v", msg)
	}
}
