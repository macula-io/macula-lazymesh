package tui

// macula-io/macula-lazymesh#112: the ring pop-up rendered the peer's own
// purpose verbatim in a bordered box, so a long purpose made the box wider
// than the terminal and the terminal wrapped it -- the screen gained rows
// the layout never reserved, the same class as #86.

import (
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/charmbracelet/lipgloss"
)

// words keeps only the characters a reader reads: letters, digits and
// punctuation, so box drawing, padding and line breaks drop out.
func words(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsPunct(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ringPopupModel is a model showing the ring pop-up for a ring whose
// purpose is long enough to force the old layout past any of these widths.
func ringPopupAtWidth(t *testing.T, width int) Model {
	t.Helper()
	m := modelAt(t, width, 24)
	m.pendingRingPopup = &pendingRing{
		RingID:      "r1",
		Peer:        "00d52e2fbb71b85ab07acafcdaf3cef8df4b304e297a86ff7c2055f6cb6598c0",
		PeerPetname: "witty-lavender-lynx-4301",
		Purpose:     "please help me land this: " + strings.Repeat("the purpose runs on and on ", 6),
	}
	m.mode = ModeRingPopup
	return m
}

// TestRingPopupFitsTheTerminal is the issue's check: every rendered line
// of View() fits the width, with the ring pop-up showing.
func TestRingPopupFitsTheTerminal(t *testing.T) {
	for _, width := range []int{30, 40, 60, 80, 120} {
		t.Run(strconv.Itoa(width), func(t *testing.T) {
			m := ringPopupAtWidth(t, width)
			if over := linesWiderThan(m.View(), width); len(over) > 0 {
				t.Fatalf("at width %d, %d rendered lines overflow:\n%s", width, len(over), strings.Join(over, "\n"))
			}
		})
	}
}

// TestRingPopupWrapsRatherThanCuts: fitting the width must not cost the
// operator anything the peer wrote. Every rune of the purpose is still on
// screen, in order, only with line breaks added.
func TestRingPopupWrapsRatherThanCuts(t *testing.T) {
	width := 40
	m := ringPopupAtWidth(t, width)
	popup := m.renderRingPopup()

	// The words, without the box's own drawing or the line breaks the wrap
	// added: what is left has to still be the whole sentence.
	if !strings.Contains(words(stripANSI(popup)), words("please help me land this: the purpose runs on and on")) {
		t.Fatalf("the pop-up lost peer text at width %d:\n%s", width, popup)
	}
	// The way out is still named: the answer keys are hidden until the
	// pop-up has been on screen for its arming delay (#59/#62), which this
	// model has not waited out.
	for _, want := range []string{"Incoming ring", "[esc] Later"} {
		if !strings.Contains(popup, want) {
			t.Fatalf("the pop-up lost %q at width %d:\n%s", want, width, popup)
		}
	}
}

// armedRingPopupAt is a model showing the ring pop-up with its answer keys
// awake: someone paused for longer than the guard (popuparm.go). The guard's
// own tests are in popuparm_test.go.
func armedRingPopupAt(t *testing.T, width, height int, purpose string) Model {
	t.Helper()
	m := modelAt(t, width, height)
	m.pendingRingPopup = &pendingRing{RingID: "r1", Peer: "00d52e2f", PeerPetname: "eve", Purpose: purpose}
	m.mode = ModeRingPopup
	t0 := time.Unix(1000, 0)
	m.ringArm = popupArm{shownAt: t0}
	m.now = func() time.Time { return t0.Add(time.Second) }
	return m
}

// TestRingPopupFitsEveryWidth is #112's review finding 3: at 1..20 columns
// the box was wider than the terminal (an unwrapped title and an inner
// floor of ten cells), so the terminal wrapped it. Every width a terminal
// can actually be, armed and asleep, on peer text chosen to be wide.
func TestRingPopupFitsEveryWidth(t *testing.T) {
	purpose := strings.Repeat("x", 300) + " 世界世界世界 " + strings.Repeat("y", 50)
	for width := 1; width <= 120; width++ {
		for _, armed := range []bool{false, true} {
			m := armedRingPopupAt(t, width, 40, purpose)
			if !armed {
				m.ringArm = popupArm{}
			}
			if over := linesWiderThan(m.View(), width); len(over) > 0 {
				t.Fatalf("at width %d (armed=%v), %d rendered lines overflow:\n%s",
					width, armed, len(over), strings.Join(over, "\n"))
			}
		}
	}
}

// TestRingPopupNeverOffersAnAnswerWithoutARefusal is review finding 1: with
// accept first in the priority order, widths 1..43 showed "[a] Answer" on
// its own -- how to accept a ring, with no way to refuse it on screen.
func TestRingPopupNeverOffersAnAnswerWithoutARefusal(t *testing.T) {
	var bad []int
	for width := 1; width <= 120; width++ {
		popup := stripANSI(armedRingPopupAt(t, width, 40, "hi").renderRingPopup())
		offers := strings.Contains(popup, "[a]") || strings.Contains(popup, "[t]")
		refuses := strings.Contains(popup, "[d]") && strings.Contains(popup, "[esc]")
		if offers && !refuses {
			bad = append(bad, width)
		}
	}
	if len(bad) > 0 {
		t.Fatalf("an answer was offered without [d] and [esc] at widths %v", bad)
	}
}

// TestRingPopupActionsStayOnScreen is review finding 2: a peer chooses how
// long its purpose is, and a sixty-line one made a 73-row view on a 24-row
// terminal -- the actions row, the only way to answer, was off screen.
func TestRingPopupActionsStayOnScreen(t *testing.T) {
	const width, height = 80, 24
	m := armedRingPopupAt(t, width, height, strings.Repeat("line\n", 60))
	view := m.View()
	if rows := strings.Split(view, "\n"); len(rows) > height {
		t.Fatalf("the view is %d rows on a %d-row terminal", len(rows), height)
	}
	popup := stripANSI(m.renderRingPopup())
	for _, want := range []string{"[a] Answer", "[d] Decline", "[esc] Later", "Incoming ring", "more line"} {
		if !strings.Contains(popup, want) {
			t.Fatalf("the pop-up lost %q:\n%s", want, popup)
		}
	}
}

// TestRingPopupFitsShortTerminals: the height clamp is not only for long
// purposes -- a short terminal with a short purpose still has to fit. Below
// three rows the status strip alone is taller than the terminal, which no
// pop-up can fix, so three is the shortest height pinned here.
func TestRingPopupFitsShortTerminals(t *testing.T) {
	for _, height := range []int{3, 4, 5, 6, 10} {
		m := armedRingPopupAt(t, 40, height, "please help")
		if rows := strings.Split(m.View(), "\n"); len(rows) > height {
			t.Fatalf("at height %d the view is %d rows", height, len(rows))
		}
	}
}

// TestWrapCellsKeepsASpellingWhole is the review's own observation: a
// control shown as <U+200B> is one character, and wrapping must move it
// whole rather than end a row with "<U+20" and start the next with "0B>".
// Below its own eight cells nothing can keep it whole and fit, so the
// widths where a whole spelling is possible are the ones pinned here.
func TestWrapCellsKeepsASpellingWhole(t *testing.T) {
	for width := 8; width <= 60; width++ {
		lines := wrapCells(termTextOf(strings.Repeat("a", 3)+"\u200b"+strings.Repeat("b", 40)), width)
		if !strings.Contains(strings.Join(lines, ""), "<U+200B>") {
			t.Fatalf("at width %d the spelling was split: %q", width, lines)
		}
		for _, line := range lines {
			if lipgloss.Width(line) > width {
				t.Fatalf("at width %d a line is %d cells: %q", width, lipgloss.Width(line), line)
			}
		}
	}
}
