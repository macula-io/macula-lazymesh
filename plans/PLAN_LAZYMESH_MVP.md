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

## Security review findings, round 2: Phase 3 (2026-09-06)

A fresh adversarial review (Fable, dispatched by a teammate against
`68541da`'s `meshservices/catalog/hexdecode.go` specifically — new
capability surface gets its own review, not just a closure check on
prior findings) found 3 required findings, more serious than round 1's:
#2 has mesh-wide blast radius, not just this instance. All three fixed:

1. **`classify_topics` was mislabeled read-only.** Curated from the tool
   NAME, not its handler. Real behavior
   (`apps/embed_corpus/maybe_classify_topics.erl`, right next to
   `prune_chunks`/`retire_document`, which the catalog already correctly
   excluded): loads a document, chunks it, calls a paid LLM classifier
   per chunk, then WRITES topic tags back into the shared corpus
   (`rag_store:tag_chunk`) — changing everyone's topic-filtered search
   results, not a read at all. **Fix:** removed from `Curated`. The
   lesson matters as much as the fix: curating this list means reading
   the handler source, never inferring safety from a name that merely
   sounds like a query.
2. **(most severe, mesh-wide) Realm was never pinned during service
   discovery.** `meshservices.go` mapped procedure name → realm from
   whatever `mesh_find_records_by_type` returned, with the last matching
   record in the dump silently winning — not stable across the 60s
   cache, and DHT records are neither signature- nor
   membership-verified. Concrete attack: anyone, no realm membership
   required, publishes a `procedure_advertisement` for e.g.
   `hecate-rag.search_chunks_semantic` under the public all-zero realm
   and serves it themselves; whenever a refresh happened to sort that
   record after the real one, every lazymesh instance on the mesh would
   send real corpus queries to the attacker's implementation and trust
   the reply as "the shared corpus" — worse because `runAgent`'s own
   system prompt tells the model to *prefer* `mesh_service_*` results
   over its own judgment. **Fix:** `pinnedRealm` (`sha256("io.macula")`,
   computed at package init — not a hardcoded hex literal that could be
   transcribed wrong — and verified independently against `sha256sum`
   before writing the code that depends on it) is now the ONLY realm
   ever trusted; a record under any other realm is invisible to this
   package entirely, not merely deprioritized. Matches what macula-mcp's
   own `device_membership.ts` already does for the identical reason. New
   regression tests plant a spoofed record under the all-zero realm
   alongside a real one under the pinned realm, in the order that broke
   the old logic, and confirm only the pinned-realm one is ever used.
3. **Retry-once fired on every error class; nothing bounded call
   volume.** Transport errors, deadline expiry, and application-level
   errors (`missing_entity_id`) were all retried identically with the
   same arguments — a deadline-expiry retry risks two expensive jobs
   running server-side for one tool call, and an argument error will
   just fail identically again. No explicit `timeout_ms` (silently used
   `mesh_call`'s own 30s default). No rate limiter anywhere: one steered
   peer message could drive dozens of real RPCs against shared
   embedder/LLM infrastructure under the operator's own identity,
   indefinitely (`Loop`'s own `maxRounds=25`, and a provider can return
   several tool_calls per round). **Fix:** `isTransportError` classifies
   by an explicit allowlist of connectivity/routing failure substrings
   mesh_call's own tool description enumerates (an unrecognized error
   shape is conservatively NOT retried, never assumed safe to); only
   that class gets the one retry. `callTimeoutMS` (20s) is now passed
   explicitly on every call. `fixedWindowLimiter` caps real `mesh_call`
   RPCs to `maxCallsPerMinute` (20) in any rolling minute, refusing
   (returned as a normal tool error, same as any other) once exhausted.

Non-blocking observations from the same review, tracked here rather than
silently dropped, not fixed in this pass:

- Discovery failure now halts room participation entirely after ~4min of
  backoff — a real functional regression Phase 1 never depended on the
  DHT for at all. Worth its own look if it recurs in practice.
- The hex-decode heuristic (`hexdecode.go`) has real false positives
  verified live, not just hypothetical — e.g. `"2024"` round-trips
  through hex decode to something else printable. A narrower heuristic
  (field-name allowlist, or requiring the decoded text to look like a
  status word) would close this at some cost to generality.
- `get_document_verbatim` is actually broken as shipped: wrong expected
  field name, and doesn't handle the real 0x-hex CBOR encoding the
  procedure returns — one of the "curated safe" tools doesn't
  functionally work. Left in `Curated` for now (a functional bug, not a
  security exposure) but flagged so it isn't mistaken for working.
- `mesh_remember`/`add_knowledge` (a macula-mcp tool, not this package's
  own) is an ungated, persistent injection channel into the same shared
  corpus `hecate-rag`'s other procedures read from — plant once, fires
  on any future matching query, no room presence needed at call time.
  Out of scope for this package to fix (macula-mcp's own tool), noted
  for whoever owns that surface.
- `hecate_graph`'s `depth` parameter is unbounded (an owned-library bug,
  not introduced here, but this package exposes it by default) —
  `depth: 1e9` is a CPU/memory DoS on shared Cozo. `narrate_*` also
  passes a caller-supplied model string to `hecate-llm` verbatim, so a
  steered agent can pick the priciest available model. Both need a fix
  in the owning service, not a client-side workaround here.
- `resolveAllowlist` silently drops ALL `mesh_service_*` defaults the
  moment an operator sets ANY `tool_allowlist` override at all (they'd
  need to re-list every curated tool name themselves to keep them). A
  footgun, not a security hole, worth a doc note or an explicit "add to
  defaults" merge mode later.

## TUI / chat interface redesign (2026-09-06, implemented)

Raf's own steer, from a live design conversation after Phase 3 landed.
Current state, checked directly against the code before writing this
down: `internal/tui/model.go` has zero keybindings beyond quit
(`q`/`ctrl+c`) and no chat rendering at all — `--room` mode's agent
activity goes only to `~/.config/lazymesh/agent.log`, tailed in a second
terminal. This section is the first real design of the interactive
surface, not a revision of one.

**The actual problem this solves:** lazymesh has two origin stories
layered on top of each other. The founding idea (the team room, before
Raf's own scope-widening steer) was purely observational — a lazygit-
style live view so a human doesn't have to poll. Phase 1's steer added a
second, bigger thing on top: a real agent with a real LLM a human
directs. Those are different modes of attention (glance at what the mesh
is doing vs. have a focused conversation with the agent), and the
current TUI only serves the first one, half-built — permanently-on mesh
panels, no conversation surface at all. The redesign below makes both
modes first-class instead of the second one being a log file you tail
separately.

- **Mesh view: a persistent one-line status strip, not a hard on/off
  toggle**, expandable into the full three-panel view (rooms/rings/
  presence) on demand and collapsible back. e.g.
  `3 rooms · 2 pending rings · 8 agents seen`, always visible, one key
  (`m`, in normal mode) expands/collapses the full panels. A pure
  hide/show toggle risks losing the ambient-awareness value this whole
  project exists for if the human forgets to expand it; the status strip
  keeps that awareness cheap and glanceable without the clutter of full
  panels competing with the chat.
- **Status strip position: bottom by default, configurable.** Matches
  the convention of the tools this borrows its whole aesthetic from —
  vim's own statusline+command-line sit at the bottom, so does tmux's
  status bar by default. Layout is just string concatenation in
  bubbletea, so exposing `status_bar_position: top|bottom` in config is
  low-cost and there's no reason to force a choice.
- **Chat pane: minimal, but never invisible.** A real conversation view
  needs to exist (it doesn't today). Tool calls render as one collapsed
  line inline in the chat stream (`→ mesh_say("...")`,
  `→ mesh_service_hecate_rag_search_chunks_semantic(...)`), expandable on
  demand — not the full JSON args+result dump inline, and not hidden
  either. Full verbose detail stays available in `agent.log` for anyone
  who wants to tail it; the two surfaces serve different needs, this
  isn't replacing the log. Given the tool-allowlist work earlier tonight
  was specifically about an operator being able to trust and reason
  about what the agent can do, a chat view that hides tool calls entirely
  would undercut that same goal from a different angle.
- **Modal input, vim-style — the full model, not a few remapped keys.**
  The reason this matters more than "nice to have hjkl": once there's
  real text entry (composing a message to the agent) alongside
  navigation (scrolling history, expanding the mesh view, switching
  focus), lazymesh has exactly the problem vim's modal design exists to
  solve — text entry and commands competing for the same keys. Adopt the
  actual solution, not just the keycaps: **normal mode by default**
  (j/k or arrow keys scroll history, `m` toggles the mesh view, `q`
  quits), **`i` enters insert mode to compose a message**, **Esc returns
  to normal**. Use `bubbles`' existing multi-key `key.Binding` support
  (binding both hjkl AND arrow keys to the same action) so this isn't
  vim-only — cheap to do correctly from the start, no reason to exclude
  non-vim users.
- **Audio cues — lean version, not an audio engine.** Genuinely useful,
  not just atmospheric: Fable's finding #3 (see above) is that the agent
  can get silently wedged in a retry loop while "the TUI still looks
  healthy" — a sound is a better signal for exactly that class of event
  than anything visual, since it doesn't require looking at the screen.
  Design, kept deliberately lean given this project's own "no theatre"
  MVP philosophy:
  - Plain terminal bell (`\a`/BEL) only — every terminal supports it,
    zero dependencies, no bundled sound assets, no audio library.
  - Differentiate by **cadence, not by different sound files**: single
    bell for an ordinary room message, quick double-bell for a ring
    addressed to you specifically, a distinct rapid/triple pattern for
    "agent entered backoff" or "hit max consecutive failures." Real
    differentiation from one universal primitive.
  - Silent for the agent's own outgoing messages — no alert needed for
    your own action, and it stops the chat from feeling like a slot
    machine during an active conversation.
  - Must degrade silently (never error) where the bell is muted/routed
    nowhere (a headless box, an SSH session, a terminal configured for
    visual bell only) — detect and no-op, don't assume it always fires.
    One-key mute toggle, default-on vs. default-off is an open call for
    whoever implements this.
  - **Explicitly out of scope for this pass:** actual sound files,
    distinct per event class, via a real audio library. Genuine future
    enhancement, deliberately not bundled into this work so it doesn't
    quietly balloon the dependency footprint of a project that's
    supposed to stay lean.

**Implemented 2026-09-06**, largely as designed above, with two
deliberate simplifications worth recording rather than letting readers
assume the design doc is the literal spec:

- **Tool-call detail expansion is a single global toggle (`e`), not
  per-line.** The plan's "expandable on demand" didn't specify per-line
  vs. global; per-line would need a focus cursor moving through chat
  history (effectively a list-navigation component), real added
  complexity for an MVP pass. Global expand/collapse satisfies "not the
  full JSON dump inline, and not hidden either" without it. Worth
  revisiting if a real conversation makes "some calls expanded, others
  not" genuinely useful in practice.
- **A message typed in insert mode is picked up on runAgent's next loop
  iteration, not mid-call.** If the agent is in the middle of a `Say()`
  (in particular a long `mesh_say` wait), a submitted message waits for
  that call to return before it becomes the next prompt --
  `cmd/lazymesh`'s `nextPrompt` drains it non-blocking at the top of each
  iteration. No in-flight LLM call gets interrupted/canceled for it. This
  is the "no theatre" lean-MVP scope holding, not an oversight -- true
  mid-call interruption would need context-cancellation plumbing through
  `agent.Loop.Say` that nothing in this pass otherwise needs.

Architecture: `internal/tui/keys.go` (bubbles `key.Binding`, both vim and
arrow/plain keys per binding), `chat.go` (chat entries + rendering,
collapsed/expanded), `bell.go` (cadence-based `bellPattern` + a `tea.Cmd`
that writes raw `\a` bytes -- never baked into `View()`'s return value,
since a one-shot side effect re-firing on every re-render would be wrong).
`cmd/lazymesh/main.go` fans agent events out to both `agent.log` (full
detail, unchanged) and a buffered channel the TUI drains (best-effort,
non-blocking send -- the agent loop must never stall waiting on a slow or
absent TUI reader). Two new `agent.EventKind`s (`EventBackoff`,
`EventMaxFailuresReached`) exist purely to give the TUI's bell a signal
for Fable's finding #3 ("the TUI still looks healthy" while wedged) --
`Loop` itself doesn't know about them, `runAgent`'s own retry/backoff
logic emits them.

Bell-cue detection is genuinely two different code paths, not one: an
ordinary-message/ring bell comes from diffing consecutive mesh-state
polls (`bell.go`'s `detectRoomMessageBell`/`detectRingBell`, comparing
against the previous poll before it's overwritten); the backoff/
max-failures triple-bell comes from the agent-event channel instead,
since that's a fact about the agent loop's own retry state, not something
visible in mesh state at all.

Verified: 30 new unit tests in `internal/tui` covering exactly the
modal-state bugs this kind of feature is prone to (`q` typed while
composing must not quit; `m` typed while composing must not toggle the
mesh view; `ctrl+c` quits from either mode; Enter submits, clears input,
returns to normal, and actually delivers on the user-input channel) plus
bell-pattern detection and chat-entry rendering. Live-verified: built the
real binary and ran it under a pty -- the status strip renders at the
correct position with live presence counts updating, no crash.

## Ring-answering UX, part 1: petnames, room purpose, config-driven version/isolation (2026-09-06)

A live "ring stuck deferred, never answered" report from Raf turned into
several real, separate fixes, landed before the ring pop-up itself
(pop-up + the corrected layered trust policy is its own section below):

- **macula-mcp version moved to config** (`config.MaculaMCPVersion`,
  default `0.24.0`), not a Go const, per Raf's own steer: the security
  property Fable's finding-2 fix needed was "not a floating tag, always
  an explicit deliberate value," not "compiled into the binary." Verified
  the 0.23.0 → 0.24.0 diff directly (all three commits: petname fields
  throughout, a real `mesh_open_room` ring-sequencing bugfix,
  `mesh_trust_agent`/`mesh_wait_room`/inbox `poll_hint`) before setting
  this default — additive only, nothing this codebase depends on was
  removed or restructured.
- **`contact_policy.json` isolated per lazymesh instance**
  (`config.ContactPolicyFile`, passed through as
  `MACULA_MCP_CONTACT_POLICY_FILE`). Found while reading macula-mcp's own
  `policy.ts`: this file defaults to ONE shared path
  (`~/.config/macula-mcp/contact_policy.json`) regardless of identity —
  every macula-mcp instance on a machine (this session's own, other
  Claude Code sessions', Goose's) shares it. Necessary before the ring
  pop-up's "Answer+Trust" can safely call `mesh_trust_agent` without
  polluting or being polluted by unrelated sessions. Live-verified: a
  real spawn with a custom path reports that exact path back via
  `mesh_hello`'s own `ring.policy_file` field.
- **Petnames** (`displayName` in `internal/tui/model.go`): 0.24.0's
  deterministic, human-legible `petname`/`*_petname` fields now render
  wherever a raw node_id would otherwise show — presence panel (behind
  `operator_name`, which stays first when a peer sets one), rooms panel,
  pending-rings panel. Live-verified: a fresh spawn's `mesh_agents` call
  against the real mesh returns real petnames (`lively_copper_eagle` for
  an actually-online peer), not a synthetic fixture only.
- **Room purpose as label** (`roomLabel`): a room's `purpose` string
  (present on `joined` rooms too, not just `seen_on_central` — verified
  against `rooms.ts`'s own `RoomState`/`RoomListing` types) is now the
  primary room label everywhere a raw topic hex would otherwise show;
  falls back to the shortened topic when absent (older/purposeless
  rooms). Same "don't make a human read raw hex" principle as petnames,
  applied to rooms.

`mcpclient.Spawn`'s signature changed from `(ctx, identityFile string)`
to `(ctx, SpawnOptions)` to fit Version/IdentityFile/ContactPolicyFile
cleanly — every call site updated, all live tests re-run against the
real mesh under 0.24.0 to confirm nothing broke.

## Ring-answering UX, part 2: the pop-up + corrected layered trust policy (2026-09-06)

The phone-call metaphor Raf asked for, built on part 1's petname/purpose
work: an incoming ring is a real blocking pop-up (`ModeRingPopup`), not a
line in the pending-rings panel someone has to notice, paired with the
double-bell already in the plan. Three actions, phone-style: `a` Answer,
`d` Decline, `t` Answer + Trust (calls `mesh_trust_agent` right after a
successful accept). `Esc` leaves it for later — the ring stays pending
(an agent's own per-cycle ring-checking, if one is running, can still
pick it up), it only suppresses the human-facing pop-up for that specific
ring going forward this session.

**The corrected design**, per the earlier finding that macula-mcp's real
`contact_policy` tiers don't support "known auto-accept, strangers still
asked" (`allowlist` mode declines strangers outright, it never defers):
`internal/contactpolicy` (new package) reads/writes the identical JSON
file `mesh_trust_agent` manages. `config.RingPolicy`'s four values —
`always-ask` (default), `auto-accept-known`, `accept-everyone`,
`do-not-disturb` — map through `config.RingPolicyContactPolicyFileValue`:
the first two both keep the file's own `contact_policy` at `"ask"` (every
ring genuinely defers, mesh-side); the known/unknown split for
`auto-accept-known` happens in lazymesh itself
(`contactpolicy.IsTrusted`, consulted in `internal/tui`'s
`processPendingRings` before ever showing the pop-up) — a peer already on
the SAME allowlist `mesh_trust_agent`/"Answer + Trust" manages skips the
pop-up and is accepted immediately, via a direct `mesh_answer_ring` call,
no LLM or pop-up involved. `accept-everyone`/`do-not-disturb` map
directly onto `open`/`closed`, since those need no per-peer judgment.
`main.go` calls `contactpolicy.Ensure` before every spawn, translating the
configured tier into the isolated file — preserving any allowlist already
built up, never clobbering past "Answer + Trust" choices.

Never more than one pop-up at a time: `seenRingIDs` tracks every ring
already auto-accepted, answered, or dismissed, so a ring already being
shown or already handled is never revisited by a later refresh; a second
new ring arriving while one pop-up is showing just waits its turn.
`ctrl+c` still force-quits during the pop-up (its own regression test,
added specifically because introducing a third `Mode` is exactly the
kind of change that could silently exempt it).

19 new tests (7 `internal/contactpolicy`, 12 `internal/tui`: the actual
`mesh_answer_ring`/`mesh_trust_agent` call construction via a fake tool
caller, all three pop-up actions plus Esc, auto-accept-vs-pop-up-vs-
already-seen-ring branching, the one-pop-up-at-a-time property, and the
force-quit-during-popup regression guard).

**What's NOT live-verified, honestly**: trying to confirm the pop-up
renders correctly against a real incoming ring (isolated identity, rang
it from this session, watched via pty) surfaced a real bug in
macula-mcp itself — `mesh_read_inbox.ts` omits its `rings` key entirely
on the very first tool call after a fresh identity's spawn (a
cold-start race: `presence.ensurePresence()` doesn't complete
synchronously before `presence.currentNodeId()` is read immediately
after, despite being called first in the same handler). Confirmed via
a throwaway diagnostic: call 1 has no `rings` key at all, calls 2-4 (2s+
later, same connection) show it correctly. A second, less understood
observation from the same session: the actual ring sent had vanished
from both `pending` and `recent` several minutes later, not fixed with
the same confidence as the first finding — flagged as unexplained
rather than folded into a single tidy story. Reported to the
coordinator with exact repro steps rather than silently worked around;
the Go-side logic above is solid per its own unit tests, but "the pop-up
actually renders on a real live ring" specifically was not confirmed
visually this session.

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
