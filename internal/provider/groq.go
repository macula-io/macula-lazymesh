package provider

import (
	"context"
	"net/http"
)

// GroqDefaultBaseURL is Groq's OpenAI-compatible API root.
const GroqDefaultBaseURL = "https://api.groq.com/openai/v1"

// GroqDefaultModel is Groq's established production flagship
// (llama-3.3-70b-versatile), verified against Groq's own current model
// list (console.groq.com/docs/models) rather than assumed -- a relayed
// suggestion to default to a Kimi K2 model was checked against that same
// list first and dropped: no kimi/moonshot model appears anywhere on it,
// current or preview, as of 2026-09-07.
const GroqDefaultModel = "llama-3.3-70b-versatile"

// Groq is a third Provider: Groq's OpenAI-compatible chat completions
// API. Its wire shape needs no separate request/response mapping from
// DeepSeek's/NVIDIA's -- see callOpenAICompatChatCompletions' own doc
// comment in openaicompat.go for why, and contrast with anthropic.go's
// Anthropic, whose genuinely different Messages API does need one and is
// why that one stays an unimplemented stub.
//
// lazymesh's own default stays DeepSeek regardless of this existing:
// DeepSeek was chosen specifically for being cheap enough to run an agent
// against continuously. Groq is here as an available option, the same
// way NVIDIA is -- not a fleet-wide default switch.
type Groq struct {
	BaseURL string
	Model   string
	APIKey  string
	HTTP    *http.Client
}

func NewGroq(baseURL, model, apiKey string, httpClient *http.Client) *Groq {
	if baseURL == "" {
		baseURL = GroqDefaultBaseURL
	}
	if model == "" {
		model = GroqDefaultModel
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Groq{BaseURL: baseURL, Model: model, APIKey: apiKey, HTTP: httpClient}
}

// ChatCompletion delegates to callOpenAICompatChatCompletions -- see
// DeepSeek.ChatCompletion's identical delegation in deepseek.go and that
// function's own doc comment in openaicompat.go.
func (g *Groq) ChatCompletion(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	return callOpenAICompatChatCompletions(ctx, g.HTTP, g.BaseURL, g.Model, g.APIKey, req)
}

// groqLlama33ContextWindow is GroqDefaultModel's (llama-3.3-70b-versatile)
// published context length, from Groq's own current model list
// (console.groq.com/docs/models' production models table), not guessed.
// Tied to GroqDefaultModel specifically -- a different model configured
// via Model would need its own real number, not this one assumed to
// still apply.
const groqLlama33ContextWindow = 131_072

// ContextWindow returns groqLlama33ContextWindow (see its own doc
// comment) -- accurate for GroqDefaultModel, the only model this has
// actually been verified against.
func (g *Groq) ContextWindow() int {
	return groqLlama33ContextWindow
}
