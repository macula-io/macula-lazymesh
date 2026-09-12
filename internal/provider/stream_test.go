package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDeepSeek_ChatCompletionStream_DeltasAndUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Accept"); got != "text/event-stream" {
			t.Errorf("expected Accept text/event-stream, got %q", got)
		}
		var req dsChatStreamRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		if !req.Stream || !req.StreamOptions.IncludeUsage {
			t.Errorf("expected stream=true + include_usage in request, got %+v", req)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		chunks := []string{
			`data: {"choices":[{"delta":{"content":"hel"}}]}`,
			`data: {"choices":[{"delta":{"content":"lo "}}]}`,
			`data: {"choices":[{"delta":{"content":"world"}}]}`,
			`data: {"choices":[],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`,
			`data: [DONE]`,
		}
		for _, c := range chunks {
			if _, err := w.Write([]byte(c + "\n\n")); err != nil {
				return
			}
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	}))
	defer srv.Close()

	d := NewDeepSeek(srv.URL, "deepseek-v4-flash", "test-key", nil)
	var deltas []string
	resp, err := d.ChatCompletionStream(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}, func(chunk string) error {
		deltas = append(deltas, chunk)
		return nil
	})
	if err != nil {
		t.Fatalf("ChatCompletionStream returned error: %v", err)
	}
	if got := joinDeltas(deltas); got != "hello world" {
		t.Fatalf("deltas joined = %q, want %q", got, "hello world")
	}
	if resp.Message.Content != "hello world" {
		t.Fatalf("completed content = %q", resp.Message.Content)
	}
	if resp.Usage.TotalTokens != 5 {
		t.Fatalf("usage total = %d, want 5", resp.Usage.TotalTokens)
	}
}

func TestDeepSeek_ChatCompletionStream_ToolCallFragments(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		chunks := []string{
			`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"mesh_say","arguments":""}}]}}]}`,
			`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"text\":"}}]}}]}`,
			`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"hi\"}"}}]}}]}`,
			`data: [DONE]`,
		}
		for _, c := range chunks {
			if _, err := w.Write([]byte(c + "\n\n")); err != nil {
				return
			}
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	}))
	defer srv.Close()

	d := NewDeepSeek(srv.URL, "deepseek-v4-flash", "test-key", nil)
	resp, err := d.ChatCompletionStream(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "say hi"}},
	}, func(chunk string) error { return nil })
	if err != nil {
		t.Fatalf("ChatCompletionStream returned error: %v", err)
	}
	if len(resp.Message.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(resp.Message.ToolCalls))
	}
	tc := resp.Message.ToolCalls[0]
	if tc.ID != "call_1" || tc.Name != "mesh_say" || tc.Arguments != `{"text":"hi"}` {
		t.Fatalf("tool call = %+v", tc)
	}
}

// TestDeepSeek_ChatCompletionStream_DeltaConsumerErrorAborts proves an
// onDelta failure aborts the stream and is what the caller sees.
func TestDeepSeek_ChatCompletionStream_DeltaConsumerErrorAborts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	defer srv.Close()

	d := NewDeepSeek(srv.URL, "deepseek-v4-flash", "test-key", nil)
	_, err := d.ChatCompletionStream(context.Background(), ChatRequest{}, func(chunk string) error {
		return context.Canceled
	})
	if err == nil {
		t.Fatal("expected the consumer error to abort the stream")
	}
}

func joinDeltas(deltas []string) string {
	out := ""
	for _, d := range deltas {
		out += d
	}
	return out
}
