package tui

import (
	"context"
	"errors"
	"testing"
)

var errBoom = errors.New("boom")

// The JSON fixtures below are copied verbatim from a real macula-mcp
// session's mesh_rooms / mesh_read_inbox / mesh_agents responses
// (captured 2026-09-06 against a live macula mesh), not hand-written --
// this is what actually confirms the parsing structs in mesh.go match
// macula-mcp's real wire shape, not just the tool descriptions.

const fixtureMeshRooms = `{
  "joined": [
    {
      "room_topic": "agents.room.58022d60606c8cdb7d726ea42cf5a675",
      "opened_by": "07a102208100325737efef7017a066c19c1c252b1ebcf919eb22f0f59cfb0e0d",
      "opened_here": 0,
      "public": 0,
      "joined_at": "2026-09-06T05:02:18.818Z",
      "participants_seen": [
        "07a102208100325737efef7017a066c19c1c252b1ebcf919eb22f0f59cfb0e0d",
        "a8384a55fde899ba2c44e91e572669ad229241b1eae407a784f88953ec14c01b"
      ],
      "messages_received": 38,
      "watched": 1
    }
  ],
  "seen_on_central": [
    {
      "room_topic": "agents.room.1db37a6014bd6de04c404c605c72637e",
      "opened_by": "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
      "purpose": "public room",
      "participants": ["dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"],
      "observed_at": "2026-09-05T20:23:24.560Z"
    }
  ],
  "rings_awaiting_answer": []
}`

const fixtureMeshReadInbox = `{
  "rings": {
    "pending": [
      {
        "ring_id": "e8d8ee1365237dcf5b8e4ab089b33a90",
        "direction": "in",
        "peer": "07a102208100325737efef7017a066c19c1c252b1ebcf919eb22f0f59cfb0e0d",
        "purpose": "Form a 3-agent team",
        "room_topic": "agents.room.58022d60606c8cdb7d726ea42cf5a675",
        "sent_at": 1788652444942,
        "recorded_at": "2026-09-05T23:54:05.264Z"
      }
    ],
    "recent": []
  },
  "rooms": [
    {
      "room_topic": "agents.room.58022d60606c8cdb7d726ea42cf5a675",
      "opened_by": "07a102208100325737efef7017a066c19c1c252b1ebcf919eb22f0f59cfb0e0d",
      "participants_seen": [],
      "total_received": 1,
      "returned": 1,
      "unparsed": 0,
      "messages": [
        {
          "message_id": "d7916d7c820b2eb88542ec09cb0089e5",
          "room_topic": "agents.room.58022d60606c8cdb7d726ea42cf5a675",
          "sent_at": 1788652882209,
          "from": "07a102208100325737efef7017a066c19c1c252b1ebcf919eb22f0f59cfb0e0d",
          "kind": "remark_made",
          "text": "Team room is live.",
          "observed_at": "2026-09-06T00:01:22.262Z",
          "thread_root": "d7916d7c820b2eb88542ec09cb0089e5",
          "depth": 0,
          "attested": 0
        }
      ]
    }
  ],
  "central_broadcasts": []
}`

const fixtureMeshAgents = `{
  "total": 2,
  "page": 1,
  "page_size": 20,
  "agents": [
    {
      "node_id": "07a102208100325737efef7017a066c19c1c252b1ebcf919eb22f0f59cfb0e0d",
      "operator_name": "goose",
      "message": "Hello from a goose agent on desk-newark",
      "model": "goose",
      "connected_via": "goose-cli 1.48.0",
      "first_seen": "2026-09-05T23:51:49.320Z",
      "last_seen": "2026-09-06T07:27:49.204Z",
      "seconds_since_seen": 0,
      "is_self": false
    },
    {
      "node_id": "a8384a55fde899ba2c44e91e572669ad229241b1eae407a784f88953ec14c01b",
      "connected_via": "claude-code 2.1.261",
      "first_seen": "2026-09-05T23:48:18.301Z",
      "last_seen": "2026-09-06T07:27:18.699Z",
      "seconds_since_seen": 31,
      "is_self": true
    }
  ]
}`

type fakeToolCaller struct {
	responses map[string]string
	errs      map[string]error
	calls     []string
}

func (f *fakeToolCaller) CallTool(ctx context.Context, name string, args map[string]any) (string, error) {
	f.calls = append(f.calls, name)
	if err, ok := f.errs[name]; ok {
		return "", err
	}
	return f.responses[name], nil
}

func TestFetchMeshState_ParsesRealCapturedShapes(t *testing.T) {
	fake := &fakeToolCaller{responses: map[string]string{
		"mesh_rooms":      fixtureMeshRooms,
		"mesh_read_inbox": fixtureMeshReadInbox,
		"mesh_agents":     fixtureMeshAgents,
	}}

	state, err := fetchMeshState(context.Background(), fake)
	if err != nil {
		t.Fatalf("fetchMeshState returned error: %v", err)
	}

	if len(state.joined) != 1 {
		t.Fatalf("expected 1 joined room, got %d", len(state.joined))
	}
	if state.joined[0].RoomTopic != "agents.room.58022d60606c8cdb7d726ea42cf5a675" {
		t.Fatalf("unexpected room topic: %q", state.joined[0].RoomTopic)
	}
	if state.joined[0].MessagesReceived != 38 {
		t.Fatalf("expected messages_received 38, got %d", state.joined[0].MessagesReceived)
	}

	if len(state.pending) != 1 || state.pending[0].RingID != "e8d8ee1365237dcf5b8e4ab089b33a90" {
		t.Fatalf("unexpected pending rings: %+v", state.pending)
	}
	if state.pending[0].Direction != "in" {
		t.Fatalf("expected direction 'in', got %q", state.pending[0].Direction)
	}

	msgs := state.recent["agents.room.58022d60606c8cdb7d726ea42cf5a675"]
	if len(msgs) != 1 || msgs[0].Text != "Team room is live." {
		t.Fatalf("unexpected recent messages: %+v", msgs)
	}

	if len(state.agents) != 2 {
		t.Fatalf("expected 2 agents, got %d", len(state.agents))
	}
	if state.agents[0].OperatorName != "goose" {
		t.Fatalf("expected first agent operator_name 'goose', got %q", state.agents[0].OperatorName)
	}
	if !state.agents[1].IsSelf {
		t.Fatalf("expected second agent to be is_self")
	}

	wantCalls := []string{"mesh_rooms", "mesh_read_inbox", "mesh_agents"}
	if len(fake.calls) != len(wantCalls) {
		t.Fatalf("expected calls %v, got %v", wantCalls, fake.calls)
	}
	for i, c := range wantCalls {
		if fake.calls[i] != c {
			t.Fatalf("call %d: expected %q, got %q", i, c, fake.calls[i])
		}
	}
}

// Fixtures below add the petname/purpose fields macula-mcp 0.24.0
// introduced (verified against its own source, not guessed -- field names
// confirmed in src/mesh_rooms.ts/mesh_read_inbox.ts/mesh_agents.ts:
// opened_by_petname, peer_petname, from_petname, petname, and joined
// rooms' pre-existing optional purpose). A separate fixture from the
// pre-0.24.0 one above on purpose: that one stays as the real backward-
// compatibility check (older data with no petname/purpose fields at all
// must still parse and render via the shortID/shortTopic fallback).

const fixtureMeshRoomsWithPetnames = `{
  "joined": [
    {
      "room_topic": "agents.room.58022d60606c8cdb7d726ea42cf5a675",
      "opened_by": "07a102208100325737efef7017a066c19c1c252b1ebcf919eb22f0f59cfb0e0d",
      "opened_by_petname": "gentle_crimson_otter",
      "purpose": "Form a 3-agent team",
      "participants_seen": ["07a102208100325737efef7017a066c19c1c252b1ebcf919eb22f0f59cfb0e0d"],
      "messages_received": 5,
      "watched": 1
    }
  ],
  "seen_on_central": [],
  "rings_awaiting_answer": []
}`

const fixtureMeshReadInboxWithPetnames = `{
  "rings": {
    "pending": [
      {
        "ring_id": "a55654d5b00351db2a781e7a2d46bdbc",
        "direction": "in",
        "peer": "07a102208100325737efef7017a066c19c1c252b1ebcf919eb22f0f59cfb0e0d",
        "peer_petname": "gentle_crimson_otter",
        "purpose": "Let's talk",
        "room_topic": "agents.room.33d8ff6513b4ba49fd07d558ee19bd0f",
        "sent_at": 1788652444942
      }
    ],
    "recent": []
  },
  "rooms": [
    {
      "room_topic": "agents.room.58022d60606c8cdb7d726ea42cf5a675",
      "opened_by": "07a102208100325737efef7017a066c19c1c252b1ebcf919eb22f0f59cfb0e0d",
      "participants_seen": [],
      "total_received": 1,
      "returned": 1,
      "unparsed": 0,
      "messages": [
        {
          "message_id": "d7916d7c820b2eb88542ec09cb0089e5",
          "room_topic": "agents.room.58022d60606c8cdb7d726ea42cf5a675",
          "sent_at": 1788652882209,
          "from": "07a102208100325737efef7017a066c19c1c252b1ebcf919eb22f0f59cfb0e0d",
          "from_petname": "gentle_crimson_otter",
          "kind": "remark_made",
          "text": "hello"
        }
      ]
    }
  ],
  "central_broadcasts": []
}`

const fixtureMeshAgentsWithPetnames = `{
  "total": 1,
  "page": 1,
  "page_size": 20,
  "agents": [
    {
      "node_id": "07a102208100325737efef7017a066c19c1c252b1ebcf919eb22f0f59cfb0e0d",
      "petname": "gentle_crimson_otter",
      "connected_via": "goose-cli 1.48.0",
      "seconds_since_seen": 0,
      "is_self": false
    }
  ]
}`

func TestFetchMeshState_ParsesPetnameAndPurposeFields(t *testing.T) {
	fake := &fakeToolCaller{responses: map[string]string{
		"mesh_rooms":      fixtureMeshRoomsWithPetnames,
		"mesh_read_inbox": fixtureMeshReadInboxWithPetnames,
		"mesh_agents":     fixtureMeshAgentsWithPetnames,
	}}

	state, err := fetchMeshState(context.Background(), fake)
	if err != nil {
		t.Fatalf("fetchMeshState returned error: %v", err)
	}

	if state.joined[0].OpenedByPetname != "gentle_crimson_otter" {
		t.Fatalf("expected opened_by_petname to parse, got %+v", state.joined[0])
	}
	if state.joined[0].Purpose != "Form a 3-agent team" {
		t.Fatalf("expected joined room purpose to parse, got %+v", state.joined[0])
	}
	if state.pending[0].PeerPetname != "gentle_crimson_otter" {
		t.Fatalf("expected peer_petname to parse, got %+v", state.pending[0])
	}
	msgs := state.recent["agents.room.58022d60606c8cdb7d726ea42cf5a675"]
	if len(msgs) != 1 || msgs[0].FromPetname != "gentle_crimson_otter" {
		t.Fatalf("expected from_petname to parse, got %+v", msgs)
	}
	if state.agents[0].Petname != "gentle_crimson_otter" {
		t.Fatalf("expected agent petname to parse, got %+v", state.agents[0])
	}
}

func TestFetchMeshState_PropagatesToolError(t *testing.T) {
	fake := &fakeToolCaller{
		responses: map[string]string{},
		errs:      map[string]error{"mesh_rooms": errBoom},
	}
	if _, err := fetchMeshState(context.Background(), fake); err == nil {
		t.Fatalf("expected an error when mesh_rooms fails")
	}
}
