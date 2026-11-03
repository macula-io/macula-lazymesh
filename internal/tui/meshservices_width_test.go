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
// box one cell wider than the terminal.
func TestMeshServiceColumnsFitTheWidth(t *testing.T) {
	for inner := 1; inner <= 60; inner++ {
		cols, widths := meshServiceColumns(inner)
		if len(cols) != len(widths) {
			t.Fatalf("inner %d: %d columns but %d widths", inner, len(cols), len(widths))
		}
		rendered := 0
		for _, w := range widths {
			if w < 1 {
				t.Fatalf("inner %d: a column is %d cells wide", inner, w)
			}
			rendered += w + 2 // bubbles/table pads every cell by 2
		}
		if rendered > inner {
			t.Fatalf("inner %d: the %d columns take %d cells", inner, len(cols), rendered)
		}
		if inner < 3 && len(cols) > 0 {
			t.Fatalf("inner %d: a table was chosen where no column fits", inner)
		}
	}
}

// TestMeshServicesPanelDropsColumnsAsItNarrows: the description goes
// first, the status second, the procedure's name last -- and below a single
// column the list is written as plain rows instead of a table that cannot
// fit (#113).
func TestMeshServicesPanelDropsColumnsAsItNarrows(t *testing.T) {
	for _, tc := range []struct {
		inner  int
		column int
	}{
		{3, 1}, {5, 1}, {6, 2}, {8, 2}, {9, 3}, {40, 3},
	} {
		cols, _ := meshServiceColumns(tc.inner)
		if len(cols) != tc.column {
			t.Fatalf("inner %d: expected %d column(s), got %d", tc.inner, tc.column, len(cols))
		}
	}
	if cols, _ := meshServiceColumns(2); cols != nil {
		t.Fatalf("inner 2: expected no table, got %d columns", len(cols))
	}
	// The fallback still names every procedure, at a width where no table
	// fits. The wrap breaks words anywhere (two cells is two cells), so
	// compare what is left once the breaks and the box are gone.
	m := servicesPanelModel(t, 6, true)
	panel := squashedWords(stripANSI(m.renderMeshServices()))
	for _, want := range []string{"mcl-", "live"} {
		if !strings.Contains(panel, squashedWords(want)) {
			t.Fatalf("the plain rows lost %q:\n%s", want, panel)
		}
	}
}

// TestMeshServicesPanelKeepsItsNotice: fitting the width must not cost the
// operator the sentence that says why the panel is empty.
func TestMeshServicesPanelKeepsItsNotice(t *testing.T) {
	width := 40
	m := servicesPanelModel(t, width, false)
	// Line breaks may fall mid-word (the same character wrap the approval
	// pop-up uses), so compare what is left once breaks and the box's own
	// drawing are gone: every word of the notice has to still be there.
	panel := squashedWords(stripANSI(m.View()))
	for _, want := range []string{"disabled", "mesh_services_enabled", "config.yaml"} {
		if !strings.Contains(panel, squashedWords(want)) {
			t.Fatalf("the panel lost %q at width %d:\n%s", want, width, panel)
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
