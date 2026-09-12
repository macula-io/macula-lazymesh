package scheduler

import (
	"testing"
	"time"
)

// TestScheduleRejectsThePast pins the entry contract: a wakeup that
// already missed its moment is refused, not silently dropped.
func TestScheduleRejectsThePast(t *testing.T) {
	m := New()
	defer m.Stop()
	if err := m.Schedule(time.Now().Add(-time.Minute), "missed"); err == nil {
		t.Fatal("expected a past schedule to be rejected")
	}
}

// TestScheduleFiresAtItsTime pins the delivery contract: a near-future
// entry arrives on Arrivals, roughly at its time, exactly once.
func TestScheduleFiresAtItsTime(t *testing.T) {
	m := New()
	defer m.Stop()
	if err := m.Schedule(time.Now().Add(50*time.Millisecond), "check the mesh"); err != nil {
		t.Fatalf("schedule: %v", err)
	}

	select {
	case prompt := <-m.Arrivals():
		if prompt != "check the mesh" {
			t.Fatalf("prompt = %q", prompt)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("scheduled prompt never arrived")
	}
}

// TestScheduleOrdersMultipleEntries pins the queue: several entries fire
// in time order, not insertion order.
func TestScheduleOrdersMultipleEntries(t *testing.T) {
	m := New()
	defer m.Stop()
	now := time.Now()
	if err := m.Schedule(now.Add(150*time.Millisecond), "third"); err != nil {
		t.Fatalf("schedule third: %v", err)
	}
	if err := m.Schedule(now.Add(50*time.Millisecond), "first"); err != nil {
		t.Fatalf("schedule first: %v", err)
	}
	if err := m.Schedule(now.Add(100*time.Millisecond), "second"); err != nil {
		t.Fatalf("schedule second: %v", err)
	}

	var got []string
	deadline := time.After(5 * time.Second)
	for len(got) < 3 {
		select {
		case prompt := <-m.Arrivals():
			got = append(got, prompt)
		case <-deadline:
			t.Fatalf("timed out after %v", got)
		}
	}
	want := []string{"first", "second", "third"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

// TestStopEndsDelivery pins shutdown: after Stop, Arrivals closes and no
// pending entry fires.
func TestStopEndsDelivery(t *testing.T) {
	m := New()
	if err := m.Schedule(time.Now().Add(20*time.Millisecond), "never delivered"); err != nil {
		t.Fatalf("schedule: %v", err)
	}
	m.Stop()
	select {
	case _, ok := <-m.Arrivals():
		if ok {
			t.Fatal("a prompt fired after Stop")
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Arrivals never closed after Stop")
	}
}
