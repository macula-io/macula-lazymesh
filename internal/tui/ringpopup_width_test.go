package tui

// macula-io/macula-lazymesh#112: the ring pop-up rendered the peer's own
// purpose verbatim in a bordered box, so a long purpose made the box wider
// than the terminal and the terminal wrapped it -- the screen gained rows
// the layout never reserved, the same class as #86.

import (
	"strconv"
	"strings"
	"testing"
	"unicode"
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
