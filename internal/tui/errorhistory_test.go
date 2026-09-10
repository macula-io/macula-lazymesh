package tui

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func refreshFailure(text string) refreshMsg {
	return refreshMsg{err: errors.New(text)}
}

// The rule that makes auto-pop usable at all. The refresh loop runs every
// two seconds, so a dialog that opened on every occurrence of the same
// failure would put the program behind a box the operator cannot get out
// of. One pop for the first sighting, silence for the repeats.
func TestAutoPop_OnlyOncePerDistinctError(t *testing.T) {
	m := newTestModel(t)

	m, _ = m.handleRefresh(refreshFailure(realRefreshError))
	if m.mode != ModeErrorPopup {
		t.Fatal("a new error should show itself without a keypress")
	}

	// Operator reads it and closes.
	updated, _ := m.handleKey(keyPress("esc"))
	m = updated.(Model)

	// Same failure, four more refresh cycles.
	for i := 0; i < 4; i++ {
		m, _ = m.handleRefresh(refreshFailure(realRefreshError))
		if m.mode == ModeErrorPopup {
			t.Fatalf("repeat %d re-opened the dialog; it must stay quiet after the first sighting", i+1)
		}
	}

	if got := m.errorHistory[len(m.errorHistory)-1].count; got != 5 {
		t.Fatalf("expected the repeats to be counted, got count=%d", got)
	}
	if len(m.errorHistory) != 1 {
		t.Fatalf("expected one distinct error in history, got %d", len(m.errorHistory))
	}
}

func TestAutoPop_ADifferentErrorPopsAgain(t *testing.T) {
	m := newTestModel(t)

	m, _ = m.handleRefresh(refreshFailure(realRefreshError))
	updated, _ := m.handleKey(keyPress("esc"))
	m = updated.(Model)

	m, _ = m.handleRefresh(refreshFailure("mesh_agents: connection refused"))
	if m.mode != ModeErrorPopup {
		t.Fatal("a genuinely different error must show itself")
	}
	if len(m.errorHistory) != 2 {
		t.Fatalf("expected two distinct errors in history, got %d", len(m.errorHistory))
	}
}

// Stealing the keyboard mid-sentence loses whatever was being typed and is
// the single most annoying thing this feature could do.
func TestAutoPop_WaitsWhileComposing(t *testing.T) {
	m := newTestModel(t)
	updated, _ := m.handleKey(keyPress("i"))
	m = updated.(Model)
	if m.mode != ModeInsert {
		t.Fatalf("expected insert mode for this test, got %v", m.mode)
	}

	m, _ = m.handleRefresh(refreshFailure(realRefreshError))
	if m.mode != ModeInsert {
		t.Fatal("an error must not interrupt composing")
	}

	// Leaving insert mode is when the operator is free to be shown it.
	updated, _ = m.handleKey(keyPress("esc"))
	m = updated.(Model)
	if m.mode != ModeErrorPopup {
		t.Fatal("the held error should appear once composing ends")
	}
}

// Same reasoning as composing: the operator is reading something they
// asked for. Hold it rather than overwrite it, and never drop it.
func TestAutoPop_WaitsWhileAnOverlayIsOpen(t *testing.T) {
	m := newTestModel(t)
	updated, _ := m.handleKey(keyPress("m")) // mesh overlay
	m = updated.(Model)
	if !m.meshExpanded {
		t.Fatal("expected the mesh overlay open for this test")
	}

	m, _ = m.handleRefresh(refreshFailure(realRefreshError))
	if m.mode == ModeErrorPopup {
		t.Fatal("an error must not cover an overlay the operator opened")
	}

	updated, _ = m.handleKey(keyPress("m")) // close it
	m = updated.(Model)
	if m.mode != ModeErrorPopup {
		t.Fatal("the held error should appear once the overlay is closed")
	}
}

// Raf's reason for wanting history: when an error repeats, the first
// sighting often carries detail the later ones lose.
func TestErrorHistory_KeepsOlderErrorsReachable(t *testing.T) {
	m := newTestModel(t)
	texts := []string{
		"mesh_rooms: dial tcp: connection refused",
		realRefreshError,
		"mesh_agents: context deadline exceeded",
	}
	for _, text := range texts {
		m, _ = m.handleRefresh(refreshFailure(text))
		updated, _ := m.handleKey(keyPress("esc"))
		m = updated.(Model)
	}

	updated, _ := m.handleKey(keyPress("E"))
	m = updated.(Model)
	if m.errorPopup.text != texts[2] {
		t.Fatalf("E should open the newest error, got %q", m.errorPopup.text)
	}

	updated, _ = m.handleKey(keyPress("p")) // previous, older
	m = updated.(Model)
	if m.errorPopup.text != texts[1] {
		t.Fatalf("p should step back to the previous error, got %q", m.errorPopup.text)
	}

	updated, _ = m.handleKey(keyPress("p"))
	m = updated.(Model)
	if m.errorPopup.text != texts[0] {
		t.Fatalf("p should reach the oldest error, got %q", m.errorPopup.text)
	}

	// Oldest is a wall, not a wrap: wrapping round to the newest makes it
	// impossible to tell you have reached the end of the list.
	updated, _ = m.handleKey(keyPress("p"))
	m = updated.(Model)
	if m.errorPopup.text != texts[0] {
		t.Fatalf("p at the oldest entry should stay put, got %q", m.errorPopup.text)
	}

	updated, _ = m.handleKey(keyPress("n"))
	m = updated.(Model)
	if m.errorPopup.text != texts[1] {
		t.Fatalf("n should step forward again, got %q", m.errorPopup.text)
	}
}

func TestErrorHistory_ShowsPositionAndRepeatCount(t *testing.T) {
	m := newTestModel(t)
	m, _ = m.handleRefresh(refreshFailure("mesh_rooms: connection refused"))
	updated, _ := m.handleKey(keyPress("esc"))
	m = updated.(Model)
	for i := 0; i < 2; i++ {
		m, _ = m.handleRefresh(refreshFailure(realRefreshError))
	}
	updated, _ = m.handleKey(keyPress("E"))
	m = updated.(Model)

	out := m.renderErrorPopup()
	if !strings.Contains(out, "2 of 2") {
		t.Fatalf("expected the position in the list, got:\n%s", out)
	}
	if !strings.Contains(out, "2 times") {
		t.Fatalf("expected the repeat count, got:\n%s", out)
	}
	t.Logf("history heading and navigation hints:\n%s", out)
}

// An unbounded list of errors in a long-running TUI is a slow leak.
func TestErrorHistory_IsBounded(t *testing.T) {
	m := newTestModel(t)
	for i := 0; i < errorHistoryMax+10; i++ {
		m, _ = m.handleRefresh(refreshFailure(errorText(i)))
		updated, _ := m.handleKey(keyPress("esc"))
		m = updated.(Model)
	}
	if len(m.errorHistory) != errorHistoryMax {
		t.Fatalf("expected history capped at %d, got %d", errorHistoryMax, len(m.errorHistory))
	}
	if oldest := m.errorHistory[0].text; oldest != errorText(10) {
		t.Fatalf("expected the oldest entries dropped first, oldest is %q", oldest)
	}
}

func errorText(i int) string {
	return "mesh_rooms: failure number " + time.Duration(i).String()
}

// A recurrence should move an error to the front of the operator's
// attention without losing when it was first seen.
func TestErrorHistory_RepeatUpdatesLastSeenNotFirstSeen(t *testing.T) {
	m := newTestModel(t)
	m, _ = m.handleRefresh(refreshFailure(realRefreshError))
	first := m.errorHistory[0].first

	time.Sleep(2 * time.Millisecond)
	m, _ = m.handleRefresh(refreshFailure(realRefreshError))

	rec := m.errorHistory[0]
	if !rec.first.Equal(first) {
		t.Fatalf("first-seen must not move: was %v, now %v", first, rec.first)
	}
	if !rec.last.After(first) {
		t.Fatalf("last-seen should advance on a repeat: first=%v last=%v", first, rec.last)
	}
}
