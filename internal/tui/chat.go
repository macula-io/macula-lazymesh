package tui

import (
	"fmt"
	"strings"
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

// Colors match the macula brand palette (macula-artwork's own documented
// hex values -- the same blue/orange pair every macula-*-full-*.svg logo
// uses), not lipgloss's generic 256-color example palette. You/Assistant
// deliberately mirror the logo's own wordmark(blue)/sub-label(orange)
// pairing instead of an arbitrary pink (ANSI 212, a Charm-tutorial
// default with no connection to this project's brand).
var (
	chatYouStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("#38BDF8")).Bold(true)
	chatAssistantStyl = lipgloss.NewStyle().Foreground(lipgloss.Color("#FB923C"))
	chatToolStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	chatErrorStyleTUI = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	chatSystemStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Italic(true)
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

// collapseNewlines flattens embedded newlines to spaces before a string
// is truncated for single-line display. Tool call/result text is often
// pretty-printed JSON (mesh_read_inbox, mesh_rooms, ...), and a truncated
// snippet that still contains a raw newline silently renders as more than
// one terminal row. That broke statusLines()'s "one slice element = one
// row" invariant (resizeComponents counts len(statusLines()) as the
// reserved row count, see model.go): the chatter line (issue #2) would
// secretly wrap to 2-3 rows depending on how many newlines happened to
// land inside that particular truncated window, so the viewport height
// calc was wrong by a different amount on every update -- the chat pane
// visibly jumped. Found live 2026-09-06.
func collapseNewlines(s string) string {
	return strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(s)
}

func truncateForChat(s string, n int) string {
	s = collapseNewlines(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
