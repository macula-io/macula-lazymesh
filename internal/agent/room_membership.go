package agent

import "encoding/json"

// RoomChangeOf reads the membership change a mesh tool's result reports:
// EventRoomJoined for mesh_join_room, mesh_ring (it opens and joins the
// room before the call goes out) and an accepted mesh_answer_ring,
// EventRoomLeft for mesh_leave_room. ok is false for any other tool, an
// error result, or a declined ring -- ring_service.ts names the room in a
// decline too, but joins only on accept, and watching that room would wait
// on one this node was never added to.
//
// It is the one place these result shapes are read, used by the event
// bridge for the model's tool calls and by the TUI for a ring the operator
// answers (#97).
func RoomChangeOf(toolName, resultJSON string) (Event, bool) {
	var parsed struct {
		Answer    int    `json:"answer"`
		RoomTopic string `json:"room_topic"`
	}
	if json.Unmarshal([]byte(resultJSON), &parsed) != nil || parsed.RoomTopic == "" {
		return Event{}, false
	}
	switch toolName {
	case "mesh_join_room", "mesh_ring":
		return Event{Kind: EventRoomJoined, Room: parsed.RoomTopic}, true
	case "mesh_answer_ring":
		if parsed.Answer != 1 {
			return Event{}, false
		}
		return Event{Kind: EventRoomJoined, Room: parsed.RoomTopic}, true
	case "mesh_leave_room":
		return Event{Kind: EventRoomLeft, Room: parsed.RoomTopic}, true
	}
	return Event{}, false
}

// RoomTopicOf extracts a result's top-level "room_topic", or "" when the
// result is unparseable or names none (an error result from a failed call).
func RoomTopicOf(resultJSON string) string {
	var parsed struct {
		RoomTopic string `json:"room_topic"`
	}
	if json.Unmarshal([]byte(resultJSON), &parsed) != nil {
		return ""
	}
	return parsed.RoomTopic
}
