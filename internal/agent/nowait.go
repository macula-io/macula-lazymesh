package agent

import (
	"context"
	"encoding/json"

	"github.com/macula-io/macula-lazymesh/internal/mcpclient"
)

// maxModelWaitSeconds is how long the model itself may still ask mesh_say
// to block for, once the loop-owned room-waiter design (macula-io/macula-
// lazymesh#14) owns real waiting. Not zero: a model that has just said
// something and wants a brief moment for an immediate reply is a
// legitimate, bounded use mesh_say's own doc already supports -- what
// this forecloses is the HOUR-LONG wait the old system prompt used to
// instruct ("listen efficiently instead of returning immediately"),
// which is exactly the blocking behavior the spike moves out of the
// model's own tool-calling turn.
//
// 10 specifically, not some other small number: the actual tradeoff is
// between catching a genuinely-immediate reply (worth a short hold) and
// re-opening the door to today's bug (worth guarding against) --
// anything long enough to matter for the second concern is also long
// enough to noticeably tie up the loop's single tool-execution slot, so
// this stays on the short side deliberately. Not separately measured
// against real reply latency in this spike; a real implementation
// should revisit this number against actual data rather than inherit it
// unquestioned.
const maxModelWaitSeconds = 10

// NoBlockingWaitSource wraps another ToolSource and clamps any
// wait_reply_seconds argument on mesh_say down to maxModelWaitSeconds,
// regardless of what the model asks for -- defense in depth matching
// AllowlistSource's own posture ("don't just tell the model what to do,
// remove the capability to do the wrong thing"): a prompt rewrite alone
// is one bad instruction (or one confused model) away from recreating
// the exact bug #14 exists to fix. mesh_wait_room itself needs no
// clamping here -- it is never on DefaultToolAllowlist, so the model
// cannot call it regardless; only the loop-owned roomwaiter.Manager does,
// directly against the client, bypassing this wrapper entirely.
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
	if name != "mesh_say" {
		return n.inner.CallToolRaw(ctx, name, argumentsJSON)
	}
	clamped, err := clampWaitReplySeconds(argumentsJSON)
	if err != nil {
		// Malformed arguments aren't this wrapper's problem to diagnose --
		// pass through unchanged and let the real tool's own validation
		// produce the error the model can act on.
		return n.inner.CallToolRaw(ctx, name, argumentsJSON)
	}
	return n.inner.CallToolRaw(ctx, name, clamped)
}

func clampWaitReplySeconds(argumentsJSON string) (string, error) {
	args := map[string]any{}
	if argumentsJSON != "" {
		if err := json.Unmarshal([]byte(argumentsJSON), &args); err != nil {
			return "", err
		}
	}
	raw, ok := args["wait_reply_seconds"]
	if !ok {
		return argumentsJSON, nil
	}
	seconds, ok := raw.(float64) // json.Unmarshal decodes any JSON number as float64
	if !ok || seconds <= maxModelWaitSeconds {
		return argumentsJSON, nil
	}
	args["wait_reply_seconds"] = float64(maxModelWaitSeconds)
	out, err := json.Marshal(args)
	if err != nil {
		return "", err
	}
	return string(out), nil
}
