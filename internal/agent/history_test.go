package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/macula-io/macula-lazymesh/internal/provider"
)

// trimProvider serves trimHistory's summarization calls: it fails them
// (amputation fallback, the pre-G6 contract) or answers with a canned
// summary, recording every summarization transcript it received.
type trimProvider struct {
	failSummary bool
	summaries   []string
}

func (p *trimProvider) ChatCompletion(ctx context.Context, req provider.ChatRequest) (provider.ChatResponse, error) {
	if len(req.Messages) > 0 && req.Messages[0].Content == compactionPrompt {
		if p.failSummary {
			return provider.ChatResponse{}, fmt.Errorf("summarization unavailable")
		}
		p.summaries = append(p.summaries, req.Messages[1].Content)
		return provider.ChatResponse{Message: provider.Message{Role: provider.RoleAssistant, Content: "canned summary"}}, nil
	}
	return provider.ChatResponse{Message: provider.Message{Role: provider.RoleAssistant, Content: "reply"}}, nil
}

func (p *trimProvider) ContextWindow() int { return 1_000_000 }

// bigTurns seeds l with n turns whose assistant messages are ~half the
// byte budget each, so a couple of turns blows past maxHistoryBytes.
func bigTurns(l *Loop, n int) {
	l.messages = append(l.messages, provider.Message{Role: provider.RoleSystem, Content: "sys"})
	big := strings.Repeat("x", maxHistoryBytes/2)
	for i := 0; i < n; i++ {
		l.messages = append(l.messages,
			provider.Message{Role: provider.RoleUser, Content: "turn"},
			provider.Message{Role: provider.RoleAssistant, Content: big},
		)
	}
}

// TestTrimHistory_SummarizesInsteadOfAmputating is the G6 contract: when
// turns must be evicted, they are summarized into a system message right
// after the real system prompt — the verbatim text is gone, the record
// is not.
func TestTrimHistory_SummarizesInsteadOfAmputating(t *testing.T) {
	p := &trimProvider{}
	l := &Loop{Provider: p}
	bigTurns(l, 3)

	l.trimHistory(context.Background(), nil)

	if len(l.messages) < 2 || l.messages[1].Role != provider.RoleSystem || l.messages[1].Content != summaryPrefix+"canned summary" {
		t.Fatalf("expected a summary message after the system prompt, messages = %+v", l.messages)
	}
	if got := messagesByteSize(l.messages[1:]); got > maxHistoryBytes {
		t.Fatalf("expected remaining history to fit maxHistoryBytes, got %d", got)
	}
	if len(p.summaries) == 0 {
		t.Fatal("the summarizer was never called")
	}
}

// TestTrimHistory_SummarizationFailureFallsBackToAmputation pins the
// degradation path: when the provider cannot summarize, eviction still
// happens (the old amputation behavior) and an EventError says so.
func TestTrimHistory_SummarizationFailureFallsBackToAmputation(t *testing.T) {
	p := &trimProvider{failSummary: true}
	l := &Loop{Provider: p}
	bigTurns(l, 3)
	before := len(l.messages)
	events := make(chan Event, 8)

	l.trimHistory(context.Background(), events)

	if len(l.messages) >= before {
		t.Fatalf("expected eviction even without summarization, was %d, now %d", before, len(l.messages))
	}
	for i, m := range l.messages[1:] {
		if strings.HasPrefix(m.Content, summaryPrefix) {
			t.Fatalf("a summary message appeared despite summarization failing (index %d): %+v", i, m)
		}
	}
	sawCompactionError := false
	close(events)
	for ev := range events {
		if ev.Kind == EventError && ev.Err != nil && strings.Contains(ev.Err.Error(), "context compaction") {
			sawCompactionError = true
		}
	}
	if !sawCompactionError {
		t.Fatal("the compaction failure never surfaced as an EventError")
	}
}

// TestTrimHistory_NoOpUnderLimit: a small conversation passes through
// untouched.
func TestTrimHistory_NoOpUnderLimit(t *testing.T) {
	l := &Loop{Provider: &trimProvider{failSummary: true}, messages: []provider.Message{
		{Role: provider.RoleSystem, Content: "sys"},
		{Role: provider.RoleUser, Content: "hi"},
		{Role: provider.RoleAssistant, Content: "hello"},
	}}
	l.trimHistory(context.Background(), nil)
	if len(l.messages) != 3 {
		t.Fatalf("expected no trimming under the limit, got %d messages", len(l.messages))
	}
}

// TestTrimHistory_KeepsSystemMessageAndDropsOldestTurns pins the
// amputation-fallback contract: message-count eviction cuts whole turns,
// the system message survives, and the oldest survivor is a user turn.
func TestTrimHistory_KeepsSystemMessageAndDropsOldestTurns(t *testing.T) {
	l := &Loop{Provider: &trimProvider{failSummary: true}}
	l.messages = append(l.messages, provider.Message{Role: provider.RoleSystem, Content: "sys"})
	// Build maxHistoryMessages+20 messages as many 2-message "turns"
	// (user, assistant) so trimming has clearly identifiable boundaries.
	for i := 0; i < (maxHistoryMessages+20)/2; i++ {
		l.messages = append(l.messages,
			provider.Message{Role: provider.RoleUser, Content: "turn"},
			provider.Message{Role: provider.RoleAssistant, Content: "reply"},
		)
	}
	before := len(l.messages)

	l.trimHistory(context.Background(), nil)

	if len(l.messages) >= before {
		t.Fatalf("expected trimming to reduce message count, was %d, now %d", before, len(l.messages))
	}
	if len(l.messages) > maxHistoryMessages {
		t.Fatalf("expected at most %d messages after trimming, got %d", maxHistoryMessages, len(l.messages))
	}
	if l.messages[0].Role != provider.RoleSystem || l.messages[0].Content != "sys" {
		t.Fatalf("expected the system message to survive trimming, got %+v", l.messages[0])
	}
	// Every remaining non-system message must start with a user turn --
	// confirms cutting happened at a turn boundary, not mid-turn.
	if l.messages[1].Role != provider.RoleUser {
		t.Fatalf("expected the oldest surviving message to be a user turn, got role %q", l.messages[1].Role)
	}
}

// TestTrimHistory_NeverSeparatesToolCallFromItsResult pins the boundary
// discipline: cutting at user-message turns keeps every tool result with
// the assistant message that called it.
func TestTrimHistory_NeverSeparatesToolCallFromItsResult(t *testing.T) {
	l := &Loop{Provider: &trimProvider{failSummary: true}}
	l.messages = append(l.messages, provider.Message{Role: provider.RoleSystem, Content: "sys"})
	for i := 0; i < (maxHistoryMessages+30)/3; i++ {
		l.messages = append(l.messages,
			provider.Message{Role: provider.RoleUser, Content: "do something"},
			provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "c1", Name: "mesh_say", Arguments: "{}"}}},
			provider.Message{Role: provider.RoleTool, ToolCallID: "c1", Name: "mesh_say", Content: "ok"},
		)
	}

	l.trimHistory(context.Background(), nil)

	for i, m := range l.messages {
		if m.Role == provider.RoleTool && i > 0 {
			prev := l.messages[i-1]
			if prev.Role != provider.RoleAssistant || len(prev.ToolCalls) == 0 {
				t.Fatalf("found a tool-result message at index %d with no preceding tool-calling assistant message: %+v then %+v", i, prev, m)
			}
		}
	}
}

func TestTruncateForHistory_LeavesShortContentAlone(t *testing.T) {
	short := "a small tool result"
	if got := truncateForHistory(short); got != short {
		t.Fatalf("expected short content unchanged, got %q", got)
	}
}

func TestTruncateForHistory_TruncatesOversizedContentWithNote(t *testing.T) {
	big := strings.Repeat("y", maxToolResultBytes+5000)
	got := truncateForHistory(big)
	if len(got) >= len(big) {
		t.Fatalf("expected truncation to shorten a %d-byte result, got %d bytes", len(big), len(got))
	}
	if !strings.Contains(got, "truncated") {
		t.Fatalf("expected a truncation note in the result, got suffix: %q", got[len(got)-80:])
	}
	if !strings.HasPrefix(got, strings.Repeat("y", 100)) {
		t.Fatalf("expected the kept prefix to be the original content's start, not something else")
	}
}
