//go:build live

package provider

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLiveDeepSeek_ReportsRealTokenUsage confirms ChatResponse.Usage is
// actually populated against the real DeepSeek API, not just parsed
// correctly from a synthetic fixture -- added for macula-io/macula-
// lazymesh#14's token-cost measurement (agent.Loop.Usage accumulates
// this across a conversation).
func TestLiveDeepSeek_ReportsRealTokenUsage(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	keyPath := filepath.Join(home, ".ai-api-keys", ".deepseek-api-keys", "lazymesh")
	keyBytes, err := os.ReadFile(keyPath)
	if err != nil {
		t.Skipf("no deepseek key at %s, skipping: %v", keyPath, err)
	}
	key := strings.TrimSpace(string(keyBytes))

	d := NewDeepSeek("", "", key, nil)
	resp, err := d.ChatCompletion(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "Reply with exactly one word: hello"}},
	})
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	t.Logf("response: %q, usage: %+v", resp.Message.Content, resp.Usage)
	if resp.Usage.TotalTokens == 0 {
		t.Errorf("expected non-zero TotalTokens from a real DeepSeek response, got %+v", resp.Usage)
	}
	if resp.Usage.PromptTokens == 0 || resp.Usage.CompletionTokens == 0 {
		t.Errorf("expected non-zero prompt and completion tokens, got %+v", resp.Usage)
	}
}
