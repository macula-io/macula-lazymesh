package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/macula-io/macula-lazymesh/internal/mcpclient"
)

// askRecordingTools records every CallToolRaw it actually serves — the
// proof that consent gates the call before the inner source runs.
type askRecordingTools struct {
	calls []string
}

func (a *askRecordingTools) ListTools(context.Context) ([]mcpclient.Tool, error) { return nil, nil }
func (a *askRecordingTools) CallToolRaw(ctx context.Context, name, argumentsJSON string) (string, error) {
	a.calls = append(a.calls, name)
	return "ok", nil
}

// TestAskSourceGatesOnlyAskListedTools pins the G9 layering: a listed
// tool waits for consent (and never runs when denied), an unlisted tool
// passes straight through, and ListTools is untouched either way.
func TestAskSourceGatesOnlyAskListedTools(t *testing.T) {
	var asked []string
	consent := func(ctx context.Context, tool, argumentsJSON string) (bool, error) {
		asked = append(asked, tool)
		return tool == "shell_exec", nil
	}
	inner := &askRecordingTools{}
	src := NewAskSource(inner, []string{"shell_exec"}, consent)

	if _, err := src.CallToolRaw(context.Background(), "mesh_say", "{}"); err != nil {
		t.Fatalf("unlisted tool: %v", err)
	}
	if len(asked) != 0 {
		t.Fatalf("unlisted tool prompted consent: %v", asked)
	}

	if _, err := src.CallToolRaw(context.Background(), "shell_exec", `{"cmd":"true"}`); err != nil {
		t.Fatalf("allowed listed tool: %v", err)
	}
	if len(asked) != 1 || asked[0] != "shell_exec" {
		t.Fatalf("consent calls = %v", asked)
	}
	if len(inner.calls) != 2 || inner.calls[1] != "shell_exec" {
		t.Fatalf("inner calls = %v", inner.calls)
	}
}

// TestAskSourceDenyRefusesTheCall proves a denial refuses the call with a
// clear error and the inner source never runs it.
func TestAskSourceDenyRefusesTheCall(t *testing.T) {
	consent := func(ctx context.Context, tool, argumentsJSON string) (bool, error) {
		return false, nil
	}
	inner := &askRecordingTools{}
	src := NewAskSource(inner, []string{"shell_exec"}, consent)

	_, err := src.CallToolRaw(context.Background(), "shell_exec", `{"cmd":"true"}`)
	if err == nil {
		t.Fatal("expected the denial to refuse the call")
	}
	if len(inner.calls) != 0 {
		t.Fatalf("denied call reached the inner source: %v", inner.calls)
	}
}

// TestAskRefusesACallTooLongToShowWhole (#39): the operator approves
// what they can read in full, so a call longer than the approval prompt
// can show is refused without asking, and never runs.
func TestAskRefusesACallTooLongToShowWhole(t *testing.T) {
	inner := &askRecordingTools{}
	asked := false
	src := NewAskSource(inner, []string{"shell_exec"}, func(ctx context.Context, tool, args string) (bool, error) {
		asked = true
		return true, nil
	})
	long := `{"command":"` + strings.Repeat("a", MaxApprovableBytes) + `"}`
	if _, err := src.CallToolRaw(context.Background(), "shell_exec", long); err == nil {
		t.Fatal("a call too long to show whole was allowed")
	}
	if asked {
		t.Fatal("the operator was asked about a call the prompt cannot show whole")
	}
	if len(inner.calls) != 0 {
		t.Fatalf("the refused call ran: %v", inner.calls)
	}
}
