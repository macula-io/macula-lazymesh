package agent

import (
	"context"
	"fmt"

	"github.com/macula-io/macula-lazymesh/internal/mcpclient"
)

// Gate decides one tool call for a pre-tool hook (G8): a program driving
// the session over the control socket answers allow or deny, with a
// reason the model sees on denial. A non-nil error means no decision
// could be had (the hook's controller went away, a timeout, an
// interrupted turn); the call is refused.
type Gate func(ctx context.Context, tool, argumentsJSON string) (allow bool, reason string, err error)

// HookSource wraps another ToolSource and puts every call, not just a
// listed few, through a Gate. It sits outside AskSource: a controller's
// policy refuses a call before a human is asked about it.
type HookSource struct {
	inner ToolSource
	gate  Gate
}

// NewHookSource wraps inner so every call waits for gate's decision.
func NewHookSource(inner ToolSource, gate Gate) *HookSource {
	return &HookSource{inner: inner, gate: gate}
}

func (h *HookSource) ListTools(ctx context.Context) ([]mcpclient.Tool, error) {
	return h.inner.ListTools(ctx)
}

func (h *HookSource) CallToolRaw(ctx context.Context, name string, argumentsJSON string) (string, error) {
	allow, reason, err := h.gate(ctx, name, argumentsJSON)
	if err != nil {
		return "", fmt.Errorf("the pre-tool hook could not decide %q: %w", name, err)
	}
	if !allow {
		if reason == "" {
			return "", fmt.Errorf("%q was refused by the pre-tool hook", name)
		}
		return "", fmt.Errorf("%q was refused by the pre-tool hook: %s", name, reason)
	}
	return h.inner.CallToolRaw(ctx, name, argumentsJSON)
}
