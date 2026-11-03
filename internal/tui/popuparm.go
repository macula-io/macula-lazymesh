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

// popupIdleDelay is how long the composer has to be still before a pop-up may
// take the screen (macula-lazymesh#62). With the composer always focused, "the
// person is composing" is "a key arrived in the last two seconds": without
// this a ring would never pop, and an approval would keep landing mid-word,
// which is the situation the arming guard then has to clean up after.
const popupIdleDelay = 2 * time.Second

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
//
// It is a hint, not the guard: the screen redraws on the model's own refresh
// tick, so the sentence can outlive the guard by up to that interval. Adding a
// timer to clear it one guard in would be a second clock for the same fact.
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
// with the draft they had. Each pop-up keeps its own: the two can be open at
// once (a ring over an approval or the other way round), and one shared slot
// would hand focus back to the wrong pop-up, or to a pop-up that has since
// been closed. A second prompt of the same kind leaves its own slot alone.
func (m Model) rememberFocus(popping Mode) Model {
	if m.mode == popping {
		return m
	}
	switch popping {
	case ModeApprovalPopup:
		m.approvalReturnTo = m.mode
	case ModeRingPopup:
		m.ringReturnTo = m.mode
	}
	return m
}

// refocus hands focus back after a pop-up closes. It goes to the mode that
// pop-up took focus from, except that a pop-up with nothing pending is
// skipped: returning into an empty prompt leaves a person pressing keys at a
// screen that ignores them, with nothing to close. A pop-up focus DOES come
// back to is re-armed from now -- the keys typed while the other pop-up was
// up were never seen by its guard, so it starts again rather than resuming
// where it left off.
func (m Model) refocus(target Mode) Model {
	now := m.clock()
	switch target {
	case ModeApprovalPopup:
		if m.pendingApproval == nil {
			m.mode = ModeCompose
			return m
		}
		m.approvalArm = popupArm{shownAt: now}
	case ModeRingPopup:
		if m.pendingRingPopup == nil {
			m.mode = ModeCompose
			return m
		}
		m.ringArm = popupArm{shownAt: now}
	}
	m.mode = target
	return m
}
