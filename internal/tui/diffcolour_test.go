package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/macula-io/macula-lazymesh/internal/agent"
)

// diffRows is each tool row's text and the colour it is given, for the
// entry the events produce.
func diffRows(t *testing.T, expanded bool, events ...agent.Event) map[string]lipgloss.TerminalColor {
	t.Helper()
	m := sendAgentEvents(t, newTestModelWithEvents(t), events...)
	rows := map[string]lipgloss.TerminalColor{}
	for _, e := range m.chatEntries {
		if e.kind != chatToolCall {
			continue
		}
		for _, l := range e.toolCallLines(expanded) {
			rows[strings.TrimSpace(l.text.String())] = l.style.GetForeground()
		}
	}
	return rows
}

const patchResult = "applied 1 hunk(s) to 1 file: a.txt\n\n--- a/a.txt\n+++ b/a.txt\n@@ -1,2 +1,2 @@\n one\n-two\n+three\n"

// The check #78 gap 3 names: in an apply_patch result, an added line and a
// removed line carry different colours, and neither is the plain body grey.
func TestDiffColour_AddedAndRemovedDiffer(t *testing.T) {
	rows := diffRows(t, false,
		agent.Event{Kind: agent.EventToolCall, ToolName: "apply_patch", Text: `{"patch":"--- a/a.txt\n+++ b/a.txt\n"}`},
		agent.Event{Kind: agent.EventToolResult, ToolName: "apply_patch", Text: patchResult},
	)
	added, removed, hunk, context := rows["+three"], rows["-two"], rows["@@ -1,2 +1,2 @@"], rows["one"]
	if added == nil || removed == nil {
		t.Fatalf("diff rows missing: %v", rows)
	}
	if added == removed {
		t.Fatalf("an added and a removed line share a colour: %v", added)
	}
	body := chatToolBodyStyle.GetForeground()
	for name, c := range map[string]lipgloss.TerminalColor{"added": added, "removed": removed, "hunk": hunk} {
		if c == body {
			t.Fatalf("the %s line is plain body grey", name)
		}
	}
	if context != body {
		t.Fatalf("a context line is coloured: %v", context)
	}
}

// A shell command's working-tree diff is coloured the same way, and its
// ordinary output is not: a "-v" flag in a test log is not a removed line.
func TestDiffColour_ShellDiffColouredOutputNot(t *testing.T) {
	rows := diffRows(t, false,
		agent.Event{Kind: agent.EventToolCall, ToolName: "shell_exec", Text: `{"command":"gofmt -w ."}`},
		agent.Event{Kind: agent.EventToolResult, ToolName: "shell_exec", Text: `{"exit_code":0,"stderr":"","stdout":"-v given\n","working_tree_diff":"--- a/x.go\n+++ b/x.go\n@@ -1 +1 @@\n-a\n+b\n"}`},
	)
	if rows["+b"] == rows["-a"] {
		t.Fatalf("the shell diff is not coloured: %v", rows)
	}
	if rows["-v given"] != chatToolBodyStyle.GetForeground() {
		t.Fatalf("plain output was coloured as a diff: %v", rows["-v given"])
	}
}

// Colour is a pure function of text already neutralised: a diff line that
// carries controls, raw or entity-like, still shows them after colouring.
func TestDiffColour_ControlsStayVisibleAfterColouring(t *testing.T) {
	hostile := "applied 1 hunk(s) to 1 file: a.txt\n\n--- a/a.txt\n+++ b/a.txt\n@@ -1 +1 @@\n" +
		"-old\x1b]52;c;cm0=\x07\n+new ‮&#27;]0;t&#7;\r rm -rf ~\n"
	m := sendAgentEvents(t, newTestModelWithEvents(t),
		agent.Event{Kind: agent.EventToolCall, ToolName: "apply_patch", Text: `{"patch":""}`},
		agent.Event{Kind: agent.EventToolResult, ToolName: "apply_patch", Text: hostile},
	)
	for _, expanded := range []bool{false, true} {
		m.detailsExpanded = expanded
		got := strings.Join(m.chatContentLines(), "\n")
		plain := sgrStripper.ReplaceAllString(got, "")
		for _, banned := range []string{"\x1b]", "\x07", "‮", "\r"} {
			if strings.Contains(plain, banned) {
				t.Fatalf("expanded=%v: %q reached the terminal:\n%q", expanded, banned, got)
			}
		}
		for _, want := range []string{"␛]52;c;cm0=␇", "<U+202E>", "&#27;]0;t&#7;", "rm -rf ~"} {
			if !strings.Contains(plain, want) {
				t.Fatalf("expanded=%v: %q is not shown:\n%s", expanded, want, plain)
			}
		}
	}
}

// Inside a hunk every line is classified by the hunk, not by how it looks:
// removing a "-- x" SQL comment and adding "++ y" render as a removed and
// an added line, never as file headers. The next file's headers, after the
// hunk's line counts run out, are headers again, with or without a
// "diff --git" line before them.
func TestDiffColour_HunkLinesThatLookLikeHeaders(t *testing.T) {
	diff := "diff --git a/m.sql b/m.sql\n--- a/m.sql\n+++ b/m.sql\n@@ -1,2 +1,2 @@\n--- keep: audit on\n-- drop the audit trigger\n+++ x\n++ y\n" +
		"--- a/n.sql\n+++ b/n.sql\n@@ -1 +1 @@\n--- old\n+++ new\n"
	lines := diffLines(textLines(termTextOf(diff)))
	want := []lipgloss.Style{
		diffFileStyle, diffFileStyle, diffFileStyle, diffHunkStyle,
		diffRemovedStyle, diffRemovedStyle, diffAddedStyle, diffAddedStyle,
		diffFileStyle, diffFileStyle, diffHunkStyle, diffRemovedStyle, diffAddedStyle,
	}
	if len(lines) != len(want) {
		t.Fatalf("%d lines, want %d", len(lines), len(want))
	}
	for i, l := range lines {
		if l.style.GetForeground() != want[i].GetForeground() || l.style.GetBold() != want[i].GetBold() {
			t.Errorf("line %q: foreground %v bold %v, want %v bold %v", l.text,
				l.style.GetForeground(), l.style.GetBold(), want[i].GetForeground(), want[i].GetBold())
		}
	}
}
