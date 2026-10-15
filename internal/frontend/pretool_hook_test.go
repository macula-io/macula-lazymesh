package frontend

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/macula-io/macula-lazymesh/internal/agent"
)

// startHookedServer starts a Server wired to a fresh PreToolHook.
func startHookedServer(t *testing.T) (*PreToolHook, string) {
	t.Helper()
	hook := NewPreToolHook()
	path := filepath.Join(t.TempDir(), "ctrl.sock")
	s, err := Start(Options{
		Path:        path,
		Input:       make(chan string, 8),
		Events:      make(chan agent.Event, 8),
		SessionID:   "test-session",
		Model:       "testmodel/1",
		PreToolHook: hook,
	})
	if err != nil {
		t.Fatalf("start server: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return hook, path
}

type decision struct {
	allow  bool
	reason string
	err    error
}

// decideAsync runs Decide the way the session's tool path does: blocked
// on another goroutine until the hook answers.
func decideAsync(hook *PreToolHook, tool, args string) <-chan decision {
	out := make(chan decision, 1)
	go func() {
		allow, reason, err := hook.Decide(context.Background(), tool, args)
		out <- decision{allow, reason, err}
	}()
	return out
}

func waitDecision(t *testing.T, ch <-chan decision) decision {
	t.Helper()
	select {
	case d := <-ch:
		return d
	case <-time.After(5 * time.Second):
		t.Fatal("Decide never returned")
		return decision{}
	}
}

// register hooks c as the pre_tool gate and reads the ack.
func register(t *testing.T, c *controller) {
	t.Helper()
	c.send(t, `{"type":"hook","event":"pre_tool"}`)
	if line := c.line(t); line["type"] != "hooked" || line["event"] != "pre_tool" {
		t.Fatalf("hook ack = %v", line)
	}
}

func TestPreToolHookAbsentLetsCallsThrough(t *testing.T) {
	hook, _ := startHookedServer(t)
	if allow, _, err := hook.Decide(context.Background(), "mesh_say", "{}"); !allow || err != nil {
		t.Fatalf("no hook registered: allow=%v err=%v", allow, err)
	}
}

// TestPreToolHookAllowAndDeny: the hooked controller receives every call
// with its full arguments and its answer, with reason, decides it.
func TestPreToolHookAllowAndDeny(t *testing.T) {
	hook, path := startHookedServer(t)
	c := dial(t, path)
	c.line(t) // handshake
	register(t, c)

	pending := decideAsync(hook, "mesh_say", `{"text":"hi"}`)
	req := c.line(t)
	if req["type"] != "pre_tool" || req["tool"] != "mesh_say" || req["args"] != `{"text":"hi"}` || req["id"] == "" {
		t.Fatalf("pre_tool request = %v", req)
	}
	c.send(t, `{"type":"hook_answer","id":"`+req["id"].(string)+`","allow":1}`)
	if d := waitDecision(t, pending); !d.allow || d.err != nil {
		t.Fatalf("allowed call decided %+v", d)
	}

	pending = decideAsync(hook, "mesh_join_room", `{}`)
	req = c.line(t)
	c.send(t, `{"type":"hook_answer","id":"`+req["id"].(string)+`","allow":0,"reason":"not today"}`)
	if d := waitDecision(t, pending); d.allow || d.reason != "not today" || d.err != nil {
		t.Fatalf("denied call decided %+v", d)
	}
}

// TestPreToolHookFailsClosedWhenItsControllerDisappears: a hook
// controller that drops without unhook denies the call in flight and
// every later call, until a controller registers again.
func TestPreToolHookFailsClosedWhenItsControllerDisappears(t *testing.T) {
	hook, path := startHookedServer(t)
	c := dial(t, path)
	c.line(t)
	register(t, c)

	pending := decideAsync(hook, "mesh_say", `{}`)
	c.line(t) // the pre_tool request, never answered
	c.conn.Close()
	if d := waitDecision(t, pending); d.allow || d.err == nil {
		t.Fatalf("in-flight call after the hook dropped decided %+v", d)
	}
	if allow, _, err := hook.Decide(context.Background(), "mesh_say", `{}`); allow || err == nil {
		t.Fatalf("call after the hook dropped: allow=%v err=%v", allow, err)
	}

	c2 := dial(t, path)
	c2.line(t)
	register(t, c2)
	pending = decideAsync(hook, "mesh_say", `{}`)
	req := c2.line(t)
	c2.send(t, `{"type":"hook_answer","id":"`+req["id"].(string)+`","allow":1}`)
	if d := waitDecision(t, pending); !d.allow {
		t.Fatalf("re-registered hook decided %+v", d)
	}
}

// TestPreToolHookUnhookReleases: an explicit unhook returns to ungated
// calls; that is the only way to drop the gate without failing closed.
func TestPreToolHookUnhookReleases(t *testing.T) {
	hook, path := startHookedServer(t)
	c := dial(t, path)
	c.line(t)
	register(t, c)
	c.send(t, `{"type":"unhook","event":"pre_tool"}`)
	if line := c.line(t); line["type"] != "unhooked" {
		t.Fatalf("unhook ack = %v", line)
	}
	c.conn.Close()
	time.Sleep(50 * time.Millisecond)
	if allow, _, err := hook.Decide(context.Background(), "mesh_say", "{}"); !allow || err != nil {
		t.Fatalf("after unhook: allow=%v err=%v", allow, err)
	}
}

// TestPreToolHookOnlyItsOwnerDecides: a second controller can neither
// take over a held hook nor answer its requests.
func TestPreToolHookOnlyItsOwnerDecides(t *testing.T) {
	hook, path := startHookedServer(t)
	owner := dial(t, path)
	owner.line(t)
	register(t, owner)

	other := dial(t, path)
	other.line(t)
	other.send(t, `{"type":"hook","event":"pre_tool"}`)
	if line := other.line(t); line["type"] != "error" || !strings.Contains(line["error"].(string), "already") {
		t.Fatalf("second registration = %v", line)
	}

	pending := decideAsync(hook, "mesh_say", `{}`)
	req := owner.line(t)
	other.send(t, `{"type":"hook_answer","id":"`+req["id"].(string)+`","allow":1}`)
	select {
	case d := <-pending:
		t.Fatalf("a non-owner's answer decided the call: %+v", d)
	case <-time.After(200 * time.Millisecond):
	}
	owner.send(t, `{"type":"hook_answer","id":"`+req["id"].(string)+`","allow":0,"reason":"owner says no"}`)
	if d := waitDecision(t, pending); d.allow || d.reason != "owner says no" {
		t.Fatalf("owner's answer decided %+v", d)
	}
}
