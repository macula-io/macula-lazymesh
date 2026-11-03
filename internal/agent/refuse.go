package agent

import (
	"context"
	"fmt"

	"github.com/macula-io/macula-lazymesh/internal/mcpclient"
)

// RefuseFunc settles a call from its own arguments, before anyone is
// asked. A nil error means "no opinion"; a non-nil error is the refusal
// the model sees.
type RefuseFunc func(tool, argumentsJSON string) error

// RefuseSource wraps another ToolSource and refuses a call on its own
// arguments before AskSource puts a question to the operator, and before
// a pre-tool hook's controller sees it. Some calls are not the operator's
// to allow -- a push to main, a write outside the checkout -- and asking
// about one wastes the operator's attention on a command that will never
// run whatever they answer (macula-io/macula-lazymesh#45).
//
// It sits outside AskSource and HookSource, so its refusal needs no
// consent and no controller; the tool source still refuses the same call
// on its own, so a caller that never wires this wrapper loses the prompt
// economy, never the guarantee.
type RefuseSource struct {
	inner  ToolSource
	refuse RefuseFunc
}

// NewRefuseSource wraps inner so every call passes refuse first.
func NewRefuseSource(inner ToolSource, refuse RefuseFunc) *RefuseSource {
	return &RefuseSource{inner: inner, refuse: refuse}
}

func (r *RefuseSource) ListTools(ctx context.Context) ([]mcpclient.Tool, error) {
	return r.inner.ListTools(ctx)
}

func (r *RefuseSource) CallToolRaw(ctx context.Context, name, argumentsJSON string) (string, error) {
	if r.refuse != nil {
		if err := r.refuse(name, argumentsJSON); err != nil {
			return "", Refused(fmt.Errorf("the %q call was refused before approval was asked: %w", name, err))
		}
	}
	return r.inner.CallToolRaw(ctx, name, argumentsJSON)
}
