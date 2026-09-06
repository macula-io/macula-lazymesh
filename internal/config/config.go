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
	// StatusBarPosition is where the TUI's persistent one-line mesh-status
	// strip renders: "top" or "bottom". Any other value (including empty)
	// is treated as "bottom" -- matching vim's statusline and tmux's
	// status bar, both bottom by default.
	StatusBarPosition string `yaml:"status_bar_position,omitempty"`
	// MaculaMCPVersion pins the exact @macula-io/mcp release Spawn runs
	// (config, not a Go const, per Raf's own steer 2026-09-06: the
	// security property Fable's finding-2 fix actually needed was "not a
	// floating tag, always an explicit deliberate value," not "compiled
	// into the binary" -- a config-driven default gives an operator who
	// never touches it the same verified value, and one who edits
	// config.yaml is making their own equally deliberate choice, without
	// needing a Go toolchain to do it). Default() sets this to the
	// version actually verified here: read every commit between 0.23.0
	// and 0.24.0 directly (petname fields, a real mesh_open_room
	// ring-sequencing bugfix, mesh_trust_agent/mesh_wait_room) and
	// confirmed all of it is additive -- nothing this codebase depends on
	// was removed or restructured. 0.24.1 (ceaa13f, single commit) is the
	// fix for the mesh_read_inbox/mesh_rooms cold-start race THIS repo's
	// own live testing found and reported upstream: presence.currentNodeId()
	// now falls back to the local identity file's node_id instead of
	// reading undefined on a fresh identity's first call, which had been
	// silently omitting/hiding pending rings -- exactly the failure mode
	// blocking this repo's own ring pop-up from ever being seen live.
	// Confirmed additive (two files touched, both gain a fallback, nothing
	// removed) and RED/GREEN tested upstream. Whoever next edits this
	// default should do the same before bumping it, never bump just to
	// "pick up whatever's newest." Kept in sync with (but not imported from, to
	// keep this package a leaf with no cross-package awareness, matching
	// how ToolAllowlist's own default is resolved in main.go instead of
	// here) mcpclient.DefaultMaculaMCPVersion.
	MaculaMCPVersion string `yaml:"macula_mcp_version,omitempty"`
	// ContactPolicyFile, if set, is passed to macula-mcp as
	// MACULA_MCP_CONTACT_POLICY_FILE so this lazymesh instance's contact
	// policy and trust allowlist (mesh_trust_agent) are isolated from
	// every other macula-mcp instance on the same machine, which
	// otherwise all share ~/.config/macula-mcp/contact_policy.json by
	// default regardless of identity.
	ContactPolicyFile string `yaml:"contact_policy_file,omitempty"`
	// RingPolicy is lazymesh's own phone-metaphor standing answer to an
	// incoming ring: "always-ask" (default, every ring pops up), "auto-
	// accept-known" (peers already on ContactPolicyFile's own allowlist
	// -- the one mesh_trust_agent/the pop-up's "Answer + Trust" action
	// manages -- skip the pop-up and are accepted immediately; anyone
	// else still pops up), "accept-everyone", or "do-not-disturb".
	//
	// This does NOT map onto macula-mcp's own contact_policy tiers
	// directly -- verified against ring_service.ts before assuming it
	// could: real "allowlist" mode DECLINES anyone not listed outright,
	// it never defers to ask, so there is no macula-mcp tier that means
	// "known auto-accept, strangers still get asked." "always-ask" and
	// "auto-accept-known" both keep the underlying file's contact_policy
	// at "ask" (every ring genuinely defers, mesh-side) and the
	// known/unknown split happens in lazymesh itself
	// (internal/contactpolicy.IsTrusted, consulted before ever showing
	// the pop-up) -- "accept-everyone"/"do-not-disturb" do map directly,
	// onto "open"/"closed", since those need no per-peer judgment at all.
	RingPolicy string `yaml:"ring_policy,omitempty"`
}

// RingPolicyContactPolicyFileValue translates p into the value written
// into ContactPolicyFile's own contact_policy field -- the single place
// this mapping happens, so main.go's setup and any future reader agree
// on it. Unrecognized values behave as "always-ask" (the safe default).
func RingPolicyContactPolicyFileValue(p string) string {
	switch p {
	case "accept-everyone":
		return "open"
	case "do-not-disturb":
		return "closed"
	default: // "always-ask", "auto-accept-known", or unrecognized
		return "ask"
	}
}

// RingPolicyAutoAcceptsKnown reports whether p means known/allowlisted
// peers should skip the ring pop-up entirely.
func RingPolicyAutoAcceptsKnown(p string) bool {
	return p == "auto-accept-known"
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
		StatusBarPosition: "bottom",
		MaculaMCPVersion:  "0.24.1",
		ContactPolicyFile: filepath.Join(home, ".config", "lazymesh", "contact_policy.json"),
		RingPolicy:        "always-ask",
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
