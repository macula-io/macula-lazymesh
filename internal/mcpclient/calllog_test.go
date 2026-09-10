package mcpclient

import (
	"bytes"
	"log"
	"sync"
	"testing"
)

func TestFirstFailureIsLogged(t *testing.T) {
	tr := newFailureThrottle()
	log, suppressed := tr.onFailure("mesh_list_realms", "boom")
	if !log {
		t.Error("the first failure must be logged")
	}
	if suppressed != 0 {
		t.Errorf("suppressed = %d, want 0 on a first failure", suppressed)
	}
}

// The refresh cycle ticks every two seconds. Without this, a persistent
// failure writes about 43,000 identical lines a day into agent.log and
// buries every other subsystem's lines under it.
func TestIdenticalRepeatsAreSuppressed(t *testing.T) {
	tr := newFailureThrottle()
	tr.onFailure("mesh_list_realms", "boom")
	for i := 0; i < 500; i++ {
		if log, _ := tr.onFailure("mesh_list_realms", "boom"); log {
			t.Fatalf("repeat %d was logged; identical consecutive failures must be suppressed", i)
		}
	}
}

// A changed error is a genuinely new condition, and the run of the
// previous one must be reported rather than vanishing.
func TestChangedErrorIsLoggedAndReportsPreviousRun(t *testing.T) {
	tr := newFailureThrottle()
	tr.onFailure("mesh_call", "connection refused")
	for i := 0; i < 4; i++ {
		tr.onFailure("mesh_call", "connection refused")
	}
	log, suppressed := tr.onFailure("mesh_call", "tool not found")
	if !log {
		t.Error("a changed error must be logged")
	}
	if suppressed != 4 {
		t.Errorf("suppressed = %d, want 4 -- the previous run must be reported, not discarded", suppressed)
	}
}

func TestRecoveryIsLoggedWithSuppressedCount(t *testing.T) {
	tr := newFailureThrottle()
	tr.onFailure("mesh_rooms", "boom")
	for i := 0; i < 9; i++ {
		tr.onFailure("mesh_rooms", "boom")
	}
	log, suppressed := tr.onSuccess("mesh_rooms")
	if !log {
		t.Error("recovery after a failure run must be logged")
	}
	if suppressed != 9 {
		t.Errorf("suppressed = %d, want 9", suppressed)
	}
}

// A healthy call is not news. Without this every successful refresh
// tick would log, which is the same flood from the other direction.
func TestSuccessOnNeverFailingToolIsNotLogged(t *testing.T) {
	tr := newFailureThrottle()
	if log, _ := tr.onSuccess("mesh_agents"); log {
		t.Error("a tool that was never failing must not log a recovery")
	}
	// And again after a genuine recovery has already been reported.
	tr.onFailure("mesh_agents", "boom")
	tr.onSuccess("mesh_agents")
	if log, _ := tr.onSuccess("mesh_agents"); log {
		t.Error("a second consecutive success must not log")
	}
}

func TestToolsAreThrottledIndependently(t *testing.T) {
	tr := newFailureThrottle()
	tr.onFailure("mesh_rooms", "boom")
	// A different tool's first failure is still a first failure.
	if log, _ := tr.onFailure("mesh_agents", "boom"); !log {
		t.Error("a different tool's first failure must be logged")
	}
	// And the first tool is still suppressed.
	if log, _ := tr.onFailure("mesh_rooms", "boom"); log {
		t.Error("mesh_rooms should still be suppressed")
	}
}

// The agent loop and the TUI refresh call tools concurrently. Run with -race.
func TestThrottleIsConcurrencySafe(t *testing.T) {
	tr := newFailureThrottle()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				tr.onFailure("mesh_rooms", "boom")
				tr.onSuccess("mesh_agents")
			}
		}(i)
	}
	wg.Wait()
}

// A Client built directly in a test has no throttle. That means silence,
// not a panic and not unthrottled logging: with a logger attached,
// nothing may be written. This pins the comment on logCallOutcome to
// what the code actually does.
func TestNilThrottleIsSilent(t *testing.T) {
	var buf bytes.Buffer
	c := &Client{}
	c.SetLogger(log.New(&buf, "", 0))
	c.logCallOutcome("mesh_rooms", nil)
	c.logCallOutcome("mesh_rooms", errForTest("boom"))
	if buf.Len() != 0 {
		t.Errorf("a nil throttle must mean silence, but the logger got %q", buf.String())
	}
}

type errForTest string

func (e errForTest) Error() string { return string(e) }
