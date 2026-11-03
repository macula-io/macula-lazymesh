package frontend

import (
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/macula-io/macula-lazymesh/internal/agent"
)

// tempSocket is a socket path inside this test's own temporary directory.
func tempSocket(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "ctrl.sock")
}

// startControllerServer starts a Server whose approvals are answered by a
// function the test supplies, so a test can decide what "the session"
// says about an answer.
func startControllerServer(t *testing.T, input chan string, events chan agent.Event, approve func(id string, allow bool) bool) (*Server, string) {
	t.Helper()
	path := tempSocket(t)
	s, err := Start(Options{
		Path:      path,
		Input:     input,
		Events:    events,
		Approve:   approve,
		SessionID: "test-session",
		Model:     "testmodel/1",
	})
	if err != nil {
		t.Fatalf("start server: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

// TestControllerObservesSessionEvents is the core of attach: a controller
// dialling the socket sees the session's own event stream, so a TUI
// driven over the wire renders the same conversation the in-process one
// does.
func TestControllerObservesSessionEvents(t *testing.T) {
	events := make(chan agent.Event, 8)
	input := make(chan string, 4)
	_, path := startControllerServer(t, input, events, nil)

	c, err := Dial(path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	if got := c.Session().SessionID; got != "test-session" {
		t.Fatalf("session id = %q, want test-session", got)
	}

	events <- agent.Event{Kind: agent.EventAssistantMessage, Text: "hello from the session"}

	select {
	case ev := <-c.Events():
		if ev.Kind != agent.EventAssistantMessage || ev.Text != "hello from the session" {
			t.Fatalf("event = %+v, want the assistant message", ev)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("controller received no event")
	}
}

// TestControllerSaysAndInterrupts proves the attached TUI can drive the
// session: composed input reaches the session's input channel and an
// interrupt reaches the interrupt hook.
func TestControllerSaysAndInterrupts(t *testing.T) {
	events := make(chan agent.Event, 8)
	input := make(chan string, 4)
	interrupted := make(chan struct{}, 1)
	path := tempSocket(t)
	s, err := Start(Options{
		Path:      path,
		Input:     input,
		Events:    events,
		Interrupt: func() { interrupted <- struct{}{} },
		SessionID: "s",
	})
	if err != nil {
		t.Fatalf("start server: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	c, err := Dial(path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	if err := c.Say("do the thing"); err != nil {
		t.Fatalf("say: %v", err)
	}
	select {
	case got := <-input:
		if got != "do the thing" {
			t.Fatalf("input = %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("input never reached the session")
	}

	if err := c.Interrupt(); err != nil {
		t.Fatalf("interrupt: %v", err)
	}
	select {
	case <-interrupted:
	case <-time.After(5 * time.Second):
		t.Fatal("interrupt never reached the session")
	}
}

// TestDetachLeavesTheSessionRunning is the purpose of #40: closing the
// attached frontend must not stop the session, and attaching again must
// work on the same socket.
func TestDetachLeavesTheSessionRunning(t *testing.T) {
	events := make(chan agent.Event, 8)
	input := make(chan string, 4)
	_, path := startControllerServer(t, input, events, nil)

	first, err := Dial(path)
	if err != nil {
		t.Fatalf("first dial: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("detach: %v", err)
	}

	second, err := Dial(path)
	if err != nil {
		t.Fatalf("reattach after detach failed: %v", err)
	}
	t.Cleanup(func() { second.Close() })

	events <- agent.Event{Kind: agent.EventAssistantMessage, Text: "still here"}
	select {
	case ev := <-second.Events():
		if ev.Text != "still here" {
			t.Fatalf("event = %+v", ev)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the session stopped serving after its frontend detached")
	}
}

// TestFirstApprovalWinsAndTheLoserIsTold settles #40's threat model on the
// wire: one approval is answered once, the first answer wins, and the
// frontend whose answer lost is told so explicitly rather than being left
// to believe it decided the call.
func TestFirstApprovalWinsAndTheLoserIsTold(t *testing.T) {
	events := make(chan agent.Event, 8)
	input := make(chan string, 4)

	// The session side: exactly one answer can be delivered for an id.
	// Each frontend answers on its own connection goroutine, so the map
	// needs its own lock.
	delivered := make(chan bool, 4)
	answeredOnce := make(map[string]bool)
	var onceMu sync.Mutex
	_, path := startControllerServer(t, input, events, func(id string, allow bool) bool {
		onceMu.Lock()
		defer onceMu.Unlock()
		if answeredOnce[id] {
			return false
		}
		answeredOnce[id] = true
		delivered <- allow
		return true
	})

	bob, err := Dial(path)
	if err != nil {
		t.Fatalf("dial bob: %v", err)
	}
	t.Cleanup(func() { bob.Close() })
	carol, err := Dial(path)
	if err != nil {
		t.Fatalf("dial carol: %v", err)
	}
	t.Cleanup(func() { carol.Close() })

	// The session asks, every attached frontend hears it.
	events <- agent.Event{Kind: agent.EventApprovalRequested, ID: "approve-1", ToolName: "shell_exec", Text: `{"cmd":"ls"}`}
	for _, c := range []*Controller{bob, carol} {
		select {
		case ev := <-c.Events():
			if ev.Kind != agent.EventApprovalRequested || ev.ID != "approve-1" {
				t.Fatalf("frontend got %+v, want the approval request", ev)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("a frontend never saw the approval request")
		}
	}

	if err := bob.Approve("approve-1", true); err != nil {
		t.Fatalf("bob's answer should have been the one delivered, got %v", err)
	}
	err = carol.Approve("approve-1", false)
	if err == nil {
		t.Fatal("carol's answer was accepted as if it decided the call; the threat model says the loser must be told")
	}
	if !strings.Contains(err.Error(), "already answered") {
		t.Fatalf("carol's refusal = %q, want it to say the approval was already answered", err)
	}

	select {
	case allow := <-delivered:
		if !allow {
			t.Fatalf("the delivered answer was deny; bob's allow should have won")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no answer reached the session")
	}

	// Every frontend is told the approval was resolved, so a popup that
	// lost the race closes itself instead of waiting out the timeout.
	for _, c := range []*Controller{bob, carol} {
		select {
		case r := <-c.Approvals():
			if r.ID != "approve-1" || r.By == "" {
				t.Fatalf("resolution = %+v, want the id and who answered", r)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("a frontend was never told the approval was resolved")
		}
	}
}

// TestResolutionBroadcastIsNotAVerdict is the regression test for a race
// this package had while #40 was being built: the approval_resolved
// broadcast reached every frontend, and a Controller with an Approve
// call waiting read it as "your answer won". Two frontends answering at
// the same moment could both be told they decided the call. The verdict
// must travel on its own message (approval_answer), so a frontend that
// lost still loses even when the winner's resolution arrives mid-call.
func TestResolutionBroadcastIsNotAVerdict(t *testing.T) {
	events := make(chan agent.Event, 8)
	input := make(chan string, 4)
	_, path := startControllerServer(t, input, events, func(id string, allow bool) bool {
		return allow // the first allow wins; a later deny does not
	})

	// The winner answers from a second controller while the loser's
	// Approve is in flight.
	winner, err := Dial(path)
	if err != nil {
		t.Fatalf("dial winner: %v", err)
	}
	t.Cleanup(func() { winner.Close() })
	loser, err := Dial(path)
	if err != nil {
		t.Fatalf("dial loser: %v", err)
	}
	t.Cleanup(func() { loser.Close() })

	if err := winner.Approve("approve-race", true); err != nil {
		t.Fatalf("winner's answer refused: %v", err)
	}
	// Whether this races ahead of the broadcast or arrives after it, the
	// answer is the same: it lost, and it must say so.
	if err := loser.Approve("approve-race", false); err == nil {
		t.Fatal("the loser was told its answer won; the resolution broadcast was read as its own verdict")
	}
}

// TestLateApprovalIsRefusedNotSilentlyDropped: today AnswerApproval drops
// a second answer with a bare select/default and says nothing, so a human
// can press deny, see nothing, and believe they decided a call that
// already ran. The second answer must come back as an error.
func TestLateApprovalIsRefusedNotSilentlyDropped(t *testing.T) {
	events := make(chan agent.Event, 8)
	input := make(chan string, 4)
	_, path := startControllerServer(t, input, events, func(id string, allow bool) bool {
		return id == "approve-9" && allow
	})

	c, err := Dial(path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	if err := c.Approve("approve-9", true); err != nil {
		t.Fatalf("first answer refused: %v", err)
	}
	if err := c.Approve("approve-9", false); err == nil {
		t.Fatal("a second answer for the same id returned success; it must be refused explicitly")
	}
}

// TestEveryEventSurvivesTheRoundTrip is the mechanical check R1 asked for:
// every event kind the server can encode is decoded back by the client and
// compared field by field. The bug it exists for was a field-name mismatch
// that no other test could see — the server wrote "args", the client read
// "text", so an attached TUI showed every approval with empty arguments and
// an `rm -rf /` reached the operator as "". A new event kind, or a renamed
// field on either side, fails here rather than in someone's terminal.
func TestEveryEventSurvivesTheRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		in   agent.Event
		want agent.Event
	}{
		{"assistant", agent.Event{Kind: agent.EventAssistantMessage, Text: "done"}, agent.Event{Kind: agent.EventAssistantMessage, Text: "done"}},
		{"delta", agent.Event{Kind: agent.EventAssistantDelta, Text: "par"}, agent.Event{Kind: agent.EventAssistantDelta, Text: "par"}},
		{"tool_call", agent.Event{Kind: agent.EventToolCall, ToolName: "shell_exec", Text: `{"cmd":"rm -rf /"}`}, agent.Event{Kind: agent.EventToolCall, ToolName: "shell_exec", Text: `{"cmd":"rm -rf /"}`}},
		{"tool_result", agent.Event{Kind: agent.EventToolResult, ToolName: "shell_exec", Text: "ok"}, agent.Event{Kind: agent.EventToolResult, ToolName: "shell_exec", Text: "ok"}},
		{"backoff", agent.Event{Kind: agent.EventBackoff}, agent.Event{Kind: agent.EventBackoff}},
		{"max_failures", agent.Event{Kind: agent.EventMaxFailuresReached}, agent.Event{Kind: agent.EventMaxFailuresReached}},
		{"approval_request", agent.Event{Kind: agent.EventApprovalRequested, ID: "approve-1", ToolName: "shell_exec", Text: `{"cmd":"rm -rf /"}`}, agent.Event{Kind: agent.EventApprovalRequested, ID: "approve-1", ToolName: "shell_exec", Text: `{"cmd":"rm -rf /"}`}},
		{"turn_complete", agent.Event{Kind: agent.EventListening}, agent.Event{Kind: agent.EventListening}},
		{"ring_pending", agent.Event{Kind: agent.EventRingPending, ID: "ring-1", From: "pluto", Text: "review this"}, agent.Event{Kind: agent.EventRingPending, ID: "ring-1", From: "pluto", Text: "review this"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			line, err := marshalEvent(tc.in)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			got, ok := parseEvent(line)
			if !ok {
				t.Fatalf("parseEvent rejected %s", line)
			}
			if got.Kind != tc.want.Kind || got.Text != tc.want.Text || got.ToolName != tc.want.ToolName || got.ID != tc.want.ID || got.From != tc.want.From {
				t.Fatalf("round trip lost data:\n  encoded %s\n  got     %+v\n  want    %+v", line, got, tc.want)
			}
		})
	}

	// EventError carries an error, not a string field.
	line, err := marshalEvent(agent.Event{Kind: agent.EventError, ToolName: "shell_exec", Err: errors.New("boom")})
	if err != nil {
		t.Fatalf("marshal error event: %v", err)
	}
	got, ok := parseEvent(line)
	if !ok || got.Kind != agent.EventError || got.ToolName != "shell_exec" || got.Err == nil || got.Err.Error() != "boom" {
		t.Fatalf("error event round trip = %+v (ok=%v)", got, ok)
	}
}

// TestTheLocalTUIHearsAnAttachedFrontendAnswer is R3: the session's own
// in-process TUI is not one of the server's connections, so the broadcast
// cannot reach it. Without Resolutions() a popup on the local TUI waits
// out the full approval timeout after an attached frontend has already
// decided the call.
func TestTheLocalTUIHearsAnAttachedFrontendAnswer(t *testing.T) {
	events := make(chan agent.Event, 8)
	input := make(chan string, 4)
	s, path := startControllerServer(t, input, events, func(id string, allow bool) bool { return true })

	c, err := Dial(path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	if err := c.Approve("approve-1", true); err != nil {
		t.Fatalf("approve: %v", err)
	}

	select {
	case r := <-s.Resolutions():
		if r.ID != "approve-1" {
			t.Fatalf("resolution = %+v", r)
		}
		if r.By == "" {
			t.Fatal("the local TUI was not told which frontend answered")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the session's own TUI was never told the approval was answered")
	}
}

// The handshake carries the session's leader as well as its identity (#63):
// an attached TUI arms the key the session answers, not the one the attaching
// side's own config happens to hold.
func TestControllerLearnsTheSessionsLeader(t *testing.T) {
	path := tempSocket(t)
	s, err := Start(Options{
		Path:      path,
		Input:     make(chan string, 4),
		Events:    make(chan agent.Event, 4),
		SessionID: "leader-session",
		Model:     "testmodel/1",
		Leader:    "ctrl+g",
	})
	if err != nil {
		t.Fatalf("start server: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	c, err := Dial(path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	if got := c.Session().Leader; got != "ctrl+g" {
		t.Fatalf("the controller read leader %q, want ctrl+g", got)
	}
}
