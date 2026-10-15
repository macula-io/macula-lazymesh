package frontend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// hookTimeout bounds how long a tool call waits for the hook's answer,
// the same bound an approval question gets: a policy process that stops
// answering must not hang a turn forever.
const hookTimeout = 2 * time.Minute

// errHookLost is what every call gets while the hook is required but held
// by nobody: its controller went away without unhooking, or (with
// pre_tool_hook_required) none has hooked since start.
var errHookLost = errors.New("the pre_tool hook is held by no controller (its controller disconnected without unhook, or none has hooked since start); tool calls are refused until a controller registers the hook")

// PreToolHook is the control socket's pre-tool gate (G8). A controller
// registers it with {"type":"hook","event":"pre_tool"}; from then on every
// tool call the session makes is sent to that controller alone as
// {"type":"pre_tool","id","tool","args"} and runs only when it answers
// {"type":"hook_answer","id","allow":1}. It fails closed: an unanswered
// call is refused at hookTimeout or when the turn is interrupted, and a
// controller that disconnects without {"type":"unhook"} leaves every
// later call refused until a controller registers again. With no hook
// registered (and none lost) calls pass straight through.
//
// The session's tool path calls Decide; the socket server routes
// registration, answers and disconnects here. It is its own object, not
// part of Server, because the session is built before the socket starts.
type PreToolHook struct {
	mu      sync.Mutex
	owner   *connection
	lost    bool
	pending map[string]chan hookAnswer
	seq     atomic.Uint64
}

type hookAnswer struct {
	allow  bool
	reason string
	err    error
}

// NewPreToolHook returns a hook nobody has registered. Not required,
// calls pass until a controller hooks; required, the hook starts
// as if its controller had dropped, so every call is refused until the
// first controller hooks. Required closes the window between start (or a
// restart) and the policy process connecting, in which a headless agent
// would otherwise run its first turns ungated.
func NewPreToolHook(required bool) *PreToolHook {
	return &PreToolHook{pending: make(map[string]chan hookAnswer), lost: required}
}

// Decide is the session's agent.Gate.
func (h *PreToolHook) Decide(ctx context.Context, tool, argumentsJSON string) (bool, string, error) {
	h.mu.Lock()
	if h.lost {
		h.mu.Unlock()
		return false, "", errHookLost
	}
	if h.owner == nil {
		h.mu.Unlock()
		return true, "", nil
	}
	id := fmt.Sprintf("hook-%d", h.seq.Add(1))
	line, err := json.Marshal(map[string]any{"type": "pre_tool", "id": id, "tool": tool, "args": argumentsJSON})
	if err != nil {
		h.mu.Unlock()
		return false, "", fmt.Errorf("encode pre_tool request: %w", err)
	}
	// Sent under the lock: drop (below) runs before the connection's
	// out channel is closed, so the owner's channel is open here.
	select {
	case h.owner.out <- append(line, '\n'):
	default:
		h.mu.Unlock()
		return false, "", errors.New("the pre_tool hook's controller is not reading its requests")
	}
	ch := make(chan hookAnswer, 1)
	h.pending[id] = ch
	h.mu.Unlock()

	defer func() {
		h.mu.Lock()
		delete(h.pending, id)
		h.mu.Unlock()
	}()
	timer := time.NewTimer(hookTimeout)
	defer timer.Stop()
	select {
	case a := <-ch:
		return a.allow, a.reason, a.err
	case <-ctx.Done():
		return false, "", fmt.Errorf("pre_tool hook for %q was interrupted", tool)
	case <-timer.C:
		return false, "", fmt.Errorf("pre_tool hook for %q timed out after %s", tool, hookTimeout)
	}
}

// register makes c the hook's controller. A hook held by another live
// controller is not taken over.
func (h *PreToolHook) register(c *connection) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.owner != nil && h.owner != c {
		return errors.New("the pre_tool hook is already registered by another controller")
	}
	h.owner = c
	h.lost = false
	return nil
}

// unhook releases the hook when c holds it: calls pass again.
func (h *PreToolHook) unhook(c *connection) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.owner != c {
		return errors.New("this controller does not hold the pre_tool hook")
	}
	h.owner = nil
	h.failPending(errors.New("the pre_tool hook was released before it answered"))
	return nil
}

// drop is called when c disconnects, before its out channel closes. If c
// held the hook, the calls in flight are refused and the hook stays
// lost, so later calls are refused too.
func (h *PreToolHook) drop(c *connection) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.owner != c {
		return
	}
	h.owner = nil
	h.lost = true
	h.failPending(errHookLost)
}

// answer delivers c's decision for id; only the hook's controller can
// decide its calls, and an unknown id is ignored.
func (h *PreToolHook) answer(c *connection, id string, allow bool, reason string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.owner != c {
		return
	}
	if ch, ok := h.pending[id]; ok {
		delete(h.pending, id)
		ch <- hookAnswer{allow: allow, reason: reason}
	}
}

// failPending refuses every call waiting for an answer. The caller holds
// the lock.
func (h *PreToolHook) failPending(err error) {
	for id, ch := range h.pending {
		delete(h.pending, id)
		ch <- hookAnswer{err: err}
	}
}
