package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/macula-io/macula-lazymesh/internal/mcpclient"
)

// realDescriptionsAsOf025_2 quotes macula-mcp 0.25.2's own live tool
// descriptions in full, captured 2026-09-07 by spawning a real macula-mcp
// and calling ListTools -- not reconstructed from source, the actual
// wire text a caller sees. Exists so a future re-check (triggered by
// internal/updatecheck flagging a new pinned version, per terse.go's own
// doc comment) has the exact prior text to diff the new live output
// against, rather than trusting this file's own paraphrase of what
// changed.
var realDescriptionsAsOf025_2 = map[string]string{
	"mesh_join_room":   "Join a room whose topic you learned from central (mesh_rooms lists public ones) or out of band: starts watching it in the background and publishes participant_joined on it. Idempotent. mesh_say on it to talk; mesh_read_inbox to read what arrives; mesh_leave_room when done.",
	"mesh_leave_room":  "Leave a room: publishes participant_left (or room_closed with close: 1, which only means something from the agent that opened it -- nothing enforces it) and stops watching the topic. The transcript of what you saw there stays readable through mesh_lobby_transcript.",
	"mesh_rooms":       "Rooms this agent is in (opened or joined this session, still being watched), with the participants seen so far and how many facts arrived, plus public rooms announced on central that you have not joined, plus rings you sent that are still awaiting the callee's model. Instant, a local read, never blocks.",
	"mesh_say":         "Say something in a room, or broadcast on central: publishes one conversation envelope ({message_id, room_topic, in_reply_to?, sent_at, from, kind, text, refs?}) with your node id, a fresh message_id and the clock filled in. kind defaults to remark_made; question_asked expects an answer_given, task_handed_over expects a result_reported, lane_claimed expects a lane_released once you're done or dropping it (so others can see a lane is still open: scan for a lane_claimed with no matching lane_released reply), and every one of those replies MUST carry in_reply_to. lane_claimed itself does not require in_reply_to -- a self-initiated claim on work nobody handed you is legitimate too. claim_confirmed/claim_disputed weigh in on a specific result_reported (also in_reply_to required) -- see claim_verification.ts's own doc for the derived status this produces and its honest limits (it can only verify evidence-backed claims, and currently caps out at a weak 'corroborated' signal, never a strong 'verified' one, pending a realm-membership-tier distinction that doesn't exist on the wire yet). On a room you are not in yet, joins it first. On central (agents.lobby) use it for help_requested/help_offered broadcasts to whoever is around, not for conversation. Pass wait_reply_seconds to also wait, in this same call, for the first envelope from another sender on that topic: the background watch on the room was already running before your message went out, so unlike a publish-then-watch pair there is no gap for a fast reply to fall into. Still no ack on the send itself (PUBLISH has none); a ring is what gives you one.",
	"mesh_answer_ring": "Answer a ring that was deferred to you (mesh_read_inbox lists them under rings.pending, with who rang and why). answer 1 accepts: you join the room first, then the caller is told and can mesh_say. answer 2 declines, with an optional reason the caller sees. The answer travels back as a proven call to the caller's own ring endpoint; if they are no longer present, caller_notified is 0 and your answer is still recorded here. Deferring again is not an answer; leave it pending instead.",
	"mesh_agents":      "List agents seen on the mesh via their agent.hello heartbeats (started with mesh_hello). Reads a persistent local SQLite roster, not a live mesh query -- it survives a restart of this process, but only reflects agents this identity has ever heard a hello from (entries unseen for 15 minutes are pruned). Sorted most-recently-seen first. `stale: true` flags an entry that has missed roughly 3+ of its own reported heartbeats -- probably gone, well before the 15-minute hard prune.",
	"mesh_read_inbox":  "Read what has arrived: rings (pending ones first -- someone rang you under your \"ask\" policy and is waiting for mesh_answer_ring -- then recent answered ones, both directions), the rooms you are in, threaded (each message carries thread_root and depth from its in_reply_to chain), and recent help_requested/help_offered broadcasts on central from other agents. Instant, a local SQLite read, never blocks. Pass room_topic to read one room only. Rooms only ever show what arrived while this process was watching them -- nothing from before you joined.",
	"mesh_ring":        "Ring another agent: an addressed invite delivered as a mesh_call to their agent.<node_id>.ring procedure with your identity proof, carrying a room to talk in (a new one, opened for the two of you, unless you pass a room you are already in). You get exactly one of: answer 1 accepted (they join the room; this call then waits up to wait_join_seconds for their participant_joined, so joined: 1 means the room is genuinely two-sided and PROVEN -- an accepted or declined answer is verified against their own key before it is trusted, not just whoever answered), 2 declined (with their reason), 3 deferred (their operator's policy is \"ask\", their model decides later and mesh_answer_ring carries the answer back to you; the room stays open), or unreachable: 1 (nobody serves that procedure right now, or answered without proving they hold the key). purpose is mandatory and short: a deferred ring is judged from it. This is the ONLY way to reach an agent that has not invited you; never write into a room they have not joined.",
}

func fakeToolsFromRealDescriptions() []mcpclient.Tool {
	tools := make([]mcpclient.Tool, 0, len(realDescriptionsAsOf025_2)+1)
	for name, desc := range realDescriptionsAsOf025_2 {
		tools = append(tools, mcpclient.Tool{Name: name, Description: desc, InputSchema: map[string]any{"type": "object"}})
	}
	// One tool with no override, to prove the wrapper leaves it alone.
	tools = append(tools, mcpclient.Tool{Name: "mesh_rooms_unrelated_tool", Description: "untouched", InputSchema: map[string]any{"type": "object"}})
	return tools
}

func TestTerseDescriptionSource_ShortensKnownToolsLeavesOthersAlone(t *testing.T) {
	inner := &fakeToolSource{tools: fakeToolsFromRealDescriptions()}
	src := NewTerseDescriptionSource(inner)

	got, err := src.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	byName := make(map[string]mcpclient.Tool, len(got))
	for _, tool := range got {
		byName[tool.Name] = tool
	}

	for name, realDesc := range realDescriptionsAsOf025_2 {
		if name == "mesh_say" || name == "mesh_ring" {
			continue // full replacement, checked separately below
		}
		tool, ok := byName[name]
		if !ok {
			t.Fatalf("expected %s in the result", name)
		}
		if tool.Description == realDesc {
			t.Fatalf("%s: expected the description to be shortened, still matches the real one verbatim", name)
		}
		if len(tool.Description) >= len(realDesc) {
			t.Fatalf("%s: expected the terse description (%d bytes) to be shorter than the real one (%d bytes)", name, len(tool.Description), len(realDesc))
		}
		if tool.InputSchema == nil {
			t.Fatalf("%s: expected InputSchema to still be set (pass-through, not stripped)", name)
		}
	}

	unrelated, ok := byName["mesh_rooms_unrelated_tool"]
	if !ok || unrelated.Description != "untouched" {
		t.Fatalf("expected the unrelated tool to pass through unchanged, got %+v", unrelated)
	}
}

func TestTerseDescriptionSource_MeshSayGetsFullReplacementShorterThanReal(t *testing.T) {
	inner := &fakeToolSource{tools: fakeToolsFromRealDescriptions()}
	src := NewTerseDescriptionSource(inner)

	got, err := src.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	var meshSay *mcpclient.Tool
	for i := range got {
		if got[i].Name == "mesh_say" {
			meshSay = &got[i]
		}
	}
	if meshSay == nil {
		t.Fatalf("expected mesh_say in the result")
	}
	if len(meshSay.Description) >= len(realDescriptionsAsOf025_2["mesh_say"]) {
		t.Fatalf("expected mesh_say's terse description shorter than the real one")
	}
	if strings.Contains(meshSay.Description, "lane_claimed") || strings.Contains(meshSay.Description, "claim_confirmed") {
		t.Fatalf("expected the unused lane/claim workflow dropped from mesh_say's description, got: %s", meshSay.Description)
	}
	schema, ok := meshSay.InputSchema.(map[string]any)
	if !ok {
		t.Fatalf("expected mesh_say's InputSchema to be a map, got %T", meshSay.InputSchema)
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("expected mesh_say's schema to have properties")
	}
	kindField, ok := props["kind"].(map[string]any)
	if !ok {
		t.Fatalf("expected a kind field in mesh_say's schema")
	}
	kindEnum, ok := kindField["enum"].([]string)
	if !ok {
		t.Fatalf("expected kind's enum to be a []string")
	}
	for _, dropped := range []string{"lane_claimed", "lane_released", "claim_confirmed", "claim_disputed", "task_handed_over", "result_reported", "room_opened", "participant_joined", "participant_left", "room_closed"} {
		for _, v := range kindEnum {
			if v == dropped {
				t.Fatalf("expected %q dropped from mesh_say's kind enum, still present: %v", dropped, kindEnum)
			}
		}
	}
	for _, required := range []string{"remark_made", "question_asked", "answer_given"} {
		found := false
		for _, v := range kindEnum {
			if v == required {
				found = true
			}
		}
		if !found {
			t.Fatalf("expected %q kept in mesh_say's kind enum, got: %v", required, kindEnum)
		}
	}
	for _, dropped := range []string{"refs", "wait_reply_seconds", "host"} {
		if _, present := props[dropped]; present {
			t.Fatalf("expected %q dropped from mesh_say's schema entirely, still present", dropped)
		}
	}
	required, ok := schema["required"].([]string)
	if !ok || len(required) != 2 || required[0] != "room_topic" || required[1] != "text" {
		t.Fatalf("expected required to still be exactly [room_topic, text], got %v", schema["required"])
	}
}

// Covers the real gap found 2026-09-08: mesh_ring joining
// DefaultToolAllowlist (see allowlist.go) meant its real, live
// description -- the largest of any allowlisted tool, 1,742 bytes
// description+schema together, measured live -- flowed straight through
// untrimmed and pushed the fixed prefix back over the 1,500-token
// ceiling R2 had just landed. Needed the same full-replacement tier as
// mesh_say, not the safe description-only one.
func TestTerseDescriptionSource_MeshRingGetsFullReplacementShorterThanReal(t *testing.T) {
	inner := &fakeToolSource{tools: fakeToolsFromRealDescriptions()}
	src := NewTerseDescriptionSource(inner)

	got, err := src.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	var meshRing *mcpclient.Tool
	for i := range got {
		if got[i].Name == "mesh_ring" {
			meshRing = &got[i]
		}
	}
	if meshRing == nil {
		t.Fatalf("expected mesh_ring in the result")
	}
	if len(meshRing.Description) >= len(realDescriptionsAsOf025_2["mesh_ring"]) {
		t.Fatalf("expected mesh_ring's terse description shorter than the real one")
	}
	for _, keep := range []string{"accepted", "declined", "deferred", "unreachable", "purpose", "only", "invited"} {
		if !strings.Contains(strings.ToLower(meshRing.Description), keep) {
			t.Fatalf("expected the terse description to keep the load-bearing word %q, got: %s", keep, meshRing.Description)
		}
	}
	schema, ok := meshRing.InputSchema.(map[string]any)
	if !ok {
		t.Fatalf("expected mesh_ring's InputSchema to be a map, got %T", meshRing.InputSchema)
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("expected mesh_ring's schema to have properties")
	}
	for _, kept := range []string{"to", "purpose", "room_topic"} {
		if _, present := props[kept]; !present {
			t.Fatalf("expected %q kept in mesh_ring's schema", kept)
		}
	}
	for _, dropped := range []string{"wait_join_seconds", "host"} {
		if _, present := props[dropped]; present {
			t.Fatalf("expected %q dropped from mesh_ring's schema entirely, still present", dropped)
		}
	}
	required, ok := schema["required"].([]string)
	if !ok || len(required) != 2 || required[0] != "to" || required[1] != "purpose" {
		t.Fatalf("expected required to be exactly [to, purpose], got %v", schema["required"])
	}
}

// Covers R2's live verification pass (2026-09-07): the identical "host"
// field (and its ~90-character description) was measured live as real
// duplicate weight across most of macula-mcp's own tools. This must
// disappear from every tool that has one, not just mesh_say's own
// hand-authored replacement.
func TestTerseDescriptionSource_DropsHostFromEveryToolWithOne(t *testing.T) {
	inner := &fakeToolSource{tools: []mcpclient.Tool{
		{Name: "mesh_join_room", Description: realDescriptionsAsOf025_2["mesh_join_room"], InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"room_topic": map[string]any{"type": "string"},
				"host":       map[string]any{"type": "string", "description": "Station to connect through..."},
			},
			"required": []string{"room_topic"},
		}},
		// A tool with no host field at all must pass through unaffected.
		{Name: "mesh_rooms", Description: realDescriptionsAsOf025_2["mesh_rooms"], InputSchema: map[string]any{"type": "object", "properties": map[string]any{}}},
	}}
	src := NewTerseDescriptionSource(inner)

	got, err := src.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	for _, tool := range got {
		schema, ok := tool.InputSchema.(map[string]any)
		if !ok {
			t.Fatalf("%s: expected a map schema, got %T", tool.Name, tool.InputSchema)
		}
		props, ok := schema["properties"].(map[string]any)
		if !ok {
			t.Fatalf("%s: expected properties", tool.Name)
		}
		if _, present := props["host"]; present {
			t.Fatalf("%s: expected host dropped from the schema, still present", tool.Name)
		}
		if tool.Name == "mesh_join_room" {
			if _, present := props["room_topic"]; !present {
				t.Fatalf("expected room_topic to survive the host strip untouched")
			}
		}
	}
}

func TestTerseDescriptionSource_CallToolRawDelegatesUnchanged(t *testing.T) {
	inner := &fakeToolSource{callText: `{"ok":true}`}
	src := NewTerseDescriptionSource(inner)

	got, err := src.CallToolRaw(context.Background(), "mesh_say", `{"room_topic":"x","text":"hi"}`)
	if err != nil {
		t.Fatalf("CallToolRaw: %v", err)
	}
	if got != `{"ok":true}` {
		t.Fatalf("expected the inner source's result passed through unchanged, got %q", got)
	}
	if len(inner.calls) != 1 || inner.calls[0] != `mesh_say({"room_topic":"x","text":"hi"})` {
		t.Fatalf("expected the call forwarded to inner unchanged, got %v", inner.calls)
	}
}
