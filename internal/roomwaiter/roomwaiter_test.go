package roomwaiter

import (
	"context"
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
