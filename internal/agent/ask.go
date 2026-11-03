package agent

import (
	"context"
	"fmt"

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

// MaxApprovableBytes is the longest call an operator is asked about.
// The prompt shows a call whole, never a cut preview (#39: a 200-byte
// preview let a long command hide its tail), so a call longer than a
// person can read through is refused without asking.
const MaxApprovableBytes = 16 << 10

// DescribeFunc turns a call into the text the operator approves, so the
// question is about the command that will actually run rather than about
// the arguments the model happened to pass (macula-io/macula-lazymesh#45:
// git_push {} resolves to a URL and a ref the operator must see). A
// non-nil error refuses the call: a call that cannot be described cannot
// be approved.
type DescribeFunc func(tool, argumentsJSON string) (string, error)

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
	inner    ToolSource
	ask      map[string]bool
	consent  Consent
	describe DescribeFunc
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

// Describe sets what the operator is shown for a call. Without one, the
// call's own arguments are shown, which is what every tool did before.
func (a *AskSource) Describe(f DescribeFunc) *AskSource {
	a.describe = f
	return a
}

func (a *AskSource) ListTools(ctx context.Context) ([]mcpclient.Tool, error) {
	return a.inner.ListTools(ctx)
}

func (a *AskSource) CallToolRaw(ctx context.Context, name string, argumentsJSON string) (string, error) {
	if !a.ask[name] || a.consent == nil {
		return a.inner.CallToolRaw(ctx, name, argumentsJSON)
	}
	shown := argumentsJSON
	if a.describe != nil {
		described, err := a.describe(name, argumentsJSON)
		if err != nil {
			return "", fmt.Errorf("%q was refused before approval was asked: %w", name, err)
		}
		if described != "" {
			shown = described
		}
	}
	if len(shown) > MaxApprovableBytes {
		return "", fmt.Errorf("the %q call is %d bytes, more than the %d an approval prompt shows whole; split it into smaller calls", name, len(shown), MaxApprovableBytes)
	}
	allowed, err := a.consent(ctx, name, shown)
	if err != nil {
		return "", fmt.Errorf("approval for %q could not be obtained: %w", name, err)
	}
	if !allowed {
		return "", fmt.Errorf("approval for %q was denied by the operator", name)
	}
	return a.inner.CallToolRaw(ctx, name, argumentsJSON)
}
