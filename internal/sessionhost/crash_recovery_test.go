package sessionhost

import (
	"context"
	"testing"
	"time"

	"ergo.services/ergo/gen"

	"github.com/macula-io/macula-lazymesh/internal/mcpclient"
	"github.com/macula-io/macula-lazymesh/internal/provider"
	"github.com/macula-io/macula-lazymesh/internal/sessionstore"
)

// sayThenCrashProvider asks for mesh_say once, then panics on the next
// round: the session crashes after a step with an effect on a peer ran.
type sayThenCrashProvider struct{ rounds int }

func (p *sayThenCrashProvider) ChatCompletion(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
	p.rounds++
	if p.rounds == 1 {
		return provider.ChatResponse{Message: provider.Message{Role: provider.RoleAssistant,
			ToolCalls: []provider.ToolCall{{ID: "c1", Name: "mesh_say", Arguments: `{"text":"hello room"}`}}}}, nil
	}
	panic("provider exploded after mesh_say")
}

func (p *sayThenCrashProvider) ContextWindow() int { return 1000 }

// crashingTools panics inside the named tool: the session crashes while
// that step is running.
type crashingTools struct{ crashIn string }

func (crashingTools) ListTools(context.Context) ([]mcpclient.Tool, error) { return nil, nil }

func (c crashingTools) CallToolRaw(_ context.Context, name, _ string) (string, error) {
	if name == c.crashIn {
		panic("tool exploded while running")
	}
	return "sent", nil
}

// crashedReplacement runs one turn that crashes and returns the session
// the supervisor started in its place.
func crashedReplacement(t *testing.T, n gen.Node, root gen.PID, args SessionArgs) gen.PID {
	t.Helper()
	old := startSessionWith(t, n, root, args)
	if _, err := SayTurn(n, root, old, "tell the room hello"); err == nil {
		t.Fatal("the crashing turn was not reported as the session gone")
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		pids, _ := Sessions(n, root)
		for _, pid := range pids {
			if pid != old {
				if _, err := SessionStatus(n, pid); err == nil {
					return pid
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("no replacement session appeared")
	return gen.PID{}
}

func testStore(t *testing.T) *sessionstore.Store {
	t.Helper()
	store, err := sessionstore.New(t.TempDir(), "fp")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	return store
}

// #99: after a crash that followed a mesh_say, the replacement session
// reports that mesh_say ran, with what it said, and the log records the
// crashed turn as ended.
func TestCrashRecovery_ReplacementReportsAStepThatRan(t *testing.T) {
	n := testNode(t)
	root := testRoot(t, n)
	store := testStore(t)
	pid := crashedReplacement(t, n, root, SessionArgs{
		Provider: &sayThenCrashProvider{}, Tools: fakeTools{}, SystemPrompt: "test",
		Store: store, SessionID: "s1",
	})

	report, err := TakeCrashReport(n, pid)
	if err != nil {
		t.Fatalf("crash report: %v", err)
	}
	if !report.Logged || len(report.Steps) != 1 {
		t.Fatalf("report = %+v, want one logged step", report)
	}
	step := report.Steps[0]
	if step.Tool != "mesh_say" || step.Arguments != `{"text":"hello room"}` || !step.Finished {
		t.Fatalf("step = %+v, want a finished mesh_say with its arguments", step)
	}
	if again, _ := TakeCrashReport(n, pid); len(again.Steps) != 0 {
		t.Fatalf("the report was given twice: %+v", again)
	}
	conv, err := store.Load("s1")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(conv.Unfinished) != 0 {
		t.Fatalf("the crashed turn is still unfinished in the log: %+v", conv.Unfinished)
	}
}

// #99: a step that was running when the session crashed is reported as
// one that may or may not have finished, and the conversation gets a
// result for it, so the next request to the model is well formed.
func TestCrashRecovery_AStepThatWasRunningIsReportedAsUnknown(t *testing.T) {
	n := testNode(t)
	root := testRoot(t, n)
	store := testStore(t)
	p := &scriptedProvider{responses: []provider.ChatResponse{
		{Message: provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "c1", Name: "shell_exec", Arguments: `{"command":"make deploy"}`}}}},
	}}
	pid := crashedReplacement(t, n, root, SessionArgs{
		Provider: p, Tools: crashingTools{crashIn: "shell_exec"}, SystemPrompt: "test",
		Store: store, SessionID: "s2",
	})

	report, err := TakeCrashReport(n, pid)
	if err != nil {
		t.Fatalf("crash report: %v", err)
	}
	if len(report.Steps) != 1 || report.Steps[0].Tool != "shell_exec" || report.Steps[0].Finished {
		t.Fatalf("report = %+v, want shell_exec not known to have finished", report)
	}
	conv, err := store.Load("s2")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	last := conv.Messages[len(conv.Messages)-1]
	if last.Role != provider.RoleTool || last.ToolCallID != "c1" {
		t.Fatalf("the interrupted call has no result in the log; last message = %+v", last)
	}
}

// #99: a session with no log cannot know what ran, and says so.
func TestCrashRecovery_WithoutALogTheStepsAreNotKnown(t *testing.T) {
	n := testNode(t)
	root := testRoot(t, n)
	pid := crashedReplacement(t, n, root, SessionArgs{Provider: &sayThenCrashProvider{}, Tools: fakeTools{}, SystemPrompt: "test"})
	report, err := TakeCrashReport(n, pid)
	if err != nil {
		t.Fatalf("crash report: %v", err)
	}
	if report.Logged {
		t.Fatalf("report = %+v, want not logged", report)
	}
}
