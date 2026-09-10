package mcpclient

import "sync"

// Tool-call failure logging, throttled.
//
// The TUI refreshes its mesh state every two seconds, so a persistent
// failure is not one event: it is the same event thirty times a minute,
// forever. Logging each one writes about 43,000 identical lines a day
// per tool into agent.log. The file grows without bound, and an operator
// who opens it in $EDITOR (the l key) finds every other subsystem's
// lines buried under one repeated symptom they already knew about.
//
// Logging only the first would be worse in the other direction: the
// tail of the file would say nothing about a failure that started an
// hour ago and is still happening, and a recovery would never show.
//
// So: log a transition, never a repetition. The first failure is logged
// in full. Identical consecutive failures are counted, not logged. A
// change in the error text is a new transition and is logged. Recovery
// is logged with the count of what was suppressed, so the suppressed
// occurrences are reported rather than discarded.
type failureThrottle struct {
	mu    sync.Mutex
	state map[string]*toolFailure
}

type toolFailure struct {
	lastErr    string
	suppressed int
}

func newFailureThrottle() *failureThrottle {
	return &failureThrottle{state: map[string]*toolFailure{}}
}

// onFailure records a failure of tool and reports whether it should be
// logged. suppressed is how many identical failures went unlogged
// before this one, and is only meaningful when log is true.
func (t *failureThrottle) onFailure(tool, errText string) (log bool, suppressed int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	prev, seen := t.state[tool]
	if !seen {
		t.state[tool] = &toolFailure{lastErr: errText}
		return true, 0
	}
	if prev.lastErr == errText {
		prev.suppressed++
		return false, 0
	}
	// The error changed: a genuinely new condition, worth a line, and
	// the run of the previous one ends here rather than vanishing.
	was := prev.suppressed
	prev.lastErr = errText
	prev.suppressed = 0
	return true, was
}

// onSuccess clears any failure run for tool and reports whether a
// recovery is worth logging, along with how many failures were
// suppressed during the run. A tool that was never failing returns
// false: a healthy call is not news.
func (t *failureThrottle) onSuccess(tool string) (log bool, suppressed int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	prev, seen := t.state[tool]
	if !seen {
		return false, 0
	}
	delete(t.state, tool)
	return true, prev.suppressed
}

// logCallOutcome records the result of one tool call through the
// throttle, emitting a line only on a transition: first failure, a
// changed error, or recovery. err == nil is a success.
//
// A Client built directly in a test (not via Spawn) has a nil throttle.
// That means silence: no tool-call outcome is logged at all, the same
// way a nil logger already means silence here. It must not panic.
func (c *Client) logCallOutcome(tool string, err error) {
	if c.throttle == nil {
		return
	}
	if err == nil {
		if shouldLog, suppressed := c.throttle.onSuccess(tool); shouldLog {
			c.log("tool call recovered: %s (after %d suppressed identical failures)", tool, suppressed)
		}
		return
	}
	shouldLog, suppressed := c.throttle.onFailure(tool, err.Error())
	if !shouldLog {
		return
	}
	if suppressed > 0 {
		c.log("tool call failed: %s: %v (previous error repeated %d more times)", tool, err, suppressed)
		return
	}
	c.log("tool call failed: %s: %v", tool, err)
}
