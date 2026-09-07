package agent

import (
	"context"
	"fmt"

	"github.com/macula-io/macula-lazymesh/internal/mcpclient"
)

// DefaultToolAllowlist is the deny-by-default set of tools an agent driven
// by untrusted mesh content is permitted to see or call at all: the
// conversational mesh primitives, nothing that reads/writes local state or
// executes anything.
//
// Why this exists (found by an adversarial review, 2026-09-06): every tool
// a ToolSource advertises goes straight to the LLM, and every tool result
// -- room messages, ring purposes, inbox contents -- is peer-authored text
// that comes straight back into the model's own context with nothing
// marking it untrusted. A peer's room post or ring purpose is exactly as
// capable of steering a subsequent tool call as an operator's own
// instruction is. Concretely: macula-mcp's own mesh_serve lets a peer ask
// the agent to register a local shell command as an RPC handler (a
// persistent backdoor, no re-registration needed per call);
// mesh_remember_directory reads an attacker-picked local directory; and
// internal/localtools' shell_exec is the sharpest version of the same
// risk, immediate arbitrary execution with no pre-registration step at
// all. AllowlistSource is what stands between "any tool this process
// happens to have wired up" and "any tool the model can actually reach."
//
// internal/localtools' tools (shell_exec/read_file/write_file) are
// DELIBERATELY not in this list, even when local_tools.enabled is set --
// that config flag controls whether the source EXISTS, not whether an
// agent whose entire context can be steered by arbitrary mesh peers is
// allowed to reach it. Making local tools reachable is a separate,
// explicit config.ToolAllowlist override the operator has to write
// themselves (see config.go) -- never a side effect of one flag.
// mesh_hello deliberately excluded (2026-09-07, R2): its own tool
// description says presence auto-starts on any of mesh_say/
// mesh_join_room/mesh_leave_room/mesh_rooms/mesh_answer_ring/
// mesh_read_inbox -- every OTHER tool on this list -- and
// agentInitialPrompt's very first instruction always calls one of them.
// Confirmed nothing here relies on operator_name (mesh_hello's own
// settable label): lazymesh's own status bar shows the deterministic
// petname instead, computed from node_id regardless of whether
// mesh_hello was ever called. This tool's own schema was also the single
// largest line item in the fixed prefix sent on every request -- pure
// cost with no remaining function once the auto-start covers it.
var DefaultToolAllowlist = []string{
	"mesh_join_room",
	"mesh_leave_room",
	"mesh_say",
	"mesh_read_inbox",
	"mesh_answer_ring",
	"mesh_rooms",
	"mesh_agents",
}

// AllowlistSource wraps another ToolSource and enforces allowed at BOTH
// steps: ListTools only ever returns allowed tools (so the model is never
// even told a disallowed tool exists), and CallToolRaw refuses anything
// not on the list regardless of how the model was asked to call it --
// defense in depth against a bug or future code path that might otherwise
// hand the model a tool spec some other way.
type AllowlistSource struct {
	inner   ToolSource
	allowed map[string]bool
}

// NewAllowlistSource wraps inner, permitting only the given tool names.
// An empty allowed list is valid and means exactly what it says: no tools
// at all get through.
func NewAllowlistSource(inner ToolSource, allowed []string) *AllowlistSource {
	set := make(map[string]bool, len(allowed))
	for _, name := range allowed {
		set[name] = true
	}
	return &AllowlistSource{inner: inner, allowed: set}
}

func (a *AllowlistSource) ListTools(ctx context.Context) ([]mcpclient.Tool, error) {
	tools, err := a.inner.ListTools(ctx)
	if err != nil {
		return nil, err
	}
	filtered := make([]mcpclient.Tool, 0, len(tools))
	for _, t := range tools {
		if a.allowed[t.Name] {
			filtered = append(filtered, t)
		}
	}
	return filtered, nil
}

func (a *AllowlistSource) CallToolRaw(ctx context.Context, name string, argumentsJSON string) (string, error) {
	if !a.allowed[name] {
		return "", fmt.Errorf("tool %q is not on the allowlist and cannot be called", name)
	}
	return a.inner.CallToolRaw(ctx, name, argumentsJSON)
}
