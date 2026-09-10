package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoad_MissingFileReturnsDefault(t *testing.T) {
	dir := t.TempDir()
	cfg, err := Load(filepath.Join(dir, "does-not-exist.yaml"))
	if err != nil {
		t.Fatalf("Load on a missing file should not error, got: %v", err)
	}
	def := Default()
	if cfg.Provider != def.Provider || cfg.Model != def.Model {
		t.Fatalf("expected default config, got %+v", cfg)
	}
}

func TestLoad_OverridesDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	yaml := "provider: anthropic\nmodel: claude-test\napi_key_file: /tmp/whatever\n"
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Provider != "anthropic" {
		t.Fatalf("expected provider anthropic, got %q", cfg.Provider)
	}
	if cfg.Model != "claude-test" {
		t.Fatalf("expected model claude-test, got %q", cfg.Model)
	}
}

func TestConfig_APIKey_ReadsAndTrims(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "key")
	if err := os.WriteFile(keyPath, []byte("  sk-test-123  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{APIKeyFile: keyPath}

	key, err := cfg.APIKey()
	if err != nil {
		t.Fatalf("APIKey returned error: %v", err)
	}
	if key != "sk-test-123" {
		t.Fatalf("expected trimmed key %q, got %q", "sk-test-123", key)
	}
}

func TestConfig_APIKey_EmptyFileErrors(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "key")
	if err := os.WriteFile(keyPath, []byte("   \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{APIKeyFile: keyPath}

	if _, err := cfg.APIKey(); err == nil {
		t.Fatalf("expected an error for an empty key file")
	}
}

func TestConfig_APIKey_MissingPathErrors(t *testing.T) {
	cfg := Config{}
	if _, err := cfg.APIKey(); err == nil {
		t.Fatalf("expected an error when api_key_file is unset")
	}
}

func TestDefault_StatusBarPositionIsBottom(t *testing.T) {
	if got := Default().StatusBarPosition; got != "bottom" {
		t.Fatalf("expected default status_bar_position bottom, got %q", got)
	}
}

func TestLoad_OverridesStatusBarPosition(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("status_bar_position: top\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.StatusBarPosition != "top" {
		t.Fatalf("expected status_bar_position top, got %q", cfg.StatusBarPosition)
	}
}

// Raf's explicit direction 2026-09-10, overruling an earlier Fable
// finding-2 pin-by-default policy: lazymesh floats to npm's latest
// @macula-io/mcp release by default now, the same as every other MCP
// client. Empty is the deliberate default, not an oversight -- see
// MaculaMCPVersion's own doc comment.
func TestDefault_MaculaMCPVersionFloatsByDefault(t *testing.T) {
	if got := Default().MaculaMCPVersion; got != "" {
		t.Fatalf("expected empty default macula_mcp_version (float to latest), got %q", got)
	}
}

func TestLoad_OverridesMaculaMCPVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("macula_mcp_version: 9.9.9\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.MaculaMCPVersion != "9.9.9" {
		t.Fatalf("expected macula_mcp_version 9.9.9, got %q", cfg.MaculaMCPVersion)
	}
}

func TestDefault_ContactPolicyFileIsIsolatedFromMaculaMCPDefault(t *testing.T) {
	got := Default().ContactPolicyFile
	if got == "" {
		t.Fatalf("expected a non-empty default contact_policy_file")
	}
	if strings.Contains(got, "macula-mcp") {
		t.Fatalf("expected a lazymesh-specific path, not macula-mcp's own shared default: %q", got)
	}
}

// Found live 2026-09-07: Default() used to set IdentityFile to a single
// fixed path (~/.config/lazymesh/identity) with no per-instance scoping.
// Every lazymesh instance on a machine passed that same path through as
// MACULA_MCP_IDENTITY, which macula-mcp reads literally and
// unconditionally -- bypassing its own already-correct default identity
// scoping (by CLAUDE_CODE_SESSION_ID or PPID, added to macula-mcp
// 2026-09-02) entirely. Two concurrent lazymesh instances with default
// config ended up sharing one mesh node_id, showing as a single identical
// entry in each other's Presence panel instead of two distinct agents.
// IdentityFile must stay empty by default -- opt-in only, for the
// legitimate "durable identity across a full harness restart" case
// macula-mcp's own docs still call out.
func TestDefault_IdentityFileIsEmptyByDefault(t *testing.T) {
	if got := Default().IdentityFile; got != "" {
		t.Fatalf("expected no default identity_file (opt-in only, see macula-io/macula-lazymesh identity-collision fix), got %q", got)
	}
}

func TestDefault_RingPolicyIsAlwaysAsk(t *testing.T) {
	if got := Default().RingPolicy; got != "always-ask" {
		t.Fatalf("expected default ring_policy always-ask, got %q", got)
	}
}

// Covers macula-io/macula-lazymesh#5: expressive_style must be off by
// default -- an operator who never touches config.yaml gets the existing
// dry tone unchanged -- and settable via config.
func TestDefault_ExpressiveStyleIsOff(t *testing.T) {
	if got := Default().ExpressiveStyle; got != false {
		t.Fatalf("expected default expressive_style false, got %v", got)
	}
}

func TestLoad_OverridesExpressiveStyle(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("expressive_style: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if !cfg.ExpressiveStyle {
		t.Fatalf("expected expressive_style true from config file, got false")
	}
}

func TestRingPolicyContactPolicyFileValue(t *testing.T) {
	cases := map[string]string{
		"always-ask":        "ask",
		"auto-accept-known": "ask", // the layered design's whole point: NOT "allowlist"
		"accept-everyone":   "open",
		"do-not-disturb":    "closed",
		"anything-else":     "ask", // unrecognized -> safe default
	}
	for in, want := range cases {
		if got := RingPolicyContactPolicyFileValue(in); got != want {
			t.Fatalf("%s: expected %q, got %q", in, want, got)
		}
	}
}

func TestRingPolicyAutoAcceptsKnown(t *testing.T) {
	if !RingPolicyAutoAcceptsKnown("auto-accept-known") {
		t.Fatalf("expected auto-accept-known to report true")
	}
	for _, other := range []string{"always-ask", "accept-everyone", "do-not-disturb", ""} {
		if RingPolicyAutoAcceptsKnown(other) {
			t.Fatalf("expected %q to report false", other)
		}
	}
}
