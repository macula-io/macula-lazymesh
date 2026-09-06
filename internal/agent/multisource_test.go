package agent

import (
	"context"
	"testing"

	"github.com/macula-io/macula-lazymesh/internal/mcpclient"
)

type namedFakeSource struct {
	name  string
	tools []mcpclient.Tool
	calls []string
}

func (f *namedFakeSource) ListTools(ctx context.Context) ([]mcpclient.Tool, error) {
	return f.tools, nil
}

func (f *namedFakeSource) CallToolRaw(ctx context.Context, name string, argumentsJSON string) (string, error) {
	f.calls = append(f.calls, name)
	return f.name + ":" + name, nil
}

func TestMultiSource_ListTools_ConcatenatesAllSources(t *testing.T) {
	a := &namedFakeSource{name: "a", tools: []mcpclient.Tool{{Name: "mesh_say"}, {Name: "mesh_rooms"}}}
	b := &namedFakeSource{name: "b", tools: []mcpclient.Tool{{Name: "shell_exec"}}}
	m := NewMultiSource(a, b)

	tools, err := m.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools returned error: %v", err)
	}
	if len(tools) != 3 {
		t.Fatalf("expected 3 tools, got %d: %+v", len(tools), tools)
	}
}

func TestMultiSource_CallToolRaw_RoutesToOwningSource(t *testing.T) {
	a := &namedFakeSource{name: "a", tools: []mcpclient.Tool{{Name: "mesh_say"}}}
	b := &namedFakeSource{name: "b", tools: []mcpclient.Tool{{Name: "shell_exec"}}}
	m := NewMultiSource(a, b)

	if _, err := m.ListTools(context.Background()); err != nil {
		t.Fatalf("ListTools returned error: %v", err)
	}

	result, err := m.CallToolRaw(context.Background(), "shell_exec", "{}")
	if err != nil {
		t.Fatalf("CallToolRaw returned error: %v", err)
	}
	if result != "b:shell_exec" {
		t.Fatalf("expected call routed to source b, got %q", result)
	}
	if len(a.calls) != 0 {
		t.Fatalf("expected source a to receive no calls, got %v", a.calls)
	}
	if len(b.calls) != 1 || b.calls[0] != "shell_exec" {
		t.Fatalf("expected source b to receive one call to shell_exec, got %v", b.calls)
	}
}

func TestMultiSource_ListTools_CollidingNamesIsAnError(t *testing.T) {
	a := &namedFakeSource{name: "a", tools: []mcpclient.Tool{{Name: "dup"}}}
	b := &namedFakeSource{name: "b", tools: []mcpclient.Tool{{Name: "dup"}}}
	m := NewMultiSource(a, b)

	if _, err := m.ListTools(context.Background()); err == nil {
		t.Fatalf("expected a collision error when two sources advertise the same tool name")
	}
}

func TestMultiSource_CallToolRaw_UnknownNameIsAnError(t *testing.T) {
	m := NewMultiSource(&namedFakeSource{name: "a"})
	if _, err := m.ListTools(context.Background()); err != nil {
		t.Fatalf("ListTools returned error: %v", err)
	}
	if _, err := m.CallToolRaw(context.Background(), "nonexistent", "{}"); err == nil {
		t.Fatalf("expected an error calling a tool no source advertised")
	}
}
