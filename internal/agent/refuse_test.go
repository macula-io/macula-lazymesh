package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/macula-io/macula-lazymesh/internal/mcpclient"
)

type countSource struct{ calls int }

func (c *countSource) ListTools(context.Context) ([]mcpclient.Tool, error) {
	return []mcpclient.Tool{{Name: "git_push"}}, nil
}

func (c *countSource) CallToolRaw(context.Context, string, string) (string, error) {
	c.calls++
	return "ran", nil
}

// TestRefuseSourceRefusesBeforeAsking: the refusal happens without the
// consent function being called, and the inner source never runs.
func TestRefuseSourceRefusesBeforeAsking(t *testing.T) {
	inner := &countSource{}
	asked := 0
	src := NewAskSource(inner, []string{"git_push"}, func(context.Context, string, string) (bool, error) {
		asked++
		return true, nil
	})
	refusing := NewRefuseSource(src, func(tool, args string) error {
		if strings.Contains(args, "main") {
			return errors.New("main is never pushed")
		}
		return nil
	})
	if _, err := refusing.CallToolRaw(context.Background(), "git_push", `{"ref":"main"}`); err == nil {
		t.Fatal("the call was not refused")
	}
	if asked != 0 {
		t.Fatalf("the operator was asked %d times about a call that was already refused", asked)
	}
	if inner.calls != 0 {
		t.Fatal("the inner source ran a refused call")
	}
	if out, err := refusing.CallToolRaw(context.Background(), "git_push", `{"ref":"work"}`); err != nil || out != "ran" {
		t.Fatalf("an allowed call: (%q, %v)", out, err)
	}
	if asked != 1 {
		t.Fatalf("the operator was asked %d times, want 1", asked)
	}
}
