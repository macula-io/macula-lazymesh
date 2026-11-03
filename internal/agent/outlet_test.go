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
	done := make(chan struct{})
	defer close(done)
	o := NewOutlet(done, ch)

	var want strings.Builder
	for i := 0; i < 500; i++ {
		chunk := fmt.Sprintf("[%d]", i)
		want.WriteString(chunk)
		o.Send(Event{Kind: EventAssistantDelta, Text: chunk})
	}
	o.Send(Event{Kind: EventApprovalRequested, ID: "approve-1"})

	var got strings.Builder
	timeout := time.After(5 * time.Second)
	for {
		select {
		case ev := <-ch:
			if ev.Kind == EventApprovalRequested {
				if got.String() != want.String() {
					t.Fatalf("delta text before the approval:\n got %q\nwant %q", got.String(), want.String())
				}
				return
			}
			got.WriteString(ev.Text)
		case <-timeout:
			t.Fatal("the approval request never arrived after a flood of deltas")
		}
	}
}

// #98 r=2: Send never blocks, however long the consumer does not read, so
// one consumer cannot hold back a sender that also feeds another.
func TestOutlet_SendNeverBlocksOnAConsumerThatIsNotReading(t *testing.T) {
	done := make(chan struct{})
	defer close(done)
	o := NewOutlet(done, make(chan Event)) // nobody reads
	finished := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			o.Send(Event{Kind: EventToolResult, Text: "r"})
			o.Send(Event{Kind: EventAssistantDelta, Text: "d"})
		}
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("Send blocked on a consumer that is not reading")
	}
}

// #98: with no consumer at all (a nil channel: the TUI's in a headless
// run, the socket's when there is none) nothing is delivered and nothing
// blocks.
func TestOutlet_NoConsumerNeverBlocks(t *testing.T) {
	o := NewOutlet(nil, nil)
	o.Send(Event{Kind: EventAssistantDelta, Text: "x"})
	o.Send(Event{Kind: EventListening})
	o.Close()
}

// Close delivers what is already queued, then closes the channel, so a
// consumer ranging over it sees every event and then the end.
func TestOutlet_CloseDeliversTheQueueThenClosesTheChannel(t *testing.T) {
	ch := make(chan Event)
	done := make(chan struct{})
	defer close(done)
	o := NewOutlet(done, ch)
	o.Send(Event{Kind: EventToolResult, Text: "one"})
	o.Send(Event{Kind: EventListening})
	o.Close()

	var kinds []EventKind
	for ev := range ch {
		kinds = append(kinds, ev.Kind)
	}
	if len(kinds) != 2 || kinds[0] != EventToolResult || kinds[1] != EventListening {
		t.Fatalf("delivered %v, want the tool result then listening", kinds)
	}
}

// When done has already stopped delivery, Close still closes the channel.
func TestOutlet_CloseAfterDoneClosesTheChannel(t *testing.T) {
	ch := make(chan Event)
	done := make(chan struct{})
	o := NewOutlet(done, ch)
	o.Send(Event{Kind: EventListening})
	close(done)
	time.Sleep(50 * time.Millisecond)
	o.Close()
	select {
	case _, ok := <-ch:
		for ok {
			_, ok = <-ch
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the channel was never closed")
	}
}
