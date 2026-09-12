# lazymesh — Agent Quality Gaps Survey

**Status:** All phases implemented -- survey closed out 2026-09-12 (Phases 0-8, including all six Phase-6 work packages)
**Created:** 2026-09-12
**Last Updated:** 2026-09-12

## End goal

> This survey exists so lazymesh can decide, on evidence, which gaps stand
> between it and being (a) a quality general-purpose-capable agent and
> (b) a locally controllable agent other programs can drive the way Claude
> Code is driven over a unix socket.

---

## Current capability (what lazymesh IS today, evidence-based)

lazymesh is a single Go binary (`cmd/lazymesh/main.go`) whose design
philosophy — stated in its own MVP plan — is deliberately narrow:

- **Sole tool source is macula-mcp**, spawned as an MCP stdio subprocess
  via `npx` (`internal/mcpclient/mcpclient.go:34-40`), with a restricted
  environment allowlist (`mcpclient.go:49`). Tools are discovered
  dynamically via `tools/list` and filtered through a deny-by-default
  allowlist (`internal/agent/allowlist.go:70-79`, enforced at both list
  and execute time `allowlist.go:103-121`).
- **The agent loop** (`internal/agent/agent.go:182-254`, driven by
  `cmd/lazymesh/main.go:590-693` `runAgent`) calls a configurable
  OpenAI-compatible LLM (DeepSeek default, NVIDIA/Groq working, Anthropic
  a config-shape stub — `internal/config/config.go:17-23`) with the
  allowlisted tool schemas, executing tool calls up to 25 rounds per
  `Say()`.
- **Waiting is loop-owned, not model-owned**: `internal/roomwaiter`
  (one goroutine per joined room blocked in `mesh_wait_room`,
  `roomwaiter.go:42`) and `internal/ringwaiter` (one goroutine in
  `mesh_wait_ring`, `ringwaiter.go:69`) wake the model only on real
  events; the model's own `wait_reply_seconds`/`wait_join_seconds` are
  clamped to 10s (`internal/agent/nowait.go:29`). A quiet mesh produces
  zero LLM calls (MVP plan, "Idle tick removed entirely", line 1160-1170).
- **The TUI** (charmbracelet/bubbletea, `internal/tui/model.go`) renders a
  live chat pane, rooms/rings/presence panels (polled every 2s via
  macula-mcp's read tools, `model.go:30`, `tui/mesh.go:122-170`), a mesh
  services panel (`s`, curated catalog `internal/meshservices/catalog.go`),
  a realms panel (`r`, realm join via `internal/realmjoin`), vim-style
  modal input (`model.go:46-74`, `keys.go:42-65`), ring pop-up with
  Answer/Decline/Answer+Trust (`model.go:799-826`), error pop-up/history,
  and terminal-bell cues (`tui/bell.go`).
- **Local tools exist but are doubly opt-in and honestly unsandboxed**:
  `shell_exec`/`read_file`/`write_file` (`internal/localtools/localtools.go:76-125`),
  off by default (`config.go:214`), excluded from the default allowlist
  (`allowlist.go:30-36`), path-confined for file tools
  (`localtools.go:186-200`) but `shell_exec` is "not a real sandbox"
  (`localtools.go:8-15`).
- **The MVP plan explicitly excludes** everything that would make this a
  general-purpose harness: "No file-editing tools, no shell execution, no
  todo lists, no skills discovery, no extension manager, no telemetry"
  (PLAN_LAZYMESH_MVP.md:35-37). Phase 2 added file/shell tools anyway
  (opt-in), but the rest of that exclusion list still stands.
- **The editor-plugins plan's Phase 0 — a headless stdio control API — is
  designed but not started** (PLAN_LAZYMESH_EDITOR_PLUGINS.md:108-111,
  files table line 131 "Not started").

So: lazymesh is an excellent mesh-cooperation agent with a genuinely good
human-facing TUI and strong security posture for peer-steered content —
and it has no control surface other than the TUI itself.

---

## Gaps, ranked

### CRITICAL

#### G1. No control protocol of any kind — no unix socket, no stdio control, no headless mode

**Evidence:**
- Flags are only `--version`, `--config`, `--room`, `--goal`
  (`cmd/lazymesh/main.go:47-51`). No `--unix-socket`, no `--stdio`, no
  `--session-id`, no headless flag.
- A repo-wide grep for `unix|socket|--stdio|headless|stdin|control
  protocol|session-id` finds only comments (bell.go:43's "headless" and
  mcpclient.go:80/config.go:52's mention of macula-mcp's own internal
  `CLAUDE_CODE_SESSION_ID` identity scoping). No socket code exists.
- `run()` always builds the bubbletea program
  (`main.go:246-247`: `tea.NewProgram(tuiModel, tea.WithAltScreen())`) —
  there is no code path that runs the agent without a terminal.
- The editor-plugins plan already identified this seam and left it
  unimplemented: "Phase 0 — Engine seam: lazymesh gains a headless stdio
  mode (`--stdio` / `--headless`)… Not started"
  (plans/PLAN_LAZYMESH_EDITOR_PLUGINS.md:108-111, 131).

**Why it matters:** No program (editor plugin, script, second lazymesh
process, orchestrator) can drive lazymesh at all. All programmatic
input/output is internal Go channels (`userInputCh`, `tuiEvents` —
`main.go:106-107`) that die with the process. This is the single biggest
gap versus Claude Code's `--unix-socket` control protocol.

**Fix direction:** Add a headless run mode plus a unix-socket control
server (see "The unix-socket control protocol" below), which is also the
editor-plugins plan's own Phase 0 — do not build the plugin seam and the
controller seam twice.

#### G2. No mid-turn interrupt or cancellation

**Evidence:**
- Documented as deliberately out of scope: "A message typed while the
  agent is mid-Say() is picked up once that call returns, not instantly —
  no in-flight call gets interrupted for it"
  (`main.go:720-722`; same wording in PLAN_LAZYMESH_MVP.md:460-468).
- `Loop.Say` runs up to 25 rounds inside one call with no cancellation
  hook between rounds other than the passed `ctx`
  (`internal/agent/agent.go:182-254`); `runAgent` only cancels via
  process-level `signal.NotifyContext` (`main.go:70`).

**Why it matters:** A long provider call (or a model looping tool calls)
cannot be interrupted by the operator, a controller, or an incoming
high-priority ring. Claude Code's `interrupt` control message and its
`Esc` interrupt are this exact capability. An agent that cannot be
interrupted cannot be safely delegated to.

**Fix direction:** Thread a per-turn cancellable context into
`Loop.Say` (check `ctx.Err()` between rounds and between tool calls),
expose it as a control message (`interrupt`) and a TUI key (`ctrl+g`
style, since ctrl+c currently force-quits the whole program —
`keys.go:53`).

#### G3. No programmatic output capture (no stream-json equivalent)

**Evidence:**
- Agent events exist only as in-process Go structs on a channel
  (`agent.Event` kinds `EventAssistantMessage/EventToolCall/
  EventToolResult/EventError/EventBackoff/EventMaxFailuresReached/
  EventListening`, `internal/agent/agent.go:31-66`), fanned to the TUI and
  to `agent.log` (`main.go:112-140`).
- The log is a flat, human-readable text file (append mode,
  `main.go:116`); there is no structured, machine-readable event stream,
  no JSONL output, no replay API.

**Why it matters:** Claude Code's TypeScript SDK is built on exactly one
thing: structured, newline-delimited output of every assistant message,
tool call, and result. Without a stream-json equivalent, a controller can
only parse an ad-hoc log format. The `Event` structs are already a good
schema — they just need a wire encoding.

**Fix direction:** Encode `agent.Event` as newline-delimited JSON on the
control socket (and optionally stdout in headless mode). Almost free:
the fan-out goroutine already sees every event (`main.go:595-639`).

#### G4. No session persistence or resume — conversation state is in-memory only

**Evidence:**
- `Loop.messages []provider.Message` is a plain in-memory slice
  (`internal/agent/agent.go:75-76`); nothing ever writes or reads it
  anywhere.
- Only the mesh *identity* survives restarts (opt-in via
  `config.IdentityFile`, `config.go:48-63`, or ephemeral-per-process via
  `mcpclient.go:137-147`). The conversation, rooms joined mid-run, and
  model history all vanish on exit/crash.
- The mcpclient respawn work (2026-09-08) heals the *mesh connection*
  (`mcpclient.go:234-288`) but not the conversation.

**Why it matters:** lazymesh is designed to run continuously ("an agent
meant to be recognized across restarts", MVP plan "Identity" section),
yet a crash or restart loses all conversation context — the agent forgets
every task in flight. Claude Code persists transcripts to
`~/.claude/projects/` and resumes with `--resume`.

**Fix direction:** Persist the message list (JSONL, one message per
line, appended after each `Say` round) under `~/.config/lazymesh/sessions/`,
with `--resume <id>` / `--continue` restoring it. Reload must round-trip
through the same `trimHistory` caps.

### HIGH

#### G5. No local agent-to-agent channel — the mesh is the only channel between two lazymesh processes on one box

**Evidence:**
- Nothing in the codebase opens a local socket, pipe, or file-based
  channel for inter-process communication. Two lazymesh processes on one
  machine can only talk through the Macula mesh (each runs its own
  macula-mcp subprocess, `mcpclient.go:194-207`).
- The identity default even *collided* two concurrent instances onto one
  mesh node_id until fixed (`config.go:56-62`, found live 2026-09-07).
- Ring delivery between two same-machine instances was broken upstream
  until macula-mcp 0.26.1 (`internal/ringwaiter/ringwaiter.go:20-33`) —
  local agents were literally unable to reach each other *locally* for a
  time, because every byte went through a remote station.

**Why it matters:** The "like Claude Code" use case (controller process +
agent process on one box, sub-agent fan-out on one box) currently
round-trips every message through a public mesh station — latency,
privacy, and availability of local coordination all depend on a remote
network. Claude Code's subagents talk to the parent over the local unix
socket, not the internet.

**Fix direction:** The control socket (G1) doubles as the local
agent-to-agent channel: agent A drives agent B by connecting to B's
socket. Alternatively/additionally, a mesh-room-local shortcut is out of
scope — this is a lazymesh-level concern, not a macula-mcp one.

#### G6. Context management is amnesia, not compaction

**Evidence:**
- `trimHistory` drops the oldest complete turns past 200 messages or
  250KB (`internal/agent/agent.go:113, 133, 256-295`), and truncates
  oversized single tool results to 20KB (`agent.go:148-160`). Deleted
  content is gone — nothing summarizes it, nothing re-injects it.
- The system prompt itself warns the model that its memory "gets
  trimmed" (`main.go:563-565`).

**Why it matters:** For long-running cooperation (the stated use case —
the 21.5h runaway-context incident is the proof of scale, MVP plan
1088-1194), a purely destructive trim means the agent periodically loses
all knowledge of past tasks. Quality agents (Claude Code, opencode)
summarize/compact instead of amputating.

**Fix direction:** On trim, ask the provider for a compact summary of the
evicted turns and prepend it as a system-adjacent note; or maintain a
short persistent "standing facts" memory file per session. Second-order
benefit: pairs with G4 persistence.

#### G7. No streaming — providers are non-streaming, UI renders per complete call

**Evidence:**
- `Provider.ChatCompletion` returns one complete response
  (`internal/provider/provider.go:91-92`); the OpenAI-compat wire path
  (`deepseek.go:155-157` delegating to `openaicompat.go`) issues a plain
  non-streaming request. A grep for `stream|SSE|text/event-stream` across
  `internal/provider` finds nothing.
- The TUI renders `EventAssistantMessage` only after the full completion
  arrives (`tui/model.go:841-867`).

**Why it matters:** First-token latency on DeepSeek for a 25-round tool
loop is visible as long silent stretches in the chat pane; the human
cannot distinguish "thinking" from "wedged" mid-call (the exact ambiguity
Fable's finding #3 already called out — MVP plan 242-253). Every
streaming-quality agent streams.

**Fix direction:** Add `stream bool` to `ChatRequest` and a
`ChatCompletionStream` callback variant to the Provider interface; emit
per-delta `EventAssistantMessageDelta` events. Bigger change than it
looks — tool-call streaming accumulation is the fiddly part.

#### G8. No skills / AGENTS.md support, no hooks

**Evidence:**
- Repo-wide grep for `AGENTS|skill|hook|subagent` matches only unrelated
  comments (main_test.go:300 "permission", identity.go:15 "skill's
  references/palette.md" — a color palette mention, not a skills system).
- The system prompt is a fixed string with config toggles
  (`main.go:509-577`); nothing reads workspace context files, and
  nothing runs pre/post-turn hooks.

**Why it matters:** Every quality coding agent reads repo/workspace
instructions (`AGENTS.md`, `CLAUDE.md`) and supports user hooks. For
lazymesh specifically, skills would encode "how to behave on this mesh /
in this room" without prompt engineering. The workspace CLAUDE.md rules
are invisible to the agent today.

**Fix direction:** Read `~/.config/lazymesh/AGENTS.md` (and optionally
the working dir's own) into the system prompt at startup; hooks are a
natural fit on the control socket (`hook`-style control messages that a
controller registers).

#### G9. No per-action approval — consent is static, not dynamic

**Evidence:**
- The only consent mechanisms are the static tool allowlist
  (`allowlist.go:103-121`) and the ring policy pop-up
  (`tui/model.go:799-826`, `internal/contactpolicy`). Once a tool is
  allowlisted (e.g. `shell_exec` by explicit operator config,
  `config.go:68-83`), every call the model makes executes immediately,
  with no per-action human confirmation.
- Realm join is the one action deliberately human-gated outside the tool
  loop (`tui/model.go:391-422`) — the exception that proves the rule.

**Why it matters:** Claude Code's permission modes (allow/ask/deny per
tool per session, with per-call asks) are a core trust feature. A peer
can steer lazymesh's conversation; with local tools allowlisted, that
steering reaches local execution with zero human in the loop.

**Fix direction:** Add an "ask" mode to the allowlist wrapper: an
`AllowlistSource` variant that emits an approval request on the TUI
(y/n popup, like the ring pop-up) and, headlessly, on the control socket
for the controller to answer.

#### G10. Local tool sandbox is weak and admits escape

**Evidence:**
- `shell_exec` is explicitly "NOT a real sandbox… a command can still
  `cd` anywhere the operator's account can reach"
  (`internal/localtools/localtools.go:8-15`).
- `resolveInSandbox` is path/string-based with no symlink resolution
  (`localtools.go:186-200`); a symlink planted inside `working_dir`
  pointing outside defeats `read_file`/`write_file` (acknowledged, not
  fixed: PLAN_LAZYMESH_MVP.md:255-262).
- No timeout-sandbox beyond the process-level shell timeout
  (`config.go:198`, 30s default `localtools.go:69-72`); no network or
  resource limits.

**Why it matters:** Quality agents run tool execution in sandboxes
(containers, firejail, landlock, seatbelt). The current posture is
"as trusted as the operator typing" — acceptable only because it is
opt-in, but it caps lazymesh below the quality bar for any real work.

**Fix direction:** `EvalSymlinks` check in `resolveInSandbox` (cheap,
immediate), then optional sandbox backends (bubblewrap/container) as a
config-selectable executor for `shell_exec`.

#### G11. No task-handoff discipline — envelope kinds are neither taught nor validated

**Evidence:**
- The system prompt never mentions the room envelope kinds
  (`question_asked`, `answer_given`, `task_handed_over`,
  `result_reported`, `lane_claimed`/`lane_released`,
  `claim_confirmed`/`claim_disputed`); `buildSystemPrompt`
  (`main.go:509-577`) teaches scope discovery and waiting etiquette only.
- Worse, the terse schema rewrite *removed* unused `kind` enum values
  from the model's view ("dropping … unused `kind` enum values, e.g.
  `lane_claimed`/`claim_confirmed`, this narrow use case never emits" —
  PLAN_LAZYMESH_MVP.md:1227-1230), so the model cannot even emit some
  kinds if it wanted to.
- No local validation of `in_reply_to` threading; handoff integrity is
  whatever the model happens to do in text.

**Why it matters:** The mesh protocol has a real task-delegation grammar
(the MCP server's `mesh_say` kinds). lazymesh agents speaking it as plain
text forfeit lane coordination, claim verification, and reply threading —
the features that make agent-to-agent cooperation reliable instead of
chat. Claude Code's subagent protocol is the local analog; the mesh kinds
are the mesh one.

**Fix direction:** Teach the kinds in the system prompt, restore the
trimmed `kind` values in `terse.go`'s `mesh_say` schema, and add a
`NoBlockingWaitSource`-style wrapper that injects `in_reply_to` when the
model answers/reports without one.

#### G12. No web fetch capability

**Evidence:** No tool anywhere fetches HTTP content for the model; the
only tools are mesh tools, curated mesh-service calls, and local
file/shell (`allowlist.go:70-79`, `localtools.go:76-125`). The
providers' HTTP clients are outbound API-only (`deepseek.go:28-33`).

**Why it matters:** Any real "research-then-act" task needs web access.
Claude Code, opencode, and Goose all ship a fetch/search tool. For a
mesh agent this is medium-weight (allowlist-gated, rate-limited), but its
absence is a genuine capability hole.

**Fix direction:** A `web_fetch` tool behind the same allowlist posture
(local tool bucket, off by default), with a byte cap and timeout.

#### G13. No deferred-work scheduling (no wakeups)

**Evidence:** The agent loop only wakes on human input, room arrivals, or
rings (`nextEvent`, `main.go:815-837`); there is no timer/scheduler
channel ("No periodic idle tick… removed 2026-09-07" — `main.go:780-787`,
MVP plan 1160-1170). The mesh etiquette's documented pattern for
checking back later is "use your harness's own scheduler" — lazymesh has
no scheduler.

**Why it matters:** "Do X in 10 minutes" or "check back at 14:00" is
unexpressible. A quiet mesh produces zero LLM calls by design; there is
no way for a future event (including a human saying "later") to schedule
one. This limits both agent quality and controller use cases (deferred
`mesh_say`, retries, reminders).

**Fix direction:** A small schedule queue (timer goroutine feeding the
same select in `nextEvent`) with control-socket exposure
(`schedule {at, prompt}`).

### MEDIUM

#### G14. Anthropic provider is a stub, one of four providers is non-functional

**Evidence:** `config.go:17-23` ("anthropic… returns an error if
selected"); `buildProvider`'s anthropic branch constructs it anyway
(`main.go:307-312`). The interface shape exists; the Messages API mapping
doesn't.

**Why it matters:** Multi-provider choice is otherwise real (DeepSeek/
NVIDIA/Groq all work). Anthropic completion would unlock Claude models —
currently the leading agent-quality models — for lazymesh operators.

**Fix direction:** Implement the Messages API mapping (streaming can land
with G7's interface change at the same time).

#### G15. No local subagent / task delegation

**Evidence:** The only delegation channel is the mesh: `mesh_ring` was
added to the allowlist (2026-09-08, `allowlist.go:49-69`) so the agent
can *ring* peers. There is no local subprocess subagent, no task
splitting, no local result collection.

**Why it matters:** Claude Code spawns subagents for parallel exploration
and task isolation. For lazymesh, the natural subagent IS another
lazymesh process (see G5/G1): with a control socket, a parent can
spawn `lazymesh --headless --unix-socket /tmp/...` workers and hand them
tasks via `input`, collecting results via the output stream.

**Fix direction:** First G1+G4+G13; subagent orchestration is then a
controller concern (SDK example), not new agent-loop machinery.

#### G16. Observability is log-centric; no metrics or structured telemetry

**Evidence:** Observability = `agent.log` (structured slog lines,
`internal/logging`), per-cycle usage lines (`main.go:705-715`), TUI error
history (`tui/errorhistory.go`), bell cues. `go.mod` has no metrics/OTel
deps. No counters (tool calls, rings answered, rooms joined, tokens/day)
are exported anywhere.

**Why it matters:** The codebase repeatedly learned things the hard way
(1M-token crash, ring bugs) that metrics would surface early. For a
long-running agent, a `/metrics`-style export or log-structured counters
are the difference between knowing and guessing.

**Fix direction:** Structured counters in agent.log + a control-socket
`status` query; Prometheus export later.

#### G17. Full tool-schema array resent on every round — real, unbounded cost

**Evidence:** The unplanned finding of the sustained run: 8.7M prompt
tokens / 36 cycles, "root cause is almost certainly the full tool-spec
array… being resent on every single ChatCompletion round"
(PLAN_LAZYMESH_MVP.md:1048-1058; carried open at 1072-1076). `Say` lists
tools once per `Say` and passes the same `toolSpecs` to every round
(`agent.go:189-203`), up to 25 times.

**Why it matters:** "DeepSeek chosen for being cheap to run
continuously" is undermined by paying full tool-schema cost per round.
This is the difference between an agent that costs cents/day and one
that costs dollars.

**Fix direction:** List tools once per session (not per `Say`) — but the
OpenAI-compat wire still needs the array each request; the real lever is
shrinking it further (fewer tools, leaner schemas) or a provider-side
tool-choice cache. At minimum: measure and log round-level token delta.

### LOW

#### G18. `resolveAllowlist` drops all `mesh_service_*` defaults on any override

**Evidence:** MVP plan finding, tracked not fixed:
"resolveAllowlist silently drops ALL `mesh_service_*` defaults the moment
an operator sets ANY `tool_allowlist` override" (PLAN_LAZYMESH_MVP.md:353-356);
code at `main.go:397-406`.

**Why it matters:** A config footgun: opting one tool in silently removes
every curated mesh service.

**Fix direction:** A merge mode (`tool_allowlist_extends: true`) or
explicit documentation + validation warning.

#### G19. Contact policy file shared across lazymesh instances

**Evidence:** `mcpclient.go:101-106`: `ContactPolicyFile` defaults to one
fixed path shared by every lazymesh instance on the machine; flagged in
the code itself as unrevisited.

**Why it matters:** One instance's "Answer + Trust" alters another
instance's ring behavior — acceptable for identity-free trust data, but
worth scoping per instance once sessions (G4) exist.

**Fix direction:** Default to
`~/.config/lazymesh/<session-id>/contact_policy.json` once session ids
exist (G1/G4).

#### G20. Streaming is also missing from the realm-join and mesh-service-call UX paths

**Evidence:** Realm join shows event-by-event status (`model.go:364-434`)
but the mesh service call renders only its final result
(`model.go:440-478`); neither is a problem today at these scales.

**Why it matters:** Purely cosmetic; listed for completeness.

**Fix direction:** No action needed now; revisit if direct mesh calls
become long-running.

---

## The unix-socket control protocol (design sketch)

> **Decided 2026-09-12 (Raf steer): the unix socket IS the control
> transport.** The HTTP/SSE-server alternative (opencode's shape) was
> considered and rejected on security grounds — a TCP listener is
> reachable by any local process and by browsers (rebinding/CSRF-shaped
> abuse); a unix socket gets filesystem permissions and optional
> `SO_PEERCRED` peer verification for free. The sketch below stands as
> the design.

### Components

1. **`--unix-socket <path>` flag** (Claude Code's exact flag): lazymesh
   creates a socket file at `<path>` (default suggestion:
   `$XDG_RUNTIME_DIR/lazymesh/<session-id>.sock`, fallback
   `/tmp/lazymesh-<uid>/<session-id>.sock`, mode 0600), serving
   newline-delimited JSON in both directions. `--headless` (or
   `--stdio`) runs the same engine without the bubbletea TUI.
2. **Session id**: one lazymesh process = one agent loop = one session.
   At startup the process mints a session id (or accepts
   `--session-id <id>` for restart/resume, pairing with G4). The id is
   reported in the socket handshake and stamped on every output message,
   exactly like Claude Code's `session_id`/`--session-id`.
3. **Control messages (controller → lazymesh)**, one JSON object per
   line:
   - `{"type":"input","session_id":"<id>","text":"<user message>"}` —
     lands on the same channel the TUI's compose line feeds today
     (`userInputCh`, `main.go:107`); the select in `nextEvent`
     (`main.go:815-837`) already drains it.
   - `{"type":"interrupt"}` — cancels the in-flight `Say()` (requires
     G2's per-turn context).
   - `{"type":"kill"}` — force-stops the agent loop (matches
     `EventMaxFailuresReached` semantics, not process exit).
   - `{"type":"shutdown"}` — graceful exit: same path as `q` today
     (tea.Quit → `sayGoodbye`, `main.go:246-261`).
   - `{"type":"query","what":"rooms|inbox|agents|realms|status"}` — the
     read-only panel data, as JSON (already assembled by
     `fetchMeshState`, `tui/mesh.go:122-170`) — this is the
     editor-plugin plan's publish/recall/rooms/inbox/lobby surface
     (PLAN_LAZYMESH_EDITOR_PLUGINS.md:108-111).
   - `{"type":"approve","id":"<tool-call-id>","allow":1}` — the answer
     side of G9's ask-mode, when enabled.
   - `{"type":"schedule","at":"<RFC3339>","prompt":"..."}` — G13.
4. **Output messages (lazymesh → controller)**, one JSON object per line
   — the stream-json equivalent, a direct encoding of the existing
   `agent.Event` kinds (`agent.go:31-66`):
   - `{"type":"assistant","text":"..."}`
   - `{"type":"tool_call","tool":"<name>","args":{...}}`
   - `{"type":"tool_result","tool":"<name>","result":...}`
   - `{"type":"error","tool":"...","error":"..."}`
   - `{"type":"backoff"}`, `{"type":"max_failures"}`
   - `{"type":"turn_complete","session_id":"..."}` — emitted on
     `EventListening` (`main.go:686`), the natural settle point.
   - `{"type":"ring_pending","ring_id":...,"from":...,"purpose":...}`
     when a ring would pop the TUI, so a headless controller sees it.
   - `{"type":"session","session_id":"...","petname":"..."}` on
     connect.
5. **Settle semantics** (how a controller knows a turn finished): the
   controller sends one `input` and waits for `turn_complete`; further
   `input` messages received mid-turn are queued — exactly today's
   behavior for TUI-composed messages (`main.go:720-722`), preserved,
   with `interrupt` now available for the case where queueing is wrong.

### How the TUI and an external controller share one session

- Extract the two channels (`userInputCh`, `tuiEvents`,
  `main.go:106-107`) into a small **frontend bus**: one input queue
  (multiple writers: TUI + socket server) and one broadcast event hub
  (multiple readers: TUI renderer + socket connections + agent.log).
  The agent loop is untouched — it already only knows channels.
- `run()` gains three frontends: the bubbletea TUI (as today), the unix
  socket server (headless AND alongside the TUI), and stdout-json (for
  `--stdio` embedding by editors). All attach to the same bus; running
  TUI + socket simultaneously gives the "watch in TUI, drive from a
  script" mode Claude Code users get with the SDK.
- Ring pop-up interplay: when a ring arrives, the TUI shows its popup
  AND the bus emits `ring_pending`; whichever frontend answers first
  (popup key or `{"type":"answer_ring",...}` control message) marks the
  ring seen (same `seenRingIDs` semantics, `model.go:142`).
- Interrupt becomes a bus message too: `interrupt` cancels the turn
  wherever it came from (TUI key, socket, signal).

### What this deliberately reuses

- The existing `Event` structs are the schema; the existing `nextEvent`
  select is the queueing policy; the existing allowlist is the
  permission model; the existing `meshState` types (`tui/mesh.go:22-113`)
  are the query payloads. The socket is a thin shell over machinery that
  already exists — which is why the editor-plugins plan calls it
  "Phase 0", and why this plan recommends building ONE seam, not two.

---

## Decisions (Raf steer, 2026-09-12)

- **D1 — Control transport is a unix socket, NDJSON-framed.** `net.Listen("unix", …)`,
  mode 0600, one JSON object per line both ways, no HTTP parsing (no
  headers/CORS/content-type surface at all). Optional `SO_PEERCRED`
  peer-uid check on Linux. The HTTP+SSE server shape is rejected —
  see the section above. Cost accepted: no generated SDK; a shared
  ~100-line envelope package covers the plugin/controller clients.
- **D2 — Session store is JSONL, no SQLite.** Append-only event log +
  resume is a log, not a table. Layout:
  `~/.local/share/lazymesh/sessions/<workspace-fingerprint>/<session-id>.jsonl`
  with claw-code-style reference aliases (`latest`/`last`/id). `O_APPEND`
  writes. SQLite is revisited only if cross-session history *search*
  becomes a real feature (grep suffices until then). JSONL also keeps
  sessions trivially exportable as content-addressed mesh artifacts
  later.
- **D3 — TUI adopts opencode's chat look-and-feel.** Chatbox-style prompt
  entry at the bottom; assistant replies rendered as markdown (charm
  stack: bubbletea + lipgloss + glamour). Consequence: **streaming is
  promoted from a Phase-5 nice-to-have to a prerequisite** (a chatbox
  that renders per-full-call feels broken) — the provider interface
  gains a streaming variant in the same phase as the socket.
- **D4 — Actor core (pending Ergo deep study).** Multi-session in-process
  hosting (OD2) is OTP's shape, and one-process-N-sessions is the one
  place where supervision trees are a structural requirement, not a
  nicety. Direction: sessions = supervised actors; supervisor tree for
  tab isolation; monitors/deathwatch for OD1's local peer channel; the
  frontend bus becomes actor mailboxes. **Candidate: Ergo**
  (ergo-services/ergo, MIT, active 2026-09-07 — the OTP-faithful Go
  option; Proto.Actor dormant since 2026-04, go-actor has no
  supervision trees, grain is dead). **Gate: the deep study
  (`RESEARCH_ERGO_FOR_LAZYMESH.md`) must confirm single-node/embedded
  operation (no Ergo network stack — macula is the transport), panic →
  supervisor-restart semantics, and blocking-IO tolerance before any
  Phase 0 code is written — the bus design is what supervision replaces,
  so the architecture must be chosen first, not bolted on later.**

---

## Phases

- [x] **Phase 0 — Actor-core port (bus-vs-actors decided: ACTORS — D4).**
      Landed 2026-09-12 as `internal/sessionhost` + the cmd/lazymesh wiring:
      embedded Ergo node with `NetworkModeDisabled` + silent logger, a
      simple_one_for_one root supervisor, and supervised session actors
      hosting the real `agent.Loop`. Dynamic N-session hosting (OD2) is
      in (`StartSession`/`Sessions`/`StopSession`); the transient strategy
      restarts only abnormal deaths (panic → fresh state + same
      SessionArgs; normal stop ends the conversation). cmd/lazymesh now
      boots the tree, `runAgent` is a driver that blocks on one Say at a
      time and REATTACHES to the supervised replacement after a
      panic-restart, and an `eventBridge` process hands loop events to the
      TUI/log/room-waiter exactly as the old forwarding goroutine did.
      Proven by tests: node boot, say→events→subscribers,
      panic→`TerminateReasonPanic`→restart with fresh state,
      normal-stop-does-not-restart, plus the full pre-existing suite.
      The TUI itself still consumes channels; view-model actors (OD3) and
      the unix-socket control plane (D1) are the next phases, not Phase 0.
- [x] **Phase 1 — Headless mode + unix socket.** Landed 2026-09-12 as
      `internal/frontend` + the run() wiring: `--headless`,
      `--unix-socket <path>`, `--session-id <id>`; socket file 0600 in
      `$XDG_RUNTIME_DIR/lazymesh/` (or a per-user /tmp dir); NDJSON
      control messages `input`/`shutdown`/`query` (status|rooms|inbox|
      agents|realms) with `query_result` replies to the asker only; output
      stream `session` (handshake)/`assistant`/`tool_call`/`tool_result`/
      `error`/`backoff`/`max_failures`/`turn_complete` (the settle point,
      mapped from EventListening). input lands on the same channel as the
      TUI compose line; the TUI is untouched and runs alongside the
      socket. Headless without a socket exits on signal only. Proven by
      six tests (perms 0600, handshake+input, event stream + turn_complete,
      query round-trip, shutdown signal, Close removes the socket). Known
      follow-ups: SO_PEERCRED peer-uid check, and `ring_pending` on the
      wire (arrives with the ring/approve phase).
- [x] **Phase 2 — Streaming + chat UX (D3).** Landed 2026-09-12:
      `provider.Streamer` interface + SSE streaming over the shared
      OpenAI-compatible wire (DeepSeek/Groq/NVIDIA), with tool-call
      fragments accumulated per index and usage taken from the final
      chunk (`stream_options.include_usage`). `agent.Loop.Say` streams
      `EventAssistantDelta` per chunk then the single completed
      `EventAssistantMessage`; non-streamers fall back with byte-identical
      behavior. The session actor drains events LIVE during a turn (a
      self-Send forwarding goroutine — the pre-streaming post-turn drain
      would have deadlocked the stream at the buffer bound) and emits the
      turn's `EventListening` itself, so deltas and the settle point ride
      one ordered delivery path to the control socket. The TUI merges
      deltas into one growing assistant entry and renders assistant
      answers as markdown via glamour (word-wrapped, cached per finished
      entry); the socket gains `{"type":"delta"}` lines. The chatbox
      compose line already existed (bottom textinput) — D3's real gap was
      streaming + readable markdown, both now covered. Proven by new
      tests in provider (SSE deltas/usage/tool-fragments/consumer-abort),
      agent (stream vs fallback), sessionhost (delta→message→listening
      order), tui (merge + markdown render) and frontend (delta wire).
- [x] **Phase 3 — Interrupt.** Landed 2026-09-12: every turn runs under a
      per-turn cancellable context; the session registers its cancel func
      in a concurrent registry (`sessionhost.Interrupt(pid)`) so an
      interrupt can reach a session whose actor goroutine is blocked
      inside the provider call — ctx-cancel at the boundary, never a
      Kill, per Ergo condition 3. Two triggers: the TUI's `x` key (normal
      mode, via a new InterruptCh option) and the control socket's
      `{"type":"interrupt"}` message (no ack — the event stream's
      EventError + turn_complete IS the answer). The driver recognizes
      `context.Canceled` in SayReply.Err and treats an interrupted turn
      as deliberate, not as a consecutive failure: it logs and returns to
      its wait without backoff accounting; the interrupted message stays
      in conversation history as the fact it is. Proven by tests:
      sessionhost (provider blocked mid-call → interrupt → SayTurn
      returns Canceled → error+listening events still delivered), frontend
      (interrupt control message reaches the interrupt func), tui (`x`
      signals InterruptCh + confirmation chat line).
- [x] **Phase 4 — Session persistence/resume (D2).** Landed 2026-09-12 as
      `internal/sessionstore`: JSONL append logs at
      `~/.local/share/lazymesh/sessions/<sha256(cwd)[:16]>/<session-id>.jsonl`
      (XDG_DATA_HOME respected), one JSON line per message, O_APPEND
      writes (a crash loses at most the final line; a torn trailing line
      is skipped on load, never fatal). Every run persists its turn's
      completed state change (user + assistant + tool messages appended
      after the turn); `--resume <id|latest|last>` and `--continue`
      (alias of `--resume latest`) restore the conversation, with the
      CURRENT run's system prompt kept (it is rebuilt from live
      config/flags, never resurrected from a log) and the history trimmed
      to live bounds. The same restore path a supervisor restart rides,
      since SOFO hands the restarted instance its SessionArgs unchanged —
      a panicked session now comes back with its conversation, closing
      the last gap between "restart" and "resume". Proven by tests:
      round-trip (roles/tool calls/order), incremental append, torn-line
      tolerance, latest/ref resolution, fingerprint scoping, Loop.Restore
      keeping the live system prompt, and the end-to-end resume test
      (persist → stop → new session same id → 3 messages restored).
- [x] **Phase 5 — SDKs.** Landed 2026-09-12: `sdk/` — a stdlib-only Go
      client for the control protocol (`Dial`/`Say` (blocks to the settle
      point, returns a structured Turn: deltas, authoritative text, tool
      calls/results, errors)/`Query`/`Interrupt`/`Shutdown`), and
      `sdk/python/lazymesh_client.py` — the same wire in a single-file
      stdlib-only Python CLI (`session|say|query|interrupt|shutdown`).
      The G5 dogfood example ships as `examples/handoff`: a parent
      process starts two headless lazymesh sessions on one box and hands
      a task back and forth over their unix sockets — the parent is the
      courier, the sockets are the channel, zero mesh round-trips, and
      the two sessions never meet on the mesh at all. Proven by tests:
      Go client full-turn collection + query round-trip + interrupt/
      shutdown over a real server, and the Python client exercised end to
      end against a live server by a Go test (skips without python3).
- [x] **Phase 6 — Quality features, work packages G9 (approval/ask mode)
      and G6 (context compaction).** G9 landed 2026-09-12:
      `agent.AskSource` gates a configured tool set behind per-action
      `Consent`, layered OUTSIDE the allowlist; `config.tool_asklist`
      names the tools. The session implements the consent itself:
      broadcasts `EventApprovalRequested` directly to subscribers (a
      self-Sent event would deadlock in the actor's own mailbox while the
      turn waits), waits on a per-turn channel bounded by ctx + a
      2-minute timeout; the TUI popup (`y`/`n`/esc) and the socket's
      `{"type":"approve","id":...,"allow":1}` both funnel into
      `sessionhost.AnswerApproval` (id-matched, stale answers discarded).
      G6 landed same day: `trimHistory` now SUMMARIZES evicted turns via
      a tool-less completion on the loop's own provider, folding them
      into a `l.summary` that rides as a system message right after the
      real system prompt — the model keeps the record, loses the verbatim
      text. Summarization failure degrades to the old amputation with an
      EventError saying so; `Restore` resets the summary with the
      history. Remaining Phase 6 packages: G8 skills/AGENTS.md, G13
      wakeups, G12 web fetch, G10 sandbox. Proven by tests: the full
      approval chain (block → answer → complete), deny path, popup y/n,
      socket approve round trip, summarization presence, failure
      fallback + EventError, and the pre-G6 boundary discipline
      (turn-cut, tool-call/result pairing) pinned on the fallback path.
- [x] **Phase 7 — Handoff discipline (G11).** Landed 2026-09-12: the
      system prompt now teaches the envelope grammar (question→answer,
      task→result, lane claim→release, claims weigh in on results; every
      reply kind REQUIRES in_reply_to naming the message it answers); the
      terse mesh_say schema restored the full model-emittable kind enum
      (the lifecycle kinds stay out — those are published by the room
      tools, never by mesh_say) and updated the in_reply_to description;
      and `agent.HandoffSource` enforces the rule at the boundary — a
      reply kind without a well-formed 32-hex in_reply_to is refused
      BEFORE it reaches the mesh, with the model told to supply the
      message_id it read from mesh_read_inbox. Deliberately validation,
      not injection: the harness cannot know which message the model
      intends to answer, so guessing an id would be worse than refusing.
      Proven by tests: refusal for every reply kind + malformed ids,
      permission for well-formed replies and plain kinds, pass-through
      for non-mesh_say tools, grammar presence in the prompt, and the
      restored terse enum (lifecycle kinds still excluded).
- [x] **Phase 8 — Anthropic provider, metrics, allowlist merge-mode
      fix (G14, G16, G18).** Landed 2026-09-12, the last phase: G14
      implements the real Anthropic Messages API (top-level system
      blocks, content-block messages, tool_use/tool_result mapping, the
      required headers, error mapping) AND its SSE stream (text deltas,
      input_json_delta accumulation per block, usage from message_start/
      message_delta) — Anthropic now satisfies both Provider and Streamer,
      and every configured provider is functional. G16 adds
      `internal/counters`: a mutex-guarded registry the event bridge
      records into (turns at the listening boundary, tool activity per
      kind and per tool, errors/backoffs/approvals), surfaced as a
      structured `[counters] ...` line in agent.log at every turn
      boundary and as a `counters` object in the control socket's status
      query — Prometheus export remains the explicitly later step. G18
      adds `tool_allowlist_extends`: a merge mode so naming one extra
      tool adds it instead of silently removing every default (and the
      mesh_service_* defaults); the replace semantics stay the default,
      as the one way to REMOVE a default tool. Proven by tests: the
      Anthropic wire mapping (request + response), tool round trip,
      SSE stream, API errors; the counters contract + log line; and the
      extends/replace merge behavior.

## Files to Create/Modify

| File | Purpose | Status |
|------|---------|--------|
| `cmd/lazymesh/main.go` | flags (`--headless`, `--unix-socket`, `--session-id`), bus wiring, frontend selection | Not started |
| `internal/frontends/` (new) | frontend bus: input queue + event hub + socket server + stdout-json encoder | Not started |
| `internal/agent/agent.go` | per-turn cancellable ctx in `Say`; optional summary-on-trim hook | Not started |
| `internal/sessionstore/` (new) | JSONL session persistence + resume | Not started |
| `internal/provider/*.go` | streaming variant on the Provider interface; Anthropic Messages API | Not started |
| `internal/localtools/localtools.go` | `EvalSymlinks` in `resolveInSandbox`; optional sandbox executor | Not started |
| `internal/agent/terse.go` | restore trimmed `mesh_say` `kind` values | Not started |
| `internal/agent/allowlist.go` | optional per-call ask mode | Not started |
| `internal/tui/*.go` | interrupt key, approval popup, streaming event rendering | Not started |
| `sdk/` (new, subdir or separate repo) | Go/Python control-protocol clients + examples | Not started |

## Success Criteria

- [x] `lazymesh --headless --unix-socket /tmp/lz.sock` runs the full
      agent loop with no terminal, and a controller can send
      `{"type":"input",...}` and receive `assistant`/`tool_call`/
      `tool_result`/`turn_complete` events as newline-delimited JSON.
- [x] `{"type":"interrupt"}` stops an in-flight turn and the loop
      returns to listening state.
- [x] Two lazymesh processes on one box can hand a task back and forth
      over their sockets with zero mesh round-trips.
- [x] The TUI and a socket controller can attach to the same session
      simultaneously: TUI renders what the controller drives, and a
      ring pop-up in the TUI is answerable from the socket.
- [x] A crash and `--resume <id>` restores the conversation; a
      controller sees no duplicate or lost turns.
- [x] A conversation that exceeds the context budget is compacted by
      summarization, not silently amputated.
- [x] `shell_exec` allowed in an "ask" permission mode prompts the
      operator (TUI popup or socket `approve`) per call.
- [x] The mesh `kind` grammar (question/answer/task/result/lane/claim)
      is taught by the prompt, emitted by the model, and threaded with
      `in_reply_to` automatically.
- [x] Streaming: assistant text appears in the TUI incrementally, and
      delta events appear on the socket.
- [x] TUI look-and-feel (D3): chatbox prompt entry, markdown-rendered
      assistant replies via glamour, in both terminal and socket-driven
      sessions.
- [x] Anthropic is a working provider, not a stub.

---

## Open directions (2026-09-12 discussion — not yet decided)

### OD1 — Squaring local on-box integration with the mesh

The unix socket above is an *operator/harness* control surface; it is not
the agent-to-agent channel. Two lazymesh agents on one box currently can
only meet through the mesh (G5). Direction under discussion: make the
**local channel a mesh transport, not a second protocol** — co-located
lazymesh processes rendezvous via a per-user sockets directory
(`$XDG_RUNTIME_DIR/lazymesh/peers/<node_id>.sock`), then speak the *same*
envelope/room/ring protocol framed as NDJSON over unix sockets instead of
QUIC-to-station. Identity (node_id), signing, contact policy, room
topics and envelope kinds are all reused unchanged — the local hop skips
only the QUIC round-trip, and gains `SO_PEERCRED` on top. Routing rule:
ring/publish checks the local peer directory first, falls back to the
mesh — so a room can mix local and remote participants, relayed by the
local process. Open questions: whether the local directory doubles as a
broker (or peers stay p2p per socket), and how a station's rooms map
onto local-only rooms.

### OD2 — In-process multi-session support (kitty-tab workflow, in TUI)

Raf today runs one agent per kitty tab. Proposal under discussion:
lazymesh hosts **N concurrent agent sessions in one process**, each a
TUI tab (new-tab key, tab bar with per-tab status), because the session
manager is the natural home for everything else this plan adds: each
session = own conversation, own context, own control-socket id (D1's
`--session-id` scales to `<id>` per session instead of per process),
own JSONL (D2), and — on the mesh — its own derived node_id (master
identity + session counter → per-session keypair, the same pattern
macula-mcp uses per process), so every tab is independently present,
ringable, and attributable. Failure isolation is the honest cost:
one process means one panic can take all tabs — mitigate with
per-session `recover()` and the socket/bus isolation already designed.
This also completes OD1's picture: sessions talk to each other locally
over the same bus/socket layer the operator controller uses.

---

## References

- `RESEARCH_CLAW_CODE_CONTROL_PROTOCOL.md` — CLI-envelope + JSONL
  session-store model (borrowed: D2).
- `RESEARCH_OPENCODE_CONTROL_PROTOCOL.md` — server + SDK + SSE model
  (borrowed: session id + directory addressing, abort endpoint,
  deny-by-default headless permissions; skipped: HTTP transport, SQLite).
