package tui

import (
	"fmt"
	"time"
)

// popupArmDelay is how long a pop-up's answer keys stay asleep after the
// pop-up appears, and after the last key that reached it (macula-lazymesh#59).
//
// It is a constant, not a setting: a guard a config file can shorten is a
// guard an attacker who can write the config has already shortened, and the
// whole point is that a keystroke meant for a message never answers a prompt.
const popupArmDelay = 600 * time.Millisecond

// popupArm is one pop-up's arming state. An answer key acts only once the
// guard has passed since the LATER of the two times, so a person still typing
// keeps extending the sleep instead of ending it -- the key that answers has
// to arrive after a pause, never in the middle of a word.
type popupArm struct {
	shownAt   time.Time
	lastKeyAt time.Time
}

// armedAt reports whether answer keys may act at now.
func (a popupArm) armedAt(now time.Time) bool {
	since := a.shownAt
	if a.lastKeyAt.After(since) {
		since = a.lastKeyAt
	}
	return !since.IsZero() && now.Sub(since) >= popupArmDelay
}

// armHint is the sentence a pop-up shows while its answer keys are asleep, so
// a key that does nothing says why instead of reading as a broken pop-up.
func armHint() string {
	return fmt.Sprintf("keys arm in %g s", popupArmDelay.Seconds())
}

// clock is the time the arming guard reads. Tests replace now, so a 600 ms
// guard costs no wall clock and a one-second wait is not a sleeping test.
func (m Model) clock() time.Time {
	if m.now != nil {
		return m.now()
	}
	return time.Now()
}

// rememberFocus records where focus was before a pop-up took it, so closing
// the pop-up puts it back -- a person who was composing carries on composing,
// with the draft they had. A second prompt of the same kind leaves the
// remembered focus alone: restoring into the pop-up being replaced would
// strand the person in it.
func (m Model) rememberFocus(popping Mode) Model {
	if m.mode == popping {
		return m
	}
	m.returnTo = m.mode
	return m
}
