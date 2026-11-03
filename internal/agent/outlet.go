package agent

import "strings"

// Droppable reports whether ev may be lost to a consumer that is not
// keeping up: only a streamed delta, because the turn's completed
// EventAssistantMessage carries its text again (#98). Everything else, an
// approval request, a turn-complete marker, a ring, an error, must arrive.
func (ev Event) Droppable() bool { return ev.Kind == EventAssistantDelta }

// Outlet delivers events to one consumer's channel and never loses one
// that matters (#98). A delta the consumer has no room for is not thrown
// away but merged with the delta text still waiting, and offered again
// with the next event; every other event waits until the consumer takes
// it, after any delta text that came before it. A nil channel means there
// is no consumer, and an Outlet on it takes everything and delivers
// nothing. Not safe for concurrent use: each sender owns its own Outlet.
type Outlet struct {
	ch chan<- Event
	// held is the delta text the consumer had no room for yet; holding
	// says there is some (it may be empty text).
	held    strings.Builder
	holding bool
}

// NewOutlet returns an Outlet that delivers to ch.
func NewOutlet(ch chan<- Event) *Outlet { return &Outlet{ch: ch} }

// Send delivers ev, or holds it as described on Outlet. It returns false
// only when done closed before an event that must arrive was taken.
func (o *Outlet) Send(done <-chan struct{}, ev Event) bool {
	if o.ch == nil {
		return true
	}
	if ev.Droppable() {
		o.held.WriteString(ev.Text)
		o.holding = true
		// Build the merged delta only when there is room for it: a
		// consumer that stays full must not cost a copy of everything
		// held so far on every new delta.
		if cap(o.ch) > 0 && len(o.ch) == cap(o.ch) {
			return true
		}
		select {
		case o.ch <- o.heldDelta():
			o.release()
		default:
		}
		return true
	}
	if o.holding {
		if !Deliver(done, o.ch, o.heldDelta()) {
			return false
		}
		o.release()
	}
	return Deliver(done, o.ch, ev)
}

func (o *Outlet) heldDelta() Event {
	return Event{Kind: EventAssistantDelta, Text: o.held.String()}
}

func (o *Outlet) release() {
	o.held.Reset()
	o.holding = false
}

// Deliver sends ev on ch, waiting until ch takes it or done closes, and
// reports whether it was taken. A nil ch has no consumer: nothing is sent
// and Deliver reports true, so a sink nobody reads can never hang a
// sender.
func Deliver(done <-chan struct{}, ch chan<- Event, ev Event) bool {
	if ch == nil {
		return true
	}
	select {
	case ch <- ev:
		return true
	case <-done:
		return false
	}
}
