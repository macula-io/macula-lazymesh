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

// ChatResponse is the assistant's reply to a ChatRequest.
type ChatResponse struct {
	Message Message
}

// Provider is an LLM chat-completions backend. DeepSeek is the default
// implementation; the interface exists so a second backend is a config
// value, not a rewrite.
type Provider interface {
	ChatCompletion(ctx context.Context, req ChatRequest) (ChatResponse, error)
}
