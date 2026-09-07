package provider

import (
	"context"
	"net/http"
)

// NVIDIADefaultBaseURL is NVIDIA's OpenAI-compatible API root (the
// integrate.api.nvidia.com endpoint), verified against this workspace's
// own live fleet config (macula-demo/infrastructure's biotope compose
// file, which already calls this exact URL) rather than assumed.
const NVIDIADefaultBaseURL = "https://integrate.api.nvidia.com/v1"

// NVIDIADefaultModel is the same model id this workspace's other
// NVIDIA-backed services already default to (hecate-spartan, the
// hecate-graph narrator, ...) -- not picked independently for lazymesh.
// NVIDIA model ids are namespaced (org/model); a bare id 404s, and a
// retired one 410s, so this is verified against a live, already-working
// caller rather than guessed.
const NVIDIADefaultModel = "moonshotai/kimi-k3"

// NVIDIA is a second Provider: NVIDIA's OpenAI-compatible chat
// completions API, the free tier the rest of this workspace's fleet
// defaults to. Its wire shape needs no separate request/response mapping
// from DeepSeek's -- see callOpenAICompatChatCompletions' own doc comment
// in openaicompat.go for why, and contrast with anthropic.go's Anthropic,
// whose genuinely different Messages API does need one and is why that
// one stays an unimplemented stub.
//
// lazymesh's own default stays DeepSeek regardless of this existing:
// DeepSeek was chosen specifically for being cheap enough to run an agent
// against continuously, and this workspace's own operational experience
// is that NVIDIA's free tier hits account-level rate limits under
// sustained use -- worth knowing before pointing a long-running agent at
// it, not a reason to avoid it for shorter or occasional use.
type NVIDIA struct {
	BaseURL string
	Model   string
	APIKey  string
	HTTP    *http.Client
}

func NewNVIDIA(baseURL, model, apiKey string, httpClient *http.Client) *NVIDIA {
	if baseURL == "" {
		baseURL = NVIDIADefaultBaseURL
	}
	if model == "" {
		model = NVIDIADefaultModel
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &NVIDIA{BaseURL: baseURL, Model: model, APIKey: apiKey, HTTP: httpClient}
}

// ChatCompletion delegates to callOpenAICompatChatCompletions -- see
// DeepSeek.ChatCompletion's identical delegation in deepseek.go and that
// function's own doc comment in openaicompat.go.
func (n *NVIDIA) ChatCompletion(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	return callOpenAICompatChatCompletions(ctx, n.HTTP, n.BaseURL, n.Model, n.APIKey, req)
}

// nvidiaKimiK3ContextWindow is NVIDIADefaultModel's (moonshotai/kimi-k3)
// real context length -- not looked up, taken directly from a live
// instance's own 400 error during the 2026-09-07 runaway-context
// incident ("This model's maximum context length is 1048576 tokens"),
// about as authoritative a source as exists: the backend reporting its
// own configured limit. Tied to NVIDIADefaultModel specifically -- a
// different model configured via NVIDIA_MODEL would need its own real
// number, not this one assumed to still apply.
const nvidiaKimiK3ContextWindow = 1_048_576

// ContextWindow returns nvidiaKimiK3ContextWindow (see its own doc
// comment) -- accurate for NVIDIADefaultModel, the only model this has
// actually been verified against.
func (n *NVIDIA) ContextWindow() int {
	return nvidiaKimiK3ContextWindow
}
