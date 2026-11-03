package rostergate

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/macula-io/macula-lazymesh/internal/agent"
	"github.com/macula-io/macula-lazymesh/internal/mcpclient"
)

// CodingMeshTools is every mesh tool the coding profile offers the
// model: speaking into a room the session already watches, and nothing
// that reads peer text, joins a room, answers or places a ring, or serves
// a procedure. Adding a name here widens what peers can reach; the
// coding-profile test in cmd/lazymesh fails on any tool it has not
// vetted.
var CodingMeshTools = []string{"mesh_say"}

// SayOnlyToWatchedRooms limits mesh_say to rooms the session watches,
// removes its reply wait, and reduces its result to the message id
// (threat model T4): the model cannot open a channel to a room repository
// text names, and a say cannot bring back an ungated reply as its tool
// result.
//
// The watched set is not only --room (#97): a ring the operator accepts,
// or the operator's auto-accept policy accepts, enters it too, and so
// becomes a room this gate lets mesh_say speak into. Each is an
// operator's own act; none is the model's.
type SayOnlyToWatchedRooms struct {
	inner   agent.ToolSource
	watches func(room string) bool
}

// NewSayOnlyToWatchedRooms wraps inner; watches answers whether the
// session watches a room.
func NewSayOnlyToWatchedRooms(inner agent.ToolSource, watches func(room string) bool) *SayOnlyToWatchedRooms {
	return &SayOnlyToWatchedRooms{inner: inner, watches: watches}
}

func (s *SayOnlyToWatchedRooms) ListTools(ctx context.Context) ([]mcpclient.Tool, error) {
	return s.inner.ListTools(ctx)
}

func (s *SayOnlyToWatchedRooms) CallToolRaw(ctx context.Context, name, argumentsJSON string) (string, error) {
	if name != "mesh_say" {
		return s.inner.CallToolRaw(ctx, name, argumentsJSON)
	}
	args := map[string]any{}
	if err := json.Unmarshal([]byte(argumentsJSON), &args); err != nil {
		return "", fmt.Errorf("mesh_say arguments are not a JSON object: %w", err)
	}
	room, _ := args["room_topic"].(string)
	if !s.watches(room) {
		return "", agent.Refused(fmt.Errorf("mesh_say is limited to the rooms this coding session watches; %q is not one of them", room))
	}
	delete(args, "wait_reply_seconds")
	out, err := json.Marshal(args)
	if err != nil {
		return "", err
	}
	result, err := s.inner.CallToolRaw(ctx, name, string(out))
	if err != nil {
		return "", err
	}
	return confirmation(result)
}

// confirmation reduces a say's result ({sent, reply, ...} in macula-mcp's
// rooms.say) to the sent envelope's message id: anything
// else in it (a reply, a dropped count, a field added later) could be a
// peer's text, and the model needs only the id to thread a reply.
func confirmation(result string) (string, error) {
	var parsed struct {
		Sent struct {
			MessageID string `json:"message_id"`
		} `json:"sent"`
	}
	id := ""
	if json.Unmarshal([]byte(result), &parsed) == nil {
		id = safe(parsed.Sent.MessageID, idAlphabet)
	}
	if id == "" {
		return "", fmt.Errorf("mesh_say did not confirm the message: its result carried no message_id")
	}
	return fmt.Sprintf(`{"said":1,"message_id":%q}`, id), nil
}
