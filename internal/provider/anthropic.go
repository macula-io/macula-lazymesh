package provider

import (
	"context"
	"fmt"
)

// Anthropic is a second Provider, deliberately unimplemented for the MVP.
// Its purpose is structural, not functional: DeepSeek's OpenAI-compatible
// chat-completions shape could be satisfied by any config-only variation
// on the same wire format (a different base_url), which wouldn't actually
// prove the Provider interface generalizes. Anthropic's own Messages API
// has a genuinely different shape (separate top-level system prompt,
// content-block messages, no /chat/completions-style tool_calls), so a
// real implementation here would need its own request/response mapping,
// not a config tweak on DeepSeek's -- which is exactly what proves
// Provider isn't DeepSeek-shaped. Wire it up in Phase 2+ if a second
// backend is actually needed.
type Anthropic struct {
	BaseURL string
	Model   string
	APIKey  string
}

func NewAnthropic(baseURL, model, apiKey string) *Anthropic {
	return &Anthropic{BaseURL: baseURL, Model: model, APIKey: apiKey}
}

func (a *Anthropic) ChatCompletion(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	return ChatResponse{}, fmt.Errorf("provider anthropic: not implemented (config stub only, see provider.go's Provider interface)")
}
