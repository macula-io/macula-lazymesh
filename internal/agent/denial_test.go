package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/macula-io/macula-lazymesh/internal/mcpclient"
)

// TestDenialSourceRecordsRefusalsOnly (#26, C8): an operator denial, an
// oversized call and an allowlist refusal are recorded with the whole
// arguments; a tool that ran and failed is not a denial.
func TestDenialSourceRecordsRefusalsOnly(t *testing.T) {
	var got []Denial
	deny := func(context.Context, string, string) (bool, error) { return false, nil }
	inner := NewAllowlistSource(failingTools{}, []string{"risky", "broken"})
	tools := NewDenialSource(NewAskSource(inner, []string{"risky"}, deny), func(d Denial) { got = append(got, d) })
	ctx := context.Background()
	_, _ = tools.CallToolRaw(ctx, "risky", `{"cmd":"rm -rf ~"}`)
	_, _ = tools.CallToolRaw(ctx, "not_allowed", `{}`)
	_, _ = tools.CallToolRaw(ctx, "broken", `{}`)
	if len(got) != 2 {
		t.Fatalf("denials = %+v, want the operator denial and the allowlist refusal", got)
	}
	if got[0].Tool != "risky" || got[0].Arguments != `{"cmd":"rm -rf ~"}` || got[0].Reason == "" || got[0].At.IsZero() {
		t.Errorf("first denial = %+v", got[0])
	}
	if got[1].Tool != "not_allowed" {
		t.Errorf("second denial = %+v", got[1])
	}
}

// TestRefusedKeepsTheMessageAndTheCause: marking an error as a refusal
// changes neither what the model reads nor what it wraps.
func TestRefusedKeepsTheMessageAndTheCause(t *testing.T) {
	cause := errors.New("approval timed out")
	err := Refused(cause)
	if err.Error() != "approval timed out" || !errors.Is(err, cause) || !IsRefusal(err) {
		t.Fatalf("Refused(%v) = %v", cause, err)
	}
	if IsRefusal(cause) {
		t.Fatal("a plain error counts as a refusal")
	}
}

// failingTools advertises risky and broken, and every call fails as a
// tool would: not a refusal.
type failingTools struct{}

func (failingTools) ListTools(context.Context) ([]mcpclient.Tool, error) {
	return []mcpclient.Tool{{Name: "risky"}, {Name: "broken"}}, nil
}

func (failingTools) CallToolRaw(context.Context, string, string) (string, error) {
	return "", errors.New("exit status 1")
}

// TestDenialSourceRecordsPolicyRefusals (#26 C8, #45): a call settled
// before asking (a push to main) and one whose approval text cannot be
// built are refusals too, so both are recorded.
func TestDenialSourceRecordsPolicyRefusals(t *testing.T) {
	var got []Denial
	allow := func(context.Context, string, string) (bool, error) { return true, nil }
	describe := func(string, string) (string, error) { return "", errors.New("no remote named origin") }
	refuse := func(tool, _ string) error {
		if tool == "git_push" {
			return errors.New("a push to main is never offered")
		}
		return nil
	}
	ask := NewAskSource(&fakeToolSource{}, []string{"git_push", "git_commit"}, allow).Describe(func(tool, args string) (string, error) {
		if tool == "git_commit" {
			return describe(tool, args)
		}
		return args, nil
	})
	tools := NewDenialSource(NewRefuseSource(ask, refuse), func(d Denial) { got = append(got, d) })
	ctx := context.Background()
	_, _ = tools.CallToolRaw(ctx, "git_push", `{"ref":"main"}`)
	_, _ = tools.CallToolRaw(ctx, "git_commit", `{}`)
	if len(got) != 2 || got[0].Tool != "git_push" || got[1].Tool != "git_commit" {
		t.Fatalf("denials = %+v, want the push refusal and the describe refusal", got)
	}
}
