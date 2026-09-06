package tui

import (
	"fmt"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/macula-io/macula-lazymesh/internal/agent"
)

type chatEntryKind int

const (
	chatYou chatEntryKind = iota
	chatAssistant
	chatToolCall
	chatToolResult
	chatError
	chatSystem
)

// chatEntry is one line (collapsed form) in the chat pane. Tool calls and
// results carry a fuller detail string, shown only when detailsExpanded is
// on -- collapsed by default so the chat stream stays readable, but never
// hidden entirely (per the plan: the two surfaces, chat and agent.log,
// serve different needs, this isn't replacing the log's full detail).
type chatEntry struct {
	kind   chatEntryKind
	at     time.Time
	tool   string // set for chatToolCall / chatToolResult
	text   string // collapsed-form text
	detail string // full text, shown only when details are expanded
}

// chatEntryFromAgentEvent converts one agent.Event into a chat line. Plain
// EventBackoff/EventMaxFailuresReached is handled by the caller separately
// (it also needs to trigger a bell), not here.
func chatEntryFromAgentEvent(ev agent.Event) chatEntry {
	now := time.Now()
	switch ev.Kind {
	case agent.EventAssistantMessage:
		return chatEntry{kind: chatAssistant, at: now, text: ev.Text}
	case agent.EventToolCall:
		return chatEntry{kind: chatToolCall, at: now, tool: ev.ToolName, text: fmt.Sprintf("→ %s(%s)", ev.ToolName, truncateForChat(ev.Text, 60)), detail: ev.Text}
	case agent.EventToolResult:
		return chatEntry{kind: chatToolResult, at: now, tool: ev.ToolName, text: fmt.Sprintf("← %s: %s", ev.ToolName, truncateForChat(ev.Text, 60)), detail: ev.Text}
	case agent.EventError:
		msg := ""
		if ev.Err != nil {
			msg = ev.Err.Error()
		}
		return chatEntry{kind: chatError, at: now, tool: ev.ToolName, text: fmt.Sprintf("error (%s): %s", ev.ToolName, truncateForChat(msg, 80)), detail: msg}
	case agent.EventBackoff:
		return chatEntry{kind: chatSystem, at: now, text: "agent hit an error, backing off before retrying"}
	case agent.EventMaxFailuresReached:
		return chatEntry{kind: chatSystem, at: now, text: "agent stopped after repeated failures -- see agent.log"}
	default:
		return chatEntry{kind: chatSystem, at: now, text: "(unrecognized event)"}
	}
}

func youChatEntry(text string) chatEntry {
	return chatEntry{kind: chatYou, at: time.Now(), text: text}
}

var (
	chatYouStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Bold(true)
	chatAssistantStyl = lipgloss.NewStyle().Foreground(lipgloss.Color("212"))
	chatToolStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	chatErrorStyleTUI = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	chatSystemStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Italic(true)
	chatTimeStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
)

// render returns this entry's line, in expanded form if detailsExpanded is
// on and this entry actually has separate detail to show.
func (e chatEntry) render(detailsExpanded bool) string {
	ts := chatTimeStyle.Render(e.at.Format("15:04:05"))
	text := e.text
	if detailsExpanded && e.detail != "" && e.detail != e.text {
		switch e.kind {
		case chatToolCall:
			text = fmt.Sprintf("→ %s(%s)", e.tool, e.detail)
		case chatToolResult:
			text = fmt.Sprintf("← %s: %s", e.tool, e.detail)
		case chatError:
			text = fmt.Sprintf("error (%s): %s", e.tool, e.detail)
		}
	}

	switch e.kind {
	case chatYou:
		return fmt.Sprintf("%s %s %s", ts, chatYouStyle.Render("you:"), text)
	case chatAssistant:
		return fmt.Sprintf("%s %s %s", ts, chatAssistantStyl.Render("agent:"), text)
	case chatToolCall, chatToolResult:
		return fmt.Sprintf("%s %s", ts, chatToolStyle.Render(text))
	case chatError:
		return fmt.Sprintf("%s %s", ts, chatErrorStyleTUI.Render(text))
	case chatSystem:
		return fmt.Sprintf("%s %s", ts, chatSystemStyle.Render(text))
	default:
		return fmt.Sprintf("%s %s", ts, text)
	}
}

func truncateForChat(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
