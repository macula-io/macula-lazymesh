package tui

import (
	"strings"
	"testing"
	"time"
)

// TestFlattenWideTables_KeepsNarrowTables pins the pass-through: a table
// that fits the pane stays a table for glamour to render properly.
func TestFlattenWideTables_KeepsNarrowTables(t *testing.T) {
	md := "| room | who |\n| --- | --- |\n| a | alice |"
	got := flattenWideTables(md, 80)
	if got != md {
		t.Fatalf("narrow table was modified: %q", got)
	}
}

// TestFlattenWideTables_FlattensWideTables pins the rewrite: a table too
// wide for the pane becomes one "column: value" list per row, so the
// viewport never soft-wraps border lines into misaligned noise.
func TestFlattenWideTables_FlattensWideTables(t *testing.T) {
	wideCell := strings.Repeat("x", 60)
	md := "| room | who |\n| --- | --- |\n| " + wideCell + " | alice |\n| b | bob |"
	got := flattenWideTables(md, 40)

	if strings.Contains(got, "| "+wideCell) {
		t.Fatalf("wide table borders survived: %q", got)
	}
	for _, want := range []string{
		"- room: " + wideCell,
		"- who: alice",
		"- room: b",
		"- who: bob",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("flattened output missing %q:\n%s", want, got)
		}
	}
}

// TestFlattenWideTables_EmptyCellBecomesDash pins the empty-cell rule: a
// blank cell flattens to "-" so the row still reads as a row.
func TestFlattenWideTables_EmptyCellBecomesDash(t *testing.T) {
	md := "| a | b |\n| --- | --- |\n| " + strings.Repeat("x", 50) + " |  |"
	got := flattenWideTables(md, 30)
	if !strings.Contains(got, "- b: -") {
		t.Fatalf("empty cell did not flatten to a dash: %q", got)
	}
}

// TestFlattenWideTables_IgnoresNonTables pins the boundary: text that is
// not a pipe table passes through untouched, even if it contains pipes
// or dashes.
func TestFlattenWideTables_IgnoresNonTables(t *testing.T) {
	md := "plain paragraph with | a pipe and - a dash\n\nalso|not|a|table (no leading pipe)"
	if got := flattenWideTables(md, 20); got != md {
		t.Fatalf("non-table content was modified: %q", got)
	}
}

// TestFlattenWideTables_HeaderOnlyTableFlattensToNothingExtra pins the
// degenerate shape: a header + separator with no data rows flattens to
// the plain lines (nothing to list).
func TestFlattenWideTables_HeaderOnlyTableFlattens(t *testing.T) {
	md := "| " + strings.Repeat("w", 40) + " | b |\n| --- | --- |\n\ntrailing text"
	got := flattenWideTables(md, 30)
	if strings.Contains(got, "|") {
		t.Fatalf("empty wide table left borders behind: %q", got)
	}
	if !strings.Contains(got, "trailing text") {
		t.Fatalf("trailing content lost: %q", got)
	}
}

// TestAssistantEntryRendersFlattenedTable pins the integration: a wide
// table inside an assistant entry renders flattened, not as border noise.
func TestAssistantEntryRendersFlattenedTable(t *testing.T) {
	entry := chatEntry{kind: chatAssistant, at: time.Now(), text: "| k | v |\n| --- | --- |\n| " + strings.Repeat("x", 50) + " | y |"}
	rendered := entry.render(false, 30)
	// glamour re-renders the flattened "k: v" lines as a bullet list --
	// readable either way; what must NOT survive is a pipe border.
	if !strings.Contains(rendered, "k:") || !strings.Contains(rendered, strings.Repeat("x", 50)) {
		t.Fatalf("flattened content lost from the render")
	}
	if strings.Contains(rendered, "| x") || strings.Contains(rendered, "|---") {
		t.Fatalf("table borders survived the flatten: %q", rendered)
	}
}
