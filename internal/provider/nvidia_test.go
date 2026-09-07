package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNVIDIA_ChatCompletion_PlainReply(t *testing.T) {
	var capturedReq dsChatRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("expected Authorization 'Bearer test-key', got %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&capturedReq); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"hi there"}}]}`))
	}))
	defer srv.Close()

	n := NewNVIDIA(srv.URL, "moonshotai/kimi-k3", "test-key", nil)
	resp, err := n.ChatCompletion(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("ChatCompletion returned error: %v", err)
	}
	if resp.Message.Content != "hi there" {
		t.Fatalf("expected content %q, got %q", "hi there", resp.Message.Content)
	}
	if capturedReq.Model != "moonshotai/kimi-k3" {
		t.Fatalf("expected model moonshotai/kimi-k3 in request, got %q", capturedReq.Model)
	}
}

func TestNVIDIA_ChatCompletion_ToolCallRoundTrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req dsChatRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if len(req.Tools) != 1 || req.Tools[0].Function.Name != "mesh_say" {
			t.Errorf("expected mesh_say tool in request, got %+v", req.Tools)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[
			{"id":"call_1","type":"function","function":{"name":"mesh_say","arguments":"{\"text\":\"hi\"}"}}
		]}}]}`))
	}))
	defer srv.Close()

	n := NewNVIDIA(srv.URL, "moonshotai/kimi-k3", "test-key", nil)
	resp, err := n.ChatCompletion(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "say hi"}},
		Tools:    []ToolSpec{{Name: "mesh_say", Description: "say something", InputSchema: map[string]any{"type": "object"}}},
	})
	if err != nil {
		t.Fatalf("ChatCompletion returned error: %v", err)
	}
	if len(resp.Message.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(resp.Message.ToolCalls))
	}
	tc := resp.Message.ToolCalls[0]
	if tc.Name != "mesh_say" || tc.Arguments != `{"text":"hi"}` || tc.ID != "call_1" {
		t.Fatalf("unexpected tool call: %+v", tc)
	}
}

func TestNVIDIA_ChatCompletion_APIErrorSurfaced(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid api key","type":"authentication_error"}}`))
	}))
	defer srv.Close()

	n := NewNVIDIA(srv.URL, "", "bad-key", nil)
	_, err := n.ChatCompletion(context.Background(), ChatRequest{Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err == nil {
		t.Fatalf("expected an error for a 401 response")
	}
}

func TestNVIDIA_Defaults(t *testing.T) {
	n := NewNVIDIA("", "", "key", nil)
	if n.BaseURL != NVIDIADefaultBaseURL {
		t.Fatalf("expected default base URL %q, got %q", NVIDIADefaultBaseURL, n.BaseURL)
	}
	if n.Model != NVIDIADefaultModel {
		t.Fatalf("expected default model %q, got %q", NVIDIADefaultModel, n.Model)
	}
}
