// Package ringwaiter is the loop-owned counterpart to a real-time ring
// signal that doesn't exist: unlike rooms, rings have no blocking-wait
// primitive at all (confirmed against macula-mcp's own source --
// mesh_ring.ts/mesh_answer_ring.ts, no wait_seconds anywhere), so this
// can't mirror internal/roomwaiter's exact mechanism (one goroutine
// blocked in a long mesh_wait_room call per room). Instead, one goroutine
// polls mesh_read_inbox on a short, fixed interval -- still zero LLM
// cost, still "deterministic harness plumbing, not a model tool call"
// (same pattern as roomwaiter/cmd/lazymesh's sayGoodbye: bypasses
// agent.ToolSource and the allowlist entirely, talks to the client
// directly), because mesh_read_inbox is documented "Instant, a local
// SQLite read, never blocks" -- a background Go poll of an
// already-cheap local read is a different thing entirely from
// macula-mcp's own mesh://etiquette warning against a MODEL sleeping and
// re-polling in its own reasoning turn; the model never touches this
// until there's something real to look at.
//
// 2026-09-07: replaces the system prompt's own former "every single
// time you are prompted, call mesh_read_inbox with no room_topic and
// check rings.pending" mandate, which ran on every single cycle
// regardless of trigger and was the direct cause of a real runaway-
// context incident (macula-comm-docs and this repo's own history).
package ringwaiter

import (
	"context"
	"encoding/json"
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

// PollInterval is how often the background goroutine checks for a new
// pending ring. Deliberately short: mesh_read_inbox is a local SQLite
// read with no external rate limit to respect, and this never reaches
// the model, so the usual "don't check too often" cost (a wasted LLM
// turn) simply doesn't apply here -- the only real cost is a small
// amount of local CPU/IPC, and 5s buys near-real-time ring responsiveness
// for that price. var, not const, so a test can shorten it (same reason
// as roomwaiter.errorBackoff) -- never reassigned outside a test.
var PollInterval = 5 * time.Second

// Manager runs one background goroutine polling for pending rings and
// reports each one, once, on a single channel.
//
// Surfaced-ring tracking (Fable's flagged failure mode, 2026-09-07): a
// ring already surfaced is never re-queued, keyed on its own ring_id --
// not a bare "anything pending" check -- so a ring the model declines to
// act on (or the human answers via the TUI's own ring pop-up first, see
// internal/tui/ringpopup.go) does not re-wake the model forever on every
// poll. Mirrors roomwaiter's own pending-map dedup shape, and the TUI's
// own seenRingIDs field, both solving the identical problem.
type Manager struct {
	client Caller
	host   string

	mu       sync.Mutex
	surfaced map[string]bool
	cancel   context.CancelFunc

	arrivals chan Ring
}

// New returns a Manager that polls nothing yet -- call Start to begin.
// host is passed through to every mesh_read_inbox call ("" uses
// macula-mcp's own default station).
func New(client Caller, host string) *Manager {
	return &Manager{
		client:   client,
		host:     host,
		surfaced: make(map[string]bool),
		arrivals: make(chan Ring, 16),
	}
}

// Arrivals is the channel to select on alongside roomwaiter's own and
// human input.
func (m *Manager) Arrivals() <-chan Ring {
	return m.arrivals
}

// Start begins polling in the background. Safe to call more than once --
// a second call while already running is a no-op, same idempotence as
// roomwaiter.Manager.Sync re-syncing an unchanged room set.
func (m *Manager) Start(ctx context.Context) {
	m.mu.Lock()
	if m.cancel != nil {
		m.mu.Unlock()
		return
	}
	pollCtx, cancel := context.WithCancel(ctx)
	m.cancel = cancel
	m.mu.Unlock()
	go m.poll(pollCtx)
}

// Stop cancels the background poll -- call on shutdown, same teardown
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

// Polling reports whether the background poll is currently running --
// for measurement/logging, not control flow.
func (m *Manager) Polling() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cancel != nil
}

func (m *Manager) poll(ctx context.Context) {
	ticker := time.NewTicker(PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.checkOnce(ctx)
		}
	}
}

func (m *Manager) checkOnce(ctx context.Context) {
	args := map[string]any{"limit": 1} // rings ignore limit entirely (mesh_read_inbox.ts); this only shrinks the incidental rooms/central payload
	if m.host != "" {
		args["host"] = m.host
	}
	result, err := m.client.CallTool(ctx, "mesh_read_inbox", args)
	if err != nil {
		return // best-effort -- the next tick tries again, nothing to back off from (see package doc)
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
		// (mesh_read_inbox will show it again on the very next poll).
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
// package's own "never misfire, worst case a missed poll tries again in
// PollInterval" posture.
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
