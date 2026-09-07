// Package meshservices is lazymesh's Phase 3 tool source: real mesh RPC
// procedures, discovered dynamically via macula-mcp's own
// mesh_find_records_by_type, exposed as synthetic tools instead of
// hardcoded local integrations -- dogfooding the mesh's own service
// directory as the preferred capability source. See Curated in catalog.go
// for the specific procedures and why each one is trusted to expose.
//
// This never gives the model a generic "call any mesh procedure" tool --
// only individually named, curated, currently-discovered procedures ever
// become tools. A generic passthrough would reopen exactly the
// mesh-content-to-arbitrary-call risk internal/agent's AllowlistSource
// exists to close off.
package meshservices

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/macula-io/macula-lazymesh/internal/mcpclient"
)

// pinnedRealmName/pinnedRealm are the ONLY realm this catalog's procedures
// are ever trusted from. Fixed by an adversarial review, 2026-09-06 (the
// most severe of three required findings): the prior version of this file
// trusted whatever realm mesh_find_records_by_type's response claimed for
// a matching procedure name, with the last record in the dump silently
// winning if more than one existed. mesh_find_records_by_type documents
// this itself: "Coverage depends on that station's own view of the DHT,"
// and DHT records are not signature- or membership-verified. Concretely:
// anyone, no realm membership required, can publish a
// procedure_advertisement for e.g. hecate-rag.search_chunks_semantic
// under the public all-zero realm and serve it themselves; whenever a
// 60s discovery refresh happened to sort that record after the real one,
// every lazymesh instance on the mesh would send real corpus queries to
// the attacker's implementation and trust the reply -- worse still,
// because runAgent's own system prompt tells the model to *prefer*
// mesh_service_* results over its own judgment.
//
// macula-mcp's own device_membership.ts hits this exact failure mode and
// deliberately computes the realm id client-side rather than trusting an
// advertisement's own claim; this does the same thing, the same way --
// sha256 of the realm name, not a hardcoded hex literal copy-pasted from
// memory (verified once, live, against `sha256sum` independently of this
// code, so a transcription error can't silently break every lookup).
const pinnedRealmName = "io.macula"

var pinnedRealm = func() string {
	sum := sha256.Sum256([]byte(pinnedRealmName))
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}()

// Discovery resolves ONCE per process lifetime, not on a TTL (changed
// 2026-09-07, R2): re-querying mesh_find_records_by_type ever, even on a
// minute-scale cache, meant the tool schema array Loop.Say() sends could
// change shape between calls -- Fable's review flagged this as directly
// self-defeating for provider-side prompt caching, and R2's whole point
// is a byte-stable fixed prefix so a provider's KV-cache actually reuses
// it. Real tradeoff, accepted deliberately: a mesh service that comes up
// AFTER this process starts is invisible until restart -- ListTools logs
// clearly (see Source.logger) exactly once, when discovery actually
// happens, so this isn't a silent gap. Previously TTL'd at 60s; that
// constant is gone, not tuned.

// callTimeoutMS is passed explicitly to every mesh_call rather than left
// to its own default (a required finding, 2026-09-06: an implicit
// default is easy to lose track of, and this package's own retry-once
// policy makes an explicit, deliberately-chosen value matter more than it
// would for a one-shot call).
const callTimeoutMS = 20000

// maxCallsPerMinute bounds how many real mesh_call RPCs this Source will
// make in any rolling minute (a required finding, 2026-09-06: nothing
// previously bounded call volume at all -- one steered peer message,
// with Loop's own maxRounds=25 and a provider that can return several
// tool_calls per round, could drive dozens of real RPCs against shared
// embedder/LLM infrastructure under the operator's own identity,
// indefinitely). Generous enough for legitimate multi-step tool use in
// one conversation turn, not generous enough to be a real load source.
const maxCallsPerMinute = 20

// mcpCaller is the subset of *mcpclient.Client Source needs: calling
// macula-mcp's own mesh_find_records_by_type and mesh_call tools. Source
// never talks to the mesh protocol directly -- it's a client of
// macula-mcp, same as everything else in this codebase.
type mcpCaller interface {
	CallTool(ctx context.Context, name string, args map[string]any) (string, error)
}

// Source implements agent.ToolSource, exposing Curated's procedures as
// tools whenever they're currently discoverable, under pinnedRealm
// specifically, on the mesh.
type Source struct {
	mcp   mcpCaller
	limit *fixedWindowLimiter
	nowFn func() time.Time

	mu          sync.Mutex
	index       map[string]string // tool name -> procedure ("domain.method"); realm is always pinnedRealm
	cachedTools []mcpclient.Tool
	discovered  bool // true once discovery has succeeded -- never re-queried after (see its own doc comment)
	logger      *log.Logger
}

// New wraps mcp (typically a *mcpclient.Client already spawned for
// macula-mcp's own tools) as a mesh-service tool source.
func New(mcp mcpCaller) *Source {
	return &Source{
		mcp:   mcp,
		limit: newFixedWindowLimiter(maxCallsPerMinute, time.Minute),
		nowFn: time.Now,
	}
}

// SetLogger sets where ListTools logs the one-time discovery event (see
// its own doc comment) -- optional, a nil logger (the default, and every
// existing test's own setup) just means that line is never printed;
// production wiring (cmd/lazymesh's buildToolSource) is the only caller
// that sets a real one, so this is a setter rather than a New parameter
// specifically to avoid touching every one of this package's own
// existing New(fake) call sites for an entirely optional capability.
func (s *Source) SetLogger(l *log.Logger) {
	s.mu.Lock()
	s.logger = l
	s.mu.Unlock()
}

func (s *Source) ListTools(ctx context.Context) ([]mcpclient.Tool, error) {
	s.mu.Lock()
	if s.discovered {
		tools := s.cachedTools
		s.mu.Unlock()
		return tools, nil
	}
	s.mu.Unlock()

	raw, err := s.mcp.CallTool(ctx, "mesh_find_records_by_type", map[string]any{"record_type": "procedure_advertisement"})
	if err != nil {
		return nil, fmt.Errorf("discover mesh procedures: %w", err)
	}

	var parsed struct {
		Records []struct {
			ProcedureAdvertisement struct {
				Realm     string `json:"realm"`
				Procedure string `json:"procedure"`
			} `json:"procedure_advertisement"`
		} `json:"records"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, fmt.Errorf("decode mesh_find_records_by_type: %w", err)
	}

	// Only records under pinnedRealm are ever considered at all -- a
	// record under any other realm is invisible to this package, not
	// merely deprioritized. Some records' decoded procedure field carries
	// a leading "_/" path segment before the domain.method name (an
	// artifact of how the advertisement's URI is split, not something
	// mesh_call's own procedure parameter accepts); stripped so lookups
	// match Curated's plain "domain.method" form.
	discovered := make(map[string]bool, len(parsed.Records))
	for _, r := range parsed.Records {
		if !strings.EqualFold(r.ProcedureAdvertisement.Realm, pinnedRealm) {
			continue
		}
		proc := strings.TrimPrefix(r.ProcedureAdvertisement.Procedure, "_/")
		discovered[proc] = true
	}

	var tools []mcpclient.Tool
	index := make(map[string]string)
	for _, cp := range Curated {
		if !discovered[cp.Procedure()] {
			continue // not currently advertised under the pinned realm -- real dynamic discovery, not a fixed catalog
		}
		name := cp.ToolName()
		tools = append(tools, mcpclient.Tool{
			Name:        name,
			Description: cp.Description,
			InputSchema: map[string]any{"type": "object"},
		})
		index[name] = cp.Procedure()
	}

	s.mu.Lock()
	s.index = index
	s.cachedTools = tools
	s.discovered = true
	logger := s.logger
	s.mu.Unlock()

	if logger != nil {
		logger.Printf("[mesh_service discovery] resolved %d of %d curated procedures live on the mesh -- once per session, won't re-check even if the mesh changes", len(tools), len(Curated))
	}
	return tools, nil
}

func (s *Source) CallToolRaw(ctx context.Context, name string, argumentsJSON string) (string, error) {
	s.mu.Lock()
	procedure, ok := s.index[name]
	s.mu.Unlock()
	if !ok {
		return "", fmt.Errorf("mesh service tool %q is not currently available (not discovered on the last refresh)", name)
	}

	if !s.limit.allow(s.nowFn()) {
		return "", fmt.Errorf("mesh service tool %q refused: more than %d real mesh calls in the last minute, waiting for the budget to reset", name, maxCallsPerMinute)
	}

	args := map[string]any{}
	if strings.TrimSpace(argumentsJSON) != "" {
		if err := json.Unmarshal([]byte(argumentsJSON), &args); err != nil {
			return "", fmt.Errorf("decode arguments for %s: %w", name, err)
		}
	}

	callArgs := map[string]any{
		"procedure":  procedure,
		"realm":      pinnedRealm,
		"args":       args,
		"timeout_ms": callTimeoutMS,
	}

	result, err := s.mcp.CallTool(ctx, "mesh_call", callArgs)
	if err != nil && isTransportError(err) {
		// Retry ONLY transport-class failures (a required finding,
		// 2026-09-06: the prior version retried every error class
		// indiscriminately, including application-level argument
		// errors that would just fail identically again, and deadline
		// expiries -- retrying one of those risks a second expensive
		// job running server-side for a call whose first attempt
		// hadn't actually given up). A teammate's survey hit a real
		// transient QUIC-level error that resolved on immediate retry
		// with identical arguments -- that class of failure, and only
		// that class, is worth one retry here.
		result, err = s.mcp.CallTool(ctx, "mesh_call", callArgs)
	}
	if err != nil {
		return "", fmt.Errorf("mesh_call %s: %w", procedure, err)
	}
	return decodeHexASCII(result), nil
}

// transportErrorSubstrings are the failure modes mesh_call's own tool
// description enumerates as connectivity/routing problems (as opposed to
// the remote procedure itself returning an application-level error) --
// an allowlist, not a blocklist, so an error shape this package doesn't
// recognize is conservatively NOT retried rather than assumed safe to.
var transportErrorSubstrings = []string{
	"temporary_relay_failure",
	"unknown_next_peer",
	"unreachable",
	"no direct-dial advertisement",
	"read stream",
	"connection:",
	"deadline exceeded",
	"context deadline exceeded",
	"i/o timeout",
}

func isTransportError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, s := range transportErrorSubstrings {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// fixedWindowLimiter is a minimal per-window call budget: max calls
// allowed, reset to max once window has elapsed since the last reset.
// Deliberately simple over a true leaky-bucket -- the goal here is
// bounding blast radius from a steered agent, not smoothing traffic.
type fixedWindowLimiter struct {
	mu        sync.Mutex
	max       int
	window    time.Duration
	remaining int
	resetAt   time.Time
}

func newFixedWindowLimiter(max int, window time.Duration) *fixedWindowLimiter {
	return &fixedWindowLimiter{max: max, window: window, remaining: max}
}

func (l *fixedWindowLimiter) allow(now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.resetAt.IsZero() || !now.Before(l.resetAt) {
		l.remaining = l.max
		l.resetAt = now.Add(l.window)
	}
	if l.remaining <= 0 {
		return false
	}
	l.remaining--
	return true
}
