# Research: opencode control protocol

- **Status:** Survey complete — desk research, no code changed
- **Created:** 2026-09-12
- **Last Updated:** 2026-09-12

## End goal

macula-lazymesh wants to borrow opencode's ideas for how an operator (and,
later, other agents) address, drive, interrupt, resume, and observe an agent
process. The concrete question this survey answers: *how does opencode let
other processes and sessions talk to and control it — its local HTTP+SSE
server, its TypeScript SDK, session addressing, headless `run`/`--print`
operation, persistence, interrupt/settle, and permissions — and what of that
fits a mesh-first Go TUI agent?* Answer up front: **opencode is the opposite
of claw-code.** Instead of subprocess-per-action CLI envelopes, every control
surface (TUI, desktop, web, SDK, `opencode run`, ACP) is a client of **one
local HTTP server** exposing an OpenAPI-declared REST API plus an SSE event
stream. Sessions are addressed by plain string IDs through that server;
durability lives in a single SQLite database. There is no unix socket and no
settle concept — the analogues are `POST /session/:id/abort` and the
`session.idle`/`session.status` events. The SDK is a generated client over
that same API, i.e. the control surface *is* the protocol, not a separate
thing. (Survey snapshot: commit `95daf906`, package version 1.18.30.)

## What opencode is

- **License:** MIT — `LICENSE:1-3` ("MIT License, Copyright (c) 2025 opencode"). Confirmed present.
- **Language/platform:** TypeScript on **Bun** (packageManager `bun@1.3.14`, root `package.json:9`), Turborepo monorepo with `packages/` workspaces (`package.json:22-27`). Effect (`effect` 4.0.0-beta) is the composition runtime for the server core; the AI loop uses the Vercel `ai` SDK (`ai` 6.0.168); web/TUI are SolidJS + opentui.
- **Repo facts:** ~30 packages. Relevant ones: `packages/opencode` (core CLI + server + session runtime; entry `src/index.ts`), `packages/core` (database schema, SessionV2, providers), `packages/protocol` + `packages/schema` (public OpenAPI declarations), `packages/server` (server-only support: auth, CORS), `packages/sdk` (`sdk/js`, npm `@opencode-ai/sdk`, regenerated client), `packages/tui`, `packages/app` (web), `packages/desktop`, `packages/cli`/`packages/client`, `packages/plugin` (`@opencode-ai/plugin`).
- **Core binary:** `opencode` — `packages/opencode/src/index.ts`, CLI built with yargs command modules in `packages/opencode/src/cli/cmd/`.
- **Default branch:** `dev` (per root `AGENTS.md`).

## Control surfaces

### The one control surface: a local HTTP server (REST + SSE)

There is no unix socket anywhere. Everything routes through a single
in-process HTTP server built with `effect/unstable/httpapi`:

- `Server.listen(opts)` at `packages/opencode/src/server/server.ts:73-98`
  binds a plain Node HTTP server (`node:http`, `server.ts:200-224`); the
  router is `HttpApiApp.createRoutes` (`server.ts:101`). Port fallback logic:
  explicit port, else prefer **4096**, else any free port
  (`server.ts:117-122`). The 4096 convention is repeated in CLI help strings
  (`packages/opencode/src/cli/cmd/attach.ts:14`,
  `packages/opencode/src/cli/cmd/run.ts:192`).
- `opencode serve` starts the headless server: `packages/opencode/src/cli/cmd/serve.ts:6-23`
  — warns if `OPENCODE_SERVER_PASSWORD` is unset (auth is basic-auth,
  `packages/opencode/src/server/auth.ts`). `--port/--hostname/--mdns` network
  options in `packages/opencode/src/cli/network.ts:7-35`; mDNS publishes
  `opencode-<port>` via bonjour-service (`packages/opencode/src/server/mdns.ts:8-30`)
  — a zero-config local discovery nicety.
- Route surface = one big OpenAPI-declared tree under
  `packages/opencode/src/server/routes/instance/httpapi/`: `groups/*.ts`
  declare endpoints, `handlers/*.ts` implement them. Notable groups: `session`
  (462-line declaration, `groups/session.ts`), `event`, `permission`,
  `question`, `config`, `provider`, `file`, `mcp`, `pty`, `tui`, `workspace`,
  `experimental`, `global`.
- **Event stream:** `GET /event` returns `text/event-stream`
  (`groups/event.ts:8-16`); handler (`handlers/event.ts`) registers a
  listener on the event bus into an unbounded queue per connection, filters by
  instance directory/workspace (`handlers/event.ts:32-44`), and maps each
  event to `{ id, type, properties }` (line 45). Events are the SDK type
  union of ~90 event names in `packages/sdk/js/src/v2/gen/types.gen.ts:7-98`
  (`session.created`, `message.updated`, `message.part.updated`,
  `permission.asked`, `question.asked`, `session.idle`,
  `session.status`, `server.instance.disposed`, …).
- The event bus itself: `packages/opencode/src/bus/global.ts` (a plain
  `EventEmitter`), bridged from EventV2 by `packages/opencode/src/event-v2-bridge.ts:39-46`.
- `GET /global/event` streams global (cross-instance) events
  (`groups/global.ts:69-94`).

### Session addressing: plain IDs + directory/workspace routing

- Sessions are addressed by a plain string `SessionID` in URL params —
  `POST/GET /session/:sessionID`, `POST /session/:sessionID/message`,
  `/session/:sessionID/abort`, `/session/:sessionID/fork`,
  `/session/:sessionID/prompt_async`, etc. — the full map at
  `packages/opencode/src/server/routes/instance/httpapi/groups/session.ts:78-105`.
- **Instance scoping** is not in the URL path but in query params/headers:
  `WorkspaceRoutingQueryFields = { directory, workspace }`
  (`middleware/workspace-routing.ts:22-27`). `directory` comes from
  `?directory=` or the `x-opencode-directory` header, defaulting to
  `process.cwd()` (`workspace-routing.ts:86-88`); a `workspace` param or
  `OPENCODE_WORKSPACE_ID` selects a workspace, and the middleware either
  routes locally or **proxies** to the remote workspace's own opencode server
  over HTTP/WebSocket (`workspace-routing.ts:113-146` — the control-plane
  sync loop, `packages/opencode/src/control-plane/workspace.ts:366-401`).
  The SDK sets `x-opencode-directory` on every request
  (`packages/sdk/js/src/client.ts:17-55`).
- So "addressing a session" from another process = `<baseUrl>/session/<id>` +
  correct `directory` (or `workspace`). Session `Info` records `directory`,
  `workspaceID`, `parentID` (`packages/opencode/src/session/session.ts:504-522`).
- CLI resumption: `opencode run --session <id> | --continue [-c] | --fork`
  (`packages/opencode/src/cli/cmd/run.ts:147-160`); `opencode attach <url>`
  connects a TUI to an already-running server (`packages/opencode/src/cli/cmd/attach.ts:6-8`).

### SDK: generated client over the same HTTP API

- npm package `@opencode-ai/sdk` at `packages/sdk/js`, versioned alongside
  the app. Two generations: legacy `src/gen/` (hey-api client) and current
  `src/v2/` (`sdk.gen.ts`, `types.gen.ts`, `client.gen.ts`, `core/`).
  `createOpencodeClient({ baseUrl, directory, headers })` at
  `packages/sdk/js/src/client.ts:33-56`.
- The protocol is *generated from the server's own OpenAPI*: `bun run
  generate` in `packages/client` regenerates `src/generated` (root
  `AGENTS.md`). So the SDK is not a separate protocol — it's a typed mirror
  of `HttpApi` (request/response schemas come from the same `Schema.Struct`s
  used by the route groups, e.g. `PromptPayload`,
  `packages/opencode/src/server/routes/instance/httpapi/groups/session.ts:70`).
- Streaming: `client.event.subscribe()` calls `GET /event` via the generated
  SSE client (`packages/sdk/js/src/v2/gen/sdk.gen.ts:1391-1411`), yielding
  `{ stream, response }`; `session.prompt` POSTs and the response is
  streamed/JSON (see `groups/session.ts:316-341` — note `prompt` returns a
  message while `prompt_async` returns `204` and runs in the background).
- The SDK also *spawns* servers: `createOpencodeServer()` launches
  `opencode serve` as a child process and parses the "opencode server
  listening on http://…" banner for the URL, with `AbortSignal` support
  (`packages/sdk/js/src/server.ts:16-120`); `createOpencodeTui()` similarly
  spawns the TUI (line 123+). This is how SDK-based harnesses (tests,
  e2e, other tools) get a controlled instance.

### Headless / print mode: `opencode run`

`opencode run [message..]` (`packages/opencode/src/cli/cmd/run.ts`) is the
non-TUI surface:

- Modes: **non-interactive default** (send one prompt, stream events, exit on
  idle), `--mini` interactive split-footer, `--attach` (drive a remote
  server's session), plus `--command` for slash commands (comment at
  `run.ts:3-15`).
- **Stdin piping:** piped stdin is merged as prompt text —
  `process.stdin.isTTY ? undefined : await Bun.stdin.text()` then
  `resolveRunInput(message, piped)` (`run.ts:416-418`, helper `run.ts:40-50`);
  `--file` attaches files (10 MiB cap, `run.ts:59`).
- **Programmatic output:** `--format json` emits one JSON line per event to
  stdout: `{ type, timestamp, sessionID, …data }` via the `emit()` helper
  (`run.ts:678-691`), mirroring the SSE stream (`tool_use`, `step_start`,
  `step_finish`, `text`, `reasoning`, `error` — `run.ts:720-799`).
  Default format prints plain text; the loop **breaks when the session goes
  idle**: `session.status` with `status.type === "idle"` (`run.ts:793-799`).
  Note: this is per-turn NDJSON-to-stdout, not a persistent control stream —
  the *control* protocol stays HTTP+SSE; `run` is a thin client of it (it
  literally builds an `OpencodeClient` against an in-process server with a
  custom `fetch`, `run.ts:948-960`, and in `--attach` mode against the remote
  server, `run.ts:349-355`).
- `--continue`/`--session`/`--fork` resume logic at `run.ts:456-533`.

### Persistence

- **Primary store: one SQLite database**, `~/.local/share/opencode/opencode.db`
  (XDG data dir via `xdg-basedir`, `packages/core/src/global.ts:10-15`;
  filename selection + `OPENCODE_DB` override at
  `packages/core/src/database/database.ts:43-55`). Pragmas: WAL,
  `synchronous=NORMAL`, `busy_timeout=5000` (`database.ts:27-32`); Drizzle
  migrations applied at boot (`database.ts:33`).
- Schema (`packages/core/src/session/sql.ts`): `session` (id PK, project_id,
  workspace_id, parent_id, directory, title, cost, token counts, JSON
  `permission`/`model`/`metadata` columns, timestamps — `sql.ts:22-67`),
  `message` (JSON `data` column holding the v1 message object — `sql.ts:68-80`),
  `part` (JSON `data` per part — `sql.ts:82-98`), plus `todo`,
  `session_message`, `session_input` (durable prompt admission queue,
  `sql.ts:140+`), `session_context_epoch`.
- **Legacy JSON store still migrates in:** `packages/opencode/src/storage/storage.ts:224`
  runs a `MIGRATIONS` array under `~/.local/share/opencode/storage/`, moving
  old per-session JSON files (`storage/session/<projectID>/<id>.json`,
  `storage/message/<sessionID>/<mid>.json`, `storage/part/…`,
  `storage/project/<gitRootCommit>.json`) into the new layout and computing
  diffs (`storage.ts:100-190`). Project identity is the git root commit
  (`git rev-list --max-parents=0`, `storage.ts:111-114`).
- **Durability of prompts:** SessionV2 admits one durable `session_input` row
  per prompt before waking execution (root `AGENTS.md`, "V2 Session Core");
  a `SessionExecution` coordinator (process-local, session-ID-keyed) drains
  the inbox — i.e. prompts survive process death up to the point of provider
  execution; post-crash *continuation* recovery is still explicitly
  un-designed ("requires a separate explicit design", root `AGENTS.md`).
- Resume = same session ID + same directory; `opencode run -c`/`--session`
  and the web/TUI list sessions from the DB.

### Interrupt / settle

- **Interrupt = `POST /session/:sessionID/abort`**
  (`groups/session.ts:253-264`, identifier `session.abort`); handler calls
  `SessionPrompt.cancel(sessionID)` (`handlers/session.ts:232-235`), which
  funnels into `SessionRunState.cancel` (`packages/opencode/src/session/run-state.ts:77-86`):
  cancels background jobs (recursively, `run-state.ts:111-143`) and cancels
  the per-session `Runner` fiber (`runner.cancel`). In the runner's scope,
  LLM calls carry `AbortController`s aborted on interrupt
  (`packages/opencode/src/session/prompt.ts:815-827`), tool/task execution
  has `taskAbort` (`prompt.ts:323-362`), and aborted shell output is marked
  "User aborted the command" (`prompt.ts:526-531`).
- **No settle event.** Completion is signaled per-session by
  `session.idle` (SDK type `EventSessionIdle`, `types.gen.ts:7-98`) and
  `session.status` transitioning to `{type:"idle"}` (status store at
  `packages/opencode/src/session/status.ts:32-42`, set from the runner's
  `onIdle`/`onBusy` hooks at `run-state.ts:60-65`). `run.ts` uses exactly
  that idle transition as its exit condition (line 793-799) — the closest
  thing to claw-code's "completion detected per stream", but event-driven
  instead of process-exit-driven.
- Concurrency guard: only one prompt per session at a time —
  `assertNotBusy` raises `SessionBusyError` (`run-state.ts:71-75`,
  `session.ts:407`), which the API surface reports (`groups/session.ts:23`).

### Permissions in headless mode

- Engine: `packages/opencode/src/permission/index.ts` — `Permission.Service`
  with `ask/reply/list` (interface at `index.ts:12-16`). Rules are
  `{permission, pattern, action: allow|deny|ask}` evaluated last-match-wins
  via wildcard (`evaluate`, `index.ts:28-38`); `ask` publishes
  `permission.asked` and suspends the turn on a `Deferred` until `reply`
  resolves it (`index.ts:67-107`). `reply` supports `once` / `always` (adds
  allow-rule for the tool's `always` patterns, `index.ts:142-166`) / `reject`
  (fails the turn and rejects every other pending ask for that session,
  `index.ts:121-140`).
- **Wire surface for external controllers:** `GET /permission`,
  `POST /permission/:requestID/reply` (`groups/permission.ts:11-39`,
  identifiers `permission.list`/`permission.reply`); plus the deprecated
  `POST /session/:id/permissions/:permissionID` (`groups/session.ts:101,395-408`).
  A headless SDK controller therefore auto-answers asks by subscribing to
  `/event` and replying — exactly what the CLI does.
- **`opencode run` non-interactive behavior:** it pre-denies `question`,
  `plan_enter`, `plan_exit` via a session permission ruleset
  (`run.ts:430-448`), and in its event loop answers each `permission.asked`
  with `once` when `--auto`/`--yolo`/`--dangerously-skip-permissions`, else
  auto-rejects with a printed warning (`run.ts:274`, `run.ts:801-821`).
  So: unattended mode is safe-by-default (reject) and auto-approve is an
  explicit opt-in flag, not a hidden behavior.
- **Questions** (multi-choice asks) are a separate system with the same
  shape: `Question.Service` (`packages/opencode/src/question/index.ts:48-156`),
  wire routes `GET /question`, `POST /question/:id/reply`,
  `POST /question/:id/reject` (`groups/question.ts:11-60`), events
  `question.asked/replied/rejected`.

## Agent machinery

### MCP

- `packages/opencode/src/mcp/` (index, catalog, browser, auth, oauth-provider,
  oauth-callback). MCP is a *tool registry* consumed by the AI loop — config
  via `mcp` config keys, servers added/listed over the API (`groups/mcp.ts`,
  156-line route group), OAuth support for remote servers
  (`mcp/oauth-provider.ts`). Tools surface in `experimental` `tool.list`/
  `tool.ids` (`groups/experimental.ts:158-181`). The ACP service exposes MCP
  servers to ACP clients too (`packages/opencode/src/acp/service.ts:1015-1076`).
  opencode acts as an MCP *client* only — there is no `mcp serve`-style
  MCP server exposing opencode's tools (unlike claw-code).

### Hooks / plugins

- Plugin system: `packages/opencode/src/plugin/` (loader, install, meta) with
  the published `@opencode-ai/plugin` package. Plugins can define
  **Hooks** — typed `(input, output) => Promise<void>` triggers
  (`packages/opencode/src/plugin/index.ts:3-56`) — and providers
  (openai/xai/azure/cloudflare/… directories in `src/plugin/` are the
  built-in provider plugins).

### Skills / agents

- **Skills:** `packages/opencode/src/skill/index.ts` + `discovery.ts` —
  Anthropic-style `SKILL.md` discovery (patterns `skills/**/SKILL.md` and
  `{skill,skills}/**/SKILL.md`, `skill/index.ts:23-24`), plus config-defined
  skill paths and git-pulled skill URLs (`skill/index.ts:211-223`).
- **Agents:** `packages/opencode/src/agent/agent.ts` — agents are
  `{name, mode: primary|subagent|all, description, model?, permission ruleset}`
  (`agent.ts:38-71`). Built-ins: `build` (default, `agent.ts:141-155`),
  `plan` (read-only, `agent.ts:156-179`), plus `general`/`explore` subagents
  (`agent.ts:184-216`) — README mentions Tab-switching and `@general`
  (`README.md:100-111`). Config-defined agents merged in (`agent.ts:267`).
  Subagent task tool runs separate conversations; per-agent permission
  rulesets (`subagent-permissions.ts`).

### ACP (Agent Client Protocol)

- **Fully implemented, not a stub.** `opencode acp` starts the ACP server
  (`packages/opencode/src/cli/cmd/acp.ts:9-72`): an internal HTTP server plus
  SDK client, bridged to the ACP protocol over **stdio NDJSON**
  (`ndJsonStream`, `acp.ts:55-61`) using `@agentclientprotocol/sdk`.
- The agent side (`packages/opencode/src/acp/agent.ts`) implements
  initialize, authenticate, newSession, loadSession, listSessions,
  resumeSession, closeSession, forkSession, setSessionMode/Model/ConfigOption,
  prompt, **cancel** (`agent.ts:35-85`) — i.e. Zed-style editors and other
  ACP clients can drive opencode as a backend agent. Service implementation
  (`acp/service.ts`) bridges ACP sessions to SDK sessions and can delegate
  permission requests to the client (`connection.requestPermission`,
  `service.ts:52-53`).

### Inter-agent / agent-to-agent communication

- None native: no agent-to-agent rooms, no agent-as-tool loop calling back
  into the same server, no git-remote channel. The only cross-process
  stories are (a) the **control-plane/workspace sync** — a local instance
  follows a remote workspace's event stream over HTTP/SSE+WebSocket and
  proxies API calls to the remote opencode server
  (`packages/opencode/src/control-plane/workspace.ts:366-401`,
  `middleware/workspace-routing.ts:113-146`, adapters in
  `control-plane/adapters/`) — which is operator/harness-facing tooling, not
  agent-to-agent; and (b) **session share** — `POST /session/:id/share`
  publishes a read-only web link (share service
  `packages/opencode/src/share/session.ts:26-44`, auto-share on session
  create `share/session.ts:43-44`; `opencode run --share`,
  `run.ts:161-164,535-548`). Subagents (`task` tool) are in-process
  conversations, not peer processes.
- Bottom line: the control surface is purely **operator/harness-facing**.
  There is no first-class "agent as a peer" concept; an agent-to-agent
  bridge would have to be built on the HTTP API or ACP.

## TUI ↔ external controller concurrency (the sharing question)

**Yes — the TUI and an SDK controller can share one live session.** The TUI
is itself a client of the same server: `tui/worker.ts` hosts the HTTP API
in-process (`Server.Default().app.fetch`, `worker.ts:42`) and can bind a
real port on demand (`worker.ts:54-58`, `opencode serve`); the desktop/web
app connects to it; `opencode attach` connects a second TUI to a running
server. Sharing works because:

- sessions are addressed by ID over HTTP, with multiple concurrent SSE
  subscribers per instance (`handlers/event.ts:29-45` — one unbounded queue
  per connection, no fan-out limit);
- write serialization is enforced by the busy guard: a second prompt on a
  session that is mid-turn gets `SessionBusyError` (`run-state.ts:71-75`);
- steering inputs queue: a `queue` input is promoted at the next idle
  boundary (root `AGENTS.md`, "V2 Session Core": prompt admission is durable
  and promotion happens at safe provider-turn boundaries).

So the model is: many viewers, one prompter per session at a time, queue for
the rest. That is exactly the concurrency story a mesh agent needs to copy.

## What lazymesh should borrow vs. skip

**Borrow (fits a mesh-first Go TUI agent):**

1. **One HTTP API as the single control surface + OpenAPI-generated SDK**
   (`groups/session.ts`, `sdk.gen.ts`): instead of inventing a socket
   protocol, declare one typed API and generate the client. For lazymesh:
   the mesh *is* the transport (macula-mcp mesh_call), but the lesson is
   "SDK = generated mirror of the server's declared schema", not "SDK = bespoke
   wire format". A Go generator over the same OpenAPI-ish declaration gives
   lazymesh its control surface for free.
2. **Session addressing by plain ID + directory/workspace scoping**
   (`workspace-routing.ts:22-88`): ID in path, location in query/header.
   Maps directly onto lazymesh's station-per-identity model: `directory` →
   station, session ID stays a session ID. Also note opencode's
   `x-opencode-directory` header trick — a cheap way to scope one server to
   many locations.
3. **`session.status`/`session.idle` as the settle replacement**
   (`status.ts:32-42`, `run-state.ts:60-65`): turn-boundary as an *event*,
   consumed by whoever cares (CLI exits on it, UI re-renders on it). lazymesh
   rooms should emit the same kind of per-session idle envelope instead of
   inventing a settle round-trip.
4. **Durable prompt admission (SQLite inbox) + busy guard**
   (`sql.ts:140+`, `run-state.ts:71-75`): prompts queued durably, one runner
   per session, `BusyError` for concurrent prompter. Directly answers
   lazymesh's "no session persistence" gap — one SQLite file beats JSONL
   here because prompts/messages/parts/todos live in one place with WAL.
5. **Headless permission policy: deny-by-default questions + explicit
   `--auto`, reject otherwise** (`run.ts:430-448,801-821`): mesh callers are
   never interactive; opencode's "auto-reject asks and print why" is the
   right unattended default, with auto-approve as an explicit opt-in.
6. **Abort-as-HTTP-endpoint → cancel fiber/AbortController**
   (`handlers/session.ts:232-235`, `run-state.ts:77-86`, `prompt.ts:815-827`):
   interrupt = one idempotent call into the runner, recursive background-job
   cancellation included. This is lazymesh's "no interrupt" gap, in its
   simplest portable form (Go: context cancel + a per-session runner map).
7. **Generated event type union** (`types.gen.ts:7-98`): a single typed
   catalog of ~90 events is what makes the SDK usable without docs.
   lazymesh's room envelopes deserve the same single-source type list.
8. **`--format json` NDJSON-to-stdout for one-shot CLI runs** (`run.ts:678-691`):
   claw-code's NDJSON contract idea, opencode-style (mirror of the event
   stream). Cheap to add to any headless mode.
9. **ACP as the "another process drives me" standard** (`acp/agent.ts`): if
   lazymesh ever wants editor/agent interop beyond the mesh, ACP (stdio
   NDJSON, cancel + sessions + permissions) is a proven shape to copy — and
   opencode shows it can be a thin bridge over an existing HTTP API.

**Skip (doesn't fit):**

- **Localhost HTTP+SSE as *the* transport**: right for a local TUI; wrong for
  a mesh-first agent whose peers are on other machines. lazymesh already has
  mesh_call/rooms; the borrow is the *shape* (one API, generated SDK, event
  stream), not TCP on 127.0.0.1. (Keep the mDNS publish idea as an optional
  LAN convenience only.)
- **Effect/TypeScript machinery**: irrelevant to Go; only the semantics
  (scoped instance state, per-connection event queues) matter.
- **Control-plane workspace sync + session share links**: product features
  of a cloud-synced coding agent; lazymesh's equivalent is mesh membership
  itself. Share-as-URL is useful only if lazymesh wants read-only web links.
- **The legacy JSON storage + its migration array** (`storage.ts`): keep the
  SQLite end-state, not the migration history.
- **No MCP-server exposure**: opencode is MCP-client-only; claw-code's
  `mcp serve` idea stays the better inter-agent answer (or macula's own
  `mesh_serve`).

## Comparison table

| | Claude Code | claw-code | opencode |
|---|---|---|---|
| **Transport** | unix domain socket + `stream-json` over stdin/control | subprocess-per-action CLI; NDJSON stdout | **local HTTP (REST, OpenAPI-declared) + SSE `/event`** (`server.ts:73-98`, `groups/event.ts`) |
| **Session addressing** | `--session-id` + `--resume` | reference string: id / `latest` / path vs workspace-fingerprinted JSONL | **`/session/:sessionID` + `?directory=` / `x-opencode-directory` header** (`workspace-routing.ts:22-88`) |
| **Interrupt** | stdin-control message → abort | Ctrl-C `HookAbortSignal` (`Arc<AtomicBool>`); no kill channel | **`POST /session/:id/abort` → runner.cancel + AbortControllers** (`handlers/session.ts:232-235`, `prompt.ts:815-827`) |
| **Settle** | explicit `settle` command | none — per-stream completion detection | **`session.idle` / `session.status→idle` events** (`status.ts:32-42`); CLI exits on them |
| **Programmatic output** | `stream-json` record stream | dual-renderer JSON envelopes + `claw-analog` NDJSON (versioned schema) | `opencode run --format json` NDJSON mirror of events (`run.ts:678-691`) |
| **SDK** | `@anthropic-ai/claude-code` (unix-socket client) | none (deprecated stub) | **`@opencode-ai/sdk` — generated typed client of the same API** (`sdk.gen.ts`) |
| **Persistence** | JSONL per session (project-scoped) | one JSONL per session, FNV-fingerprinted dirs | **single SQLite DB `~/.local/share/opencode/opencode.db`, WAL** (`database.ts:27-55`); durable prompt inbox rows |

## Open questions

1. lazymesh's sessions span stations (mesh, no shared filesystem). opencode's
   SQLite assumes one machine. Should lazymesh's session store be
   per-station SQLite (opencode-style) with mesh-level session references
   (id + station) — or content-addressed (mesh_put + MCID) so another agent
   can pull the whole session? The opencode answer: durability is local, the
   *address* is what travels. Does that hold when the peer needs the history?
2. Is a generated Go SDK from a declared API worth the generator investment,
   or is a hand-written typed envelope (claw-code style) enough for
   lazymesh's smaller surface? opencode pays the generator because it has
   ~40 endpoint groups; lazymesh may not.
3. Do we want ACP on lazymesh (stdio NDJSON, Zed-compatible) as a *local*
   control surface in addition to the mesh? opencode proves it's a thin
   bridge over the HTTP API — for lazymesh it would be a thin bridge over
   mesh_call.
4. opencode's "durable prompt inbox, execution not durable" boundary (root
   `AGENTS.md`) is exactly the line between "resume after crash" and "resume
   after reconnect". Where should lazymesh draw it, given mesh RPC already
   has request/reply timeouts?
