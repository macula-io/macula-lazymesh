package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/macula-io/macula-lazymesh/internal/agent"
	"github.com/macula-io/macula-lazymesh/internal/counters"
	"github.com/macula-io/macula-lazymesh/internal/frontend"
	"github.com/macula-io/macula-lazymesh/internal/mcpclient"
	"github.com/macula-io/macula-lazymesh/internal/provider"
	"github.com/macula-io/macula-lazymesh/internal/ringwaiter"
	"github.com/macula-io/macula-lazymesh/internal/roomwaiter"
	"github.com/macula-io/macula-lazymesh/internal/sessionhost"
	"github.com/macula-io/macula-lazymesh/internal/sessionstore"
	"github.com/macula-io/macula-lazymesh/internal/toolchain"
)

// crashAfterSayProvider asks for mesh_say, panics on the round after it,
// and answers plainly from then on. It records the last user message of
// every request, so a test sees which prompts reached the model.
type crashAfterSayProvider struct {
	mu      sync.Mutex
	rounds  int
	prompts []string
}

func (p *crashAfterSayProvider) ChatCompletion(_ context.Context, req provider.ChatRequest) (provider.ChatResponse, error) {
	p.mu.Lock()
	p.rounds++
	round := p.rounds
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == provider.RoleUser {
			p.prompts = append(p.prompts, req.Messages[i].Content)
			break
		}
	}
	p.mu.Unlock()
	switch round {
	case 1:
		return provider.ChatResponse{Message: provider.Message{Role: provider.RoleAssistant,
			ToolCalls: []provider.ToolCall{{ID: "c1", Name: "mesh_say", Arguments: `{"text":"hello room"}`}}}}, nil
	case 2:
		panic("provider exploded after mesh_say")
	}
	return provider.ChatResponse{Message: provider.Message{Role: provider.RoleAssistant, Content: "done"}}, nil
}

func (p *crashAfterSayProvider) ContextWindow() int { return 1000 }

func (p *crashAfterSayProvider) promptsSeen() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.prompts...)
}

// countingMeshTools counts the mesh_say calls that reached the mesh.
type countingMeshTools struct {
	mu   sync.Mutex
	says int
}

func (c *countingMeshTools) ListTools(context.Context) ([]mcpclient.Tool, error) { return nil, nil }

func (c *countingMeshTools) CallToolRaw(_ context.Context, name, _ string) (string, error) {
	if name == "mesh_say" {
		c.mu.Lock()
		c.says++
		c.mu.Unlock()
	}
	return "sent", nil
}

func (c *countingMeshTools) saysSent() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.says
}

// awaitEvent reads events until one of kind arrives and returns those seen.
func awaitEvent(t *testing.T, events <-chan agent.Event, kind agent.EventKind, seen []agent.Event) []agent.Event {
	t.Helper()
	timeout := time.After(15 * time.Second)
	for {
		select {
		case ev := <-events:
			seen = append(seen, ev)
			if ev.Kind == kind {
				return seen
			}
		case <-timeout:
			t.Fatalf("no event of kind %v; saw %+v", kind, seen)
		}
	}
}

// #99: a session that crashes after a mesh_say is not sent the prompt
// again. The operator is told the turn crashed and that mesh_say ran, the
// turn settles, and the replacement session's events still reach the TUI.
func TestCrashRecovery_NoResendAfterACrashFollowingMeshSay(t *testing.T) {
	n, err := sessionhost.Node(io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	root, err := sessionhost.StartRoot(n)
	if err != nil {
		t.Fatal(err)
	}
	store, err := sessionstore.New(t.TempDir(), "fp")
	if err != nil {
		t.Fatal(err)
	}
	p := &crashAfterSayProvider{}
	tools := &countingMeshTools{}
	sess, err := sessionhost.StartSession(n, root, sessionhost.SessionArgs{
		Provider: p, Toolchain: toolchain.Policy{Base: tools, Allowlist: []string{"mesh_say"}}, SystemPrompt: "test", Store: store, SessionID: "s1",
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	events := make(chan agent.Event, 256)
	tuiOut := agent.NewOutlet(ctx.Done(), events)
	frontendOut := agent.NewOutlet(ctx.Done(), nil)
	agentLog := log.New(io.Discard, "", 0)
	bridge, err := startEventBridge(n, ctx, sess, tuiOut, frontendOut, agentLog, roomwaiter.New(nil, ""), func(agent.Event) {}, counters.New())
	if err != nil {
		t.Fatal(err)
	}

	userInput := make(chan string, 4)
	userInput <- "tell the room hello"
	done := make(chan struct{})
	go func() {
		defer close(done)
		runAgent(ctx, n, root, newCurrentSession(sess), bridge, roomwaiter.New(nil, ""), ringwaiter.New(nil, ""), agentLog, tuiOut, frontendOut, userInput, nil, true, nil, nil)
	}()
	t.Cleanup(func() { cancel(); <-done })

	seen := awaitEvent(t, events, agent.EventTurnCrashed, nil)
	crashed := seen[len(seen)-1]
	if !strings.Contains(crashed.Text, "mesh_say") || !strings.Contains(crashed.Text, "hello room") {
		t.Fatalf("the crash notice does not name the mesh_say that ran: %q", crashed.Text)
	}
	if next := awaitEvent(t, events, agent.EventListening, nil); len(next) != 1 {
		t.Fatalf("the crash notice is not followed by the listening beat: %+v", next)
	}

	userInput <- "anything new?"
	seen = awaitEvent(t, events, agent.EventAssistantMessage, nil)
	if got := seen[len(seen)-1].Text; got != "done" {
		t.Fatalf("the replacement session's answer did not reach the TUI: %q", got)
	}

	if got := tools.saysSent(); got != 1 {
		t.Fatalf("mesh_say reached the mesh %d times, want 1", got)
	}
	prompts := p.promptsSeen()
	if strings.Count(strings.Join(prompts, "\n"), "tell the room hello") != 2 || prompts[len(prompts)-1] != "anything new?" {
		t.Fatalf("prompts the model saw = %q; want the original only in the crashed turn's two rounds", prompts)
	}
}

// #99: a run that was killed mid-turn leaves the turn unfinished in the
// log. The next run tells the operator which steps ran before it waits
// for anything, and sends nothing on its own.
func TestCrashRecovery_AKilledRunIsReportedWhenTheNextOneStarts(t *testing.T) {
	store, err := sessionstore.New(t.TempDir(), "fp")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Append("s1", []provider.Message{
		{Role: provider.RoleUser, Content: "tell the room hello"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "c1", Name: "mesh_say", Arguments: `{"text":"hello room"}`}}},
		{Role: provider.RoleTool, ToolCallID: "c1", Content: "sent"},
	}); err != nil {
		t.Fatal(err)
	}
	n, err := sessionhost.Node(io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	root, err := sessionhost.StartRoot(n)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := sessionhost.StartSession(n, root, sessionhost.SessionArgs{
		Provider: &crashAfterSayProvider{rounds: 2}, Toolchain: toolchain.Policy{Base: &countingMeshTools{}, Allowlist: []string{"mesh_say"}}, SystemPrompt: "test", Store: store, SessionID: "s1",
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	events := make(chan agent.Event, 256)
	tuiOut := agent.NewOutlet(ctx.Done(), events)
	sockPath := filepath.Join(t.TempDir(), "ctrl.sock")
	ctrl, err := frontend.Start(frontend.Options{
		Path: sockPath, Input: make(chan string, 1), Events: make(chan agent.Event, 1), SessionID: "s1",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ctrl.Close() })

	reportEarlierCrash(n, sess, tuiOut, ctrl.Announce, log.New(io.Discard, "", 0))

	seen := awaitEvent(t, events, agent.EventTurnCrashed, nil)
	if len(seen) != 1 || !strings.Contains(seen[0].Text, "mesh_say") {
		t.Fatalf("the killed run's turn is not reported first: %+v", seen)
	}
	// A controller that connects after the start still learns of it, as
	// turn_crashed with no turn_complete for a turn it never sent.
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	r := bufio.NewReader(conn)
	var types []string
	for {
		conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
		raw, err := r.ReadBytes('\n')
		if err != nil {
			break
		}
		var line struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(raw, &line)
		types = append(types, line.Type)
	}
	if strings.Join(types, ",") != "session,turn_crashed" {
		t.Fatalf("a controller connecting after the start got %v, want session then turn_crashed", types)
	}
	select {
	case ev := <-events:
		t.Fatalf("the TUI got %v after the earlier crash, want nothing: no turn ran", ev.Kind)
	default:
	}
}
