package tui

import (
	"regexp"
	"strings"
	"testing"
)

// ansiColor extracts the first non-reset 256-color SGR code on a rendered
// line, so tests can assert on styling without depending on the exact
// escape layout. glamour pads lines with spaces styled in the document
// color (252), so 252 itself is never a distinctive heading color.
var ansiColor = regexp.MustCompile(`\x1b\[38;5;(\d+)(?:;\d+)*m`)

// lineColors returns, for every non-empty line containing one of the
// given markers, its first distinctive color ("" when the line has no
// color code at all).
func lineColors(out string, markers ...string) []string {
	var colors []string
	for _, line := range strings.Split(out, "\n") {
		found := false
		for _, marker := range markers {
			if strings.Contains(line, marker) {
				found = true
				break
			}
		}
		if !found {
			continue
		}
		m := ansiColor.FindStringSubmatch(line)
		if m == nil {
			colors = append(colors, "")
			continue
		}
		colors = append(colors, m[1])
	}
	return colors
}

// TestHeadingLevelsCarryDistinctColors pins the 2026-09-12 live fix: the
// stock dark style left h2-h5 uncolored, so five levels rendered
// identically. With maculaMarkdownStyle every level renders with its own
// color.
func TestHeadingLevelsCarryDistinctColors(t *testing.T) {
	renderer := markdownRenderer(60)
	if renderer == nil {
		t.Fatal("markdown renderer unavailable")
	}
	out, err := renderer.Render("# one\n\n## two\n\n### three\n\n#### four\n\n##### five\n\n###### six")
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	colors := lineColors(out, "one", "two", "three", "four", "five", "six")
	if len(colors) != 6 {
		t.Fatalf("expected 6 heading lines, got %d", len(colors))
	}
	seen := map[string]bool{}
	for i, c := range colors {
		if c == "" {
			t.Fatalf("heading %d rendered without any color", i+1)
		}
		if seen[c] {
			t.Fatalf("two heading levels share color %q: %q", c, out)
		}
		seen[c] = true
	}
}

// TestNoDocumentMarginAndPadding pins the layout fix: no leading blank
// line, no uniform document indent, and — because the renderer width
// equals the pane width — no visible over-padding either (every styled
// line is at most the renderer width).
func TestNoDocumentMarginAndPadding(t *testing.T) {
	renderer := markdownRenderer(40)
	if renderer == nil {
		t.Fatal("markdown renderer unavailable")
	}
	out, err := renderer.Render("plain paragraph")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if strings.HasPrefix(out, "\n") {
		t.Fatalf("render starts with a blank line: %q", out)
	}
	if strings.HasPrefix(out, "  ") {
		t.Fatalf("render carries the document indent: %q", out)
	}
	if !strings.Contains(stripANSI(out), "plain paragraph") {
		t.Fatalf("content lost: %q", out)
	}
	// Padding discipline: with the renderer at width 40, no rendered
	// line may exceed 40 runes — a mismatched default width would pad to
	// 80 and visibly overrun a narrower pane.
	for _, line := range strings.Split(out, "\n") {
		visible := strings.TrimRight(stripANSI(line), " ")
		if len([]rune(visible)) > 40 {
			t.Fatalf("line overruns the renderer width: %q", line)
		}
	}
}

// stripANSI removes SGR escape sequences for width measurement.
func stripANSI(s string) string {
	return ansiSGR.ReplaceAllString(s, "")
}

// ansiSGR matches any SGR sequence.
var ansiSGR = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// TestMarkdownFeaturesStillRender pins the non-regression contract: bold,
// inline code, lists, quotes and code blocks still survive the custom
// style (content-wise; styling is asserted elsewhere).
func TestMarkdownFeaturesStillRender(t *testing.T) {
	renderer := markdownRenderer(60)
	if renderer == nil {
		t.Fatal("markdown renderer unavailable")
	}
	out, err := renderer.Render("**bold** and `code`\n\n- item\n\n> quote\n\n```go\nx := 1\n```")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, want := range []string{"bold", "code", "item", "quote", "x := 1"} {
		if !strings.Contains(out, want) {
			t.Fatalf("render lost %q: %q", want, out)
		}
	}
}
