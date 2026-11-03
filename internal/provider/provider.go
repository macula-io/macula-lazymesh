// Package provider defines the LLM backend interface lazymesh's agent loop
// drives, and the concrete backends that satisfy it.
package provider

import (
	"context"
	"strings"
	"unicode"
)

// Role is a chat message's role, matching OpenAI-compatible chat completions.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// ToolCall is a tool invocation the assistant asked for. Arguments is the
// raw JSON object the model produced, not yet decoded -- the caller knows
// the tool's schema and decodes it against that.
type ToolCall struct {
	ID        string
	Name      string
	Arguments string
}

// Message is one turn in the conversation. ToolCalls is set on an assistant
// message that wants to invoke tools; ToolCallID and Name are set on a tool
// message reporting one call's result back.
type Message struct {
	Role       Role
	Content    string
	ToolCalls  []ToolCall
	ToolCallID string
	Name       string
}

// ToolSpec describes one callable tool, in the shape an OpenAI-compatible
// "tools" array expects (function name/description/parameters). This is
// filled in directly from an MCP Tool's own Name/Description/InputSchema --
// no separate schema representation of lazymesh's own.
type ToolSpec struct {
	Name        string
	Description string
	InputSchema any
}

// ChatRequest is one call to a provider's chat completions endpoint.
type ChatRequest struct {
	Messages []Message
	Tools    []ToolSpec
}

// Usage is a provider's own token accounting for one ChatCompletion call,
// when it reports one -- not every provider does, so all fields are zero
// rather than an error when unavailable. Added for macula-io/macula-
// lazymesh#14/#15 (the room-waiter concurrency work): measuring token
// cost needed real numbers, not an estimate.
//
// PromptCacheHitTokens/PromptCacheMissTokens (added investigating the
// 2026-09-07 runaway-context incident): DeepSeek's API reports these in
// its usage object -- automatic server-side prefix caching, no request-
// side opt-in needed -- but nothing in this codebase previously captured
// them, so "is caching actually working" was unanswerable from here.
// Zero on backends that don't report the split, same as the other
// fields.
type Usage struct {
	PromptTokens          int
	CompletionTokens      int
	TotalTokens           int
	PromptCacheHitTokens  int
	PromptCacheMissTokens int
}

// StopReason is the normalized answer to "why did this reply end": the one
// piece of a backend's own stop signal lazymesh keeps. Added for
// macula-io/macula-lazymesh#77 -- a reply that stopped mid-sentence arrived
// as an ordinary complete reply, because finish_reason was never decoded and
// a stream that ended with no terminal event returned a nil error.
//
// The zero value is StopUnspecified: a backend that named nothing, or a test
// double, is not a cut and must not be reported as one. Only a backend that
// positively named a stopping condition can produce a marker.
type StopReason string

const (
	StopUnspecified      StopReason = ""
	StopNormal           StopReason = "normal"             // the model finished its turn
	StopOutputLimit      StopReason = "output_limit"       // the backend's output cap ran out
	StopStreamEndedEarly StopReason = "stream_ended_early" // the stream ended with no terminal event
	StopOther            StopReason = "other"              // a named condition not classified here
)

// Stop carries why a reply ended, plus -- when the backend named a condition
// lazymesh does not classify (content_filter, refusal) -- the backend's own
// word for it, so the log and the marker can say what actually happened
// instead of "other".
type Stop struct {
	Reason StopReason
	Detail string
}

// Cut reports whether the reply ended for a reason other than the model
// finishing its turn. StopUnspecified is not a cut: nothing claimed otherwise,
// and treating silence as a cut would flag every backend that reports nothing.
func (s Stop) Cut() bool {
	switch s.Reason {
	case StopOutputLimit, StopStreamEndedEarly, StopOther:
		return true
	}
	return false
}

// String is what the usage log line and an operator read: "normal",
// "output_limit", "other(content_filter)", "unspecified".
func (s Stop) String() string {
	if s.Reason == StopUnspecified {
		return "unspecified"
	}
	if s.Detail != "" {
		return string(s.Reason) + "(" + s.Detail + ")"
	}
	return string(s.Reason)
}

// stopFromReason normalizes a backend's own stop word. OpenAI-compatible
// backends say "stop"/"length", and -- easily missed, since it is not in
// their own prose -- "tool_calls" ("function_call" on the legacy shape) at
// the end of every round that asks for tools: that is the model finishing a
// normal turn, not a cut. Anthropic's Messages API says "end_turn",
// "stop_sequence", "tool_use", "max_tokens", "pause_turn", "refusal". A word
// not in the table above is kept verbatim (control runes stripped, since it
// reaches a terminal) as StopOther's detail rather than guessed at.
func stopFromReason(reason string) Stop {
	switch reason {
	case "":
		return Stop{Reason: StopUnspecified}
	case "stop", "tool_calls", "function_call", "end_turn", "stop_sequence", "tool_use":
		return Stop{Reason: StopNormal}
	case "length", "max_tokens":
		return Stop{Reason: StopOutputLimit}
	default:
		// pause_turn lands here on purpose: the server paused a long turn
		// and expects the caller to continue, so the reply is NOT complete
		// and belongs with the other cuts rather than with a finished turn.
		return Stop{Reason: StopOther, Detail: printableReason(reason)}
	}
}

// printableReason keeps a backend's own word safe to put on one terminal
// line: escape and control runes are dropped, and an over-long word is cut.
// The word is remote input rendered into agent.log and the chat pane, so it
// must not be able to move the cursor, clear the screen or forge a line.
func printableReason(reason string) string {
	const maxLen = 40
	var b strings.Builder
	for _, r := range reason {
		if unicode.IsControl(r) || !unicode.IsPrint(r) && r != ' ' {
			continue
		}
		if b.Len() >= maxLen {
			return b.String() + "..."
		}
		b.WriteRune(r)
	}
	return b.String()
}

// ChatResponse is the assistant's reply to a ChatRequest.
type ChatResponse struct {
	Message Message
	Usage   Usage
	// Stop is why the reply ended (see StopReason). Zero on a backend that
	// reports nothing -- same zero-as-fact posture as Usage.
	Stop Stop
}

// Provider is an LLM chat-completions backend. DeepSeek is the default
// implementation; the interface exists so a second backend is a config
// value, not a rewrite.
//
// ContextWindow added 2026-09-07 (R2, the runaway-context incident's
// follow-up): a real, provider-reported number cmd/lazymesh's own
// startup budget check compares the fixed prefix (system prompt + tool
// schemas) against, so a genuinely small-context model gets a clear
// refuse-to-start message instead of silently running until the same
// class of crash this incident already produced once.
type Provider interface {
	ChatCompletion(ctx context.Context, req ChatRequest) (ChatResponse, error)
	ContextWindow() int
}

// Streamer is a Provider that can deliver assistant content incrementally
// (D3: the chat UX decision made streaming a prerequisite, not a
// nice-to-have). A Provider that does not satisfy Streamer is driven
// through ChatCompletion by the agent loop with no behavior change.
//
// onDelta is invoked once per content chunk, in order, as it arrives; an
// error it returns aborts the stream and is what ChatCompletionStream
// returns. The returned ChatResponse carries the completed message (tool
// calls accumulated across chunks included) and the usage the backend
// reported, which is zero for backends that only report it when asked.
type Streamer interface {
	Provider
	ChatCompletionStream(ctx context.Context, req ChatRequest, onDelta func(chunk string) error) (ChatResponse, error)
}
