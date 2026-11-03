package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
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
		Usage: agent.UsageReport{ContextTokens: 12_345, ContextWindow: 200_000, RunTokens: 48_100},
	}))
	got := m.renderSummaryLine()
	for _, want := range []string{"context 12.3k/200k (6%)", "48.1k this run"} {
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
		999_950:   "1M",
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
		Usage: agent.UsageReport{ContextTokens: 12_345, RunTokens: 12_345},
	}))
	got := m.renderSummaryLine()
	if !strings.Contains(got, "context 12.3k · 12.3k this run") {
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
		Usage: agent.UsageReport{ContextTokens: 912_345, ContextWindow: 1_000_000, RunTokens: 4_800_000},
	}))
	got := m.renderSummaryLine()
	if w := lipgloss.Width(got); w > 40 {
		t.Fatalf("summary line is %d columns at a 40-column terminal: %q", w, got)
	}
	if strings.Contains(got, "\n") {
		t.Fatalf("summary line wrapped: %q", got)
	}
}

// The line is cut to the terminal's width, so the context figure is placed
// only when it fits whole (#78 gap 4 review): a cut through it ("context
// 64", "(6") reads as a different number.
func TestSummaryLine_ContextFigureIsWholeOrAbsentAtEveryWidth(t *testing.T) {
	const model = "deepseek/deepseek-v4-flash"
	report := agent.UsageReport{ContextTokens: 645_000, ContextWindow: 1_000_000, RunTokens: 4_800_000}
	seg := usageSegment(report)
	shown := 0
	for w := 40; w <= 140; w++ {
		m := New(nil, Options{UserInputCh: make(chan string, 8), StatusBarPosition: "bottom", AgentModel: model})
		m.width, m.height = w, 24
		m.resizeComponents()
		m, _ = m.handleAgentEvent(agentEventMsg(agent.Event{Kind: agent.EventUsageReported, Usage: report}))
		line := ansi.Strip(m.renderSummaryLine())
		i := strings.Index(line, model)
		if i < 0 {
			continue
		}
		rest := line[i+len(model):]
		if rest != "" && rest != " · "+seg {
			t.Fatalf("at %d columns the figure is cut: %q", w, line)
		}
		if rest != "" {
			shown++
		}
	}
	if shown == 0 {
		t.Fatal("the figure never showed at any width up to 140")
	}
}

// What the figure counts is this run: a resumed session starts it at zero,
// so the label says "this run", never a session total it does not know.
func TestSummaryLine_SpendIsLabelledAsThisRun(t *testing.T) {
	if got := usageSegment(agent.UsageReport{ContextTokens: 1_200, ContextWindow: 200_000, RunTokens: 1_200}); !strings.HasSuffix(got, "1.2k this run") {
		t.Fatalf("usage segment = %q, want the spend labelled as this run", got)
	}
}
