package main

import (
	"sync"

	"ergo.services/ergo/gen"
)

// currentSession is the one place that knows which session process is the
// live one (#107). The driver moves it to the supervised replacement after
// a crash; the operator's interrupt and approval, the control socket's
// status query and the metrics scrape all read it at the moment they act,
// so none of them is left aimed at a dead pid.
type currentSession struct {
	mu  sync.RWMutex
	pid gen.PID
}

func newCurrentSession(pid gen.PID) *currentSession {
	return &currentSession{pid: pid}
}

// Pid is the session process that is live now.
func (c *currentSession) Pid() gen.PID {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.pid
}

// MoveTo records the replacement the driver reattached to.
func (c *currentSession) MoveTo(pid gen.PID) {
	c.mu.Lock()
	c.pid = pid
	c.mu.Unlock()
}
