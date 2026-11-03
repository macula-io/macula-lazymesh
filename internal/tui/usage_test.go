package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/macula-io/macula-lazymesh/internal/agent"
)

// Context use and spend are on the summary line (#78 gap 4): after a turn
// with known usage, the line names the tokens the conversation holds, the
// share of the model's context that is, and what the session has used.
func TestSummaryLine_ShowsContextUseAfterATurn(t *testing.T) {
	m := New(nil, Options{UserInputCh: make(chan string, 8), StatusBarPosition: "bottom", AgentModel: "anthropic/claude"})
	m.width, m.height = 200, 24
	m.resizeComponents()

	if got := m.renderSummaryLine(); strings.Contains(got, "context") {
		t.Fatalf("expected no context figure before any usage is known, got %q", got)
	}

	m, _ = m.handleAgentEvent(agentEventMsg(agent.Event{
		Kind:  agent.EventUsageReported,
		Usage: agent.UsageReport{ContextTokens: 12_345, ContextWindow: 200_000, SessionTokens: 48_100},
	}))
	got := m.renderSummaryLine()
	for _, want := range []string{"context 12.3k/200k (6%)", "48.1k used"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected the summary line to contain %q, got %q", want, got)
		}
	}
	for _, e := range m.chatEntries {
		if strings.Contains(e.text, "12.3k") || strings.Contains(e.text, "12345") {
			t.Fatalf("a usage report is a status figure, not a chat line: %+v", e)
		}
	}
}

func TestTokenCountReadsAtAGlance(t *testing.T) {
	cases := map[int]string{
		0:         "0",
		999:       "999",
		1_000:     "1k",
		12_345:    "12.3k",
		200_000:   "200k",
		1_048_576: "1M",
		2_500_000: "2.5M",
	}
	for n, want := range cases {
		if got := shortTokens(n); got != want {
			t.Errorf("shortTokens(%d) = %q, want %q", n, got, want)
		}
	}
}

// Under one percent is not zero percent: a fresh session holding a few
// thousand tokens of a million-token window still holds something.
func TestContextShareUnderOnePercentIsNotZero(t *testing.T) {
	if got := contextShare(3_000, 1_000_000); got != "<1%" {
		t.Fatalf("contextShare(3000, 1M) = %q, want <1%%", got)
	}
	if got := contextShare(190_000, 200_000); got != "95%" {
		t.Fatalf("contextShare(190000, 200000) = %q, want 95%%", got)
	}
}

// A backend with no known window shows the tokens alone: a percentage of a
// made-up window would be a figure nobody measured.
func TestSummaryLine_NoShareWithoutAKnownWindow(t *testing.T) {
	m := New(nil, Options{UserInputCh: make(chan string, 8), StatusBarPosition: "bottom"})
	m.width, m.height = 200, 24
	m.resizeComponents()
	m, _ = m.handleAgentEvent(agentEventMsg(agent.Event{
		Kind:  agent.EventUsageReported,
		Usage: agent.UsageReport{ContextTokens: 12_345, SessionTokens: 12_345},
	}))
	got := m.renderSummaryLine()
	if !strings.Contains(got, "context 12.3k · 12.3k used") {
		t.Fatalf("expected the tokens without a window, got %q", got)
	}
	if strings.Contains(got, "%") {
		t.Fatalf("expected no percentage without a known window, got %q", got)
	}
}

// The figure joins a line that is cut to the terminal's width (#61): at a
// narrow terminal the line still fits on one row.
func TestSummaryLine_WithUsageStillFitsANarrowTerminal(t *testing.T) {
	m := New(nil, Options{UserInputCh: make(chan string, 8), StatusBarPosition: "bottom", AgentModel: "deepseek/deepseek-v4-flash"})
	m.width, m.height = 40, 24
	m.resizeComponents()
	m, _ = m.handleAgentEvent(agentEventMsg(agent.Event{
		Kind:  agent.EventUsageReported,
		Usage: agent.UsageReport{ContextTokens: 912_345, ContextWindow: 1_000_000, SessionTokens: 4_800_000},
	}))
	got := m.renderSummaryLine()
	if w := lipgloss.Width(got); w > 40 {
		t.Fatalf("summary line is %d columns at a 40-column terminal: %q", w, got)
	}
	if strings.Contains(got, "\n") {
		t.Fatalf("summary line wrapped: %q", got)
	}
}
