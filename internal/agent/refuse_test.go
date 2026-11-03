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

// TestAskSourceShowsWhatWillRun: the operator approves the described
// command, not the arguments the model passed, and a call that cannot be
// described is refused without asking.
func TestAskSourceShowsWhatWillRun(t *testing.T) {
	inner := &countSource{}
	var shown []string
	src := NewAskSource(inner, []string{"git_push", "git_commit"}, func(_ context.Context, _, args string) (bool, error) {
		shown = append(shown, args)
		return true, nil
	}).Describe(func(tool, args string) (string, error) {
		if tool == "git_push" {
			return `{"remote":"origin","url":"https://example.invalid/x.git","branch":"work"}`, nil
		}
		if tool == "git_commit" {
			return "", errors.New("the checkout's .git changed")
		}
		return args, nil
	})
	if _, err := src.CallToolRaw(context.Background(), "git_push", `{}`); err != nil {
		t.Fatal(err)
	}
	if len(shown) != 1 || !strings.Contains(shown[0], "https://example.invalid/x.git") {
		t.Fatalf("the operator was shown %v, not the resolved command", shown)
	}
	if _, err := src.CallToolRaw(context.Background(), "git_commit", `{"message":"m"}`); err == nil {
		t.Fatal("a call that cannot be described was allowed")
	}
	if len(shown) != 1 {
		t.Fatalf("the operator was asked about a call that could not be described: %v", shown)
	}
}
