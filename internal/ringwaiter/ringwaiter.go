// Package ringwaiter is the loop-owned counterpart to macula-mcp's
// mesh_wait_ring: the Go agent loop itself blocks in mesh_wait_ring, one
// goroutine for the whole process (rings aren't scoped per-room the way
// rooms are -- there is exactly one ring endpoint per agent identity),
// feeding arrivals back into the same prompt-channel the TUI's human
// input and internal/roomwaiter's own arrivals already use. Same shape
// and same reasoning as roomwaiter's own relationship to mesh_wait_room:
// "deterministic harness plumbing, bypasses the model" (see
// cmd/lazymesh's sayGoodbye for the same pattern again), talking to the
// client directly rather than through agent.ToolSource/the allowlist.
//
// 2026-09-07: replaced the system prompt's own former "every single time
// you are prompted, call mesh_read_inbox with no room_topic and check
// rings.pending" mandate, which ran on every single cycle regardless of
// trigger and was the direct cause of a real runaway-context incident
// (macula-comm-docs and this repo's own history) -- with a Go-side
// polling loop (mesh_read_inbox on a short fixed interval), since
// mesh_wait_ring did not exist yet at the time.
//
// 2026-09-08: switched from that polling loop to a real blocking
// mesh_wait_ring call (macula-mcp 0.26.0, github-com-9a) -- the tool
// this package always wanted but didn't have when R1 was built. Same
// day, investigating why a real ring never surfaced on the recipient's
// side of two same-machine lazymesh instances, this package's own
// mesh_read_inbox-based checkOnce (kept below as the error-backoff/
// startup catch-up path) helped prove the actual bug was upstream, in
// macula-mcp's own rings.sqlite3 schema (ring_id alone as primary key
// couldn't hold both a ring's caller-side and callee-side bookkeeping
// rows in the one shared per-machine file) -- fixed in macula-mcp 0.26.1
// (see internal/config's MaculaMCPVersion doc comment for the full
// account, and for why this codebase no longer pins to a fixed release
// at all). Polling was never the problem; there was nothing correct to
// poll OR wait for on the recipient's own side until that fix landed.
package ringwaiter

import (
	"context"
	"encoding/json"
	"log"
	"sync"
	"time"
)

// Caller is the minimal subset of *mcpclient.Client a Manager needs --
// same shape as roomwaiter.Caller, for the same reason: this is
// deterministic harness plumbing, not a model tool call.
type Caller interface {
	CallTool(ctx context.Context, name string, args map[string]any) (string, error)
}

// Ring is one pending ring worth waking the model for. Unlike
// roomwaiter.Arrival (deliberately thin, content read separately via
// mesh_read_inbox), this carries enough for the model to act on
// immediately without a second round trip -- there is no cheaper,
// ring-scoped tool call to make afterward the way roomArrivalPrompt's
// mesh_read_inbox(room_topic=...) is for a room.
type Ring struct {
	RingID      string
	Purpose     string
	FromPetname string
}

// waitSeconds is how long each individual mesh_wait_ring call asks for --
// same reasoning and same value as roomwaiter's own waitSeconds: close
// to the tool's own max (3600) rather than short and re-armed
// frequently, since mesh_wait_ring's own doc is explicit that one long
// call beats polling. Responsiveness on shutdown comes from context
// cancellation, not from shortening this.
const waitSeconds = 3600

// errorBackoff bounds how fast watch retries after a real error (a
// timeout is NOT an error -- mesh_wait_ring returns normally with
// timed_out=1 -- so this only guards against a persistent failure
// spinning the goroutine hot). Same value and same reasoning as
// roomwaiter's own errorBackoff -- reused rather than picked
// independently, so this codebase has one answer to "how fast do we
// retry after an error," not two unexplained ones. var, not const, so a
// test can shorten it -- never reassigned outside a test.
var errorBackoff = 5 * time.Second

// Manager runs one background goroutine blocked in mesh_wait_ring and
// reports each still-pending ring, once, on a single channel.
//
// Surfaced-ring tracking (Fable's flagged failure mode, 2026-09-07): a
// ring already surfaced is never re-queued, keyed on its own ring_id --
// not a bare "anything pending" check -- so a ring the model declines to
// act on (or the human answers via the TUI's own ring pop-up first, see
// internal/tui/ringpopup.go) does not re-wake the model forever.
// Mirrors roomwaiter's own pending-map dedup shape, and the TUI's own
// seenRingIDs field, both solving the identical problem.
type Manager struct {
	client Caller
	host   string

	mu       sync.Mutex
	surfaced map[string]bool
	cancel   context.CancelFunc
	logger   *log.Logger

	arrivals chan Ring
}

// New returns a Manager that watches nothing yet -- call Start to begin.
// host is passed through to every mesh_wait_ring/mesh_read_inbox call
// ("" uses macula-mcp's own default station).
func New(client Caller, host string) *Manager {
	return &Manager{
		client:   client,
		host:     host,
		surfaced: make(map[string]bool),
		arrivals: make(chan Ring, 16),
	}
}

// SetLogger sets where Start's own confirmation line and a failed
// mesh_wait_ring/mesh_read_inbox call get logged -- same optional-setter
// shape as meshservices.Source.SetLogger (nil, the default, means
// silence; every existing test's own New() call site is unaffected).
// Added 2026-09-08 after a real live incident was hard to diagnose from
// agent.log alone: a failed background call used to be swallowed
// completely silently ("best-effort, the next attempt tries again"),
// which is the right RECOVERY behavior but left no trace at all if the
// call kept failing -- indistinguishable, from the log alone, between
// "ringwaiter is quietly healthy on an idle mesh" (R1's own stated goal:
// a quiet mesh produces zero output) and "ringwaiter's watch has been
// failing since startup." Also logs once when watching actually starts,
// for the same reason: there was no way to confirm from agent.log alone
// that Start(ctx) had even been called successfully, versus not running
// at all.
func (m *Manager) SetLogger(l *log.Logger) {
	m.mu.Lock()
	m.logger = l
	m.mu.Unlock()
}

func (m *Manager) log(format string, args ...any) {
	m.mu.Lock()
	logger := m.logger
	m.mu.Unlock()
	if logger != nil {
		logger.Printf(format, args...)
	}
}

// Arrivals is the channel to select on alongside roomwaiter's own and
// human input.
func (m *Manager) Arrivals() <-chan Ring {
	return m.arrivals
}

// Start begins watching in the background. Safe to call more than once --
// a second call while already running is a no-op, same idempotence as
// roomwaiter.Manager.Sync re-syncing an unchanged room set.
func (m *Manager) Start(ctx context.Context) {
	m.mu.Lock()
	if m.cancel != nil {
		m.mu.Unlock()
		return
	}
	watchCtx, cancel := context.WithCancel(ctx)
	m.cancel = cancel
	m.mu.Unlock()
	m.log("[ringwaiter] watching started")
	go m.watch(watchCtx)
}

// Stop cancels the background watch -- call on shutdown, same teardown
// discipline as roomwaiter.Manager.StopAll (before, not after,
// client.Close() tears down the subprocess these calls need).
func (m *Manager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
}

// Watching reports whether the background watch is currently running --
// for measurement/logging, not control flow.
func (m *Manager) Watching() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cancel != nil
}

// watch calls checkOnce once up front -- catching anything already
// pending from before this process (or this Manager) started watching,
// since mesh_wait_ring's own cursor only ever reports the NEXT ring
// after a given call starts, the same "afterId computed fresh at call
// time" shape as mesh_wait_room (rooms.ts) -- then blocks in
// mesh_wait_ring, re-arming immediately on every return (a timeout or a
// real ring), the same continuous-call shape roomwaiter.watch already
// uses for rooms.
func (m *Manager) watch(ctx context.Context) {
	m.checkOnce(ctx)
	for {
		args := map[string]any{"wait_seconds": waitSeconds}
		if m.host != "" {
			args["host"] = m.host
		}
		result, err := m.client.CallTool(ctx, "mesh_wait_ring", args)
		if ctx.Err() != nil {
			return // shutting down -- not a real failure
		}
		if err != nil {
			m.log("[ringwaiter] mesh_wait_ring failed, backing off %s: %v", errorBackoff, err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(errorBackoff):
			}
			// Force a catch-up check after the backoff pause, same
			// reasoning as roomwaiter's own post-backoff enqueue
			// (macula-io/macula-lazymesh#15): the next mesh_wait_ring
			// call computes its own cursor fresh, from THAT call's own
			// start -- a ring recorded during this exact sleep would
			// otherwise be invisible to it permanently, not just
			// delayed. checkOnce (mesh_read_inbox) is a direct read, not
			// cursor-based, so it always sees whatever is genuinely
			// pending right now regardless of when it arrived.
			m.checkOnce(ctx)
			continue
		}
		if r, ok := parseWaitRingResult(result); ok {
			m.enqueue(r)
		}
		// Re-arm immediately either way -- wait_seconds above IS the
		// pacing, same as roomwaiter's own mesh_wait_room loop.
	}
}

// checkOnce is a direct, cursor-free read of whatever is genuinely
// pending right now (mesh_read_inbox's own rings.pending, already
// filtered to answer IS NULL by macula-mcp itself -- see
// pendingIncoming in rings.ts) -- used for the one-time startup catch-up
// in watch, and again after any error+backoff pause, so a ring recorded
// while mesh_wait_ring's own connection was down or backing off is never
// permanently missed.
func (m *Manager) checkOnce(ctx context.Context) {
	args := map[string]any{"limit": 1} // rings ignore limit entirely (mesh_read_inbox.ts); this only shrinks the incidental rooms/central payload
	if m.host != "" {
		args["host"] = m.host
	}
	result, err := m.client.CallTool(ctx, "mesh_read_inbox", args)
	if err != nil {
		m.log("[ringwaiter] catch-up mesh_read_inbox failed: %v", err)
		return // best-effort -- the next mesh_wait_ring call still tries
	}
	for _, r := range parsePendingRings(result) {
		m.enqueue(r)
	}
}

func (m *Manager) enqueue(r Ring) {
	m.mu.Lock()
	if m.surfaced[r.RingID] {
		m.mu.Unlock()
		return
	}
	m.surfaced[r.RingID] = true
	m.mu.Unlock()

	select {
	case m.arrivals <- r:
	default:
		// Buffer full -- drop and un-surface, same reasoning as
		// roomwaiter.Manager.enqueue's own drop branch: leaving surfaced
		// set here would permanently stop this ring from ever being
		// re-offered over one unlucky drop, and nothing is actually lost
		// -- the ring stays genuinely pending mesh-side regardless
		// (the next mesh_wait_ring/checkOnce will show it again).
		m.mu.Lock()
		delete(m.surfaced, r.RingID)
		m.mu.Unlock()
	}
}

type readInboxRings struct {
	Rings *struct {
		Pending []struct {
			RingID      string `json:"ring_id"`
			Purpose     string `json:"purpose"`
			PeerPetname string `json:"peer_petname"`
		} `json:"pending"`
	} `json:"rings"`
}

// parsePendingRings extracts mesh_read_inbox's own rings.pending array --
// nil (never an error) on anything unparseable or absent, matching this
// package's own "never misfire" posture: checkOnce is a best-effort
// catch-up, not the only path a ring can be found through.
func parsePendingRings(resultJSON string) []Ring {
	var parsed readInboxRings
	if err := json.Unmarshal([]byte(resultJSON), &parsed); err != nil || parsed.Rings == nil {
		return nil
	}
	out := make([]Ring, 0, len(parsed.Rings.Pending))
	for _, p := range parsed.Rings.Pending {
		out = append(out, Ring{RingID: p.RingID, Purpose: p.Purpose, FromPetname: p.PeerPetname})
	}
	return out
}

type waitRingResult struct {
	TimedOut int `json:"timed_out"`
	Ring     *struct {
		RingID      string `json:"ring_id"`
		Purpose     string `json:"purpose"`
		PeerPetname string `json:"peer_petname"`
		// Pointer, not a bare int: mesh_wait_ring returns EVERY incoming
		// ring, not only ones still awaiting an answer (open/closed/
		// allowlist policies resolve theirs immediately, "ask" leaves
		// answer null -- see mesh_wait_ring.ts's own doc). nil means
		// still pending, still needs mesh_answer_ring -- exactly what
		// ringArrivalPrompt (cmd/lazymesh) tells the model to do with an
		// arrival. A non-nil answer means this ring was already resolved
		// without the model, so it is deliberately NOT surfaced here as
		// something to answer (see parseWaitRingResult).
		Answer *int `json:"answer"`
	} `json:"ring"`
}

// parseWaitRingResult extracts the next-arrived ring from a mesh_wait_ring
// result, if any -- false on a clean timeout, an unparseable result
// (never misfire), or a ring that already has an answer (already
// resolved by open/closed/allowlist policy, nothing for the model to do;
// see waitRingResult.Ring.Answer's own doc comment).
func parseWaitRingResult(resultJSON string) (Ring, bool) {
	var parsed waitRingResult
	if err := json.Unmarshal([]byte(resultJSON), &parsed); err != nil || parsed.Ring == nil {
		return Ring{}, false
	}
	if parsed.Ring.Answer != nil {
		return Ring{}, false
	}
	return Ring{RingID: parsed.Ring.RingID, Purpose: parsed.Ring.Purpose, FromPetname: parsed.Ring.PeerPetname}, true
}
