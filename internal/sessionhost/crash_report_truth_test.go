package sessionhost

// The review probes for macula-lazymesh#99 r=2 (PR 108): the operator's
// account of a crashed turn says what each step's outcome was.

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/macula-io/macula-lazymesh/internal/mcpclient"
	"github.com/macula-io/macula-lazymesh/internal/provider"
	"github.com/macula-io/macula-lazymesh/internal/toolchain"
)

// compactThenSayThenCrash answers a history compaction with a summary,
// the turn's prompt with a mesh_say, and crashes once that mesh_say's
// result is in the request: whatever compaction a large restored log
// needs first, the mesh_say has run before the crash.
type compactThenSayThenCrash struct{}

func (compactThenSayThenCrash) ChatCompletion(_ context.Context, req provider.ChatRequest) (provider.ChatResponse, error) {
	for _, m := range req.Messages {
		if m.Role == provider.RoleTool && m.ToolCallID == "c1" {
			panic("provider exploded after mesh_say")
		}
	}
	last := req.Messages[len(req.Messages)-1]
	if last.Role == provider.RoleUser && last.Content == "tell the room hello" {
		return provider.ChatResponse{Message: provider.Message{Role: provider.RoleAssistant,
			ToolCalls: []provider.ToolCall{{ID: "c1", Name: "mesh_say", Arguments: `{"text":"hello room"}`}}}}, nil
	}
	return provider.ChatResponse{Message: provider.Message{Role: provider.RoleAssistant, Content: "an earlier long message"}}, nil
}

func (compactThenSayThenCrash) ContextWindow() int { return 1000 }

// deniedTools refuses mesh_say the way the approval gate does.
type deniedTools struct{}

func (deniedTools) ListTools(context.Context) ([]mcpclient.Tool, error) { return nil, nil }
func (deniedTools) CallToolRaw(context.Context, string, string) (string, error) {
	return "", errors.New(`approval for "mesh_say" was denied by the operator`)
}

// An operator who denied mesh_say is told it was refused, not that it ran.
func TestProbe99_DeniedStepIsNotReportedAsRan(t *testing.T) {
	n := testNode(t)
	root := testRoot(t, n)
	pid := crashedReplacement(t, n, root, SessionArgs{
		Provider: &sayThenCrashProvider{}, Toolchain: toolchain.Policy{Base: deniedTools{}, Allowlist: []string{"mesh_say"}}, SystemPrompt: "test",
		Store: testStore(t), SessionID: "s1",
	})
	report, err := TakeCrashReport(n, pid)
	if err != nil {
		t.Fatalf("crash report: %v", err)
	}
	notice := report.Notice()
	if len(report.Steps) != 1 || !report.Steps[0].Failed {
		t.Fatalf("a denied mesh_say is not marked failed: %+v", report.Steps)
	}
	if !strings.Contains(notice, "did not complete") || !strings.Contains(notice, "denied") || strings.Contains(notice, "it ran") {
		t.Fatalf("a denied mesh_say is reported as a step that ran:\n%s", notice)
	}
}

// A log holding a line over the old 1 MB reader cap loads, so the crashed
// turn's mesh_say is persisted and reported.
func TestProbe99_UnreadableLogIsNotReportedAsNothingRan(t *testing.T) {
	n := testNode(t)
	root := testRoot(t, n)
	store := testStore(t)
	big := `{"schema":2}` + "\n" + `{"role":"user","content":"` + strings.Repeat("x", 1100*1024) + `"}` + "\n" + `{"turn":"ended"}` + "\n"
	if err := os.WriteFile(store.Path("s1"), []byte(big), 0o600); err != nil {
		t.Fatal(err)
	}
	pid := crashedReplacement(t, n, root, SessionArgs{
		Provider: compactThenSayThenCrash{}, Toolchain: fakeToolchain(), SystemPrompt: "test",
		Store: store, SessionID: "s1",
	})
	report, err := TakeCrashReport(n, pid)
	if err != nil {
		t.Fatalf("crash report: %v", err)
	}
	if report.Logged && len(report.Steps) == 0 {
		t.Fatalf("mesh_say ran, but the operator is told: %s", report.Notice())
	}
}

// A log the session cannot read keeps nothing, so the replacement says it
// does not know which steps ran, never that none did.
func TestCrashReport_AnUnreadableLogIsReportedAsNoLog(t *testing.T) {
	n := testNode(t)
	root := testRoot(t, n)
	store := testStore(t)
	if err := os.WriteFile(store.Path("s1"), []byte(`{"schema":3}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pid := crashedReplacement(t, n, root, SessionArgs{
		Provider: &sayThenCrashProvider{}, Toolchain: fakeToolchain(), SystemPrompt: "test",
		Store: store, SessionID: "s1",
	})
	report, err := TakeCrashReport(n, pid)
	if err != nil {
		t.Fatalf("crash report: %v", err)
	}
	if report.Logged {
		t.Fatalf("a session whose log could not be read reports one: %s", report.Notice())
	}
}

// The running call's synthetic result tells the next turn's model not to
// repeat it.
func TestCrashedTurn_TheRunningCallIsNotToBeRepeated(t *testing.T) {
	_, closing := crashedTurn([]provider.Message{
		{Role: provider.RoleUser, Content: "go"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "c1", Name: "mesh_say", Arguments: "{}"}}},
	})
	if len(closing) != 1 || !strings.Contains(closing[0].Content, "Do not repeat it") {
		t.Fatalf("closing = %+v", closing)
	}
}

// A crash before the model replied closes the turn with a reply, so the
// next turn does not send two user messages in a row.
func TestCrashedTurn_ACrashBeforeAnyReplyIsClosedByOne(t *testing.T) {
	_, closing := crashedTurn([]provider.Message{{Role: provider.RoleUser, Content: "go"}})
	if len(closing) != 1 || closing[0].Role != provider.RoleAssistant {
		t.Fatalf("closing = %+v, want one assistant message", closing)
	}
}
