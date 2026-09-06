// Package roomwaiter is the loop-owned counterpart to macula-mcp's
// mesh_wait_room: the Go agent loop itself blocks in mesh_wait_room, one
// goroutine per joined room, feeding arrivals back into the same
// prompt-channel the TUI's human input already uses -- instead of the
// model calling mesh_say/mesh_wait_room with a long wait itself, which
// ties up the loop's single tool-execution slot for up to an hour (see
// macula-io/macula-lazymesh#10/#13/#14). Prototyped and measured as a
// spike in #14; wired into cmd/lazymesh's actual production path in #15.
package roomwaiter

import (
	"context"
	"encoding/json"
	"sync"
	"time"
)

// Caller is the minimal subset of *mcpclient.Client a Manager needs --
// same pattern as cmd/lazymesh's own goodbyeCaller (#6): this is
// deterministic harness plumbing, not a model tool call, so it bypasses
// agent.ToolSource and the allowlist entirely and talks to the client
// directly. mesh_wait_room is deliberately never added to
// agent.DefaultToolAllowlist -- only this Manager ever calls it.
type Caller interface {
	CallTool(ctx context.Context, name string, args map[string]any) (string, error)
}

// Arrival is one room having something new to check. Deliberately thin:
// the actual content is read via the existing, already-allowlisted
// mesh_read_inbox(room_topic=...), not carried here, so the model's
// normal "something happened, go look" tool-use pattern stays unchanged
// -- only who wakes it up changes, not how it reads what woke it.
type Arrival struct {
	RoomTopic string
}

// waitSeconds is how long each individual mesh_wait_room call asks for --
// deliberately close to the tool's own max (3600) rather than short and
// re-armed frequently: mesh_wait_room's own doc is explicit that one long
// call beats polling. Per-room responsiveness on room-leave/shutdown
// comes from context cancellation, not from shortening this.
const waitSeconds = 3600

// errorBackoff bounds how fast watch retries after a real error (a
// timeout is NOT an error -- mesh_wait_room returns normally with
// timed_out=1 -- so this only guards against a persistent failure
// spinning the goroutine hot). 5s matches cmd/lazymesh's own
// initialBackoff for the main agent retry loop -- reused rather than
// picked independently, so this codebase has one answer to "how fast do
// we retry after an error," not two unexplained ones.
const errorBackoff = 5 * time.Second

// Manager runs one goroutine per joined room, each blocked in a real
// mesh_wait_room call, and reports arrivals on a single channel.
//
// Fairness policy (macula-io/macula-lazymesh#14, Atlas's flagged gap): a
// room already pending -- its last arrival not yet Ack'd by the consumer
// -- is never re-queued. This caps any single room's influence to one
// outstanding "check me" slot, so a chatty room cannot flood the channel
// and starve others purely by message volume. Whatever the consumer's own
// select does among simultaneously-pending rooms (see cmd/lazymesh's
// nextEvent) then shares turns among a bounded, not volume-weighted, set.
type Manager struct {
	client Caller
	host   string

	mu      sync.Mutex
	cancels map[string]context.CancelFunc
	pending map[string]bool

	arrivals chan Arrival
}

// New returns a Manager with no rooms watched yet -- call Sync to start.
// host is passed through to every mesh_wait_room call ("" uses
// macula-mcp's own default station).
func New(client Caller, host string) *Manager {
	return &Manager{
		client:   client,
		host:     host,
		cancels:  make(map[string]context.CancelFunc),
		pending:  make(map[string]bool),
		arrivals: make(chan Arrival, 64),
	}
}

// Arrivals is the channel to select on alongside human input.
func (m *Manager) Arrivals() <-chan Arrival {
	return m.arrivals
}

// Ack clears room's pending flag, letting it be re-queued the next time
// its waiter actually observes a new arrival. Call this once the agent
// loop has processed (or deliberately skipped) an Arrival for room.
func (m *Manager) Ack(room string) {
	m.mu.Lock()
	delete(m.pending, room)
	m.mu.Unlock()
}

// Sync starts a waiter for every room in joined not already watched, and
// stops the waiter for every watched room no longer in joined.
//
// Reactive, not polling (macula-io/macula-lazymesh#14, Vega's flagged
// requirement): callers feed this room lists observed from the agent
// loop's own mesh_rooms/mesh_join_room/mesh_leave_room tool traffic as it
// already flows through runAgent's event stream -- Sync itself never
// starts a timer or polls mesh_rooms on its own initiative.
func (m *Manager) Sync(ctx context.Context, joined []string) {
	want := make(map[string]bool, len(joined))
	for _, r := range joined {
		want[r] = true
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	for room := range want {
		if _, ok := m.cancels[room]; ok {
			continue
		}
		roomCtx, cancel := context.WithCancel(ctx)
		m.cancels[room] = cancel
		go m.watch(roomCtx, room)
	}
	for room, cancel := range m.cancels {
		if !want[room] {
			cancel()
			delete(m.cancels, room)
			delete(m.pending, room)
		}
	}
}

// StopAll cancels every running waiter -- call on shutdown, same teardown
// discipline as #6's sayGoodbye (cancel this before, not after,
// client.Close() tears down the subprocess these calls need).
func (m *Manager) StopAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for room, cancel := range m.cancels {
		cancel()
		delete(m.cancels, room)
	}
}

// Watching reports how many rooms currently have a live waiter -- for
// measurement/logging, not control flow.
func (m *Manager) Watching() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.cancels)
}

func (m *Manager) watch(ctx context.Context, room string) {
	for {
		args := map[string]any{"room_topic": room, "wait_seconds": waitSeconds}
		if m.host != "" {
			args["host"] = m.host
		}
		result, err := m.client.CallTool(ctx, "mesh_wait_room", args)
		if ctx.Err() != nil {
			return // room left, or shutting down -- not a real failure
		}
		if err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(errorBackoff):
			}
			continue
		}
		if arrived(result) {
			m.enqueue(room)
		}
		// Re-arm immediately either way -- wait_seconds above IS the
		// pacing; mesh_wait_room's own doc is explicit that one
		// continuous call, re-armed on return, is what beats polling.
	}
}

func (m *Manager) enqueue(room string) {
	m.mu.Lock()
	if m.pending[room] {
		m.mu.Unlock()
		return
	}
	m.pending[room] = true
	m.mu.Unlock()

	select {
	case m.arrivals <- Arrival{RoomTopic: room}:
	default:
		// 64 distinct rooms already pending at once -- drop this specific
		// arrival rather than block a waiter goroutine forever. Clearing
		// pending here (not leaving it set) matters: this room's own
		// mesh_wait_room goroutine keeps looping regardless and will call
		// enqueue again on its next real arrival -- if pending stayed
		// true, that next call would see "already pending" and silently
		// no-op forever, permanently stopping this room from ever being
		// surfaced again over one unlucky drop. A room this busy losing
		// one prompt-trigger is an acceptable, rare cost; the room going
		// permanently dark is not. 64 concurrent pending rooms is already
		// past what this codebase's own live measurements have exercised
		// (macula-io/macula-lazymesh#14/#15) -- if it becomes a real
		// operating point, a bigger buffer is the more direct fix.
		m.mu.Lock()
		delete(m.pending, room)
		m.mu.Unlock()
	}
}

type waitRoomResult struct {
	TimedOut int `json:"timed_out"`
}

// arrived reports whether a mesh_wait_room result actually contains a new
// envelope, vs. a clean timeout with nothing new -- checked against
// rooms.ts's own WaitRoomResult shape ({reply, timed_out: 0|1}) rather
// than assumed; this codebase's own wire convention is 0/1, never a JSON
// bool, so timed_out is int here to match, not bool.
func arrived(resultJSON string) bool {
	var r waitRoomResult
	if err := json.Unmarshal([]byte(resultJSON), &r); err != nil {
		return false // can't tell -- treat as nothing new, never misfire
	}
	return r.TimedOut == 0
}
