package agent

import (
	"context"
	"encoding/json"

	"github.com/macula-io/macula-lazymesh/internal/mcpclient"
)

// maxModelWaitSeconds is how long the model itself may still ask mesh_say
// to block for, once the loop-owned room-waiter design (macula-io/macula-
// lazymesh#14/#15) owns real waiting. Not zero: a model that has just said
// something and wants a brief moment for an immediate reply is a
// legitimate, bounded use mesh_say's own doc already supports -- what
// this forecloses is the HOUR-LONG wait the old system prompt used to
// instruct ("listen efficiently instead of returning immediately"),
// which is exactly the blocking behavior this design moves out of the
// model's own tool-calling turn.
//
// 10 specifically, not some other small number: the actual tradeoff is
// between catching a genuinely-immediate reply (worth a short hold) and
// re-opening the door to the original bug (worth guarding against) --
// anything long enough to matter for the second concern is also long
// enough to noticeably tie up the loop's single tool-execution slot, so
// this stays on the short side deliberately. Still not separately
// measured against real reply latency, carried over unchanged from the
// #14 spike into #15's real implementation -- a genuinely open, minor
// tuning question, not silently resolved just because this shipped.
const maxModelWaitSeconds = 10

// NoBlockingWaitSource wraps another ToolSource and clamps any
// wait_reply_seconds argument on mesh_say, or wait_join_seconds on
// mesh_ring, down to maxModelWaitSeconds, regardless of what the model
// asks for -- defense in depth matching AllowlistSource's own posture
// ("don't just tell the model what to do, remove the capability to do
// the wrong thing"): a prompt rewrite alone is one bad instruction (or
// one confused model) away from recreating the exact bug #14 exists to
// fix. mesh_wait_room itself needs no clamping here -- it is never on
// DefaultToolAllowlist, so the model cannot call it regardless; only the
// loop-owned roomwaiter.Manager does, directly against the client,
// bypassing this wrapper entirely.
//
// mesh_ring's wait_join_seconds added 2026-09-08, the same day mesh_ring
// itself joined DefaultToolAllowlist (see allowlist.go's own doc
// comment): its max is 600s (10 minutes) -- verified against
// mesh_ring.ts directly -- well past maxModelWaitSeconds even before
// considering what the model might ask for, so adding mesh_ring to the
// allowlist without this clamp would have quietly reopened the exact
// blocking-tool-slot failure mode this wrapper exists to close. Not a
// functional loss: mesh_ring's own reply already tells the model what to
// do when the join wasn't seen in time ("Accepted, but their
// participant_joined was not seen in time. mesh_read_inbox will show it
// when it lands; you can mesh_say already"), and the room mesh_ring
// opens is being watched in the background regardless of whether this
// call itself waited for it.
//
// A real asymmetry from mesh_say, checked directly in each tool's own
// source rather than assumed identical: mesh_say's wait_reply_seconds is
// z.optional() with no server-side fallback (rooms.ts), so an omitted
// field genuinely means "don't wait" and needs no clamp. mesh_ring's
// wait_join_seconds is also z.optional() in its own schema, but
// placeRing (mesh_ring.ts) applies `args.waitJoinSeconds ??
// DEFAULT_WAIT_JOIN_SECONDS` -- omitting the field there does NOT mean
// "don't wait," it means "wait the server's own default of 30s," itself
// already 3x maxModelWaitSeconds. clampWaitSeconds' forceWhenAbsent
// parameter exists specifically for this: false for mesh_say (leave a
// genuinely-absent field alone), true for mesh_ring (an absent field
// still needs an explicit value injected, or the server-side default
// slips through this wrapper untouched).
type NoBlockingWaitSource struct {
	inner ToolSource
}

// NewNoBlockingWaitSource wraps inner.
func NewNoBlockingWaitSource(inner ToolSource) *NoBlockingWaitSource {
	return &NoBlockingWaitSource{inner: inner}
}

func (n *NoBlockingWaitSource) ListTools(ctx context.Context) ([]mcpclient.Tool, error) {
	return n.inner.ListTools(ctx)
}

func (n *NoBlockingWaitSource) CallToolRaw(ctx context.Context, name string, argumentsJSON string) (string, error) {
	var field string
	var forceWhenAbsent bool
	switch name {
	case "mesh_say":
		field = "wait_reply_seconds"
	case "mesh_ring":
		field = "wait_join_seconds"
		forceWhenAbsent = true
	default:
		return n.inner.CallToolRaw(ctx, name, argumentsJSON)
	}
	clamped, err := clampWaitSeconds(argumentsJSON, field, forceWhenAbsent)
	if err != nil {
		// Malformed arguments aren't this wrapper's problem to diagnose --
		// pass through unchanged and let the real tool's own validation
		// produce the error the model can act on.
		return n.inner.CallToolRaw(ctx, name, argumentsJSON)
	}
	return n.inner.CallToolRaw(ctx, name, clamped)
}

func clampWaitSeconds(argumentsJSON, field string, forceWhenAbsent bool) (string, error) {
	args := map[string]any{}
	if argumentsJSON != "" {
		if err := json.Unmarshal([]byte(argumentsJSON), &args); err != nil {
			return "", err
		}
	}
	raw, present := args[field]
	if !present {
		if !forceWhenAbsent {
			return argumentsJSON, nil
		}
		args[field] = float64(maxModelWaitSeconds)
		out, err := json.Marshal(args)
		if err != nil {
			return "", err
		}
		return string(out), nil
	}
	seconds, ok := raw.(float64) // json.Unmarshal decodes any JSON number as float64
	if !ok || seconds <= maxModelWaitSeconds {
		return argumentsJSON, nil
	}
	args[field] = float64(maxModelWaitSeconds)
	out, err := json.Marshal(args)
	if err != nil {
		return "", err
	}
	return string(out), nil
}
