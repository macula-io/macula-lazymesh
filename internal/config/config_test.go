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

func TestDefault_MaculaMCPVersionIsSet(t *testing.T) {
	if got := Default().MaculaMCPVersion; got == "" {
		t.Fatalf("expected a non-empty default macula_mcp_version")
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
