//go:build live

package roomwaiter

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/macula-io/macula-lazymesh/internal/mcpclient"
)

// TestLiveResourceUsage_NConcurrentWaiters is macula-io/macula-lazymesh
// #14's resource-usage measurement (Fable's flagged concern): opens N
// real rooms and syncs a Manager to watch all of them concurrently --
// each one a real, outstanding mesh_wait_room call, each polling
// macula-mcp's own local SQLite transcript every REPLY_POLL_MS (250ms,
// verified against macula-mcp's own rooms.ts) -- and holds them open long
// enough for an external `ps` snapshot (see the spike report for the
// actual CPU/RSS numbers this run produced; this test only proves N
// waiters stay open concurrently without erroring, timing is out of
// process here).
//
// N and holdSeconds are overridable via env vars so the same test scales
// from a quick sanity check to whatever N an external `ps` probe wants to
// sample against, without editing the source each time.
func TestLiveResourceUsage_NConcurrentWaiters(t *testing.T) {
	n := envInt(t, "SPIKE_N_ROOMS", 30)
	holdSeconds := envInt(t, "SPIKE_HOLD_SECONDS", 15)

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(holdSeconds+60)*time.Second)
	defer cancel()

	client, err := mcpclient.Spawn(ctx, mcpclient.SpawnOptions{})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	defer client.Close()
	defer func() { _, _ = client.CallTool(context.Background(), "mesh_goodbye", nil) }()

	if pidFile := os.Getenv("SPIKE_PID_MARKER_FILE"); pidFile != "" {
		// Signal to an external `ps` probe (which can't see this
		// process's own child PIDs directly) that the client is up and
		// its subprocess(es) already exist -- see the report for how
		// this was correlated against `pgrep -f macula-mcp` externally.
		_ = os.WriteFile(pidFile, []byte("spawned\n"), 0o644)
	}

	rooms := make([]string, 0, n)
	for i := 0; i < n; i++ {
		res, err := client.CallTool(ctx, "mesh_open_room", map[string]any{"purpose": fmt.Sprintf("lazymesh#14 resource probe %d/%d", i+1, n)})
		if err != nil {
			t.Fatalf("mesh_open_room %d: %v", i, err)
		}
		var parsed struct {
			RoomTopic string `json:"room_topic"`
		}
		if err := json.Unmarshal([]byte(res), &parsed); err != nil || parsed.RoomTopic == "" {
			t.Fatalf("mesh_open_room %d: unexpected result %q (err %v)", i, res, err)
		}
		rooms = append(rooms, parsed.RoomTopic)
	}
	t.Logf("opened %d rooms, syncing waiters", n)

	mgr := New(client, "")
	mgr.Sync(ctx, rooms)
	defer mgr.StopAll()

	if got := mgr.Watching(); got != n {
		t.Fatalf("expected Watching()==%d right after Sync, got %d", n, got)
	}

	t.Logf("holding %d concurrent mesh_wait_room waiters open for %ds -- sample macula-mcp's process now", n, holdSeconds)
	time.Sleep(time.Duration(holdSeconds) * time.Second)

	if got := mgr.Watching(); got != n {
		t.Errorf("expected all %d waiters still running after the hold, got %d -- one or more errored out", n, got)
	}
}

func envInt(t *testing.T, name string, def int) int {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	var out int
	if _, err := fmt.Sscanf(v, "%d", &out); err != nil {
		t.Fatalf("invalid %s=%q: %v", name, v, err)
	}
	return out
}
