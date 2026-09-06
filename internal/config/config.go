// Package config loads lazymesh's configuration: which LLM provider to
// drive the agent loop with, and where to keep a stable mesh identity.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is lazymesh's on-disk configuration shape.
type Config struct {
	// Provider selects the LLM backend. Currently "deepseek" is the only
	// working implementation; "anthropic" exists as an interface-shape
	// stub (see internal/provider/anthropic.go) and returns an error if
	// selected.
	Provider string `yaml:"provider"`
	// Model is the backend's own model id string, verified against that
	// backend's own docs, not copied from another harness's config.
	Model string `yaml:"model"`
	// BaseURL overrides the provider's default API root. Empty uses the
	// provider's own default.
	BaseURL string `yaml:"base_url,omitempty"`
	// APIKeyFile points at a bare-value key file, matching this
	// workspace's ~/.ai-api-keys/.<provider>-api-keys/<consumer>
	// convention. Never a literal key in this struct or on disk here.
	APIKeyFile string `yaml:"api_key_file"`
	// IdentityFile, if set, is passed to macula-mcp as MACULA_MCP_IDENTITY
	// so this lazymesh instance keeps the same mesh node_id across
	// restarts instead of macula-mcp's default fresh-identity-per-launch
	// behavior.
	IdentityFile string `yaml:"identity_file,omitempty"`
	// LocalTools is Phase 2's second, separately configurable tool source
	// (shell_exec/read_file/write_file). Off by default -- the MVP's whole
	// value proposition is having NO extra tools unless explicitly enabled.
	LocalTools LocalTools `yaml:"local_tools,omitempty"`
	// ToolAllowlist, if set, overrides agent.DefaultToolAllowlist -- the
	// deny-by-default set of tool names an agent driven by untrusted mesh
	// content is permitted to see or call at all (see
	// internal/agent/allowlist.go for why this exists). Left empty by
	// Default() deliberately: main.go falls back to
	// agent.DefaultToolAllowlist rather than this package hardcoding or
	// importing that list, keeping config a leaf package. Setting this
	// yourself, including adding local_tools' own tool names
	// (shell_exec/read_file/write_file), is an explicit, conscious choice
	// on your own machine -- never a side effect of local_tools.enabled
	// alone. Adding shell_exec/read_file/write_file here re-exposes the
	// mesh-content-to-arbitrary-execution risk the default allowlist
	// exists to prevent: this removes the capability from the default,
	// it does not mark peer-authored room text as untrusted. Only do this
	// in a trusted/private mesh context.
	ToolAllowlist []string `yaml:"tool_allowlist,omitempty"`
}

// LocalTools configures internal/localtools. Disabled by default; a
// working directory is required once enabled -- see localtools.New.
type LocalTools struct {
	Enabled             bool   `yaml:"enabled"`
	WorkingDir          string `yaml:"working_dir,omitempty"`
	ShellTimeoutSeconds int    `yaml:"shell_timeout_seconds,omitempty"`
}

// Default returns the MVP's default configuration: DeepSeek, its current
// cheapest GA model, and lazymesh's own conventional key/identity paths
// under the user's home directory.
func Default() Config {
	home, _ := os.UserHomeDir()
	return Config{
		Provider:     "deepseek",
		Model:        "deepseek-v4-flash",
		APIKeyFile:   filepath.Join(home, ".ai-api-keys", ".deepseek-api-keys", "lazymesh"),
		IdentityFile: filepath.Join(home, ".config", "lazymesh", "identity"),
		LocalTools: LocalTools{
			Enabled:    false,
			WorkingDir: filepath.Join(home, ".config", "lazymesh", "workspace"),
		},
	}
}

// DefaultPath is where Load looks when no path is given: matching
// macula-mcp's own ~/.config/<name>/ convention.
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".config", "lazymesh", "config.yaml"), nil
}

// Load reads and parses the config file at path. If path is empty,
// DefaultPath is used. A missing file is not an error -- Default() is
// returned instead, so a first run works with zero setup beyond an API
// key file.
func Load(path string) (Config, error) {
	if path == "" {
		p, err := DefaultPath()
		if err != nil {
			return Config{}, err
		}
		path = p
	}

	cfg := Default()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return Config{}, fmt.Errorf("read config %s: %w", path, err)
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config %s: %w", path, err)
	}
	return cfg, nil
}

// APIKey reads and trims the bare-value key file at c.APIKeyFile. It is a
// method, not part of Load, so a key never sits in a Config value that
// might get logged or serialized wholesale.
func (c Config) APIKey() (string, error) {
	if c.APIKeyFile == "" {
		return "", fmt.Errorf("api_key_file not set in config")
	}
	data, err := os.ReadFile(c.APIKeyFile)
	if err != nil {
		return "", fmt.Errorf("read api key file %s: %w", c.APIKeyFile, err)
	}
	key := strings.TrimSpace(string(data))
	if key == "" {
		return "", fmt.Errorf("api key file %s is empty", c.APIKeyFile)
	}
	return key, nil
}
