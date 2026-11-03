package agent

import (
	"strings"
	"sync"
)

// Droppable reports whether ev may be held back from a consumer that is
// not keeping up: only a streamed delta, because the turn's completed
// EventAssistantMessage carries its text again (#98). Everything else, an
// approval request, a turn-complete marker, a ring, an error, must arrive.
func (ev Event) Droppable() bool { return ev.Kind == EventAssistantDelta }

// Outlet delivers events to one consumer's channel, in order, and never
// loses one (#98). Send never blocks: it queues the event, and the
// Outlet's own goroutine hands the queue to the consumer as fast as it
// reads. A consumer that is not reading (a TUI whose operator is in
// $EDITOR) therefore holds back nobody but itself, and every sender
// shares the one Outlet of a consumer, so their events keep the order
// they were sent in. Deltas queued back to back are merged into one, so
// a stalled consumer costs one entry per event that must arrive plus the
// text it has not read, not one per chunk.
//
// A nil channel means there is no consumer: the Outlet takes everything
// and delivers nothing. Delivery stops when done closes.
type Outlet struct {
	ch   chan<- Event
	done <-chan struct{}
	wake chan struct{}

	mu     sync.Mutex
	queue  []queued
	closed bool // Close was called: close ch once the queue is delivered
	exited bool // the delivering goroutine has returned
}

// queued is one event waiting for the consumer. A delta keeps its text in
// held, so merging the next delta into it appends instead of copying.
type queued struct {
	ev   Event
	held *strings.Builder
}

// NewOutlet starts an Outlet delivering to ch until done closes.
func NewOutlet(done <-chan struct{}, ch chan<- Event) *Outlet {
	o := &Outlet{ch: ch, done: done, wake: make(chan struct{}, 1)}
	if ch != nil {
		go o.deliver()
	}
	return o
}

// Send queues ev for the consumer. It never blocks.
func (o *Outlet) Send(ev Event) {
	if o.ch == nil {
		return
	}
	o.mu.Lock()
	if o.closed {
		o.mu.Unlock()
		return
	}
	if n := len(o.queue); ev.Droppable() && n > 0 && o.queue[n-1].held != nil {
		o.queue[n-1].held.WriteString(ev.Text)
	} else {
		q := queued{ev: ev}
		if ev.Droppable() {
			q.held = &strings.Builder{}
			q.held.WriteString(ev.Text)
		}
		o.queue = append(o.queue, q)
	}
	o.mu.Unlock()
	o.signal()
}

// Close stops taking events and closes the consumer's channel once
// everything already queued has been delivered, or at once if delivery
// has already stopped. Only an Outlet that owns its channel may be
// closed.
func (o *Outlet) Close() {
	if o.ch == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return
	}
	o.closed = true
	if o.exited {
		close(o.ch)
		return
	}
	o.signal()
}

func (o *Outlet) signal() {
	select {
	case o.wake <- struct{}{}:
	default:
	}
}

// deliver is the Outlet's goroutine: it hands queued events to the
// consumer one at a time, waiting on the consumer, never on a sender.
func (o *Outlet) deliver() {
	defer o.exit()
	for {
		ev, ok := o.next()
		if !ok {
			if o.drainedAndClosed() {
				return
			}
			select {
			case <-o.wake:
				continue
			case <-o.done:
				return
			}
		}
		select {
		case o.ch <- ev:
		case <-o.done:
			return
		}
	}
}

// next takes the queue's head, if there is one.
func (o *Outlet) next() (Event, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.queue) == 0 {
		return Event{}, false
	}
	q := o.queue[0]
	o.queue[0] = queued{}
	o.queue = o.queue[1:]
	if q.held != nil {
		q.ev.Text = q.held.String()
	}
	return q.ev, true
}

func (o *Outlet) drainedAndClosed() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.closed && len(o.queue) == 0
}

// exit records that delivery stopped, and closes the consumer's channel
// if Close already asked for it; otherwise Close will.
func (o *Outlet) exit() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.exited = true
	if o.closed {
		close(o.ch)
	}
}
