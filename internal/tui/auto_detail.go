package tui

import (
	"github.com/macula-io/macula-lazymesh/internal/agent"
)

// AutoDetail is the text an event shows without the details toggle: empty
// when its entry stays collapsed. It is the one place that answers "what
// does the operator see without asking for it", so a test of what is on
// screen cannot disagree with the renderer (#46).
// AutoDetail is the text an event shows without the details toggle: empty
// when its entry stays collapsed.
func AutoDetail(ev agent.Event) string {
	entry := chatEntryFromAgentEvent(ev)
	if !entry.alwaysDetail {
		return ""
	}
	return entry.detail
}
