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
	// Provider selects the LLM backend. "deepseek" (also what empty
	// resolves to), "nvidia", and "groq" are working implementations;
	// "anthropic" exists as an interface-shape stub (see
	// internal/provider/anthropic.go) and returns an error if selected --
	// Anthropic's Messages API has a genuinely different shape from the
	// other three's shared OpenAI-compatible one, so it needs real work,
	// not just a config value. Default stays deepseek regardless of what
	// else is available: it was chosen specifically for being cheap
	// enough to run an agent against continuously.
	//
	// nvidia is available but this workspace's own fleet dropped it as
	// its default elsewhere (2026-09-07): its "free" tier turned out to
	// be a trial-credit pool that exhausted under sustained production
	// load, not a genuine free tier, and continued use past that was
	// against NVIDIA's own ToS for the purpose -- worth knowing before
	// switching a long-running agent to it, not a reason it was removed
	// as an option here.
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
	// Set this explicitly to the new provider's own key file when you
	// change Provider -- there is no auto-switching default path per
	// provider (deliberately: see IdentityFile's own doc comment below
	// for why an auto-switched default is the wrong shape here).
	APIKeyFile string `yaml:"api_key_file"`
	// IdentityFile, if set, is passed to macula-mcp as MACULA_MCP_IDENTITY
	// so this lazymesh instance keeps the same mesh node_id across a full
	// harness restart (not just this process's own macula-mcp child
	// dying and respawning). Empty by default, deliberately: macula-mcp's
	// own default identity logic (session-id/PPID-scoped) already gives
	// stable-across-restart AND distinct-across-concurrent-instances for
	// free, with no override needed. Setting this to a FIXED path shared
	// by more than one concurrently-running lazymesh instance collides
	// them onto the same node_id -- macula-mcp's own docs call that
	// "old-style shared-identity behavior". Found live 2026-09-07:
	// Default() used to set this unconditionally, so any two lazymesh
	// instances on one machine looked like the same agent to the mesh
	// and to each other's Presence panel. Only set this yourself if you
	// specifically need identity to survive a full restart of this one
	// instance, and give it a path unique to that instance.
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
	// MaculaMCPVersion pins the exact @macula-io/mcp release Spawn and
	// realmjoin.Join run. Empty (Default()'s own value, since 2026-09-10)
	// means no pin: both float to npm's latest published release on every
	// launch, the same way every other MCP client on this machine --
	// including this workspace's own Claude Code .mcp.json config --
	// already spawns macula-mcp.
	//
	// This reverses an earlier pinned-by-default policy that a Fable
	// security review (finding-2) drove, and which this field's own history
	// used to document commit-by-commit (0.23.0 through 0.27.0, each bump
	// individually read and confirmed additive before being trusted). Raf
	// explicitly overruled that policy 2026-09-10: "frankly, I'll overrule
	// Fable here... I know of no other harness that pins MCP servers to 1
	// version." The concrete cost that surfaced the question: this repo had
	// stayed pinned to 0.28.1 through 0.29.0 (which shipped session_name)
	// purely because nobody had done the deliberate manual bump -- exactly
	// the friction a mandatory pin imposes, and what this reversal removes.
	//
	// DO NOT revert this to a pinned default because the history above
	// looks like evidence it should stay pinned -- it is not an oversight,
	// it is Raf's explicit direction overruling that earlier finding. An
	// operator who wants a pin back (their own review cadence, an
	// environment where floating is unacceptable) can still set this
	// explicitly in their own config.yaml; that path is untouched, it's
	// simply no longer the default. Kept in sync with (but not imported
	// from, to keep this package a leaf with no cross-package awareness)
	// mcpclient's own launchCommand and realmjoin's own newCommand, which
	// both treat empty the same way.
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
	// ExpressiveStyle permits the agent's buildSystemPrompt tone guidance
	// to encourage emoji/expressive conversational style in room text
	// (mesh_say), the way goose's own mesh conversation does -- an
	// operator preference, per Raf's own steer 2026-09-06
	// (macula-io/macula-lazymesh#5), not a hardcoded persona. Off by
	// default, same conservative posture as LocalTools/ToolAllowlist: an
	// operator who wants the drier existing tone is never stuck with a
	// style change they didn't ask for; one who wants it sets this true
	// in their own config.yaml.
	ExpressiveStyle bool `yaml:"expressive_style,omitempty"`
	// MeshServicesEnabled gates internal/meshservices' 16 curated
	// mesh_service_* tools (hecate-rag/hecate_agora/hecate_graph corpus
	// search) entirely -- off by default, per Fable's own R2 review
	// (2026-09-07): it explicitly rejected trimming this catalog further
	// or exposing it per-turn, and instead accepted gating it session-
	// static, at startup, because a room-chat-only agent will usually
	// never touch corpus search at all. Measured live the same day: with
	// this off, the fixed prefix (system prompt + every allowlisted
	// tool's schema) drops well under the 1,500-token design target for
	// the common case; an operator who actually wants corpus search sets
	// this true in their own config.yaml and pays that catalog's real
	// cost deliberately. Same conservative posture as LocalTools and
	// ExpressiveStyle: a capability nobody asked for is never silently
	// on. See internal/meshservices/catalog.go's own doc comment for what
	// the 16 procedures are and why each one was curated as read-safe.
	MeshServicesEnabled bool `yaml:"mesh_services_enabled,omitempty"`
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
// cheapest GA model, and lazymesh's own conventional key path under the
// user's home directory. IdentityFile is deliberately left empty -- see
// its own doc comment on Config -- letting macula-mcp's own default
// identity scoping take over rather than defaulting into a path that
// collides two concurrently-running instances onto one node_id.
func Default() Config {
	home, _ := os.UserHomeDir()
	return Config{
		Provider:   "deepseek",
		Model:      "deepseek-v4-flash",
		APIKeyFile: filepath.Join(home, ".ai-api-keys", ".deepseek-api-keys", "lazymesh"),
		LocalTools: LocalTools{
			Enabled:    false,
			WorkingDir: filepath.Join(home, ".config", "lazymesh", "workspace"),
		},
		StatusBarPosition:   "bottom",
		MaculaMCPVersion:    "", // float to latest -- see the field's own doc comment
		ContactPolicyFile:   filepath.Join(home, ".config", "lazymesh", "contact_policy.json"),
		RingPolicy:          "always-ask",
		ExpressiveStyle:     false,
		MeshServicesEnabled: false,
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
