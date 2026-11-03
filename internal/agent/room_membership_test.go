package agent

import "testing"

func TestRoomChangeOf_MapsEachMembershipTool(t *testing.T) {
	cases := []struct {
		tool, result string
		want         Event
		ok           bool
	}{
		{"mesh_join_room", `{"room_topic":"agents.room.a","already_joined":1}`, Event{Kind: EventRoomJoined, Room: "agents.room.a"}, true},
		{"mesh_ring", `{"ring_id":"abc","to":"deadbeef","room_topic":"agents.room.b","answer":3,"answer_label":"deferred"}`, Event{Kind: EventRoomJoined, Room: "agents.room.b"}, true},
		{"mesh_answer_ring", `{"ring_id":"abc","answer":1,"peer":"deadbeef","room_topic":"agents.room.c","caller_notified":1}`, Event{Kind: EventRoomJoined, Room: "agents.room.c"}, true},
		{"mesh_leave_room", `{"room_topic":"agents.room.d"}`, Event{Kind: EventRoomLeft, Room: "agents.room.d"}, true},
		// ring_service.ts reports room_topic for a decline too, but joins
		// only on accept: a decline is no membership change.
		{"mesh_answer_ring", `{"ring_id":"abc","answer":2,"peer":"deadbeef","room_topic":"agents.room.c","caller_notified":1}`, Event{}, false},
		{"mesh_join_room", `{"error":"mesh_join_room failed"}`, Event{}, false},
		{"mesh_leave_room", "not json", Event{}, false},
		{"mesh_say", `{"room_topic":"agents.room.a"}`, Event{}, false},
	}
	for _, c := range cases {
		got, ok := RoomChangeOf(c.tool, c.result)
		if ok != c.ok || got.Kind != c.want.Kind || got.Room != c.want.Room {
			t.Errorf("RoomChangeOf(%s, %s) = (%+v, %v), want (%+v, %v)", c.tool, c.result, got, ok, c.want, c.ok)
		}
	}
}

func TestRoomTopicOf_MalformedOrAbsentIsEmpty(t *testing.T) {
	if got := RoomTopicOf(`{"room_topic":"agents.room.a"}`); got != "agents.room.a" {
		t.Fatalf("got %q, want agents.room.a", got)
	}
	if got := RoomTopicOf("not json"); got != "" {
		t.Fatalf("got %q for a malformed result, want empty", got)
	}
	if got := RoomTopicOf(`{"error":"unreachable"}`); got != "" {
		t.Fatalf("got %q for a result with no room_topic, want empty", got)
	}
}
