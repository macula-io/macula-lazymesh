package agent

import (
	"context"

	"github.com/macula-io/macula-lazymesh/internal/mcpclient"
)

// TerseDescriptionSource wraps another ToolSource and shortens specific
// tools' descriptions for a small/cheap model that doesn't need macula-
// mcp's own documentation -- written for a smarter, general-purpose
// coding-agent audience, with cross-references and edge-case rationale
// this narrow, repetitive mesh-chat use case doesn't need to operate
// correctly.
//
// Found investigating R2 (2026-09-07, the runaway-context incident's
// follow-up): measured against a real macula-mcp spawn, the fixed
// prefix's biggest cost by far was tool schemas, not the system prompt
// (335 tokens on its own -- never the problem). The 8 (now 7, mesh_hello
// dropped separately) default-allowlisted tools alone ran roughly 2,700
// tokens.
//
// Two tiers, by risk: terseDescriptions replaces ONLY a tool's top-level
// Description, leaving InputSchema (types, required-ness, enums, field
// descriptions) completely untouched -- a macula-mcp version bump can't
// silently break a call by having this override miss a newly-required
// field, for tools whose schema is already small enough not to be worth
// the higher-risk tier. terseSchemaOverrides is a full hand-authored
// Tool replacement (description AND schema), used where the schema
// itself was measured as real remaining weight after the safe tier
// alone wasn't enough to hit R2's 1.5K-token target -- justified per
// tool by (a) the live schema being captured and verified here in the
// same change, not guessed, and (b) every trim being conservative in
// the direction it goes: dropping a field (host, unconditionally unused
// -- lazymesh has no design that lets the model choose a station) or an
// enum value (mesh_say's kind, trimmed to what this agent's own system
// prompt actually references) can only make the model ask for LESS of a
// tool's real surface, never produce a call the live tool would reject
// for a reason this override doesn't already anticipate.
//
// Both maps are keyed by tool name and checked against macula-mcp's
// real, live descriptions each time internal/updatecheck flags a new
// pinned version -- see that package's own doc comment for why lazymesh
// pins an exact version rather than floating: the same discipline that
// requires reading a diff before bumping the pin extends to re-checking
// these two hand-authored texts still match reality.
type TerseDescriptionSource struct {
	inner ToolSource
}

// NewTerseDescriptionSource wraps inner.
func NewTerseDescriptionSource(inner ToolSource) *TerseDescriptionSource {
	return &TerseDescriptionSource{inner: inner}
}

// ListTools passes inner's tools through unchanged except for the
// specific overrides above, plus one generic step applied to every tool
// (see dropHostParam's own doc comment).
func (t *TerseDescriptionSource) ListTools(ctx context.Context) ([]mcpclient.Tool, error) {
	tools, err := t.inner.ListTools(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]mcpclient.Tool, len(tools))
	for i, tool := range tools {
		if replacement, ok := terseSchemaOverrides[tool.Name]; ok {
			out[i] = replacement
			continue
		}
		if desc, ok := terseDescriptions[tool.Name]; ok {
			tool.Description = desc
		}
		tool.InputSchema = dropHostParam(tool.InputSchema)
		out[i] = tool
	}
	return out, nil
}

// dropHostParam removes the "host" property from a tool's InputSchema,
// if present -- measured live, 2026-09-07: the identical ~90-character
// "Station to connect through..." field description is repeated
// verbatim across most of macula-mcp's own tools, real duplicate weight
// on every request. Never in any tool's own "required" list (confirmed
// against every macula-mcp tool this codebase allowlists), and lazymesh
// has no design that lets the model choose a different station -- the
// configured default is always correct, so the model never legitimately
// needs this parameter offered at all. Silently a no-op if "host" isn't
// present or the schema isn't the map[string]any shape every macula-mcp
// tool actually uses.
func dropHostParam(schema any) any {
	m, ok := schema.(map[string]any)
	if !ok {
		return schema
	}
	props, ok := m["properties"].(map[string]any)
	if !ok {
		return schema
	}
	if _, has := props["host"]; has {
		delete(props, "host")
	}
	return schema
}

// CallToolRaw delegates straight through -- this wrapper only ever
// changes what ListTools advertises, never call semantics.
func (t *TerseDescriptionSource) CallToolRaw(ctx context.Context, name string, argumentsJSON string) (string, error) {
	return t.inner.CallToolRaw(ctx, name, argumentsJSON)
}

// terseDescriptions replaces ONLY each tool's top-level Description --
// see this file's own doc comment for why InputSchema is deliberately
// left untouched here. mesh_rooms's own live schema is already empty
// ({}), so there's nothing a schema override could improve -- description
// trim only, safe tier. Real descriptions as of macula-mcp 0.25.2
// (verified live, 2026-09-07) are quoted in full in this package's own
// terse_test.go so a future re-check has the exact prior text to diff
// against, not just this file's own paraphrase of what changed.
var terseDescriptions = map[string]string{
	"mesh_rooms": "List the rooms you're in, plus public rooms seen on central you haven't " +
		"joined. Instant, local, never blocks.",
}

// terseSchemaOverrides replaces a tool's ENTIRE Tool value (Description
// and InputSchema both) -- see this file's own doc comment for the
// higher-risk tier this is and why each entry here is justified over
// the safer description-only tier.
var terseSchemaOverrides = map[string]mcpclient.Tool{
	"mesh_join_room": {
		Name: "mesh_join_room",
		Description: "Join a room by its topic (from mesh_rooms, or learned some other way). " +
			"Starts watching it in the background. Idempotent.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"room_topic": map[string]any{"type": "string", "description": "The agents.room.<32 hex> topic."},
			},
			"required": []string{"room_topic"},
		},
	},
	"mesh_leave_room": {
		Name: "mesh_leave_room",
		Description: "Leave a room and stop watching it. Pass close: 1 if you opened it and want " +
			"to signal it's done (not enforced on anyone else).",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"room_topic": map[string]any{"type": "string", "description": "A room you are in."},
				"close":      map[string]any{"type": "integer", "minimum": 0, "maximum": 1, "description": "1 to close instead of just leaving."},
			},
			"required": []string{"room_topic"},
		},
	},
	"mesh_answer_ring": {
		Name: "mesh_answer_ring",
		Description: "Answer a ring from mesh_read_inbox's rings.pending. answer 1 accepts " +
			"(joins the room first), 2 declines (reason optional, shown to the caller).",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"ring_id": map[string]any{"type": "string", "minLength": 32, "maxLength": 32, "pattern": "^[0-9a-f]+$", "description": "From rings.pending."},
				"answer":  map[string]any{"type": "integer", "minimum": 1, "maximum": 2, "description": "1 accept, 2 decline."},
				"reason":  map[string]any{"type": "string", "maxLength": 280, "description": "Shown to the caller on a decline."},
			},
			"required": []string{"ring_id", "answer"},
		},
	},
	"mesh_agents": {
		Name: "mesh_agents",
		Description: "List agents seen via their hello heartbeats. Local roster read, not a live " +
			"mesh query. stale: true means probably gone, before the 15-minute hard prune.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"page":      map[string]any{"type": "integer", "default": 1, "description": "1-based page number."},
				"page_size": map[string]any{"type": "integer", "default": 20, "maximum": 100},
			},
		},
	},
	"mesh_read_inbox": {
		Name: "mesh_read_inbox",
		Description: "Read pending rings, your rooms' recent messages (threaded), and recent " +
			"central help_requested/help_offered. Instant, local, never blocks.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"room_topic": map[string]any{"type": "string", "description": "One room only. Omit for every room you're in."},
				"limit":      map[string]any{"type": "integer", "default": 50, "maximum": 500, "description": "Messages per room (default 50). Does not affect rings."},
			},
		},
	},
	// mesh_ring (2026-09-08, added to DefaultToolAllowlist the same day --
	// see allowlist.go's own doc comment): its real, live description is
	// the single largest of any allowlisted tool (1,742 bytes, description
	// + schema together, measured live against a real macula-mcp spawn --
	// bigger than mesh_say's 925), so this needed the schema-override tier
	// from day one, not the safe description-only one. wait_join_seconds
	// dropped entirely from the exposed schema, same reasoning as
	// mesh_say's wait_reply_seconds above: internal/agent/nowait.go's
	// NoBlockingWaitSource force-clamps it to maxModelWaitSeconds
	// regardless of what the model passes OR omits (mesh_ring's own
	// server-side default when omitted is 30s, well past the clamp --
	// see nowait.go's own doc comment on that asymmetry), so offering the
	// parameter is pure cost with no effective control attached. host
	// hand-omitted for the same reason as mesh_say's own override: this
	// is already a full hand-authored replacement, dropHostParam's own
	// generic step never runs for it.
	"mesh_ring": {
		Name: "mesh_ring",
		Description: "Ring another agent: an addressed, proven invite carrying a room to talk in. " +
			"Reply is one of: 1 accepted (room proven two-sided, verified against their own key), " +
			"2 declined (with their reason), 3 deferred (their model decides later, answer arrives " +
			"via mesh_answer_ring; the room stays open), or unreachable (not serving right now). " +
			"purpose is mandatory and short -- a deferred ring is judged from it. This is the ONLY " +
			"way to reach an agent that has not invited you; never write into a room they have not " +
			"joined.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"to":         map[string]any{"type": "string", "description": "Node id or petname, from mesh_agents."},
				"purpose":    map[string]any{"type": "string", "minLength": 1, "description": "Why you're ringing, one line."},
				"room_topic": map[string]any{"type": "string", "description": "A room you're already in to invite them into. Omit to open a fresh one."},
			},
			"required": []string{"to", "purpose"},
		},
	},
	"mesh_say": {
		Name: "mesh_say",
		Description: "Say something in a room, or broadcast on central (agents.lobby -- for " +
			"help_requested/help_offered only, not conversation). Publishes one envelope with your " +
			"node id. kind defaults to remark_made; question_asked expects an answer_given reply " +
			"with in_reply_to set to the question's message_id. Joins the room first if you're not " +
			"in it yet.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"room_topic": map[string]any{
					"type":        "string",
					"description": "A room you opened or joined, or \"agents.lobby\" for a broadcast.",
				},
				"text": map[string]any{
					"type":        "string",
					"description": "The message.",
				},
				"kind": map[string]any{
					"type":        "string",
					"enum":        []string{"remark_made", "question_asked", "answer_given", "help_requested", "help_offered"},
					"description": "Default remark_made.",
				},
				"in_reply_to": map[string]any{
					"type":        "string",
					"minLength":   32,
					"maxLength":   32,
					"pattern":     "^[0-9a-f]+$",
					"description": "message_id this replies to. Required for answer_given.",
				},
			},
			"required": []string{"room_topic", "text"},
		},
	},
}

// refs and wait_reply_seconds are deliberately dropped from mesh_say's
// schema above, not just described tersely (2026-09-07, R2's live
// verification pass): refs is an artifact-attachment escape hatch this
// narrow conversational use case has no design for actually reaching
// (nothing in buildSystemPrompt ever tells the model to mesh_put
// something first); wait_reply_seconds is actively wrong to expose now
// that internal/roomwaiter and internal/ringwaiter own waiting -- the
// system prompt already tells the model not to use it, so offering the
// parameter at all is pure cost with an instruction to ignore it
// attached. host is hand-omitted here too, even though ListTools' own
// generic dropHostParam step would also catch it -- this schema is
// already a full hand-authored replacement, so there's no "what a
// maintainer would naturally write" default left to preserve the way
// there is for the six description-only tools dropHostParam exists for.
