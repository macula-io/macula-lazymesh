package agent

import (
	"context"
	"errors"
	"time"

	"github.com/macula-io/macula-lazymesh/internal/mcpclient"
)

// refusal marks an error as a tool call lazymesh refused on purpose (the
// operator denied it, a policy or the allowlist forbade it, no shell is
// offered), as opposed to a tool that ran and failed. Its message is the
// wrapped error's own.
type refusal struct{ err error }

func (r refusal) Error() string { return r.err.Error() }
func (r refusal) Unwrap() error { return r.err }

// Refused marks err as a refusal.
func Refused(err error) error {
	return refusal{err: err}
}

// IsRefusal reports whether err, or anything it wraps, is a refusal.
func IsRefusal(err error) bool {
	var r refusal
	return errors.As(err, &r)
}

// Denial is one refused tool call, recorded whole (#26, threat model
// C8): a pattern of attempts must be visible afterwards, not only the
// one call on screen.
type Denial struct {
	At        time.Time `json:"at"`
	Tool      string    `json:"tool"`
	Arguments string    `json:"arguments"`
	Reason    string    `json:"reason"`
}

// DenialSource records every refused call of the chain it wraps. It
// belongs outermost, so it sees refusals from every layer.
type DenialSource struct {
	inner  ToolSource
	record func(Denial)
}

// NewDenialSource wraps inner so each refusal is passed to record.
func NewDenialSource(inner ToolSource, record func(Denial)) *DenialSource {
	return &DenialSource{inner: inner, record: record}
}

func (d *DenialSource) ListTools(ctx context.Context) ([]mcpclient.Tool, error) {
	return d.inner.ListTools(ctx)
}

func (d *DenialSource) CallToolRaw(ctx context.Context, name string, argumentsJSON string) (string, error) {
	result, err := d.inner.CallToolRaw(ctx, name, argumentsJSON)
	if err != nil && IsRefusal(err) {
		d.record(Denial{At: time.Now().UTC(), Tool: name, Arguments: argumentsJSON, Reason: err.Error()})
	}
	return result, err
}
