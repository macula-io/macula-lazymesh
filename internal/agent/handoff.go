package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/macula-io/macula-lazymesh/internal/mcpclient"
)

// replyKinds are the envelope kinds whose contract REQUIRES in_reply_to:
// the MCP wire rule every reply kind carries the message_id it answers,
// so rooms stay threaded and claim verification can match a
// claim_confirmed to the result_reported it weighs in on. lane_claimed
// is deliberately not here: a self-initiated claim on work nobody handed
// you is legitimate and has nothing to answer.
var replyKinds = map[string]bool{
	"answer_given":    true,
	"result_reported": true,
	"lane_released":   true,
	"claim_confirmed": true,
	"claim_disputed":  true,
}

// HandoffSource enforces the envelope reply discipline on mesh_say (G11):
// a reply-kind envelope without a well-formed in_reply_to is refused
// BEFORE it reaches the mesh — the model is told to supply the message_id
// it read from mesh_read_inbox, instead of the mesh receiving an
// unthreaded reply it would have to misfile. It validates ONLY this one
// rule; everything else about a mesh_say call stays macula-mcp's own
// validation, which this wrapper must not duplicate or drift from.
//
// Why validation instead of true injection: the harness cannot know which
// message the model intends to answer — the model read the message_ids
// itself, so the honest mechanism is teaching (the system prompt's
// grammar paragraph) plus this refusal at the boundary, not guessing a
// target and silently attaching the wrong id.
type HandoffSource struct {
	inner ToolSource
}

// NewHandoffSource wraps inner.
func NewHandoffSource(inner ToolSource) *HandoffSource {
	return &HandoffSource{inner: inner}
}

func (h *HandoffSource) ListTools(ctx context.Context) ([]mcpclient.Tool, error) {
	return h.inner.ListTools(ctx)
}

func (h *HandoffSource) CallToolRaw(ctx context.Context, name string, argumentsJSON string) (string, error) {
	if name != "mesh_say" {
		return h.inner.CallToolRaw(ctx, name, argumentsJSON)
	}
	var parsed struct {
		Kind      string `json:"kind"`
		InReplyTo string `json:"in_reply_to"`
	}
	// Tolerant unmarshal on purpose: an argument shape macula-mcp would
	// reject is macula-mcp's call to make — this wrapper only ever adds
	// the one refusal below.
	_ = json.Unmarshal([]byte(argumentsJSON), &parsed)
	if replyKinds[parsed.Kind] && !validMessageID(parsed.InReplyTo) {
		return "", fmt.Errorf("mesh_say: kind %q is a reply and REQUIRES in_reply_to set to the message_id it answers (32 hex chars, read from mesh_read_inbox); the envelope was not sent", parsed.Kind)
	}
	return h.inner.CallToolRaw(ctx, name, argumentsJSON)
}

// validMessageID checks the 32-lowercase-hex shape every message_id on
// this mesh has.
func validMessageID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, r := range id {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}
