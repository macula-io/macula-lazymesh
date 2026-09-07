package ringwaiter

import (
	"context"
	"sync"
	"testing"
	"time"
)

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

// fakeCaller lets a test control what each mesh_read_inbox poll
// "returns" without a real macula-mcp spawn -- same shape as
// roomwaiter's own fakeCaller.
type fakeCaller struct {
	mu    sync.Mutex
	calls int

	respond func(ctx context.Context) (string, error)
}

func (f *fakeCaller) CallTool(ctx context.Context, name string, args map[string]any) (string, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	return f.respond(ctx)
}

func (f *fakeCaller) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func TestManager_StartPollsAndSurfacesANewRing(t *testing.T) {
	f := &fakeCaller{respond: func(ctx context.Context) (string, error) {
		return `{"rings":{"pending":[{"ring_id":"r1","purpose":"p","peer_petname":"Nova"}]}}`, nil
	}}
	m := New(f, "")
	orig := PollInterval
	PollInterval = time.Millisecond
	defer func() { PollInterval = orig }()

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

func TestManager_SameRingIDNotResurfacedAfterFirstArrival(t *testing.T) {
	f := &fakeCaller{respond: func(ctx context.Context) (string, error) {
		return `{"rings":{"pending":[{"ring_id":"r1","purpose":"p","peer_petname":"Nova"}]}}`, nil
	}}
	m := New(f, "")
	orig := PollInterval
	PollInterval = time.Millisecond
	defer func() { PollInterval = orig }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Start(ctx)

	select {
	case <-m.Arrivals():
	case <-time.After(2 * time.Second):
		t.Fatalf("expected the first arrival")
	}

	// Same ring_id keeps coming back from every poll (still pending
	// mesh-side, nobody answered it) -- must not be queued a second time.
	select {
	case r := <-m.Arrivals():
		t.Fatalf("expected no second arrival for the same ring_id, got: %+v", r)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestManager_StopCancelsPolling(t *testing.T) {
	polled := make(chan struct{}, 8)
	f := &fakeCaller{respond: func(ctx context.Context) (string, error) {
		select {
		case polled <- struct{}{}:
		default:
		}
		return `{"rings":{"pending":[]}}`, nil
	}}
	m := New(f, "")
	orig := PollInterval
	PollInterval = time.Millisecond
	defer func() { PollInterval = orig }()

	m.Start(context.Background())
	select {
	case <-polled:
	case <-time.After(2 * time.Second):
		t.Fatalf("expected at least one poll before Stop")
	}
	if !m.Polling() {
		t.Fatalf("expected Polling()==true while running")
	}

	m.Stop()
	if m.Polling() {
		t.Fatalf("expected Polling()==false after Stop")
	}

	callsAtStop := f.callCount()
	time.Sleep(50 * time.Millisecond)
	if got := f.callCount(); got != callsAtStop {
		t.Fatalf("expected no further polls after Stop (calls %d -> %d)", callsAtStop, got)
	}
}

// TestManager_StartTwiceDoesNotStartASecondPoller mirrors roomwaiter's
// own "re-Sync with the same set doesn't restart a waiter" test: a
// second Start call must not spin up a second background goroutine,
// which polling call VOLUME (not just Polling()'s own bool) is what
// actually proves -- two pollers would double the call rate.
func TestManager_StartTwiceDoesNotStartASecondPoller(t *testing.T) {
	f := &fakeCaller{respond: func(ctx context.Context) (string, error) {
		return `{"rings":{"pending":[]}}`, nil
	}}
	m := New(f, "")
	orig := PollInterval
	PollInterval = 5 * time.Millisecond
	defer func() { PollInterval = orig }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Start(ctx)
	m.Start(ctx) // second call -- must be a no-op, not a second goroutine

	time.Sleep(120 * time.Millisecond) // ~24 ticks at 5ms if only one poller is running
	got := f.callCount()
	if got > 40 {
		t.Fatalf("expected roughly one poller's worth of calls (~24 at 5ms ticks over 120ms), got %d -- looks like Start started a second poller", got)
	}
	if got == 0 {
		t.Fatalf("expected at least one poll to have happened")
	}
}
