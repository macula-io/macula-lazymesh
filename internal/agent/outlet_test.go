package agent

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// #98: a flood of deltas into a consumer that is not reading, then an
// approval request: the approval still arrives, after every delta's text,
// and no delta text is lost or reordered on the way.
func TestOutlet_FloodOfDeltasThenApprovalArrivesInOrder(t *testing.T) {
	ch := make(chan Event, 4)
	o := NewOutlet(ch)
	done := make(chan struct{})
	defer close(done)

	var want strings.Builder
	for i := 0; i < 500; i++ {
		chunk := fmt.Sprintf("[%d]", i)
		want.WriteString(chunk)
		o.Send(done, Event{Kind: EventAssistantDelta, Text: chunk})
	}
	sent := make(chan bool, 1)
	go func() { sent <- o.Send(done, Event{Kind: EventApprovalRequested, ID: "approve-1"}) }()

	var got strings.Builder
	timeout := time.After(5 * time.Second)
	for {
		select {
		case ev := <-ch:
			if ev.Kind == EventApprovalRequested {
				if got.String() != want.String() {
					t.Fatalf("delta text before the approval:\n got %q\nwant %q", got.String(), want.String())
				}
				if !<-sent {
					t.Fatal("Send reported the approval as not taken")
				}
				return
			}
			got.WriteString(ev.Text)
		case <-timeout:
			t.Fatal("the approval request never arrived after a flood of deltas")
		}
	}
}

// #98: with no consumer at all (a nil channel: the TUI's in a headless
// run, the socket's when there is none) nothing blocks.
func TestOutlet_NoConsumerNeverBlocks(t *testing.T) {
	o := NewOutlet(nil)
	finished := make(chan struct{})
	go func() {
		o.Send(nil, Event{Kind: EventAssistantDelta, Text: "x"})
		o.Send(nil, Event{Kind: EventListening})
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("an Outlet without a consumer blocked its sender")
	}
}

// #98: a sender waiting on a consumer that never reads is released when
// done closes, and is told the event was not taken.
func TestOutlet_DoneReleasesABlockedSender(t *testing.T) {
	ch := make(chan Event)
	o := NewOutlet(ch)
	done := make(chan struct{})
	sent := make(chan bool, 1)
	go func() { sent <- o.Send(done, Event{Kind: EventListening}) }()
	close(done)
	select {
	case ok := <-sent:
		if ok {
			t.Fatal("Send reported an event nobody took as delivered")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("closing done did not release the blocked sender")
	}
}
