package tui

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/glamour"
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
//
// streaming marks the entry currently being built from EventAssistantDelta
// chunks: its text grows with every delta until the turn's completed
// EventAssistantMessage finishes it, and only a finished entry may cache
// its markdown rendering (md, keyed by the width it was rendered at —
// the table preprocessor is width-dependent).
type chatEntry struct {
	kind      chatEntryKind
	at        time.Time
	tool      string // set for chatToolCall / chatToolResult
	text      string // collapsed-form text
	detail    string // full text, shown only when details are expanded
	streaming bool   // assistant entry still accumulating deltas
	md        string // glamour rendering, cached once finished
	mdWidth   int    // the width md was rendered at
}

// chatEntryFromAgentEvent converts one agent.Event into a chat line. Plain
// EventBackoff/EventMaxFailuresReached is handled by the caller separately
// (it also needs to trigger a bell), not here.
func chatEntryFromAgentEvent(ev agent.Event) chatEntry {
	now := time.Now()
	switch ev.Kind {
	case agent.EventAssistantMessage:
		return chatEntry{kind: chatAssistant, at: now, text: ev.Text}
	case agent.EventAssistantDelta:
		return chatEntry{kind: chatAssistant, at: now, text: ev.Text, streaming: true}
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
	case agent.EventListening:
		// macula-io/macula-lazymesh#13/#15: the loop-owned room-waiter
		// design parks silently between real events, with none of the
		// old design's periodic mesh_say tool-call traffic to show
		// something is alive. This is the replacement liveness signal --
		// routed to the status strip as chatter (see isChatter in
		// model.go), not the main chat pane, since it fires every cycle
		// and would otherwise drown out real conversation. The captured
		// clock time is what makes it a genuine liveness cue rather than
		// static text: a frozen process would show the same timestamp
		// forever, a working one keeps advancing it.
		return chatEntry{kind: chatSystem, at: now, text: fmt.Sprintf("listening (%s)", now.Format("15:04:05"))}
	default:
		return chatEntry{kind: chatSystem, at: now, text: "(unrecognized event)"}
	}
}

func youChatEntry(text string) chatEntry {
	return chatEntry{kind: chatYou, at: time.Now(), text: text}
}

// directMeshServiceCallEntry converts a meshServiceCallResultMsg (a human,
// not the AI, invoked this -- see plans/PLAN_DIRECT_MESH_SERVICE_CALLS.md)
// into a chat line. "[direct]" in the tool field, not a separate
// chatEntryKind: reuses chatToolCall/chatError's own existing render
// cases and truncateForChat convention exactly, distinguished only by that
// prefix -- an operator scanning the transcript for "did the AI do this"
// needs to see the marker, not a different color scheme.
func directMeshServiceCallEntry(msg meshServiceCallResultMsg) chatEntry {
	now := time.Now()
	tool := "[direct] " + msg.procedure
	if msg.err != nil {
		errText := msg.err.Error()
		return chatEntry{kind: chatError, at: now, tool: tool, text: fmt.Sprintf("error (%s): %s", tool, truncateForChat(errText, 80)), detail: errText}
	}
	return chatEntry{kind: chatToolResult, at: now, tool: tool, text: fmt.Sprintf("← %s: %s", tool, truncateForChat(msg.result, 60)), detail: msg.result}
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

// render returns this entry's line(s), in expanded form if detailsExpanded
// is on and this entry actually has separate detail to show. Assistant
// entries render through glamour (D3: markdown-capable answers made
// readable), with over-wide tables flattened first (see
// flattenWideTables); every other kind stays a single line. Must be a
// pointer receiver: a completed assistant entry caches its markdown
// rendering in md.
func (e *chatEntry) render(detailsExpanded bool, width int) string {
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
		return fmt.Sprintf("%s %s\n%s", ts, chatAssistantStyl.Render("agent:"), e.markdownBody(width))
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

// maculaMarkdownStyle is glamour's own dark.json (v1.0.0), modified in
// two ways found live 2026-09-12:
//
//   - the stock style gives h2-h5 NO color at all (just the literal
//     "## " prefix), so five heading levels render identically; here
//     every level carries a distinct color and the hash prefixes are
//     dropped entirely -- a chat pane reads headings, not markdown
//     source, and the colors (bold) carry the level;
//   - the stock style wraps the whole document in a 2-space margin with
//     a leading blank line (the "agent:"-then-empty-line-then-indented
//     text artifact); the document margin is zeroed and the leading
//     block prefix removed.
//
// It lives embedded (not generated) so a glamour upgrade cannot silently
// change how the chat pane reads; re-diff against the module's
// styles/dark.json on every version bump.
const maculaMarkdownStyle = `{"document":{"block_prefix":"","block_suffix":"\n","color":"252","margin":0},"block_quote":{"indent":1,"indent_token":"\u2502 "},"paragraph":{},"list":{"level_indent":2},"heading":{"block_suffix":"\n","color":"39","bold":true},"h1":{"prefix":"","suffix":" ","color":"208","bold":true},"h2":{"prefix":"","color":"214","bold":true},"h3":{"prefix":"","color":"228","bold":true},"h4":{"prefix":"","color":"114","bold":true},"h5":{"prefix":"","color":"81","bold":true},"h6":{"prefix":"","color":"141","bold":false},"text":{},"strikethrough":{"crossed_out":true},"emph":{"italic":true},"strong":{"bold":true},"hr":{"color":"240","format":"\n--------\n"},"item":{"block_prefix":"\u2022 "},"enumeration":{"block_prefix":". "},"task":{"ticked":"[\u2713] ","unticked":"[ ] "},"link":{"color":"30","underline":true},"link_text":{"color":"35","bold":true},"image":{"color":"212","underline":true},"image_text":{"color":"243","format":"Image: {{.text}} \u2192"},"code":{"prefix":" ","suffix":" ","color":"203","background_color":"236"},"code_block":{"color":"244","margin":2,"chroma":{"text":{"color":"#C4C4C4"},"error":{"color":"#F1F1F1","background_color":"#F05B5B"},"comment":{"color":"#676767"},"comment_preproc":{"color":"#FF875F"},"keyword":{"color":"#00AAFF"},"keyword_reserved":{"color":"#FF5FD2"},"keyword_namespace":{"color":"#FF5F87"},"keyword_type":{"color":"#6E6ED8"},"operator":{"color":"#EF8080"},"punctuation":{"color":"#E8E8A8"},"name":{"color":"#C4C4C4"},"name_builtin":{"color":"#FF8EC7"},"name_tag":{"color":"#B083EA"},"name_attribute":{"color":"#7A7AE6"},"name_class":{"color":"#F1F1F1","underline":true,"bold":true},"name_constant":{},"name_decorator":{"color":"#FFFF87"},"name_exception":{},"name_function":{"color":"#00D787"},"name_other":{},"literal":{},"literal_number":{"color":"#6EEFC0"},"literal_date":{},"literal_string":{"color":"#C69669"},"literal_string_escape":{"color":"#AFFFD7"},"generic_deleted":{"color":"#FD5B5B"},"generic_emph":{"italic":true},"generic_inserted":{"color":"#00D787"},"generic_strong":{"bold":true},"generic_subheading":{"color":"#777777"},"background":{"background_color":"#373737"}}},"table":{},"definition_list":{},"definition_term":{},"definition_description":{"block_prefix":"\n\ud83e\udc36 "},"html_block":{},"html_span":{}}`

// markdownRenderer returns ONE glamour renderer for the process,
// built lazily for the pane's current width: glamour pads every line to
// its renderer width with styled spaces, so the renderer width must
// equal the pane width — otherwise trailing padding visibly overruns the
// viewport, and the viewport re-wraps lines that were already wrapped.
// A width change (terminal resize) rebuilds the renderer; the per-entry
// md cache is width-keyed for the same reason.
//
// Fixed custom style (maculaMarkdownStyle), deliberately NOT WithAutoStyle
// — auto style queries the terminal for its background color and WAITS
// for the answer, which never arrives through bubbletea's alt screen:
// the first markdown render would block the Update loop forever (the
// "TUI hangs, shortcuts dead" symptom found live 2026-09-12).
var (
	markdownRendererWidth int
	markdownRendererOnce  *glamour.TermRenderer
)

func markdownRenderer(width int) *glamour.TermRenderer {
	if width < 20 {
		width = 20
	}
	if markdownRendererOnce != nil && markdownRendererWidth == width {
		return markdownRendererOnce
	}
	renderer, err := glamour.NewTermRenderer(
		glamour.WithStylesFromJSONBytes([]byte(maculaMarkdownStyle)),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		// The only honest fallback: a renderer that cannot be built must
		// not take the chat pane down — raw text renders.
		return nil
	}
	markdownRendererOnce = renderer
	markdownRendererWidth = width
	return renderer
}

// collapseBlankLines folds runs of two-or-more blank lines into one, so
// a verbose model's double-spaced output does not push real content out
// of a chat pane with a small viewport. In CommonMark extra blank lines
// carry no structure, so the collapse is semantically safe — EXCEPT
// inside fenced code blocks, where blank lines are content and are
// preserved exactly. A single blank line (block separation) is kept.
func collapseBlankLines(md string) string {
	lines := strings.Split(md, "\n")
	out := make([]string, 0, len(lines))
	inFence := false
	fence := ""
	previousBlank := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !inFence && (strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~")) {
			inFence = true
			fence = trimmed[:3]
			previousBlank = false
			out = append(out, line)
			continue
		}
		if inFence {
			if strings.HasPrefix(trimmed, fence) {
				inFence = false
			}
			out = append(out, line)
			continue
		}
		blank := trimmed == ""
		if blank && previousBlank {
			continue
		}
		previousBlank = blank
		if blank {
			// Normalize the survivor: a whitespace-only line IS a blank
			// line, and the renderer gets a clean "".
			out = append(out, "")
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// markdownBody renders the entry's text through the shared glamour
// renderer, after flattening any table too wide for the pane and
// collapsing double-spaced blank runs. A finished entry caches the
// rendering keyed by width (the table preprocessor is width-dependent);
// a streaming entry re-renders as its text grows, and any failure falls
// back to the raw text — a display concern must never lose content.
func (e *chatEntry) markdownBody(width int) string {
	if !e.streaming && e.md != "" && e.mdWidth == width {
		return e.md
	}
	renderer := markdownRenderer(width)
	if renderer == nil {
		return e.text
	}
	rendered, err := renderer.Render(flattenWideTables(collapseBlankLines(e.text), width-8))
	if err != nil {
		return e.text
	}
	if !e.streaming {
		e.md = rendered
		e.mdWidth = width
	}
	return rendered
}

// flattenWideTables rewrites pipe tables whose rendered width would
// exceed maxWidth into a "column: value" list per row. glamour (via
// goldmark) does not wrap table cells, so an over-wide table arrives as
// full-width border lines that the chat viewport then soft-wraps into
// misaligned noise — flattening is strictly more readable than broken
// borders. A table that fits is left untouched for glamour to render
// properly, and anything that is not a table passes through unchanged.
func flattenWideTables(md string, maxWidth int) string {
	if maxWidth < 20 {
		maxWidth = 20
	}
	lines := strings.Split(md, "\n")
	out := make([]string, 0, len(lines))
	for i := 0; i < len(lines); i++ {
		if i+1 >= len(lines) || !isTableRow(lines[i]) || !isSeparatorRow(lines[i+1]) {
			out = append(out, lines[i])
			continue
		}
		header := splitRow(lines[i])
		j := i + 2
		var rows [][]string
		for j < len(lines) && isTableRow(lines[j]) {
			rows = append(rows, splitRow(lines[j]))
			j++
		}
		if tableWidth(header, rows) <= maxWidth {
			out = append(out, lines[i:j]...)
			i = j - 1
			continue
		}
		for _, row := range rows {
			for c := 0; c < len(header) && c < len(row); c++ {
				value := strings.TrimSpace(row[c])
				if value == "" {
					value = "-"
				}
				out = append(out, fmt.Sprintf("- %s: %s", strings.TrimSpace(header[c]), value))
			}
			out = append(out, "")
		}
		i = j - 1
	}
	return strings.Join(out, "\n")
}

// isTableRow reports whether a line is a pipe-table row of any kind
// (header, separator, or data): it must start and end with a pipe and
// hold at least one inner pipe.
func isTableRow(line string) bool {
	trimmed := strings.TrimSpace(line)
	if len(trimmed) < 3 || trimmed[0] != '|' || trimmed[len(trimmed)-1] != '|' {
		return false
	}
	return strings.Contains(trimmed[1:len(trimmed)-1], "|")
}

// isSeparatorRow reports whether a table row is the |-|-| alignment row.
func isSeparatorRow(line string) bool {
	trimmed := strings.TrimSpace(line)
	if !isTableRow(trimmed) {
		return false
	}
	for _, cell := range splitRow(trimmed) {
		cell = strings.TrimSpace(cell)
		if cell == "" || !strings.Contains(cell, "-") {
			return false
		}
	}
	return true
}

// splitRow splits one pipe-table row into its cells (leading/trailing
// pipes stripped, cells trimmed).
func splitRow(line string) []string {
	trimmed := strings.TrimSpace(line)
	inner := strings.TrimPrefix(strings.TrimSuffix(trimmed, "|"), "|")
	parts := strings.Split(inner, "|")
	cells := make([]string, 0, len(parts))
	for _, p := range parts {
		cells = append(cells, strings.TrimSpace(p))
	}
	return cells
}

// tableWidth is the rendered width of a table with the given header and
// rows: each column is as wide as its widest cell plus padding.
func tableWidth(header []string, rows [][]string) int {
	cols := len(header)
	widths := make([]int, cols)
	for c := 0; c < cols; c++ {
		widths[c] = utf8.RuneCountInString(header[c])
	}
	for _, row := range rows {
		for c := 0; c < cols && c < len(row); c++ {
			if w := utf8.RuneCountInString(row[c]); w > widths[c] {
				widths[c] = w
			}
		}
	}
	total := 1 // leading pipe
	for c := 0; c < cols; c++ {
		total += 1 + widths[c] + 2 // " cell " padding
	}
	return total + 1 // trailing pipe
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

// truncateForChat cuts to n CHARACTERS, not n bytes. It used to slice the
// raw string, which splits any multi-byte rune straddling the cut and
// renders the fragment as a replacement glyph -- so a petname, a room
// topic or an error message containing anything outside ASCII ended in
// visible corruption rather than a clean ellipsis. Counting runes costs
// one pass and removes the whole class.
func truncateForChat(s string, n int) string {
	s = collapseNewlines(s)
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "..."
}
