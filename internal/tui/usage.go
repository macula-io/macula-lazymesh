package tui

import (
	"fmt"
	"strings"

	"github.com/macula-io/macula-lazymesh/internal/agent"
)

// usageSegment is the summary line's context figure (#78 gap 4): what the
// conversation holds against the model's window, and what the session has
// used. Empty until the first usage report, so a backend that reports none
// shows nothing rather than a zero nobody measured. Without a known window
// it shows the tokens alone, never a share of a window it would have to
// make up.
func usageSegment(u agent.UsageReport) string {
	if u.ContextTokens == 0 {
		return ""
	}
	held := "context " + shortTokens(u.ContextTokens)
	if u.ContextWindow > 0 {
		held += "/" + shortTokens(u.ContextWindow) + " (" + contextShare(u.ContextTokens, u.ContextWindow) + ")"
	}
	return held + " · " + shortTokens(u.SessionTokens) + " used"
}

// shortTokens is a token count read at a glance: 999, 12.3k, 200k, 2.5M.
func shortTokens(n int) string {
	switch {
	case n < 1_000:
		return fmt.Sprintf("%d", n)
	case n < 1_000_000:
		return trimPointZero(fmt.Sprintf("%.1f", float64(n)/1_000)) + "k"
	default:
		return trimPointZero(fmt.Sprintf("%.1f", float64(n)/1_000_000)) + "M"
	}
}

func trimPointZero(s string) string {
	return strings.TrimSuffix(s, ".0")
}

// contextShare is how much of the window the conversation fills, as a whole
// percentage. Something under one percent reads "<1%", not "0%": a fresh
// session in a million-token window still holds something.
func contextShare(tokens, window int) string {
	pct := tokens * 100 / window
	if pct == 0 {
		return "<1%"
	}
	return fmt.Sprintf("%d%%", pct)
}
