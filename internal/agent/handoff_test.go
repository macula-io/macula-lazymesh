package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/macula-io/macula-lazymesh/internal/mcpclient"
)

// recordingInner records calls that pass the wrapper — the proof that the
// wrapper is the boundary, not a post-hoc filter.
type recordingInner struct {
	calls []string
}

func (r *recordingInner) ListTools(context.Context) ([]mcpclient.Tool, error) { return nil, nil }
func (r *recordingInner) CallToolRaw(ctx context.Context, name, argumentsJSON string) (string, error) {
	r.calls = append(r.calls, name)
	return "sent", nil
}

const hex32 = "0123456789abcdef0123456789abcdef"

// TestHandoffSourceRefusesReplyKindsWithoutInReplyTo pins G11's
// boundary: every reply kind without a well-formed in_reply_to is
// refused before the inner source runs.
func TestHandoffSourceRefusesReplyKindsWithoutInReplyTo(t *testing.T) {
	for _, kind := range []string{"answer_given", "result_reported", "lane_released", "claim_confirmed", "claim_disputed"} {
		inner := &recordingInner{}
		h := NewHandoffSource(inner)
		args := `{"kind":"` + kind + `","text":"hi"}`
		if _, err := h.CallToolRaw(context.Background(), "mesh_say", args); err == nil {
			t.Fatalf("kind %q without in_reply_to was not refused", kind)
		}
		if len(inner.calls) != 0 {
			t.Fatalf("kind %q without in_reply_to reached the mesh", kind)
		}
	}
}

// TestHandoffSourceRefusesMalformedInReplyTo pins the id shape: a
// reply's in_reply_to must be exactly 32 hex chars.
func TestHandoffSourceRefusesMalformedInReplyTo(t *testing.T) {
	for _, id := range []string{"", "short", strings.Repeat("g", 32), strings.Repeat("a", 31), "0123456789ABCDEF0123456789ABCDEF"} {
		inner := &recordingInner{}
		h := NewHandoffSource(inner)
		args := `{"kind":"answer_given","in_reply_to":"` + id + `"}`
		if _, err := h.CallToolRaw(context.Background(), "mesh_say", args); err == nil {
			t.Fatalf("malformed in_reply_to %q was not refused", id)
		}
	}
}

// TestHandoffSourcePermitsWellFormedRepliesAndPlainKinds pins the other
// half: a reply WITH its id passes, and remark_made/lane_claimed need no
// id at all.
func TestHandoffSourcePermitsWellFormedRepliesAndPlainKinds(t *testing.T) {
	cases := []string{
		`{"kind":"answer_given","in_reply_to":"` + hex32 + `"}`,
		`{"kind":"result_reported","in_reply_to":"` + hex32 + `"}`,
		`{"kind":"remark_made","text":"hi"}`,
		`{"kind":"lane_claimed","text":"taking this"}`,
		`{"kind":"question_asked","text":"anyone?"}`,
	}
	for _, args := range cases {
		inner := &recordingInner{}
		h := NewHandoffSource(inner)
		if _, err := h.CallToolRaw(context.Background(), "mesh_say", args); err != nil {
			t.Fatalf("valid call %s refused: %v", args, err)
		}
		if len(inner.calls) != 1 {
			t.Fatalf("valid call %s never reached the mesh", args)
		}
	}
}

// TestHandoffSourcePassesOtherToolsThrough pins the scope: only mesh_say
// is ever examined; other tools pass through untouched.
func TestHandoffSourcePassesOtherToolsThrough(t *testing.T) {
	inner := &recordingInner{}
	h := NewHandoffSource(inner)
	if _, err := h.CallToolRaw(context.Background(), "mesh_rooms", "{}"); err != nil {
		t.Fatalf("non-mesh_say call refused: %v", err)
	}
	if len(inner.calls) != 1 {
		t.Fatal("non-mesh_say call never reached the inner source")
	}
}
