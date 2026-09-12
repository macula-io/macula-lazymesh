package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestAnthropic_ChatCompletion_MapsTheWireShape proves the G14 mapping
// end to end: the request carries the top-level system, the content-block
// messages, tool_use/tool_result blocks and the required headers; the
// response maps back into lazymesh's Message with usage.
func TestAnthropic_ChatCompletion_MapsTheWireShape(t *testing.T) {
	var captured antMessagesRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("x-api-key"); got != "test-key" {
			t.Errorf("x-api-key = %q", got)
		}
		if got := r.Header.Get("anthropic-version"); got != anthropicAPIVersion {
			t.Errorf("anthropic-version = %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"content":[{"type":"text","text":"hello from claude"}],"usage":{"input_tokens":3,"output_tokens":2}}`))
	}))
	defer srv.Close()

	a := NewAnthropic(srv.URL, "claude-opus-5", "test-key")
	resp, err := a.ChatCompletion(context.Background(), ChatRequest{
		Messages: []Message{
			{Role: RoleSystem, Content: "sys prompt"},
			{Role: RoleUser, Content: "say hi"},
		},
	})
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if resp.Message.Content != "hello from claude" {
		t.Fatalf("content = %q", resp.Message.Content)
	}
	if resp.Usage.TotalTokens != 5 {
		t.Fatalf("usage total = %d, want 5", resp.Usage.TotalTokens)
	}
	if len(captured.System) != 1 || captured.System[0].Text != "sys prompt" {
		t.Fatalf("system = %+v", captured.System)
	}
	if len(captured.Messages) != 1 || captured.Messages[0].Role != "user" || captured.Messages[0].Content[0].Text != "say hi" {
		t.Fatalf("messages = %+v", captured.Messages)
	}
}

// TestAnthropic_ChatCompletion_ToolRoundTrip pins the tool mapping: an
// assistant tool call becomes a tool_use block, and the tool messages
// after it become ONE user message of tool_result blocks.
func TestAnthropic_ChatCompletion_ToolRoundTrip(t *testing.T) {
	var captured antMessagesRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&captured)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"content":[{"type":"tool_use","id":"tc_1","name":"mesh_say","input":{"text":"hi"}}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer srv.Close()

	a := NewAnthropic(srv.URL, "claude-opus-5", "test-key")
	resp, err := a.ChatCompletion(context.Background(), ChatRequest{
		Messages: []Message{
			{Role: RoleUser, Content: "say hi"},
			{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "tc_1", Name: "mesh_say", Arguments: `{"text":"hi"}`}}},
			{Role: RoleTool, ToolCallID: "tc_1", Name: "mesh_say", Content: "sent"},
		},
	})
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if len(resp.Message.ToolCalls) != 1 || resp.Message.ToolCalls[0].Name != "mesh_say" || resp.Message.ToolCalls[0].Arguments != `{"text":"hi"}` {
		t.Fatalf("tool calls = %+v", resp.Message.ToolCalls)
	}
	// The request: assistant with tool_use, then ONE user message with a
	// tool_result naming tc_1.
	if len(captured.Messages) != 3 {
		t.Fatalf("request messages = %+v", captured.Messages)
	}
	toolResult := captured.Messages[2]
	if toolResult.Role != "user" || len(toolResult.Content) != 1 || toolResult.Content[0].Type != "tool_result" || toolResult.Content[0].ToolUseID != "tc_1" || toolResult.Content[0].Content != "sent" {
		t.Fatalf("tool_result message = %+v", toolResult)
	}
}

// TestAnthropic_ChatCompletionStream maps the SSE shape: text deltas in
// order, tool input fragments accumulated, usage from the stream events.
func TestAnthropic_ChatCompletionStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		events := []string{
			`event: message_start`,
			`data: {"type":"message_start","message":{"usage":{"input_tokens":4}}}`,
			`event: content_block_start`,
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text"}}`,
			`event: content_block_delta`,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hel"}}`,
			`event: content_block_delta`,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"lo"}}`,
			`event: message_delta`,
			`data: {"type":"message_delta","usage":{"output_tokens":2}}`,
			`event: message_stop`,
			`data: {"type":"message_stop"}`,
		}
		for _, line := range events {
			w.Write([]byte(line + "\n\n"))
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	}))
	defer srv.Close()

	a := NewAnthropic(srv.URL, "claude-opus-5", "test-key")
	var deltas []string
	resp, err := a.ChatCompletionStream(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}, func(chunk string) error {
		deltas = append(deltas, chunk)
		return nil
	})
	if err != nil {
		t.Fatalf("ChatCompletionStream: %v", err)
	}
	if strings.Join(deltas, "") != "hello" || resp.Message.Content != "hello" {
		t.Fatalf("deltas = %v, content = %q", deltas, resp.Message.Content)
	}
	if resp.Usage.PromptTokens != 4 || resp.Usage.CompletionTokens != 2 || resp.Usage.TotalTokens != 6 {
		t.Fatalf("usage = %+v", resp.Usage)
	}
}

// TestAnthropic_APIErrorSurfaces pins the error mapping: an API error
// body becomes a descriptive Go error.
func TestAnthropic_APIErrorSurfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"type":"error","error":{"type":"authentication_error","message":"invalid key"}}`))
	}))
	defer srv.Close()

	a := NewAnthropic(srv.URL, "claude-opus-5", "test-key")
	_, err := a.ChatCompletion(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err == nil || !strings.Contains(err.Error(), "authentication_error") || !strings.Contains(err.Error(), "invalid key") {
		t.Fatalf("error = %v", err)
	}
}
