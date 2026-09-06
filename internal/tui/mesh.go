package tui

import (
	"context"
	"encoding/json"
	"fmt"
)

// toolCaller is the subset of *mcpclient.Client fetchMeshState needs.
// *mcpclient.Client satisfies this structurally; the interface exists so
// tests can supply canned tool responses without a real macula-mcp
// subprocess.
type toolCaller interface {
	CallTool(ctx context.Context, name string, args map[string]any) (string, error)
}

// The structs below mirror macula-mcp's own mesh_rooms / mesh_read_inbox /
// mesh_agents JSON response shapes exactly (verified live against a
// running macula-mcp session, not guessed from the tool descriptions) --
// only the fields the TUI's three panels actually render are kept.

type joinedRoom struct {
	RoomTopic        string   `json:"room_topic"`
	OpenedBy         string   `json:"opened_by"`
	OpenedByPetname  string   `json:"opened_by_petname"`
	Purpose          string   `json:"purpose,omitempty"`
	ParticipantsSeen []string `json:"participants_seen"`
	MessagesReceived int      `json:"messages_received"`
	Watched          int      `json:"watched"`
}

type publicRoom struct {
	RoomTopic string `json:"room_topic"`
	OpenedBy  string `json:"opened_by"`
	Purpose   string `json:"purpose"`
}

type meshRoomsResult struct {
	Joined        []joinedRoom `json:"joined"`
	SeenOnCentral []publicRoom `json:"seen_on_central"`
}

type pendingRing struct {
	RingID      string `json:"ring_id"`
	Direction   string `json:"direction"`
	Peer        string `json:"peer"`
	PeerPetname string `json:"peer_petname"`
	Purpose     string `json:"purpose"`
	RoomTopic   string `json:"room_topic"`
	SentAt      int64  `json:"sent_at"`
}

type roomMessage struct {
	MessageID   string `json:"message_id"`
	RoomTopic   string `json:"room_topic"`
	From        string `json:"from"`
	FromPetname string `json:"from_petname"`
	Kind        string `json:"kind"`
	Text        string `json:"text"`
	SentAt      int64  `json:"sent_at"`
}

type inboxRoom struct {
	RoomTopic string        `json:"room_topic"`
	Messages  []roomMessage `json:"messages"`
}

type meshReadInboxResult struct {
	Rings struct {
		Pending []pendingRing `json:"pending"`
	} `json:"rings"`
	Rooms []inboxRoom `json:"rooms"`
}

type agentPresence struct {
	NodeID           string `json:"node_id"`
	Petname          string `json:"petname"`
	OperatorName     string `json:"operator_name"`
	Model            string `json:"model"`
	ConnectedVia     string `json:"connected_via"`
	SecondsSinceSeen int    `json:"seconds_since_seen"`
	IsSelf           bool   `json:"is_self"`
}

type meshAgentsResult struct {
	Total  int             `json:"total"`
	Agents []agentPresence `json:"agents"`
}

// meshState is one refreshed snapshot of everything the TUI's three panels
// render.
type meshState struct {
	joined  []joinedRoom
	pending []pendingRing
	recent  map[string][]roomMessage // room_topic -> recent messages
	agents  []agentPresence
}

// fetchMeshState calls macula-mcp's own read tools -- mesh_rooms,
// mesh_read_inbox, mesh_agents -- through the same MCP connection the
// agent loop uses. These are documented by macula-mcp itself as instant
// local reads that never block, so calling all three on every tick is
// cheap.
func fetchMeshState(ctx context.Context, client toolCaller) (meshState, error) {
	var state meshState

	roomsText, err := client.CallTool(ctx, "mesh_rooms", nil)
	if err != nil {
		return state, fmt.Errorf("mesh_rooms: %w", err)
	}
	var rooms meshRoomsResult
	if err := json.Unmarshal([]byte(roomsText), &rooms); err != nil {
		return state, fmt.Errorf("decode mesh_rooms: %w", err)
	}
	state.joined = rooms.Joined

	inboxText, err := client.CallTool(ctx, "mesh_read_inbox", nil)
	if err != nil {
		return state, fmt.Errorf("mesh_read_inbox: %w", err)
	}
	var inbox meshReadInboxResult
	if err := json.Unmarshal([]byte(inboxText), &inbox); err != nil {
		return state, fmt.Errorf("decode mesh_read_inbox: %w", err)
	}
	state.pending = inbox.Rings.Pending
	state.recent = make(map[string][]roomMessage, len(inbox.Rooms))
	for _, r := range inbox.Rooms {
		state.recent[r.RoomTopic] = r.Messages
	}

	agentsText, err := client.CallTool(ctx, "mesh_agents", map[string]any{"page_size": 50})
	if err != nil {
		return state, fmt.Errorf("mesh_agents: %w", err)
	}
	var agents meshAgentsResult
	if err := json.Unmarshal([]byte(agentsText), &agents); err != nil {
		return state, fmt.Errorf("decode mesh_agents: %w", err)
	}
	state.agents = agents.Agents

	return state, nil
}
