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
// and enabled.
func TestMeshServicesPanelFitsTheTerminal(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, width := range []int{30, 40, 60, 80, 120} {
			name := "disabled"
			if enabled {
				name = "enabled"
			}
			t.Run(name+"/"+strconv.Itoa(width), func(t *testing.T) {
				m := servicesPanelModel(t, width, enabled)
				if over := linesWiderThan(m.View(), width); len(over) > 0 {
					t.Fatalf("at width %d, %d rendered lines overflow:\n%s", width, len(over), strings.Join(over, "\n"))
				}
			})
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
	for _, width := range []int{30, 40, 60, 80} {
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
