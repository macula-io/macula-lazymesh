package roomwaiter

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestArrived_TimedOutZeroMeansArrived(t *testing.T) {
	if !arrived(`{"reply":{"from":"x"},"timed_out":0}`) {
		t.Fatalf("expected timed_out:0 to mean arrived")
	}
}

func TestArrived_TimedOutOneMeansNothingNew(t *testing.T) {
	if arrived(`{"reply":null,"timed_out":1}`) {
		t.Fatalf("expected timed_out:1 to mean nothing new")
	}
}

func TestArrived_UnparseableResultIsTreatedAsNothingNew(t *testing.T) {
	if arrived(`not json`) {
		t.Fatalf("expected unparseable result to never misfire an arrival")
	}
}

// fakeCaller lets a test control exactly when each mesh_wait_room call
// "returns" (an arrival, a clean timeout, or blocks until ctx is
// cancelled), so Manager's goroutine lifecycle is testable without a real
// macula-mcp spawn -- that's what the live probe in the report is for.
type fakeCaller struct {
	mu    sync.Mutex
	calls int

	// respond, if set, is called for every mesh_wait_room invocation and
	// returns the result JSON (or blocks on ctx itself).
	respond func(ctx context.Context, room string) (string, error)
}

func (f *fakeCaller) CallTool(ctx context.Context, name string, args map[string]any) (string, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	room, _ := args["room_topic"].(string)
	return f.respond(ctx, room)
}

func TestManager_SyncStartsAndStopsWaiters(t *testing.T) {
	started := make(chan string, 4)
	f := &fakeCaller{respond: func(ctx context.Context, room string) (string, error) {
		started <- room
		<-ctx.Done()
		return "", ctx.Err()
	}}
	m := New(f, "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	m.Sync(ctx, []string{"agents.room.a", "agents.room.b"})

	seen := map[string]bool{}
	for i := 0; i < 2; i++ {
		select {
		case r := <-started:
			seen[r] = true
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for waiters to start, saw: %v", seen)
		}
	}
	if !seen["agents.room.a"] || !seen["agents.room.b"] {
		t.Fatalf("expected waiters for both rooms, got: %v", seen)
	}
	if got := m.Watching(); got != 2 {
		t.Fatalf("expected Watching()==2, got %d", got)
	}

	// Drop room b -- Sync again with only room a.
	m.Sync(ctx, []string{"agents.room.a"})
	if got := m.Watching(); got != 1 {
		t.Fatalf("expected Watching()==1 after dropping room b, got %d", got)
	}

	// Re-Sync with the same set is idempotent -- no new goroutine starts.
	callsBefore := func() int { f.mu.Lock(); defer f.mu.Unlock(); return f.calls }()
	m.Sync(ctx, []string{"agents.room.a"})
	time.Sleep(50 * time.Millisecond)
	callsAfter := func() int { f.mu.Lock(); defer f.mu.Unlock(); return f.calls }()
	if callsAfter != callsBefore {
		t.Fatalf("expected re-Sync with the same room set not to restart a waiter (calls %d -> %d)", callsBefore, callsAfter)
	}
}

func TestManager_StopAllCancelsEveryWaiter(t *testing.T) {
	cancelled := make(chan string, 2)
	f := &fakeCaller{respond: func(ctx context.Context, room string) (string, error) {
		<-ctx.Done()
		cancelled <- room
		return "", ctx.Err()
	}}
	m := New(f, "")
	m.Sync(context.Background(), []string{"agents.room.a", "agents.room.b"})
	time.Sleep(50 * time.Millisecond) // let both goroutines reach CallTool

	m.StopAll()

	seen := map[string]bool{}
	for i := 0; i < 2; i++ {
		select {
		case r := <-cancelled:
			seen[r] = true
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for StopAll to cancel both waiters, saw: %v", seen)
		}
	}
	if got := m.Watching(); got != 0 {
		t.Fatalf("expected Watching()==0 after StopAll, got %d", got)
	}
}

func TestManager_ArrivalDedupUntilAck(t *testing.T) {
	release := make(chan struct{})
	callN := 0
	f := &fakeCaller{respond: func(ctx context.Context, room string) (string, error) {
		callN++
		if callN == 1 {
			return `{"reply":{"from":"x"},"timed_out":0}`, nil // first call: arrives immediately
		}
		<-release // subsequent calls: hang until the test lets them go
		<-ctx.Done()
		return "", ctx.Err()
	}}
	m := New(f, "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Sync(ctx, []string{"agents.room.a"})

	select {
	case a := <-m.Arrivals():
		if a.RoomTopic != "agents.room.a" {
			t.Fatalf("unexpected arrival: %+v", a)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("expected one arrival for agents.room.a")
	}

	// Without Ack, the second CallTool round (which also returns
	// timed_out:0-shaped data if it weren't gated by `release`) must not
	// produce a second queued arrival -- the pending flag is still set.
	select {
	case a := <-m.Arrivals():
		t.Fatalf("expected no second arrival before Ack, got: %+v", a)
	case <-time.After(200 * time.Millisecond):
	}

	m.Ack("agents.room.a")
	close(release)
	cancel() // let the blocked second call return via ctx.Done(), ending the test cleanly
}

// TestManager_ErrorBackoffForcesACheckAfterward is a regression test for
// the blind-spot Fable found 2026-09-07: mesh_wait_room's own afterId
// baseline is read fresh at call time (macula-mcp's rooms.ts), so an
// envelope landing during the error-backoff sleep was previously
// invisible to the next call forever, not just delayed -- accidentally
// covered until now by cmd/lazymesh's own idle tick, which is being
// removed as part of the same change. Confirms watch() surfaces an
// Arrival after an error+backoff pause even though the RETRY itself
// (scripted to hang until ctx is cancelled, same as every other test
// here) never reports one.
func TestManager_ErrorBackoffForcesACheckAfterward(t *testing.T) {
	callN := 0
	f := &fakeCaller{respond: func(ctx context.Context, room string) (string, error) {
		callN++
		if callN == 1 {
			return "", errors.New("transient failure")
		}
		<-ctx.Done() // the retry after backoff -- never itself reports an arrival
		return "", ctx.Err()
	}}
	m := New(f, "")
	orig := errorBackoff
	errorBackoff = time.Millisecond
	defer func() { errorBackoff = orig }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Sync(ctx, []string{"agents.room.a"})

	select {
	case a := <-m.Arrivals():
		if a.RoomTopic != "agents.room.a" {
			t.Fatalf("unexpected arrival: %+v", a)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("expected an arrival forced after the error+backoff pause")
	}
}

// TestManager_DroppedEnqueueClearsPendingSoTheRoomIsNotStuckForever is a
// regression test for a real bug found reviewing this for production
// (macula-io/macula-lazymesh#15): a dropped enqueue (arrivals channel
// full) used to leave pending[room] set forever, which meant every
// FUTURE enqueue for that room would also silently no-op -- permanently
// stopping that room from ever being surfaced again over one unlucky
// drop. Fixed: a drop clears pending, so the room's next real arrival is
// queued normally.
func TestManager_DroppedEnqueueClearsPendingSoTheRoomIsNotStuckForever(t *testing.T) {
	m := New(&fakeCaller{respond: func(ctx context.Context, room string) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}}, "")

	// Fill the arrivals channel to capacity directly (bypassing enqueue's
	// own dedup) so the next enqueue call is guaranteed to hit the
	// default/drop branch, not race against goroutine scheduling.
	for len(m.arrivals) < cap(m.arrivals) {
		m.arrivals <- Arrival{RoomTopic: "filler"}
	}

	// Channel is full, so this call takes the drop branch synchronously
	// and returns with pending already cleared -- the bug this test
	// guards against was pending staying true past this point.
	m.enqueue("agents.room.busy")
	if m.pending["agents.room.busy"] {
		t.Fatalf("expected pending to be cleared after a dropped enqueue, got it still set (the exact stuck-forever bug)")
	}

	// Drain one filler slot so this room's NEXT enqueue can actually
	// succeed -- proving it isn't permanently stuck. The channel is FIFO
	// and still has other filler entries ahead of this one, so drain
	// everything and look for it rather than checking just the front.
	<-m.arrivals
	m.enqueue("agents.room.busy")

	found := false
	for {
		select {
		case a := <-m.arrivals:
			if a.RoomTopic == "agents.room.busy" {
				found = true
			}
		default:
			if !found {
				t.Fatalf("expected the second enqueue to succeed once pending was cleared by the earlier drop -- room got stuck")
			}
			return
		}
	}
}
