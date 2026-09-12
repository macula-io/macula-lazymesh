package tui

import (
	"strings"
	"testing"
	"time"
)

// TestCollapseBlankLines_FoldsRuns pins the core rule: two or more blank
// lines become one; a single blank (block separation) is kept.
func TestCollapseBlankLines_FoldsRuns(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"a\n\n\n\nb", "a\n\nb"},
		{"a\n\nb", "a\n\nb"},                 // single blank kept
		{"a\n\n\nb\n\n\nc", "a\n\nb\n\nc"},   // multiple runs
		{"a\n \t \nb", "a\n\nb"},             // whitespace-only lines are blank
		{"no blanks here", "no blanks here"}, // untouched
	}
	for _, c := range cases {
		if got := collapseBlankLines(c.in); got != c.want {
			t.Fatalf("collapseBlankLines(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestCollapseBlankLines_PreservesFencedCode pins the content rule: blank
// lines inside fenced code blocks (backticks or tildes) are significant
// and never collapsed.
func TestCollapseBlankLines_PreservesFencedCode(t *testing.T) {
	in := "before\n\n\n```go\nx := 1\n\n\ny := 2\n```\n\nafter\n\n\nafter2"
	got := collapseBlankLines(in)
	for _, want := range []string{"x := 1\n\n\ny := 2", "```go\n", "```\n\nafter"} {
		if !strings.Contains(got, want) {
			t.Fatalf("code block content damaged: want %q in %q", want, got)
		}
	}
	// The outside runs collapsed: exactly one blank before the fence and
	// after it.
	if !strings.Contains(got, "before\n\n```go") {
		t.Fatalf("outside blank run not collapsed: %q", got)
	}

	tilde := "a\n\n\n~~~md\nkeep\n\n\nme\n~~~"
	got = collapseBlankLines(tilde)
	if !strings.Contains(got, "keep\n\n\nme") {
		t.Fatalf("tilde fence content damaged: %q", got)
	}
}

// TestAssistantEntryCollapsesBlankLines pins the integration: a
// double-spaced assistant answer renders compactly.
func TestAssistantEntryCollapsesBlankLines(t *testing.T) {
	entry := chatEntry{kind: chatAssistant, at: time.Now(), text: "line one\n\n\n\nline two\n\n\n\nline three"}
	rendered := entry.render(false, 60)
	stripped := sgrStripper.ReplaceAllString(rendered, "")
	// The render must not contain two consecutive blank lines.
	if strings.Contains(stripped, "\n\n\n") {
		t.Fatalf("blank runs survived rendering: %q", stripped)
	}
	for _, want := range []string{"line one", "line two", "line three"} {
		if !strings.Contains(stripped, want) {
			t.Fatalf("content lost: %q", stripped)
		}
	}
}
