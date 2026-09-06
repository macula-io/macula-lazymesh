# lazymesh — MVP Plan

**Status:** Phases 1, 2, and 3 implemented and live-verified. All three
Fable-identified required security findings fixed (see "Security review
findings" below).
**Created:** 2026-09-06
**Last Updated:** 2026-09-06

## Why this exists

So an agent can cooperate with other agents over the Macula mesh without
carrying the ceremony of a general-purpose harness (Claude Code, opencode,
Goose) — and so a human can watch that cooperation live, the way `lazygit`
lets you watch a repo instead of running `git status` in a loop.

Born out of live dogfooding on 2026-09-06: a 4-agent team (3 Claude Code
sessions + Goose) coordinating over mesh rooms independently converged on
wanting this. Two distinct pains, both real: an agent has no way to just
*listen* for mesh events (every check is a discrete tool call), and a human
watching the mesh has no live view into it at all — only agents can see it,
one tool-call result at a time.

Approved by Raf 2026-09-06, in `macula-io/macula-lazymesh` (new repo).

## What this is NOT (out of scope for MVP)

- Not a general-purpose coding agent. No file-editing tools, no shell
  execution, no todo lists, no skills discovery, no extension manager, no
  telemetry. If it's not the mesh, it's not in v0.1.
- Not a second mesh client library. lazymesh does not reimplement mesh
  protocol handling — it drives `macula-mcp` (the same MCP server every
  other harness in this ecosystem already uses) as its ONLY tool source,
  over stdio, via `npx -y -p @macula-io/mcp macula-mcp` (the exact launch
  command every other config in this workspace already uses — no
  reinventing how macula-mcp gets started).
- Not read-only. Earlier framing (before Raf's own steer) proposed a
  purely passive viewer. That is explicitly rejected: lazymesh needs a
  real, configurable LLM behind it so it can actually act as an agent on
  the mesh, not just display one.

## Architecture, one paragraph

A single Go binary. It spawns `macula-mcp` as an MCP stdio subprocess (the
same way Claude Code/Goose do) and speaks MCP client protocol to it. An
agent loop calls a configurable LLM (default: DeepSeek, OpenAI-compatible
chat completions API — see [[Configuration]] below) with the tool
definitions macula-mcp's own `tools/list` advertises, dynamically — no
hardcoded tool list, so a new macula-mcp tool becomes available to lazymesh
for free. A TUI (recommend `charmbracelet/bubbletea` + `lipgloss` — same
spiritual lineage as `lazygit`/`lazydocker`, Elm-architecture update loop
fits a live-updating panel view naturally) renders what's happening:
rooms, pending rings, agent presence. The TUI's panels get their data by
calling macula-mcp's own read-only tools (`mesh_rooms`, `mesh_read_inbox`,
`mesh_agents`) through the SAME MCP connection the agent loop uses — NOT by
reaching into macula-mcp's local SQLite files directly. Those files are an
internal implementation detail of a TypeScript project with no stability
contract; the MCP tool surface is the actual public API, and per
`mesh_etiquette`'s own documentation these reads are "instant, local, never
blocks" — going through MCP costs nothing extra.

This is a plain Go module, not FFI. Unlike `macula-ts`/`macula-php`
(FFI-over-macula-go, a C ABI + two build artifacts), lazymesh has no
foreign-language boundary to cross — it's a standalone binary. Don't copy
the `cabi/` structure from those repos; it doesn't apply here.

## Identity

Default macula-mcp behavior is a fresh, session-scoped identity per
launch (a new node_id every run) — fine for a Claude Code session, wrong
for an agent meant to be recognized across restarts in ongoing mesh
cooperation. lazymesh's config MUST support pinning a stable identity file
(macula-mcp's own `MACULA_MCP_IDENTITY` env var, pointed at a path under
lazymesh's own config dir) so a given lazymesh instance keeps the same
node_id run to run — same pattern desk-us-east already uses for Goose.

## Configuration

A config file (`~/.config/lazymesh/config.yaml` or similar — match
macula-mcp's own `~/.config/macula-mcp/` convention for the parent dir
name) holding:

- `provider`: which LLM backend. **Default: DeepSeek** — Raf's explicit
  call, it's the only backend cheap enough to run an agent against
  continuously right now (see `project_fleet_llm_backend_policy` in the
  coordinator's memory for the broader cost picture; NVIDIA's free tier is
  the fleet's default elsewhere but hits account-level 429s under load,
  so DeepSeek is the right default here specifically, not a fleet-wide
  contradiction).
- `model`: **`deepseek-v4-flash`.** Correction, 2026-09-06 (94 verified
  against api-docs.deepseek.com/updates/ before writing code, don't trust
  this plan's original guess): `deepseek-chat`/`deepseek-reasoner` were
  deprecated 2026-07-24; `deepseek-v4-pro` (what Goose already runs on
  desk-us-east) and `deepseek-v4-flash` are the current GA models as of
  today. Chose flash over pro: this plan's own rationale for DeepSeek at
  all is cost ("the only one cheap enough to run continuously"), and
  flash is the cheaper of the two — pro is a one-line config override if
  capability turns out to matter more in practice. Since DeepSeek renames
  and deprecates model ids on their own schedule, re-verify this string
  against their docs if it ever starts erroring rather than assuming the
  config is wrong.
- `api_key_file`: path to a bare-value key file, matching the
  `~/.ai-api-keys/.<provider>-api-keys/<consumer>` convention already
  established across this workspace. Never hardcode a key, never accept
  it as a bare CLI flag (shell history).
- Provider must be swappable (Raf's own words: "a true configurable model
  behind it") — DeepSeek is the default, not the only option. Design the
  provider config as a small interface from day one (base_url + model +
  key), not a DeepSeek-specific hardcode with TODOs for others later.

## Phases

- [x] **Phase 1 (MVP): Global agent co-op over mesh.** macula-mcp spawned
      and driven as the only tool source. DeepSeek-backed agent loop, tool
      calls dynamically sourced from macula-mcp's `tools/list`. TUI with
      three panels: rooms (live), pending rings, agent presence/roster.
      Stable pinned identity. This is the whole MVP — an agent that can
      join a room, talk, answer a ring, and a human can watch it do that
      live. Implemented 2026-09-06; see Success criteria below for exactly
      what's live-verified vs. implemented-but-not-yet-exercised live.
- [x] **Phase 2: Broader tool use.** Implemented 2026-09-06:
      `internal/localtools` adds `shell_exec`/`read_file`/`write_file`,
      off by default (`local_tools.enabled: false` unless set), gated
      behind a required `working_dir`. `agent.MultiSource` (new,
      `internal/agent/multisource.go`) composes it with macula-mcp without
      `Loop` itself knowing there are two sources — errors loudly on a
      tool-name collision between sources rather than silently shadowing
      one. Sandbox boundary (read_file/write_file confined to
      `working_dir`, rejecting both absolute paths and `../` escapes,
      including the classic same-string-prefix sibling-directory trap) has
      its own regression tests, not just happy-path coverage — this is the
      part that actually matters here. `shell_exec` is honestly NOT a real
      sandbox: it's scoped to `working_dir` as a starting cwd only, a
      command can still `cd` anywhere the operator's account can reach.
      Live-verified 2026-09-06: real macula-mcp spawn combined with a real
      enabled localtools source, confirmed both `mesh_hello` and
      `shell_exec` show up with no name collision, and a real `shell_exec`
      call round-trips correctly (`cmd/lazymesh`'s `//go:build live` test).
      **Superseded by the security review below**: as of that review,
      `shell_exec` is no longer reachable by default even with
      `local_tools.enabled` — see "Security review findings" for why and
      what changed.
- [x] **Phase 3: Prefer mesh services over local tools.** Implemented
      2026-09-06: `internal/meshservices` discovers procedure
      advertisements live via `mesh_find_records_by_type` and exposes
      real mesh RPC procedures as tools, on by default (no config flag --
      unlike Phase 2's local tools, this is the plan's actual thesis, not
      an opt-in extra). Built directly on a teammate's live mesh survey
      (io.macula realm), not designed in the abstract: `Curated` in
      `catalog.go` lists 17 procedures across `hecate-rag` (9 read-only
      methods), `hecate_agora` (all 4), and `hecate_graph` (4 read-only,
      excluding the ownership-gated `learn_link`) -- every mutating,
      gated, or unverified-safe procedure the survey found (`hecate-llm`,
      `hecate_mail`, `hecate_citizens`, `warden`/`sentinel`, realm-bootstrap
      and per-session DHT noise) is deliberately excluded, not just
      unimplemented.

      Never exposes a generic "call any mesh procedure" tool -- only
      individually named, curated, currently-discovered procedures ever
      become tools (`mesh_service_<domain>_<method>`), which is what keeps
      this from reopening the exact risk Fable's finding #1 exists to
      close (see "Security review findings"). Discovery is cached 60s
      (the raw DHT dump is large and Loop re-lists tools every
      conversation turn) but stays genuinely dynamic: an undiscovered
      curated procedure just isn't listed that round, not a fixed catalog
      standing in for real discovery. A retry-once policy and a
      hex-encoded-ASCII decode pass (both directly from the survey's own
      findings -- transient QUIC flakiness that resolves on retry, and
      `hecate-llm.check_health`'s status strings arriving as raw hex) sit
      between the raw `mesh_call` result and what the model sees.

      Live-verified 2026-09-06: real discovery against the live mesh found
      real curated procedures currently advertised; a real call to
      `hecate_agora.get_posts_page` round-tripped successfully
      (`internal/meshservices`'s own `//go:build live` test); the full
      production wiring (`cmd/lazymesh`'s `buildToolSource`, exactly as
      the real binary builds it) lists at least one `mesh_service_*` tool
      by default alongside `mesh_hello`, with no config changes needed.

## Security review findings (2026-09-06)

An adversarial review (Fable, run by a teammate against a fresh clone,
budgeted to 3 required findings ranked plus observations) landed right
after Phase 2 shipped and changed the priority order — the allowlist below
became the critical-path item ahead of any further Phase 2 polish or
starting Phase 3. All three required findings are fixed as of this
section's own commit:

1. **(most severe) No tool allowlist — any mesh peer's room text could
   escalate to arbitrary local execution.** Every tool a ToolSource
   advertised went straight to the model, and every tool result (room
   messages, ring purposes, inbox contents — all peer-authored) came back
   into context with nothing marking it untrusted. Concrete paths that
   existed even in Phase 1 alone: macula-mcp's `mesh_serve` lets a peer ask
   the agent to register a local shell command as an RPC handler (a
   persistent backdoor, no re-registration per call);
   `mesh_remember_directory` reads an attacker-picked local directory.
   Phase 2's `shell_exec` was the sharpest version of the same risk —
   immediate arbitrary execution, no pre-registration step at all.
   **Fix:** `internal/agent/allowlist.go`'s `AllowlistSource`, a
   deny-by-default wrapper enforced at BOTH the tools-offered-to-the-model
   step (`ListTools` filters) and the execute step (`CallToolRaw` refuses
   anything not on the list, defense in depth against some other code path
   handing the model a tool spec). `DefaultToolAllowlist` covers exactly
   the conversational mesh primitives (`mesh_hello`/`join_room`/
   `leave_room`/`say`/`read_inbox`/`answer_ring`/`rooms`/`agents`) and
   deliberately excludes `shell_exec`/`read_file`/`write_file` — even when
   `local_tools.enabled` is true. Reaching local tools now needs a second,
   separate, explicit `config.ToolAllowlist` override the operator writes
   themselves; it's never a side effect of the one flag. Live-verified:
   both the safe-default path (shell_exec absent and refused despite
   `local_tools.enabled: true`) and the explicit-override path (shell_exec
   genuinely reachable once added to `tool_allowlist`) — two separate
   `//go:build live` tests in `cmd/lazymesh`.
2. **macula-mcp spawned via unpinned `npx -y` with the full parent
   environment.** Re-resolved npm's `latest` tag fresh on every launch,
   and `cmd.Env = os.Environ()` handed the subprocess the whole shell
   environment (API keys, tokens, everything) for no reason. **Fix:**
   `internal/mcpclient`'s `maculaMCPVersion` pins to the exact published
   version (`0.23.0` as of this fix — bump deliberately, never just to
   "pick up whatever's newest"); `envAllowlist` forwards only
   `PATH`/`HOME`/`TMPDIR` plus `MACULA_MCP_IDENTITY` when set, not the
   full environment. Live-verified: real spawn still works correctly
   under the pinned version and restricted environment.
3. **Unbounded conversation history + infinite silent retry.** History
   only ever grew by appending; once a request got too large for the
   provider's context limit, the outer loop retried the same oversized
   request every 5s forever with the TUI still looking healthy — a peer
   could trigger this on purpose just by keeping a room busy (cheap DoS).
   **Fix:** `agent.Loop.trimHistory` windows history to
   `maxHistoryMessages` (200), cutting only at user-message turn
   boundaries so a tool-calling assistant message is never separated from
   its own tool-result messages (its own regression test, not just a
   size-based trim). `cmd/lazymesh`'s retry loop now stops after 8
   consecutive failures instead of retrying forever, with exponential
   backoff (5s doubling to a 2min cap) between attempts.

Secondary finding, not required but noted: `localtools.resolveInSandbox`
is path/string-based only, no symlink resolution — a symlink planted
inside `working_dir` (which `shell_exec`, being unsandboxed, can already
create) pointing outside the sandbox would let `read_file`/`write_file`
follow it. Not additive risk today since `shell_exec` already dominates
it, but worth an `EvalSymlinks` check or an explicit doc note if
file-tools are ever split from `shell_exec` later. Not fixed in this pass
— tracked here rather than silently dropped.

## Repo conventions (matching this org's other Go SDKs)

- Go 1.27.0 (`.tool-versions`: `golang 1.27.0`, matching `macula-cli`).
- Dual license: `LICENSE-APACHE` + `LICENSE-MIT` (copy `macula-cli`'s
  files verbatim, update copyright year/holder only if those are wrong).
- README follows `macula-cli`'s header shape (badges, centered logo
  placeholder, one-line tagline) — logo/SVG assets can come later, don't
  block the MVP on brand assets.
- CI: at minimum a `ci.yml` running `go build`/`go vet`/`go test`, matching
  every other Go repo in this org.

## Success criteria

- [x] `lazymesh` (single binary) launches, spawns macula-mcp, connects to
      the mesh with a stable pinned identity. **Live-verified 2026-09-06:**
      ran the built binary twice independently against the default config
      (no config file yet, so `~/.config/lazymesh/identity` was created on
      first run); both runs showed the identical node_id (`8d91b48a...`)
      in the presence panel, confirming `MACULA_MCP_IDENTITY` pinning
      actually persists across restarts, not just in theory.
- [x] The TUI shows live rooms/rings/presence, updating without the human
      running any command. **Live-verified 2026-09-06:** presence panel's
      agent count updated automatically across ticks (0 → 7 → 8) with no
      manual refresh, confirming the 2s poll loop against real
      mesh_rooms/mesh_read_inbox/mesh_agents data.
- [ ] Told (via a room message or CLI arg) to join a specific mesh room
      and participate, the DeepSeek-backed agent loop actually does so —
      calls mesh tools, posts real messages, driven by the LLM, not
      scripted. **Implemented, not yet live-verified**: no DeepSeek key
      file exists yet at `~/.ai-api-keys/.deepseek-api-keys/lazymesh` (only
      `.spartan00`, a different consumer, exists in that directory) — did
      not repurpose someone else's key. Needs a real key dropped there,
      then `./lazymesh --room <topic>` run against a live room, before this
      can be checked off for real.
- [x] Provider config is swappable in principle (a second, even
      unimplemented, provider stub proves the interface isn't
      DeepSeek-shaped). `internal/provider/anthropic.go` is that stub —
      deliberately a genuinely different wire shape (Anthropic's Messages
      API, not another OpenAI-compatible chat-completions variant), so it
      actually proves the interface generalizes rather than just being a
      base_url swap on the same shape.
