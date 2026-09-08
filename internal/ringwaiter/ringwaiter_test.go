package ringwaiter

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"sync"
	"testing"
	"time"
)

var errFakeWaitFailure = errors.New("fake mesh_wait_ring failure")

// syncBuffer wraps bytes.Buffer with a mutex -- a plain bytes.Buffer is
// not safe for concurrent use, and SetLogger's whole point is a
// background goroutine writing to it while the test reads it back, so
// tests need this rather than the bare type roomwaiter's own equivalent
// (never facing a background writer) can get away with.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestParsePendingRings_ExtractsRingIDPurposeAndPetname(t *testing.T) {
	got := parsePendingRings(`{"rings":{"pending":[{"ring_id":"abc123","purpose":"come help","peer_petname":"Nova","peer":"deadbeef"}],"recent":[]},"rooms":[]}`)
	if len(got) != 1 {
		t.Fatalf("expected one pending ring, got %d: %+v", len(got), got)
	}
	want := Ring{RingID: "abc123", Purpose: "come help", FromPetname: "Nova"}
	if got[0] != want {
		t.Fatalf("expected %+v, got %+v", want, got[0])
	}
}

func TestParsePendingRings_NoRingsKeyReturnsNil(t *testing.T) {
	if got := parsePendingRings(`{"rooms":[]}`); got != nil {
		t.Fatalf("expected nil when the rings key is absent (room_topic was set server-side), got %+v", got)
	}
}

func TestParsePendingRings_UnparseableResultReturnsNil(t *testing.T) {
	if got := parsePendingRings(`not json`); got != nil {
		t.Fatalf("expected nil for unparseable input, never a misfire, got %+v", got)
	}
}

func TestParseWaitRingResult_ExtractsStillPendingRing(t *testing.T) {
	r, ok := parseWaitRingResult(`{"timed_out":0,"ring":{"ring_id":"abc","purpose":"come help","peer_petname":"Nova","peer":"deadbeef","answer":null}}`)
	if !ok {
		t.Fatalf("expected ok=true for a still-pending ring")
	}
	want := Ring{RingID: "abc", Purpose: "come help", FromPetname: "Nova"}
	if r != want {
		t.Fatalf("expected %+v, got %+v", want, r)
	}
}

// mesh_wait_ring returns EVERY incoming ring, not only ones still
// awaiting an answer -- open/closed/allowlist policies resolve theirs
// immediately (see mesh_wait_ring.ts's own doc). Surfacing one of those
// via Arrivals would tell the model to mesh_answer_ring something
// already answered, which macula-mcp's own answerPendingRing refuses
// ("ring was already answered").
func TestParseWaitRingResult_AlreadyAnsweredRingIsNotSurfaced(t *testing.T) {
	_, ok := parseWaitRingResult(`{"timed_out":0,"ring":{"ring_id":"abc","purpose":"p","peer_petname":"Nova","answer":1}}`)
	if ok {
		t.Fatalf("expected ok=false for a ring that already has an answer")
	}
}

func TestParseWaitRingResult_TimedOutReturnsFalse(t *testing.T) {
	_, ok := parseWaitRingResult(`{"timed_out":1,"ring":null}`)
	if ok {
		t.Fatalf("expected ok=false on a clean timeout")
	}
}

func TestParseWaitRingResult_UnparseableResultReturnsFalse(t *testing.T) {
	_, ok := parseWaitRingResult(`not json`)
	if ok {
		t.Fatalf("expected ok=false for unparseable input, never a misfire")
	}
}

// fakeCaller lets a test control exactly when each mesh_wait_ring or
// mesh_read_inbox call "returns" -- same pattern as roomwaiter's own
// fakeCaller, dispatching on the tool name since watch() now calls both
// (mesh_wait_ring as the primary mechanism, mesh_read_inbox via
// checkOnce for the startup and post-backoff catch-up reads).
type fakeCaller struct {
	mu    sync.Mutex
	calls int

	// respondWaitRing, if set, handles every mesh_wait_ring call; unset
	// blocks until ctx is cancelled, same as a real call that never sees
	// a ring.
	respondWaitRing func(ctx context.Context) (string, error)
	// respondReadInbox, if set, handles every mesh_read_inbox call;
	// unset returns an empty pending list.
	respondReadInbox func(ctx context.Context) (string, error)
}

func (f *fakeCaller) CallTool(ctx context.Context, name string, args map[string]any) (string, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	switch name {
	case "mesh_wait_ring":
		if f.respondWaitRing != nil {
			return f.respondWaitRing(ctx)
		}
		<-ctx.Done()
		return "", ctx.Err()
	case "mesh_read_inbox":
		if f.respondReadInbox != nil {
			return f.respondReadInbox(ctx)
		}
		return `{"rings":{"pending":[]}}`, nil
	default:
		return "", nil
	}
}

func (f *fakeCaller) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func TestManager_StartWatchesAndSurfacesANewRing(t *testing.T) {
	callN := 0
	f := &fakeCaller{respondWaitRing: func(ctx context.Context) (string, error) {
		callN++
		if callN == 1 {
			return `{"timed_out":0,"ring":{"ring_id":"r1","purpose":"p","peer_petname":"Nova","answer":null}}`, nil
		}
		<-ctx.Done()
		return "", ctx.Err()
	}}
	m := New(f, "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Start(ctx)

	select {
	case r := <-m.Arrivals():
		if r.RingID != "r1" || r.Purpose != "p" || r.FromPetname != "Nova" {
			t.Fatalf("unexpected arrival: %+v", r)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("expected an arrival for the pending ring")
	}
}

// Real gap this covers (2026-09-08): mesh_wait_ring covers every
// incoming ring, not just ones under "ask" policy -- a ring already
// resolved by open/closed/allowlist must not be surfaced as something
// the model needs to mesh_answer_ring.
func TestManager_AlreadyResolvedRingIsNotSurfaced(t *testing.T) {
	callN := 0
	f := &fakeCaller{respondWaitRing: func(ctx context.Context) (string, error) {
		callN++
		if callN == 1 {
			return `{"timed_out":0,"ring":{"ring_id":"r1","purpose":"p","peer_petname":"Nova","answer":1}}`, nil
		}
		<-ctx.Done()
		return "", ctx.Err()
	}}
	m := New(f, "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Start(ctx)

	select {
	case r := <-m.Arrivals():
		t.Fatalf("expected no arrival for an already-resolved ring, got: %+v", r)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestManager_SameRingIDNotResurfacedAfterFirstArrival(t *testing.T) {
	callN := 0
	const ring = `{"timed_out":0,"ring":{"ring_id":"r1","purpose":"p","peer_petname":"Nova","answer":null}}`
	f := &fakeCaller{respondWaitRing: func(ctx context.Context) (string, error) {
		callN++
		if callN <= 2 {
			return ring, nil
		}
		<-ctx.Done()
		return "", ctx.Err()
	}}
	m := New(f, "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Start(ctx)

	select {
	case <-m.Arrivals():
	case <-time.After(2 * time.Second):
		t.Fatalf("expected the first arrival")
	}

	// Still pending mesh-side, nobody answered it -- must not be queued
	// a second time.
	select {
	case r := <-m.Arrivals():
		t.Fatalf("expected no second arrival for the same ring_id, got: %+v", r)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestManager_StopCancelsWatching(t *testing.T) {
	started := make(chan struct{}, 1)
	f := &fakeCaller{respondWaitRing: func(ctx context.Context) (string, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return "", ctx.Err()
	}}
	m := New(f, "")
	m.Start(context.Background())
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatalf("expected watch to reach mesh_wait_ring before Stop")
	}
	if !m.Watching() {
		t.Fatalf("expected Watching()==true while running")
	}

	m.Stop()
	if m.Watching() {
		t.Fatalf("expected Watching()==false after Stop")
	}

	callsAtStop := f.callCount()
	time.Sleep(50 * time.Millisecond)
	if got := f.callCount(); got != callsAtStop {
		t.Fatalf("expected no further calls after Stop (calls %d -> %d)", callsAtStop, got)
	}
}

// TestManager_StartTwiceDoesNotStartASecondWatcher mirrors roomwaiter's
// own "re-Sync with the same set doesn't restart a waiter" test: a
// second Start call must not spin up a second background goroutine. Each
// watcher here calls mesh_wait_ring exactly once before blocking forever
// on ctx.Done(), so two watchers would show up as two calls -- a much
// more direct signal than the old ticker-based design needed.
func TestManager_StartTwiceDoesNotStartASecondWatcher(t *testing.T) {
	f := &fakeCaller{respondWaitRing: func(ctx context.Context) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}}
	m := New(f, "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Start(ctx)
	m.Start(ctx) // second call -- must be a no-op, not a second goroutine

	time.Sleep(50 * time.Millisecond)
	// One watcher's own startup checkOnce (mesh_read_inbox) plus its one
	// mesh_wait_ring call -- two calls total for a single watcher. A
	// second watcher would double both.
	if got := f.callCount(); got > 2 {
		t.Fatalf("expected at most 2 calls (one watcher's startup check + wait call), got %d -- looks like Start started a second watcher", got)
	}
}

// TestManager_ErrorBackoffForcesACheckAfterward is a regression test for
// the same blind-spot roomwaiter's own equivalent test guards (Fable
// found 2026-09-07, applies here identically): mesh_wait_ring's own
// cursor is read fresh at call time, so a ring recorded during the
// error-backoff sleep would otherwise be invisible to the next call
// forever, not just delayed. Distinguishes the STARTUP checkOnce (which
// must find nothing, so this test actually exercises the post-backoff
// path) from the checkOnce AFTER the backoff pause (which finds the
// ring) via a call counter on mesh_read_inbox.
func TestManager_ErrorBackoffForcesACheckAfterward(t *testing.T) {
	var mu sync.Mutex
	waitCallN, readInboxCallN := 0, 0
	f := &fakeCaller{
		respondWaitRing: func(ctx context.Context) (string, error) {
			mu.Lock()
			waitCallN++
			n := waitCallN
			mu.Unlock()
			if n == 1 {
				return "", errors.New("transient failure")
			}
			<-ctx.Done() // the retry after backoff -- never itself reports a ring
			return "", ctx.Err()
		},
		respondReadInbox: func(ctx context.Context) (string, error) {
			mu.Lock()
			readInboxCallN++
			n := readInboxCallN
			mu.Unlock()
			if n == 1 {
				return `{"rings":{"pending":[]}}`, nil // the startup catch-up: nothing yet
			}
			return `{"rings":{"pending":[{"ring_id":"r1","purpose":"p","peer_petname":"Nova"}]}}`, nil
		},
	}
	m := New(f, "")
	orig := errorBackoff
	errorBackoff = time.Millisecond
	defer func() { errorBackoff = orig }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Start(ctx)

	select {
	case r := <-m.Arrivals():
		if r.RingID != "r1" {
			t.Fatalf("unexpected arrival: %+v", r)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("expected an arrival forced after the error+backoff pause")
	}
}

// TestManager_StartCatchesAlreadyPendingRingBeforeFirstWait covers the
// new startup catch-up (2026-09-08): mesh_wait_ring only ever reports
// the NEXT ring after a given call starts (same cursor shape as
// mesh_wait_room), so a ring that arrived before Start was ever called
// needs its own separate check. mesh_wait_ring here never returns
// anything real -- proving the arrival can only have come from the
// startup checkOnce, not from the wait call.
func TestManager_StartCatchesAlreadyPendingRingBeforeFirstWait(t *testing.T) {
	f := &fakeCaller{
		respondReadInbox: func(ctx context.Context) (string, error) {
			return `{"rings":{"pending":[{"ring_id":"r1","purpose":"already here","peer_petname":"Nova"}]}}`, nil
		},
		respondWaitRing: func(ctx context.Context) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		},
	}
	m := New(f, "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Start(ctx)

	select {
	case r := <-m.Arrivals():
		if r.RingID != "r1" || r.Purpose != "already here" {
			t.Fatalf("unexpected arrival: %+v", r)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("expected Start's own catch-up check to surface an already-pending ring")
	}
}

// Real gap found live 2026-09-08: a failed background call used to be
// swallowed completely silently, which left agent.log looking identical
// whether ringwaiter was quietly healthy on an idle mesh or had been
// failing since startup. SetLogger closes that gap; nil (the default,
// every other test in this file) must stay silent so a caller that
// never sets one is unaffected.
func TestManager_SetLogger_LogsAFailedWait(t *testing.T) {
	// Fails once, then hangs on ctx.Done() -- errorBackoff is deliberately
	// left untouched here: the log call happens synchronously BEFORE
	// watch() ever reads errorBackoff (see the doc comment on watch's own
	// error branch), so this test's assertion never depends on its value.
	// Mutating a package-level var a background goroutine might still be
	// reading after this test function returns is exactly the race
	// TestManager_ErrorBackoffForcesACheckAfterward's own bounded-fake
	// shape avoids -- same reasoning applies here.
	callN := 0
	f := &fakeCaller{respondWaitRing: func(ctx context.Context) (string, error) {
		callN++
		if callN == 1 {
			return "", errFakeWaitFailure
		}
		<-ctx.Done()
		return "", ctx.Err()
	}}
	m := New(f, "")
	buf := &syncBuffer{}
	m.SetLogger(log.New(buf, "", 0))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Start(ctx)

	deadline := time.After(2 * time.Second)
	for {
		if strings.Contains(buf.String(), "mesh_wait_ring failed") {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("expected a logged failure within 2s, got: %q", buf.String())
		case <-time.After(time.Millisecond):
		}
	}
}

func TestManager_SetLogger_LogsWatchingStarted(t *testing.T) {
	f := &fakeCaller{respondWaitRing: func(ctx context.Context) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}}
	m := New(f, "")
	buf := &syncBuffer{}
	m.SetLogger(log.New(buf, "", 0))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Start(ctx)

	if got := buf.String(); !strings.Contains(got, "watching started") {
		t.Fatalf("expected a watching-started log line immediately on Start, got: %q", got)
	}
}

func TestManager_NoLoggerSetStaysSilentOnFailure(t *testing.T) {
	// errorBackoff deliberately left at its real default here too, same
	// race-avoidance reasoning as TestManager_SetLogger_LogsAFailedWait --
	// this test's own assertion (no panic on a nil logger) is fully
	// exercised by the first failed attempt alone, which logs before
	// watch() ever reads errorBackoff.
	f := &fakeCaller{respondWaitRing: func(ctx context.Context) (string, error) {
		return "", errFakeWaitFailure
	}}
	m := New(f, "") // no SetLogger call
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Start(ctx)
	time.Sleep(20 * time.Millisecond) // at least one failed attempt -- must not panic on a nil logger
}
