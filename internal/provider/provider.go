// Package provider defines the LLM backend interface lazymesh's agent loop
// drives, and the concrete backends that satisfy it.
package provider

import "context"

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

// ChatResponse is the assistant's reply to a ChatRequest.
type ChatResponse struct {
	Message Message
	Usage   Usage
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
