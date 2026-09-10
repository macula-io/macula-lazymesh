package realmjoin

import (
	"log"
	"sync"
)

// Optional logging for the join lifecycle.
//
// Same optional-setter shape as mcpclient.Client, meshservices.Source
// and ringwaiter.Manager: nil (the default) means silence, so every
// existing test's own construction is unaffected and Join's signature
// does not move. It is package-level rather than per-call because Join
// is a package-level function called from internal/tui, and threading a
// logger through that call site would change an API another package
// owns for no gain.
//
// A realm join is the one operation here that a human triggers by hand,
// waits on, and cannot retry blindly -- a failed join leaves no
// credential and the reason is otherwise only visible for as long as
// the TUI panel is on screen. Logging it means the reason survives the
// panel being dismissed.
var (
	loggerMu sync.RWMutex
	logger   *log.Logger
)

// SetLogger sets where join lifecycle lines go. nil means silence.
func SetLogger(l *log.Logger) {
	loggerMu.Lock()
	logger = l
	loggerMu.Unlock()
}

func logf(format string, args ...any) {
	loggerMu.RLock()
	l := logger
	loggerMu.RUnlock()
	if l != nil {
		l.Printf(format, args...)
	}
}
