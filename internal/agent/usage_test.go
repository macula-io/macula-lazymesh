package agent

import (
	"context"
	"testing"

	"github.com/macula-io/macula-lazymesh/internal/mcpclient"
	"github.com/macula-io/macula-lazymesh/internal/provider"
)

// Context use and spend reach the screen (#78 gap 4): after every round
// whose response reports usage, Say emits what the conversation now holds
// (that round's prompt plus completion), the provider's window, and the
// running total -- so a consumer never has to sum or guess.
func TestLoop_Say_ReportsUsageAfterEveryRound(t *testing.T) {
	tools := &fakeToolSource{
		tools:    []mcpclient.Tool{{Name: "mesh_say", Description: "say something"}},
		callText: `{"sent": true}`,
	}
	p := &scriptedProvider{responses: []provider.ChatResponse{
		{
			Message: provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "c1", Name: "mesh_say", Arguments: `{}`}}},
			Usage:   provider.Usage{PromptTokens: 1000, CompletionTokens: 50, TotalTokens: 1050},
		},
		{
			Message: provider.Message{Role: provider.RoleAssistant, Content: "done"},
			Usage:   provider.Usage{PromptTokens: 1200, CompletionTokens: 30, TotalTokens: 1230},
		},
	}}
	loop := NewLoop(p, tools, "system prompt")

	events := make(chan Event, 16)
	if err := loop.Say(context.Background(), "hi", events); err != nil {
		t.Fatalf("Say returned error: %v", err)
	}
	close(events)

	var got []UsageReport
	for e := range events {
		if e.Kind == EventUsageReported {
			got = append(got, e.Usage)
		}
	}
	want := []UsageReport{
		{ContextTokens: 1050, ContextWindow: 1_000_000, RunTokens: 1050},
		{ContextTokens: 1230, ContextWindow: 1_000_000, RunTokens: 2280},
	}
	if len(got) != len(want) {
		t.Fatalf("expected %d usage reports, got %+v", len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("usage report %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// A backend that reports no usage gets no report: zero would read as an
// empty context, which is a claim nobody measured.
func TestLoop_Say_NoUsageReportWhenTheBackendReportsNone(t *testing.T) {
	p := &scriptedProvider{responses: []provider.ChatResponse{
		{Message: provider.Message{Role: provider.RoleAssistant, Content: "hello back"}},
	}}
	loop := NewLoop(p, &fakeToolSource{}, "system prompt")

	events := make(chan Event, 8)
	if err := loop.Say(context.Background(), "hi", events); err != nil {
		t.Fatalf("Say returned error: %v", err)
	}
	close(events)
	for e := range events {
		if e.Kind == EventUsageReported {
			t.Fatalf("expected no usage report from a backend that reports none, got %+v", e)
		}
	}
}

// A resumed session's earlier rounds are not counted again: the run total
// starts at this process (#78 gap 4 review), which is what the TUI labels
// it as.
func TestLoop_Say_RunTokensStartAtThisRunAfterARestore(t *testing.T) {
	p := &scriptedProvider{responses: []provider.ChatResponse{
		{Message: provider.Message{Role: provider.RoleAssistant, Content: "back"}, Usage: provider.Usage{PromptTokens: 9_000, CompletionTokens: 20}},
	}}
	loop := NewLoop(p, &fakeToolSource{}, "system prompt")
	loop.Restore([]provider.Message{
		{Role: provider.RoleUser, Content: "earlier"},
		{Role: provider.RoleAssistant, Content: "a long reply from an earlier run"},
	})

	events := make(chan Event, 8)
	if err := loop.Say(context.Background(), "again", events); err != nil {
		t.Fatalf("Say returned error: %v", err)
	}
	close(events)
	for e := range events {
		if e.Kind == EventUsageReported {
			if e.Usage.RunTokens != 9_020 || e.Usage.ContextTokens != 9_020 {
				t.Fatalf("usage after a restore = %+v, want this run's 9020 only", e.Usage)
			}
			return
		}
	}
	t.Fatal("no usage report")
}
