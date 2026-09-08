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
	// removed) and RED/GREEN tested upstream. 0.24.2 (96dee2a, single commit)
	// fixes a related but distinct bug: serve()'s teardown-then-serve
	// ordering could leave ring_service.ts's own direct-dial registration
	// completely torn down (not degraded) if a periodic 20-minute renewal's
	// connect/serve() call failed, until the next renewal happened to
	// succeed -- serve() now connects and serves the replacement FIRST,
	// only retiring the previous still-working registration once that
	// succeeds, plus backoff on a failed renewal instead of waiting the
	// full interval again. Confirmed additive (both changed functions keep
	// their existing happy path, only reorder/add retry logic) and
	// RED/GREEN tested upstream (30 files, 433 tests). Explicitly does NOT
	// claim to fix the separate, still-open "ring recorded then vanished"
	// mystery -- left open on its own terms, not force-unified.
	//
	// 0.25.0 (03d7c3d, single commit): mesh_ring's `to`, mesh_open_room's
	// `participants`, and mesh_trust_agent/mesh_untrust_agent's `node_id`
	// now also accept a petname, resolved to the real node_id once at each
	// call's own top before anything else runs -- a raw 64-hex value (all
	// this repo has ever sent) still passes through unchanged, so this is
	// new INPUT flexibility only, nothing existing removed or renamed on
	// any arg or response shape. 16 new tests upstream (including a real
	// brute-forced sha256 petname collision, not simulated).
	//
	// 0.25.1 (three commits, all reviewed directly by this repo's own team
	// -- see macula-io/macula-mcp#2/#3/#4/#5): cf7c7d8 fixes
	// ring_service.ts's INITIAL registration (not just renewal, which
	// 0.24.2 above already covered) retrying with backoff instead of
	// permanently giving up on one transient failure -- pure internal
	// reliability fix, no wire shape change. 3f6b7b1 adds a new
	// `interval_seconds` field to every agent.hello and a new computed
	// `stale` field to mesh_agents' response (both additive -- existing
	// fields untouched), plus documentation-only comments about the
	// hello/goodbye trust model (zero functional change). 8eaa3b3 is the
	// version bump itself, no code change. Confirmed additive throughout:
	// no tool renamed or removed, no existing response field removed, no
	// existing arg made required that wasn't before. 458 tests passing
	// upstream, typecheck clean.
	//
	// 0.25.2 (4c534fb, single commit): purely cosmetic -- mesh_hello's
	// DEFAULT_BANNER figlet art had one letter wrong (rendered "MTCULA"),
	// fixed to spell "MACULA". A string-literal-only diff in one file
	// (src/mesh_hello.ts), read directly: no other line touched, no
	// behavior, no tool arg/response shape affected at all.
	//
	// 0.26.0 (two commits): 515f227 adds mesh_wait_ring, the blocking
	// counterpart to polling mesh_read_inbox for a new ring -- purely
	// additive, a new tool, nothing existing touched (this is what
	// internal/ringwaiter switched to, replacing its own former polling
	// loop -- see that package's own doc comment). d2a1744 adds
	// MACULA_MCP_TERSE_TOOLS as an opt-in env var for shorter tool
	// descriptions -- unset by default, so every existing tool's
	// description is byte-for-byte unchanged unless an operator opts in;
	// this codebase's own internal/agent.TerseDescriptionSource already
	// does the equivalent client-side and doesn't set this var, so
	// nothing here is affected either way.
	//
	// 0.26.1 (1849ff5, single commit, cut same-day as a correctness
	// fix): a REAL bug, found live 2026-09-07/08 investigating why a
	// ring never surfaced on the recipient's side of two same-machine
	// lazymesh instances (credit to this investigation: reproduced with
	// two real macula-mcp processes, confirmed via the raw sqlite row,
	// not theorized). rings.sqlite3 is one file per MACHINE; one ring
	// produces two legitimate rows in that shared file (the caller's own
	// "out" bookkeeping, written synchronously before the network call
	// even goes out, and the callee's own "in" bookkeeping, written when
	// the call arrives) -- but the old schema's ring_id TEXT PRIMARY KEY
	// alone meant the second insert always silently no-op'd via
	// ON CONFLICT DO NOTHING, so the callee's own copy -- what
	// mesh_read_inbox/mesh_wait_ring/mesh_answer_ring all read on ITS
	// side -- simply never existed whenever caller and callee shared a
	// machine (deterministic, not a race: the caller's local write always
	// precedes the callee's network-triggered one). Fixed: primary key is
	// now (ring_id, direction), and answerRing's own UPDATE is now scoped
	// by direction too (the same collision would otherwise have let one
	// party's answer silently overwrite the other's once two rows could
	// share a ring_id). A real on-disk migration rebuilds an existing
	// old-schema file, verified against one, not just a fresh in-memory
	// db. No MCP tool's own argument or response shape changed --
	// confirmed by reading the diff directly (src/mesh_ring.ts,
	// src/ring_service.ts): every changed call site is an internal
	// TypeScript function signature (answerRing gaining a direction
	// parameter), nothing tool-schema-facing. Empirically re-verified
	// live with two real 0.26.1 processes after the bump: the callee's
	// own mesh_read_inbox now shows the pending ring, and
	// mesh_answer_ring succeeds against it.
	//
	// 0.27.0 (2026-09-08): adds mesh_list_realms (confirmed realm
	// memberships, an ordinary read-only tool) -- backs the `r` panel's
	// own listing (internal/tui/mesh.go's fetchMeshState). Also adds
	// mesh_join_realm's own multi-realm counterpart, macula-mcp-realm
	// (a separate CLI binary, deliberately NEVER an MCP tool -- see
	// internal/realmjoin's own doc comment for why), which this bump
	// makes available via npx at the SAME pinned version the persistent
	// macula-mcp server already runs, so a fresh join's credential lands
	// under the same @macula-io/mcp release's own schema/behavior.
	// mesh_join_realm's own existing shape is completely unchanged.
	// Confirmed by reading the diff directly, not assumed: no other tool
	// schema changed.
	//
	// Whoever next edits this default should do the same before bumping it, never
	// bump just to "pick up whatever's newest." Kept in sync with (but not
	// imported from, to
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
		MaculaMCPVersion:    "0.27.0",
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
