// Package scheduler holds lazymesh's deferred prompts (G13): an entry
// fires exactly once, at its time, and lands on Arrivals for the driver
// to hand to the agent as its next prompt — the control plane's schedule
// message is the entry point ("remind me to check X at 14:00" without an
// operator having to be present to type it).
package scheduler

import (
	"fmt"
	"sync"
	"time"
)

// arrivalsBuffer bounds how many fired prompts can wait for the driver;
// the driver drains Arrivals at every turn boundary, so the buffer only
// ever holds a burst of coincident schedules.
const arrivalsBuffer = 8

// Manager schedules deferred prompts. It owns one timer armed for the
// earliest pending entry; Schedule re-arms when it moves the deadline
// earlier. All state is mutex-guarded because Schedule runs on the
// control plane's reader goroutine while the timer's own goroutine
// fires.
type Manager struct {
	mu       sync.Mutex
	entries  []entry
	arrivals chan string
	timer    *time.Timer
	stopped  bool
}

type entry struct {
	at     time.Time
	prompt string
}

// New returns a running Manager. Stop terminates it.
func New() *Manager {
	m := &Manager{arrivals: make(chan string, arrivalsBuffer)}
	m.timer = time.NewTimer(time.Hour)
	m.timer.Stop()
	go m.wait()
	return m
}

// Schedule arms a prompt to fire at at. A time in the past is rejected —
// a wakeup that already missed its moment is not a wakeup.
func (m *Manager) Schedule(at time.Time, prompt string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stopped {
		return fmt.Errorf("scheduler: stopped")
	}
	if !at.After(time.Now()) {
		return fmt.Errorf("scheduler: %s is in the past", at.Format(time.RFC3339))
	}
	m.entries = append(m.entries, entry{at: at, prompt: prompt})
	m.rearm()
	return nil
}

// Arrivals delivers fired prompts, one per entry, in fire order.
func (m *Manager) Arrivals() <-chan string {
	return m.arrivals
}

// Stop ends the manager: no further entries fire, and Arrivals closes
// once the timer goroutine exits.
func (m *Manager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stopped {
		return
	}
	m.stopped = true
	m.timer.Stop()
	close(m.arrivals)
}

// wait drains the timer channel and delivers every entry whose time has
// come, then re-arms for the next.
func (m *Manager) wait() {
	for range m.timer.C {
		m.mu.Lock()
		if m.stopped {
			m.mu.Unlock()
			return
		}
		now := time.Now()
		var fired []string
		kept := m.entries[:0]
		for _, e := range m.entries {
			if !e.at.After(now) {
				fired = append(fired, e.prompt)
				continue
			}
			kept = append(kept, e)
		}
		m.entries = kept
		m.rearm()
		m.mu.Unlock()

		// Blocking by design: the driver always drains Arrivals, and a
		// dropped wakeup would silently lose the operator's request.
		for _, prompt := range fired {
			m.arrivals <- prompt
		}
	}
}

// rearm points the timer at the earliest pending entry; the caller holds
// m.mu.
func (m *Manager) rearm() {
	if len(m.entries) == 0 {
		m.timer.Stop()
		return
	}
	earliest := m.entries[0].at
	for _, e := range m.entries[1:] {
		if e.at.Before(earliest) {
			earliest = e.at
		}
	}
	// Reset on a stopped timer is safe; on a live one, drain any stale
	// tick first so a reset never races an in-flight fire.
	if !m.timer.Stop() {
		select {
		case <-m.timer.C:
		default:
		}
	}
	m.timer.Reset(time.Until(earliest))
}
