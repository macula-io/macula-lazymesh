package agent

import (
	"context"
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

// TestApprovalPreviewTruncates pins the display helper: small args pass
// through, large args truncate with a marker.
func TestApprovalPreviewTruncates(t *testing.T) {
	if got := ApprovalPreview(`{"a":1}`, 100); got != `{"a":1}` {
		t.Fatalf("small preview = %q", got)
	}
	big := "123456789012345678901234567890"
	got := ApprovalPreview(big, 10)
	if len(got) <= 10 || !contains(got, "truncated") {
		t.Fatalf("truncated preview = %q", got)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
