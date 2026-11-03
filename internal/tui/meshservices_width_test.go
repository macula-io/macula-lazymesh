package tui

// macula-io/macula-lazymesh#113: the MeshServices panel's own lines (the
// disabled notice, the title, the header) were written at a fixed width,
// so the panel was 72 cells wide whatever terminal it was shown on and the
// terminal wrapped every line below 72 columns -- the class #86 closed for
// the status strip and #112 for the ring pop-up.

import (
	"strconv"
	"strings"
	"testing"
	"unicode"

	"github.com/macula-io/macula-lazymesh/internal/meshservices"
)

// squashedWords keeps the characters a reader reads -- letters, digits and
// punctuation -- so the box's own drawing, its padding and the line breaks
// a wrap added drop out and only the words remain.
func squashedWords(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsPunct(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// servicesPanelModel opens the `s` panel at the given width, through the
// leader, the way an operator does.
func servicesPanelModel(t *testing.T, width int, enabled bool) Model {
	t.Helper()
	m := modelAt(t, width, 24)
	if enabled {
		m.meshServices = meshservices.New(&fakeMeshMCP{})
	}
	m, _ = leader(t, m, keyPress("s"))
	if !m.meshServicesExpanded {
		t.Fatal("the leader did not open the mesh services panel")
	}
	return m
}

// TestMeshServicesPanelFitsTheTerminal is the issue's check: every
// rendered line of View() fits the width, with the panel open, disabled
// and enabled, at every width from the narrowest terminal the rest of the
// screen survives (below seven columns the composer keeps its default
// width -- filed separately, not this panel) up to a wide one.
func TestMeshServicesPanelFitsTheTerminal(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		name := "disabled"
		if enabled {
			name = "enabled"
		}
		for width := 7; width <= 120; width++ {
			t.Run(name+"/"+strconv.Itoa(width), func(t *testing.T) {
				m := servicesPanelModel(t, width, enabled)
				if over := linesWiderThan(m.View(), width); len(over) > 0 {
					t.Fatalf("at width %d, %d rendered lines overflow:\n%s", width, len(over), strings.Join(over, "\n"))
				}
			})
		}
	}
}

// TestMeshServiceColumnsFitTheWidth is the invariant behind the sweep
// above: whatever is left after the panel's own chrome, the columns a
// width gets must sum to no more than that -- one cell of overrun is a
// box one cell wider than the terminal. A column that comes back zero is
// not shown, so only the shown ones count.
func TestMeshServiceColumnsFitTheWidth(t *testing.T) {
	for inner := 1; inner <= 120; inner++ {
		cols, widths := meshServiceColumns(inner)
		if len(cols) != len(widths) || len(widths) != 3 {
			t.Fatalf("inner %d: %d columns but %d widths", inner, len(cols), len(widths))
		}
		shown, rendered := 0, 0
		for _, w := range widths {
			if w < 0 {
				t.Fatalf("inner %d: a column is %d cells wide", inner, w)
			}
			if w == 0 {
				continue
			}
			shown++
			rendered += w + 2 // bubbles/table pads every cell by 2
		}
		if rendered > inner {
			t.Fatalf("inner %d: the %d shown columns take %d cells", inner, shown, rendered)
		}
		if inner >= 3 && shown == 0 {
			t.Fatalf("inner %d: no column at all, where one always fits", inner)
		}
	}
}

// TestMeshServicesProcedureComesFirst is Pluto's r=1 finding on #113:
// the procedure is the column that says which row this is, so wherever a
// description is shown beside it, the procedure must have at least as many
// cells. Before the fix, proportionally-shrunk columns gave an 80-column
// terminal procedure=19, description=40.
func TestMeshServicesProcedureComesFirst(t *testing.T) {
	for inner := 3; inner <= 120; inner++ {
		_, widths := meshServiceColumns(inner)
		if widths[2] > 0 && widths[0] < widths[2] {
			t.Fatalf("inner %d: procedure %d cells, description %d", inner, widths[0], widths[2])
		}
	}
}

// TestMeshServicesProcedureKeepsItsFullName: from the width the longest
// procedure name needs plus its own two cells, the procedure column shows
// that name whole -- the panel sizes to the catalogue's longest name, not
// to a share of the terminal.
func TestMeshServicesProcedureKeepsItsFullName(t *testing.T) {
	name := longestProcedureCells()
	for inner := name + 2; inner <= 200; inner++ {
		cols := meshServiceShownColumns(inner)
		if len(cols) == 0 || cols[0].Title != "Procedure" {
			t.Fatalf("inner %d: no procedure column", inner)
		}
		if cols[0].Width < name {
			t.Fatalf("inner %d: procedure column is %d cells for names of %d", inner, cols[0].Width, name)
		}
	}
}

// TestMeshServicesPanelDropsColumnsAsItNarrows: the description goes
// first, the status second, the procedure's name last -- and below a single
// column the list is written as plain rows instead of a table that cannot
// fit (#113).
func TestMeshServicesPanelDropsColumnsAsItNarrows(t *testing.T) {
	// The widths the columns change at follow from the longest procedure
	// name: two columns once the status can have its four cells beside the
	// whole name, three once the description can have its eight beside
	// those two.
	name := longestProcedureCells()
	for _, tc := range []struct {
		inner  int
		column int
	}{
		{2, 0}, {3, 1}, {name + 7, 1}, {name + 8, 2}, {name + 24, 2}, {name + 25, 3}, {120, 3},
	} {
		if got := len(meshServiceShownColumns(tc.inner)); got != tc.column {
			t.Fatalf("inner %d: expected %d column(s), got %d", tc.inner, tc.column, got)
		}
	}
	// The fallback still names every procedure, at a width where no table
	// fits. The wrap breaks words anywhere (two cells is two cells), so
	// compare what is left once the breaks and the box are gone.
	m := servicesPanelModel(t, 6, true)
	panel := squashedWords(stripANSI(m.renderMeshServices()))
	for _, want := range []string{"mcl-", "live", "›"} {
		if !strings.Contains(panel, squashedWords(want)) {
			t.Fatalf("the plain rows lost %q:\n%s", want, panel)
		}
	}
}

// TestMeshServicesWidthZero is Pluto's r=1 finding 2: before the first
// resize the panel has no width to follow, and a sentinel width there made
// View() tens of megabytes of table. Nothing that comes out of a model
// with no size yet should cost more than a screenful.
func TestMeshServicesWidthZero(t *testing.T) {
	for _, mode := range []string{"services", "call", "mesh", "realms"} {
		m := modelAt(t, 0, 0)
		m.meshServices = meshservices.New(&fakeMeshMCP{})
		switch mode {
		case "services":
			m.meshServicesExpanded = true
		case "call":
			m.meshServicesExpanded = true
			m.mode = ModeMeshServiceCall
			m.meshServiceCallProcedure = "mcl-rag/search_chunks_semantic"
		case "mesh":
			m.meshExpanded = true
		case "realms":
			m.realmExpanded = true
		}
		if got := len(m.View()); got >= 64*1024 {
			t.Fatalf("%s: View() is %d bytes with no width yet", mode, got)
		}
	}
}

// TestMeshServicesCallInputFitsTheTerminal covers the panel's other mode:
// a call's procedure name and the args field are both long enough to widen
// the box past the terminal unless the title wraps and the field is given
// the panel's inner width.
func TestMeshServicesCallInputFitsTheTerminal(t *testing.T) {
	for width := 7; width <= 120; width++ {
		t.Run(strconv.Itoa(width), func(t *testing.T) {
			m := modelAt(t, width, 24)
			m.meshServices = meshservices.New(&fakeMeshMCP{})
			m.meshServicesExpanded = true
			m.mode = ModeMeshServiceCall
			m.meshServiceCallProcedure = "mcl-rag/search_chunks_semantic_with_a_very_long_suffix_indeed"
			m.meshServiceCallInput.SetValue(`{"query":"` + strings.Repeat("x", 80) + `"}`)
			if over := linesWiderThan(m.View(), width); len(over) > 0 {
				t.Fatalf("at width %d, %d rendered lines overflow:\n%s", width, len(over), strings.Join(over, "\n"))
			}
		})
	}
}
