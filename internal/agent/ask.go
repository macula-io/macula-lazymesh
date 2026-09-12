package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/macula-io/macula-lazymesh/internal/mcpclient"
)

// Consent decides whether one tool call may run: the operator answers the
// approval prompt the consent implementation surfaces (TUI popup, control
// socket approve message). A false return with a nil error means a
// deliberate denial; a non-nil error means the question could not be
// answered (no operator, timeout, an interrupted turn) — the call is
// refused either way, but the distinction is what the model's tool
// result says.
type Consent func(ctx context.Context, tool, argumentsJSON string) (bool, error)

// AskSource wraps another ToolSource and gates a configured set of tools
// behind Consent (G9: per-action approval for the sharp tools — the
// operator answers per call instead of a static allowlist answering
// forever). Tools not on the ask list pass through untouched, and the
// ask list only ever applies to tools the inner chain already allows:
// ask is layered OUTSIDE the allowlist, never in place of it.
//
// ListTools is unchanged — an ask-gated tool is still advertised, so the
// model can legitimately attempt it and learn from a denial's result;
// what changes is that each attempt waits for a human decision.
type AskSource struct {
	inner   ToolSource
	ask     map[string]bool
	consent Consent
}

// NewAskSource wraps inner, gating exactly the given tool names behind
// consent. An empty ask list is valid: every call passes through.
func NewAskSource(inner ToolSource, ask []string, consent Consent) *AskSource {
	set := make(map[string]bool, len(ask))
	for _, name := range ask {
		set[name] = true
	}
	return &AskSource{inner: inner, ask: set, consent: consent}
}

func (a *AskSource) ListTools(ctx context.Context) ([]mcpclient.Tool, error) {
	return a.inner.ListTools(ctx)
}

func (a *AskSource) CallToolRaw(ctx context.Context, name string, argumentsJSON string) (string, error) {
	if !a.ask[name] || a.consent == nil {
		return a.inner.CallToolRaw(ctx, name, argumentsJSON)
	}
	allowed, err := a.consent(ctx, name, argumentsJSON)
	if err != nil {
		return "", fmt.Errorf("approval for %q could not be obtained: %w", name, err)
	}
	if !allowed {
		return "", fmt.Errorf("approval for %q was denied by the operator", name)
	}
	return a.inner.CallToolRaw(ctx, name, argumentsJSON)
}

// ApprovalPreview truncates a tool call's arguments for display in an
// approval prompt: the operator needs enough to judge the call, never
// the whole (possibly very large) argument object.
func ApprovalPreview(argumentsJSON string, limit int) string {
	args := strings.TrimSpace(argumentsJSON)
	if limit <= 0 || len(args) <= limit {
		return args
	}
	return args[:limit] + "... [truncated]"
}
