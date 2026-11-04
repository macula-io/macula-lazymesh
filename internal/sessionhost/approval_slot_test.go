package sessionhost

import (
	"sync"
	"testing"
	"time"
)

// newSlot is an approval question with nothing answering it yet.
func newSlot(id string) *approvalSlot {
	return &approvalSlot{id: id, ch: make(chan approvalAnswer, 1)}
}

// TestOnlyOneAnswerWins is Neptunus' probe as a test: two frontends answer
// the same approval, and exactly one may be told its answer was used.
//
// The bug this exists for was a window between the winner's answer and the
// slot's deletion: consent drains the buffered channel as soon as it wakes,
// so a second caller arriving in that window found an empty buffer, sent
// successfully, and was told it had won too — while the session had
// already taken the first answer. Consent is not modelled here at all: the
// slot must decide on its own.
func TestOnlyOneAnswerWins(t *testing.T) {
	s := newSlot("approve-1")

	var wg sync.WaitGroup
	results := make([]ApprovalOutcome, 2)
	for i := range results {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			results[n] = s.answer("approve-1", n == 0)
		}(i)
	}
	wg.Wait()

	wins := 0
	for _, outcome := range results {
		if outcome == ApprovalAccepted {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("%d frontends were told their answer was used, want exactly 1 (results %v)", wins, results)
	}
}

// TestAnswerAfterTheDrainLoses covers the ordering the concurrency test
// cannot force: the winner was delivered and the session has drained the
// channel, but the slot is still registered. A second answer here must
// lose, not silently take the emptied buffer.
func TestAnswerAfterTheDrainLoses(t *testing.T) {
	s := newSlot("approve-1")
	if s.answer("approve-1", true) != ApprovalAccepted {
		t.Fatal("the first answer should have won")
	}
	<-s.ch // the session wakes and takes its answer

	if s.answer("approve-1", false) == ApprovalAccepted {
		t.Fatal("a second answer was accepted after the session had taken the first")
	}
}

// TestAnswerAfterTheQuestionWasAbandonedLoses: a decision arriving after
// the approval timed out was never going to be used, so it must not be
// reported as a decision that took effect -- and the operator must be told
// the clock refused it, not that another frontend answered it (#121).
func TestAnswerAfterTheQuestionWasAbandonedLoses(t *testing.T) {
	s := newSlot("approve-1")
	s.abandon(ApprovalTimedOut)

	if got := s.answer("approve-1", true); got != ApprovalTimedOut {
		t.Fatalf("a late answer for a timed-out question was reported as %v, want %v", got, ApprovalTimedOut)
	}
}

// TestAnInterruptedQuestionSaysInterrupted: the other abandon path keeps
// its own reason, so the operator is told the turn was cut, not that the
// clock ran out.
func TestAnInterruptedQuestionSaysInterrupted(t *testing.T) {
	s := newSlot("approve-1")
	s.abandon(ApprovalInterrupted)

	if got := s.answer("approve-1", true); got != ApprovalInterrupted {
		t.Fatalf("a late answer for an interrupted question was reported as %v, want %v", got, ApprovalInterrupted)
	}
}

// TestAnswerForAnotherQuestionLoses keeps a stale answer from landing on a
// newer call.
func TestAnswerForAnotherQuestionLoses(t *testing.T) {
	s := newSlot("approve-2")
	if got := s.answer("approve-1", true); got != ApprovalOtherQuestion {
		t.Fatalf("an answer for a different approval id was reported as %v, want %v", got, ApprovalOtherQuestion)
	}
}

// TestAbandonAfterAnAnswerKeepsTheAnswer: consent abandons only on its
// timeout and interrupt paths, but a late abandon must not rewrite the
// record of who answered.
func TestAbandonAfterAnAnswerKeepsTheAnswer(t *testing.T) {
	s := newSlot("approve-1")
	if s.answer("approve-1", true) != ApprovalAccepted {
		t.Fatal("first answer should have won")
	}
	s.abandon(ApprovalTimedOut)
	if got := s.answer("approve-1", true); got != ApprovalAlreadyAnswered {
		t.Fatalf("after an answer, a late abandon rewrote the record: got %v, want %v", got, ApprovalAlreadyAnswered)
	}
}

// TestAnswerIsDeliveredPromptly is the liveness half: a winning answer
// must actually arrive for the blocked session to act on.
func TestAnswerIsDeliveredPromptly(t *testing.T) {
	s := newSlot("approve-1")
	if s.answer("approve-1", false) != ApprovalAccepted {
		t.Fatal("answer should have won")
	}
	select {
	case got := <-s.ch:
		if got.allow {
			t.Fatal("delivered allow, want deny")
		}
	case <-time.After(time.Second):
		t.Fatal("the winning answer was never delivered")
	}
}
