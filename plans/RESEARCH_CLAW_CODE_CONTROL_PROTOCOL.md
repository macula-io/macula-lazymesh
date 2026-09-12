# Research: claw-code control protocol

- **Status:** Survey complete — desk research, no code changed
- **Created:** 2026-09-12
- **Last Updated:** 2026-09-12

## End goal

macula-lazymesh wants to borrow claw-code's ideas for how an operator (and, later,
other agents) address, drive, interrupt, and resume an agent process. The concrete
question this survey answers: *does claw-code implement the claude-code-like
unix-socket + stream-json + session-id + interrupt/settle control protocol, and if
not, what does it do instead?* Answer up front: **it does not.** There is no unix
socket, no stream-json, no `--session-id`/`--stdin-control` equivalent, and no SDK.
What it has instead is a small set of disciplined CLI conventions (JSON envelopes,
session-id aliases, JSONL persistence, a stdio MCP server, an NDJSON contract for
external consumers) that are directly transferable to a mesh-first Go TUI agent.

## What claw-code is

- **License:** MIT (SPDX: `MIT`), copyright UltraWorkers and Claw Code contributors (`LICENSE:1`).
- **Language:** Rust, Cargo workspace, edition 2021, resolver 2, `publish = false`, workspace version 0.1.3 (`rust/AGENTS.md`). 11 crates (`rust/crates/`). `unsafe_code = "forbid"` workspace-wide.
- **Repo:** `instructkr/claw-code` (fork line of `ultraworkers/claw-code`). The README is explicit that this repo is an "agent-managed exhibit", and the canonical runtime is `rust/` — `src/` + `tests/` are a Python porting/parity workspace, not production code (`README.md:96-111`, `concept.md:108`).
- **Core binary:** `claw`, produced by crate `rusty-claude-cli` (package/bin name mismatch) — `rust/crates/rusty-claude-cli/src/main.rs` (~19,800 lines, hand-rolled arg parser, no clap).
- **Supporting programs:** `claw-analog` (lean CI/external-agent loop over api+runtime, `rust/crates/claw-analog/`), `claw-rag-service` (axum + SQLite RAG, `rust/crates/claw-rag-service/`), `mock-anthropic-service` (deterministic test double).
- **Containerfile:** merely a Rust build image (`FROM rust:bookworm` + libssl/pkg-config/git) — it is **not** the sandbox mechanism (`Containerfile:1-13`). Sandboxing is runtime `unshare`-based (below).

## Control interface

### The honest headline

There is **no claude-code-style control socket**. Concretely absent:

- No unix domain socket anywhere in the workspace (grep for `unix_socket`/`UnixSocket`/`stream-json`/`StreamJson` finds nothing except a doc comment mentioning "`opencode serve`").
- `claw acp serve` (the ACP/Zed JSON-RPC daemon that would be the nearest analogue) is **explicitly a stub**: it prints a status envelope and exits 2 (`main.rs:1092-1095`, `main.rs:2545-2547`, help text at `main.rs:10139-10142`; contract documented in `docs/g011-acp-json-rpc-status-contract.md`).
- No `--stdin-control`, no `settle`, no kill/clear command channel. No SDK crate; the crates.io `claw-code` package is a deprecated stub (README.md:117-121).

### What the control surface actually is

Everything is subprocess-per-action over the `claw` CLI, with a uniform output-format contract. `run()` at `main.rs:995-1158` flat-matches the `CliAction` enum (25 variants, `main.rs:1162-1280`).

1. **Dual-renderer JSON contract.** Every subcommand takes `--output-format text|json` (`main.rs:1312-1316`). Each has `render_x` + `render_x_json`; JSON errors go to **stdout** with fields `status`, `error_kind`, `action`, `hint`, `exit_code` (`rust/crates/rusty-claude-cli/AGENTS.md`); text errors go to stderr. Automation relies on exit codes (`main.rs:8542-8545`, `main.rs:8570-8574`). The JSON contract is regression-pinned by ~105 tests in `tests/output_format_contract.rs` (5,986 lines).
2. **Session addressing.** Sessions are addressed by *reference string*, not a live handle:
   - explicit session id or absolute/relative path (`session_control.rs:95-136`),
   - aliases `latest` / `last` / `recent` (`session_control.rs:531-533`, `is_session_reference_alias` at `session_control.rs:747-752`), resolving to the most recent non-empty session, with cross-workspace fallback (`session_control.rs:179-214`).
   - CLI entry points: `claw --resume latest` (REPL, `USAGE.md:556`), `claw --resume <ref> /status /diff` (batch of slash commands against a resumed session, `main.rs:5020-5208`), `claw export [--session <id|latest>] [--output <path>]` (markdown transcript, `main.rs:11715-11745`), `claw session list` (`main.rs:1096`).
3. **Programmatic one-shot input.** `claw prompt "…"` runs one turn; piped stdin is merged as prompt context **only** in `danger-full-access` mode, because in prompt mode stdin must stay free for the interactive permission prompter (`main.rs:1071-1082`). `claw prompt --stdin` is covered in `tests/compact_output.rs:306-356`.
4. **Machine-readable stream output.** Not stream-json, but `claw-analog` defines a versioned **NDJSON** event contract for external agents/CI: `NDJSON_SCHEMA = "claw-analog-ndjson"`, `NDJSON_FORMAT_VERSION = 1` (`rust/crates/claw-analog/src/lib.rs:238-241`), events starting with a `run_start` carrying `schema` + `format_version` (`lib.rs:1270-1272`), plus structured `tool_result` events. This is the closest thing to an SDK: `claw-analog` deliberately exposes read/list/grep tools only, no arbitrary shell, TOML config, permission presets, for "CI, scripts and external agents" (`concept.md:14`, `concept.md:88-92`).
5. **Interrupt.** Ctrl-C is captured by `HookAbortMonitor` (`main.rs:7573-7626`, tokio `ctrl_c` at `main.rs:7594-7598`) which flips a `HookAbortSignal` (`Arc<AtomicBool>`, `runtime/src/hooks.rs:62-81`); the signal cancels running hooks (`hooks.rs:327`, `hooks.rs:804`). Bash timeouts set `interrupted` on results (`runtime/src/bash.rs:46`, `bash.rs:231`). There is no "settle" event — completion is detected per-stream, and an interrupted-transport classification exists (`tools/src/lib.rs:4992-4994`).
6. **Kill/clear.** No runtime kill channel. Session deletion is file removal: `delete_session()` unlinks the JSONL (`session_control.rs:221-225`), exposed as `claw`/`/session` surface; `clear` equivalents are compaction (below).

## Session & state

- **Storage:** one file per session, JSONL (`.jsonl`, legacy `.json`), appended one line per message rather than rewritten: `save_to_path` at `session.rs:231`, `append_persisted_message` at `session.rs:628-642` (append-mode `OpenOptions`). `Session` struct at `session.rs:116-133` carries `version`, `session_id`, timestamps, `messages`, `compaction`, `fork` (parent id + branch name), `workspace_root`, `prompt_history`, `model`, `persistence` path.
- **Location & namespacing:** `SessionStore` lays out `<cwd>/.claw/sessions/<workspace_hash>/<session_id>.jsonl`, where `<workspace_hash>` is an FNV-1a hex fingerprint of the canonical workspace root (`session_control.rs:33-48`, `session_control.rs:512-520`) — explicitly so parallel instances never collide (`session_control.rs:10-12`). A global root `~/.claw/sessions/` (or `$CLAW_CONFIG_HOME/sessions/`) is scanned as fallback (`session_control.rs:324-358`, `global_sessions_root` at `session_control.rs:524-527`). `--data-dir` supports an explicit store (`session_control.rs:55-72`).
- **Workspace binding:** sessions record `workspace_root`; loading from a different workspace is rejected with `WorkspaceMismatch` (`session_control.rs:360-380`) except via alias references, which warn (`session_control.rs:265-275`). Why: shared global stores race across parallel instances (`session.rs:108-115`).
- **Resume mechanics:** `--resume <ref> [slash commands…]` loads, validates, and replays a batch of slash commands against the session, emitting per-command JSON (`main.rs:5020-5208`). Forking with lineage exists (`fork_session` at `session_control.rs:288-310`).
- **Compaction:** `compact.rs` (invariant: never split a ToolUse/ToolResult pair, `rust/crates/runtime/AGENTS.md`), with an auto-compaction threshold from env (`PARITY.md:177-178`) and summary compression (`runtime/src/summary_compression.rs`, `trident.rs`).
- **State file:** interactive worker writes `.claw/worker-state.json` (worker id, session reference, model, permission mode) for `claw state` inspection (`USAGE.md:118`).

## Agent machinery

### Tools

Static 55-tool table `mvp_tool_specs()` at `tools/src/lib.rs:484` (~10,900-line flat file): core execution exists for `bash`, `read_file`, `write_file`, `edit_file`, `glob_search`, `grep_search` plus WebFetch/WebSearch/TodoWrite/Agent/Skill/NotebookEdit/EnterPlanMode/etc.; Task*/Team*/Cron*/LSP/MCP tools are registry-backed (in-memory registries) rather than full external integrations (`PARITY.md:147-161`). Bash flows run through `runtime/src/bash.rs` with timeout/background support (`PARITY.md:61`); file ops are edge-case-guarded (binary detection, size limits, symlink escape, workspace boundary, `PARITY.md:80-83`, `runtime/src/file_ops.rs`).

### Sandboxing

**Not bubblewrap.** Linux user namespaces via `unshare`:
- `build_linux_sandbox_command()` wraps the shell command in `unshare --user --map-root-user --mount --ipc --pid --uts --fork` (+`--map-auto` fallback for hardened containers, +`--net` only when network isolation requested), with `HOME`/`TMPDIR` redirected to `.sandbox-home`/`.sandbox-tmp` inside the workspace (`runtime/src/sandbox.rs:211-261`, candidates at `sandbox.rs:311-331`).
- Capability is probed at startup by actually running the candidate with `true`, cached in a `OnceLock` (`sandbox.rs:333-372`) — binary presence is not trusted (PARITY.md Lane 2).
- Filesystem isolation modes: `off | workspace-only | allow-list` with `allowed_mounts` (`sandbox.rs:7-14`). Container detection is separate and only observational (markers from env/`.dockerenv`/cgroup, `sandbox.rs:108-153`) — running *inside* a container is reported, not enforced.
- Applies **only to Bash execution** (`bash.rs:296-352`); non-Linux degrades to plain `sh -lc` with the sandbox HOME/TMPDIR env (`bash.rs:314-320`). The Containerfile is unrelated to this mechanism (build image only).

### Hooks / skills / AGENTS.md / rules

- **Hooks:** Claude-Code-compatible `PreToolUse`, `PostToolUse`, `PostToolUseFailure` shell hooks, string or object-with-`matcher` forms (`USAGE.md:598-619`); `HookRunner` at `runtime/src/hooks.rs:155`, abortable via `HookAbortSignal`; hook stdout JSON can carry `decision`, `updatedInput`, `permissionDecision` (`hooks.rs:553-664`).
- **AGENTS.md / memory files:** commit `08106b0` "docs: add hierarchical AGENTS.md knowledge base" added root + per-crate AGENTS.md files — note these are *docs for agents working on the repo*, generated by an `init-deep` run (13 parallel explore agents), not a runtime feature. The **runtime** memory-file machinery loads `CLAUDE.md` / `CLAW.md` / `AGENTS.md` / `.claw/CLAUDE.md` / `.claude/CLAUDE.md` / `.claw/instructions.md` plus `.claw/rules/` (`.md/.txt/.mdc`), `.claw/rules.local/`, with priority CLAUDE.md > CLAW.md > AGENTS.md per directory, bounded to git root (`USAGE.md:621-638`); files surface in `status --output-format json` as `workspace.memory_files`.
- **Skills:** `/skills` slash command + `claw skills` with list/install/uninstall/invoke; roots discovered from `.claude/skills` etc. (`commands/src/lib.rs:2235` `SkillRoot`, `discover_skill_roots` at `commands/src/lib.rs:3528`, `load_skills_from_roots` at `commands/src/lib.rs:4104`).
- **Subagents:** `Agent` tool spawns a real sub-conversation — `run_agent` at `tools/src/lib.rs:2514`, `ConversationRuntime<…, SubagentToolExecutor>` at `tools/src/lib.rs:4213-4222`; local agent definitions scaffolded via `claw agents create` into `.claw/agents/<name>.toml` (`USAGE.md:541-548`).

### MCP

- **Client:** `McpServerManager` (`runtime/src/mcp_stdio.rs:488`) spawns stdio JSON-RPC MCP processes. Config schema supports six transport families — `Stdio`, `Sse`, `Http`, `Ws`, `Sdk`, `ManagedProxy` (`config.rs:299-319`, `McpStdioServerConfig` at `config.rs:323-328`, remote config at `config.rs:330-337`) — though the honest runtime state per PARITY.md is that end-to-end lifecycle beyond the registry bridge (`mcp_tool_bridge.rs`, 406 LOC) is still open (`PARITY.md:175`). Servers configure via `.claw.json` `mcpServers`; `claw mcp --output-format json` validates entries (`USAGE.md:574-596`).
- **Server:** `claw mcp serve` runs a **stdio MCP server exposing claw's built-in tool table** — `run_mcp_serve()` at `main.rs:3802-3829` maps `mvp_tool_specs()` into a `McpServer` and hands tool calls to `execute_tool`. This is the one genuinely *machine-facing* control surface in the whole repo (an editor or another agent can drive claw over MCP stdio).

### Providers & model config

- Providers: Anthropic Messages API (native client `api/src/providers/anthropic.rs`) and an OpenAI-compatible client (`api/src/providers/openai_compat.rs`) used for OpenAI, OpenRouter, xAI (grok), DashScope (qwen), and Ollama. Auth by env: `ANTHROPIC_API_KEY`/`ANTHROPIC_AUTH_TOKEN`, `OPENAI_API_KEY`, `XAI_API_KEY`, `DASHSCOPE_API_KEY`, plus `*_BASE_URL` and `OLLAMA_HOST` overrides (`USAGE.md:224-425`). OAuth (`claw login`) was removed — key-based only (`main.rs:3891`). Prefix routing: `--model openai/…`, `--model grok`, `--model qwen-plus` (`USAGE.md:252`).

### Approval / permissions

- Modes: `read-only`, `workspace-write`, `danger-full-access`, `prompt`, `allow` (`permissions.rs:9-15`). Config key `permissions.defaultMode` (old `permissionMode` deprecated, `AGENTS.md:65`).
- `PermissionPolicy` (allow/deny/ask rules + per-tool required modes, `permissions.rs:99-120`) + `PermissionEnforcer` pre-dispatch gate (`permission_enforcer.rs:27`; workspace file-write boundaries, read-only bash heuristics, `PARITY.md:133-146`); leading read-only token must not launder a trailing destructive one (`permission_enforcer.rs:450`).
- Interactive prompting through a `PermissionPrompter` trait (`permissions.rs:86-88`) — deliberately only when stdin is a TTY: `claw-analog` refuses `danger-full-access`/`allow` when stdin is not a TTY unless explicitly acknowledged, and prompt-mode non-interactive is blocked (`claw-analog/src/lib.rs:36-54`).
- Trust resolution for repos (`trust_resolver.rs`), approval-token delegation audit (`approval_tokens.rs`), hook-supplied permission overrides (`hooks.rs:19`, `permissions.rs:31-36`).

## What lazymesh should borrow vs. skip

**Borrow (fits a mesh-first Go TUI agent):**

1. **Session-as-JSONL-appended-file + reference addressing** (`session_control.rs`): a session id / `latest` / path reference that resolves against a workspace-fingerprinted directory. Lighter than a control socket, trivially shareable across processes — and the fingerprint trick (FNV of canonical cwd, `session_control.rs:512-520`) directly answers "which session store does this instance see?" which lazymesh's station-per-identity model will hit immediately.
2. **Uniform JSON envelope contract** (`status`, `error_kind`, `action`, `hint`, `exit_code` on stdout; text on stderr) — regression-pinned by `output_format_contract.rs`. This is the part that makes claw automatable *without* stream-json; lazymesh's own command surface deserves the same discipline.
3. **`claw mcp serve`** (`main.rs:3802-3829`): the cheapest way to make one agent's toolset callable by another process. For lazymesh this pattern (expose the TUI's own capabilities as a stdio MCP server) is a better inter-agent story than inventing a control socket.
4. **NDJSON versioned event contract** (`claw-analog/src/lib.rs:238-241`): a `run_start` with `schema` + `format_version` is a 10-line idea that makes any future stream output forward-compatible; worth copying into mesh tool results.
5. **Non-TTY permission hardening** (`claw-analog/src/lib.rs:36-54`): refuse or gate unattended `danger-full-access`/`allow` when stdin isn't a TTY. Exactly the rule a mesh agent needs, since mesh callers are never interactive.
6. **Abort-by-shared-flag** (`HookAbortSignal`, `main.rs:7573-7626`): cheap interrupt plumbing (an `Arc<AtomicBool>` checked in long-running paths) that ports directly to Go (`atomic.Bool` + context).
7. **Config precedence reporting** (`USAGE.md:562-572`): status output says *which file wins which key* — invaluable for debugging multi-source config on a mesh box.

**Skip (doesn't fit):**

- **Unix-socket/stream-json parity itself**: claw-code never had it; lazymesh shouldn't either. Mesh RPC (macula-mcp's mesh_call/rooms) *is* the control transport; the useful lessons are the file/JSON contracts above, not a socket daemon.
- **Bash `unshare` sandboxing**: useless on a mesh station where commands already run inside a container/namespace; keep lazymesh's command exec inside whatever isolation the station provides. The probe-then-trust idea is worth keeping though.
- **The 25-subcommand / 19,800-line main.rs / 55-tool-table approach**: positional mega-files and registry-backed stub tools (`PARITY.md:155-161`) are artifacts of the exhibit repo, not a model.
- **ACP daemon** — a stub even in claw; no lesson.
- **OAuth removal / key-only auth, RAG service, team/cron/LSP registries** — product choices unrelated to the control-interface question.

## Open questions

1. Does lazymesh want *any* local operator surface besides the mesh (a plain file-based session store the operator can `cat`, like claw's JSONL), or is the mesh itself the only store? The claw answer (workspace-fingerprinted `.claw/sessions/`) assumes a local filesystem per workspace — mesh sessions may need a content-addressed variant (mesh_put + MCID) instead.
2. Should lazymesh expose its toolset to *other agents* as a stdio MCP server (`claw mcp serve` pattern), or is the existing macula procedure-advertisement (`mesh_serve`) already that surface? If the latter, is the missing piece just a session-reference convention over mesh calls?
3. claw has no settle/kill protocol — the process either exits or the next invocation re-reads the JSONL. For a long-lived mesh agent, do we need an explicit "turn boundary"/acknowledgement concept, and if so where does it live (room envelope vs. session file)?
4. The borrow-list item 2 (JSON envelope contract) presumes lazymesh commands are subprocess-style. If lazymesh is a single long-lived Go process, the equivalent is an RPC response schema — worth pinning in a contract test like `output_format_contract.rs`?
