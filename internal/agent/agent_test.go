package agent

import (
	"context"
	"fmt"
	"testing"

	"github.com/macula-io/macula-lazymesh/internal/mcpclient"
	"github.com/macula-io/macula-lazymesh/internal/provider"
)

type fakeToolSource struct {
	tools    []mcpclient.Tool
	calls    []string // "name(args)" for each CallToolRaw invocation
	callErr  error
	callText string
}

func (f *fakeToolSource) ListTools(ctx context.Context) ([]mcpclient.Tool, error) {
	return f.tools, nil
}

func (f *fakeToolSource) CallToolRaw(ctx context.Context, name string, argumentsJSON string) (string, error) {
	f.calls = append(f.calls, fmt.Sprintf("%s(%s)", name, argumentsJSON))
	if f.callErr != nil {
		return "", f.callErr
	}
	return f.callText, nil
}

// scriptedProvider returns one canned response per call, in order, so a
// test can drive a specific tool-call round trip without a real LLM.
type scriptedProvider struct {
	responses []provider.ChatResponse
	calls     int
}

func (s *scriptedProvider) ChatCompletion(ctx context.Context, req provider.ChatRequest) (provider.ChatResponse, error) {
	if s.calls >= len(s.responses) {
		return provider.ChatResponse{}, fmt.Errorf("scriptedProvider: no more responses scripted (call %d)", s.calls)
	}
	resp := s.responses[s.calls]
	s.calls++
	return resp, nil
}

func TestLoop_Say_PlainReplyNoToolCalls(t *testing.T) {
	tools := &fakeToolSource{}
	p := &scriptedProvider{responses: []provider.ChatResponse{
		{Message: provider.Message{Role: provider.RoleAssistant, Content: "hello back"}},
	}}
	loop := NewLoop(p, tools, "system prompt")

	events := make(chan Event, 8)
	if err := loop.Say(context.Background(), "hi", events); err != nil {
		t.Fatalf("Say returned error: %v", err)
	}
	close(events)

	var got []Event
	for e := range events {
		got = append(got, e)
	}
	if len(got) != 1 || got[0].Kind != EventAssistantMessage || got[0].Text != "hello back" {
		t.Fatalf("expected one EventAssistantMessage(hello back), got %+v", got)
	}
	if len(tools.calls) != 0 {
		t.Fatalf("expected no tool calls, got %v", tools.calls)
	}
}

func TestLoop_Say_OneToolCallThenReply(t *testing.T) {
	tools := &fakeToolSource{
		tools:    []mcpclient.Tool{{Name: "mesh_say", Description: "say something"}},
		callText: `{"sent": true}`,
	}
	p := &scriptedProvider{responses: []provider.ChatResponse{
		{Message: provider.Message{
			Role: provider.RoleAssistant,
			ToolCalls: []provider.ToolCall{
				{ID: "call_1", Name: "mesh_say", Arguments: `{"room_topic":"agents.room.abc","text":"hi"}`},
			},
		}},
		{Message: provider.Message{Role: provider.RoleAssistant, Content: "done"}},
	}}
	loop := NewLoop(p, tools, "")

	events := make(chan Event, 8)
	if err := loop.Say(context.Background(), "join and say hi", events); err != nil {
		t.Fatalf("Say returned error: %v", err)
	}
	close(events)

	var kinds []EventKind
	for e := range events {
		kinds = append(kinds, e.Kind)
	}
	want := []EventKind{EventToolCall, EventToolResult, EventAssistantMessage}
	if len(kinds) != len(want) {
		t.Fatalf("expected %d events, got %d: %+v", len(want), len(kinds), kinds)
	}
	for i, k := range want {
		if kinds[i] != k {
			t.Fatalf("event %d: expected kind %d, got %d", i, k, kinds[i])
		}
	}

	if len(tools.calls) != 1 || tools.calls[0] != `mesh_say({"room_topic":"agents.room.abc","text":"hi"})` {
		t.Fatalf("unexpected tool calls: %v", tools.calls)
	}
}

func TestLoop_Say_ToolErrorIsFedBackNotFatal(t *testing.T) {
	tools := &fakeToolSource{callErr: fmt.Errorf("boom")}
	p := &scriptedProvider{responses: []provider.ChatResponse{
		{Message: provider.Message{
			Role:      provider.RoleAssistant,
			ToolCalls: []provider.ToolCall{{ID: "call_1", Name: "mesh_say", Arguments: `{}`}},
		}},
		{Message: provider.Message{Role: provider.RoleAssistant, Content: "recovered"}},
	}}
	loop := NewLoop(p, tools, "")

	events := make(chan Event, 8)
	if err := loop.Say(context.Background(), "go", events); err != nil {
		t.Fatalf("Say returned error even though the model recovered: %v", err)
	}
	close(events)

	sawErr := false
	for e := range events {
		if e.Kind == EventError {
			sawErr = true
		}
	}
	if !sawErr {
		t.Fatalf("expected an EventError for the failed tool call")
	}
}

func TestLoop_Say_ExceedsMaxRoundsReturnsError(t *testing.T) {
	tools := &fakeToolSource{callText: "ok"}
	responses := make([]provider.ChatResponse, 0, 26)
	for i := 0; i < 26; i++ {
		responses = append(responses, provider.ChatResponse{Message: provider.Message{
			Role:      provider.RoleAssistant,
			ToolCalls: []provider.ToolCall{{ID: "x", Name: "noop", Arguments: "{}"}},
		}})
	}
	p := &scriptedProvider{responses: responses}
	loop := NewLoop(p, tools, "")

	events := make(chan Event, 256)
	err := loop.Say(context.Background(), "loop forever", events)
	if err == nil {
		t.Fatalf("expected an error when the model never stops calling tools")
	}
}
