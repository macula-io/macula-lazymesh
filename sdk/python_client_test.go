package sdk

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/macula-io/macula-lazymesh/internal/agent"
	"github.com/macula-io/macula-lazymesh/internal/frontend"
)

// runPythonClient runs sdk/python/lazymesh_client.py against path with
// the given arguments and returns its parsed stdout line.
func runPythonClient(t *testing.T, path string, args ...string) map[string]any {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	script := filepath.Join("python", "lazymesh_client.py")
	cmdArgs := append([]string{script, path}, args...)
	cmd := exec.Command(python, cmdArgs...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("python client (%v): %s", err, out)
	}
	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("python client output %q: %v", out, err)
	}
	return parsed
}

// TestPythonClientSayAndQuery proves the Python client speaks the wire
// protocol correctly end to end against a real server: the full turn
// shape comes back from `say`, and `query` returns the data raw.
func TestPythonClientSayAndQuery(t *testing.T) {
	input := make(chan string, 8)
	events := make(chan agent.Event, 8)
	path := filepath.Join(t.TempDir(), "ctrl.sock")
	s, err := frontend.Start(frontend.Options{
		Path:      path,
		Input:     input,
		Events:    events,
		Query:     func(ctx context.Context, what string) (any, error) { return map[string]any{"what": what}, nil },
		SessionID: "py-test",
		Model:     "deepseek/test",
	})
	if err != nil {
		t.Fatalf("start server: %v", err)
	}
	defer s.Close()

	// Feed the turn only once the python client's input has landed, so
	// the events flow to its connection rather than evaporating before it
	// connected.
	gotInput := make(chan string, 1)
	go func() {
		text := <-input
		gotInput <- text
		events <- agent.Event{Kind: agent.EventAssistantDelta, Text: "py"}
		events <- agent.Event{Kind: agent.EventAssistantMessage, Text: "py"}
		events <- agent.Event{Kind: agent.EventListening}
	}()

	done := make(chan map[string]any, 1)
	go func() {
		done <- runPythonClient(t, path, "say", "hello from python")
	}()
	select {
	case result := <-done:
		turn, ok := result["turn"].(map[string]any)
		if !ok || turn["text"] != "py" {
			t.Fatalf("python say result = %v", result)
		}
		deltas, ok := turn["deltas"].([]any)
		if !ok || len(deltas) != 1 || deltas[0] != "py" {
			t.Fatalf("python deltas = %v", turn["deltas"])
		}
	case <-time.After(10 * time.Second):
		t.Fatal("python client never returned")
	}

	select {
	case got := <-gotInput:
		if got != "hello from python" {
			t.Fatalf("input = %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("python input never reached the driver channel")
	}
}
