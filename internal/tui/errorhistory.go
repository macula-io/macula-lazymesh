package tui

import "time"

// errorHistoryMax bounds what a long-running session accumulates. The
// list exists so a repeat does not bury the first sighting, not as a log
// -- that is what the l overlay and agent.log are for.
const errorHistoryMax = 20

// errorRecord is one DISTINCT error and how often it has come back. The
// refresh loop runs every two seconds, so the same failure arrives over
// and over; collapsing those into one record with a count is what makes
// both the history and the auto-pop usable.
type errorRecord struct {
	text  string
	first time.Time
	last  time.Time
	count int
}

// recordError files err into the history and reports whether this is the
// first time it has been seen. Identical text is the definition of "same
// error" here: a repeating refresh failure produces byte-identical
// strings, while a genuinely different failure reads differently and
// deserves to be shown on its own.
//
// A repeat keeps its original first-seen. That is the point of the list:
// Raf's reasoning is that the earliest sighting often carries detail the
// later ones lose, so nothing about the first occurrence is overwritten.
func (m *Model) recordError(err error, now time.Time) (isNew bool) {
	if err == nil {
		return false
	}
	text := err.Error()

	for i := range m.errorHistory {
		if m.errorHistory[i].text == text {
			m.errorHistory[i].count++
			m.errorHistory[i].last = now
			return false
		}
	}

	m.errorHistory = append(m.errorHistory, errorRecord{
		text:  text,
		first: now,
		last:  now,
		count: 1,
	})
	if len(m.errorHistory) > errorHistoryMax {
		m.errorHistory = m.errorHistory[len(m.errorHistory)-errorHistoryMax:]
	}
	return true
}

// operatorIsFree reports whether opening a pop-up right now would take
// the screen away from something the operator is in the middle of.
// Composing loses typing; an open overlay is something they asked to
// look at. Both are worth waiting for.
func (m Model) operatorIsFree() bool {
	return m.mode == ModeNormal &&
		!m.meshExpanded &&
		!m.meshServicesExpanded &&
		!m.realmExpanded
}

// maybeAutoPopError shows a held error the moment the operator is free to
// see it. Called after every keypress and on every tick, so an error that
// arrived mid-compose appears on leaving insert mode rather than being
// dropped or forced through immediately.
func (m Model) maybeAutoPopError() Model {
	if !m.autoPopPending || !m.operatorIsFree() || len(m.errorHistory) == 0 {
		return m
	}
	m.autoPopPending = false
	return m.openErrorPopup(len(m.errorHistory) - 1)
}

// openErrorPopup shows history entry i, clamped into range.
func (m Model) openErrorPopup(i int) Model {
	if len(m.errorHistory) == 0 {
		return m
	}
	if i < 0 {
		i = 0
	}
	if i > len(m.errorHistory)-1 {
		i = len(m.errorHistory) - 1
	}
	e := newErrorPopup(m.errorHistory[i].text, m.width, m.height)
	e.index = i
	m.errorPopup = &e
	m.mode = ModeErrorPopup
	return m
}
