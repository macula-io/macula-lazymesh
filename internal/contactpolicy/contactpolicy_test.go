package contactpolicy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestIsTrusted_MissingFileIsNotTrusted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.json")
	if IsTrusted(path, "abc123") {
		t.Fatalf("expected a missing file to mean not trusted")
	}
}

func TestIsTrusted_EmptyPathIsNotTrusted(t *testing.T) {
	if IsTrusted("", "abc123") {
		t.Fatalf("expected an empty path to mean not trusted")
	}
}

func TestIsTrusted_MatchesAllowlistedID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "contact_policy.json")
	writeJSON(t, path, map[string]any{
		"contact_policy": "ask",
		"allowlist":      []string{"AABBCC"},
	})
	if !IsTrusted(path, "aabbcc") {
		t.Fatalf("expected case-insensitive match to succeed")
	}
	if IsTrusted(path, "ddeeff") {
		t.Fatalf("expected an unlisted id to be untrusted")
	}
}

func TestIsTrusted_MalformedFileIsNotTrusted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "contact_policy.json")
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if IsTrusted(path, "anything") {
		t.Fatalf("expected a malformed file to mean not trusted, never a crash")
	}
}

func TestEnsure_CreatesFileWithPolicy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "contact_policy.json")
	if err := Ensure(path, "open"); err != nil {
		t.Fatalf("Ensure returned error: %v", err)
	}
	got := readJSON(t, path)
	if got["contact_policy"] != "open" {
		t.Fatalf("expected contact_policy open, got %v", got["contact_policy"])
	}
}

// This is the specific discipline that matters: Ensure must never destroy
// an allowlist mesh_trust_agent (or a human) already built up, even
// though this package only ever writes the contact_policy field itself.
func TestEnsure_PreservesExistingAllowlist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "contact_policy.json")
	writeJSON(t, path, map[string]any{
		"contact_policy": "ask",
		"allowlist":      []string{"aabbcc"},
		"offers":         []string{"code review"},
	})

	if err := Ensure(path, "closed"); err != nil {
		t.Fatalf("Ensure returned error: %v", err)
	}

	got := readJSON(t, path)
	if got["contact_policy"] != "closed" {
		t.Fatalf("expected contact_policy closed, got %v", got["contact_policy"])
	}
	allowlist, ok := got["allowlist"].([]any)
	if !ok || len(allowlist) != 1 || allowlist[0] != "aabbcc" {
		t.Fatalf("expected the existing allowlist to survive, got %v", got["allowlist"])
	}
	if got["offers"] == nil {
		t.Fatalf("expected offers to survive, got %v", got)
	}
}

func TestEnsure_ThenIsTrusted_RoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "contact_policy.json")
	writeJSON(t, path, map[string]any{"allowlist": []string{"deadbeef"}})

	if err := Ensure(path, "ask"); err != nil {
		t.Fatalf("Ensure returned error: %v", err)
	}
	if !IsTrusted(path, "deadbeef") {
		t.Fatalf("expected the allowlist entry to survive Ensure and be found by IsTrusted")
	}
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	return v
}
