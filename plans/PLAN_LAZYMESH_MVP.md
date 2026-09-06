# lazymesh — MVP Plan

**Status:** Planning
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
- `model`: **verify the exact DeepSeek API model string before shipping**
  (e.g. `deepseek-chat` per DeepSeek's own API docs) — do not copy Goose's
  `deepseek-v4-pro` string verbatim, that's Goose's own internal alias
  mapped to a real model id behind the scenes, not necessarily the raw
  string DeepSeek's API expects. lazymesh calls the API directly, so this
  needs its own verification.
- `api_key_file`: path to a bare-value key file, matching the
  `~/.ai-api-keys/.<provider>-api-keys/<consumer>` convention already
  established across this workspace. Never hardcode a key, never accept
  it as a bare CLI flag (shell history).
- Provider must be swappable (Raf's own words: "a true configurable model
  behind it") — DeepSeek is the default, not the only option. Design the
  provider config as a small interface from day one (base_url + model +
  key), not a DeepSeek-specific hardcode with TODOs for others later.

## Phases

- [ ] **Phase 1 (MVP): Global agent co-op over mesh.** macula-mcp spawned
      and driven as the only tool source. DeepSeek-backed agent loop, tool
      calls dynamically sourced from macula-mcp's `tools/list`. TUI with
      three panels: rooms (live), pending rings, agent presence/roster.
      Stable pinned identity. This is the whole MVP — an agent that can
      join a room, talk, answer a ring, and a human can watch it do that
      live.
- [ ] **Phase 2: Broader tool use.** Once co-op works, add a second,
      separately configurable tool source beyond macula-mcp (a minimal
      shell/file tool set) so a lazymesh agent can actually do work
      arising from mesh coordination, not just talk about it. Keep it
      opt-in / off by default — the MVP's whole value proposition is
      having NO extra tools by default.
- [ ] **Phase 3: Prefer mesh services over local tools.** When a task can
      be done by calling a real mesh RPC procedure (discovered via
      `mesh_find_records_by_type("procedure_advertisement")`) instead of
      a local tool, the agent should default to that — dogfooding the
      mesh's own service directory as the preferred capability source
      over hardcoded integrations. This is the philosophically important
      one for this project's actual thesis (decentralized capability
      discovery, not another app with a fixed integration list) but it
      depends on Phase 1 actually working first.

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

- [ ] `lazymesh` (single binary) launches, spawns macula-mcp, connects to
      the mesh with a stable pinned identity.
- [ ] The TUI shows live rooms/rings/presence, updating without the human
      running any command.
- [ ] Told (via a room message or CLI arg) to join a specific mesh room
      and participate, the DeepSeek-backed agent loop actually does so —
      calls mesh tools, posts real messages, driven by the LLM, not
      scripted.
- [ ] Provider config is swappable in principle (a second, even
      unimplemented, provider stub proves the interface isn't
      DeepSeek-shaped).
