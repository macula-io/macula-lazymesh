# Changelog

All notable changes to this project are documented here. Releases are cut by
goreleaser from a `v*` tag; this file records what each one changes.

## [Unreleased]

### Added

- **Git tool for the coding profile (#45, slice E of #38).** `git_branch`,
  `git_commit` and `git_push`, offered only in the coding profile and only
  when `tool_allowlist` names them, working in `local_tools.working_dir`.
  A push goes only to the checkout's own configured remote, read once at
  start, and never to `main`, `master` or a tag, never by force, with no
  refspec and no URL: those calls are refused on their arguments alone,
  before the operator is asked, whatever an approval would answer. Every
  call asks by construction, and the approval shows the remote, its URL and
  the exact ref, not an empty argument object. The push URL is the one
  resolved at start; a config that rewrites it, adds push refs, sets a
  mirror, or names a filter, a gpg program, an ssh or askpass command, a
  remote `receivepack`/`uploadpack` or an include is refused, and every git
  invocation pins `core.hooksPath`, `core.fsmonitor`, `commit.gpgSign`,
  `core.sshCommand`, `credential.helper`, `core.askPass` and
  `protocol.ext.allow` on its own command line, drops the environment's
  `GIT_*` variables, and refuses a `.git` that is a symlink or that changed
  since start. Every refusal is logged with its arguments and reason.

- **Coding profile with a roster gate (#38, slice A).** `profile: coding`
  plus `roster:` (node ids): lazymesh reads each room arrival itself and
  passes the model only messages the station attests came from a roster
  node, fenced with the sender named. The model's only mesh tool is
  `mesh_say`, limited to the rooms the session watches, with its reply wait
  removed and its result reduced to the message id; rings never reach the
  model. Every allowed local tool asks the operator before each call,
  whatever `tool_asklist` says. Delivered text is escaped so no spelling
  of the closing tag ends its fence, and each fence carries the message's
  age; a failed inbox read is shown in the TUI and on the control socket.
  The approval prompt writes bidi and zero-width characters out as
  `\u{XXXX}`, wraps by display width, and allows only after every line of
  the call has been on screen. A roster without the coding profile only
  logs what would be dropped.

- **The coding profile's shell is confined, or not offered (#26, #38
  slice C).** `shell_exec` runs inside bubblewrap: the working directory
  read-write (its `.git` read-only), a per-checkout scratch directory, the
  system and Go toolchain read-only, no network, a cleared environment, no
  controlling terminal. A self-test at start proves it on the machine; if
  it fails, bubblewrap is missing, or the system is not Linux, the profile
  has no shell. `local_tools.read_only_paths` mounts other toolchains and
  refuses home, its parents and lazymesh's private files. Output longer
  than one result is kept whole in the scratch directory and paged. Every
  refused tool call, in either profile, is recorded in
  `<session-id>.denials.jsonl` (mode 0600, full arguments). The shell runs
  only in a git checkout: `.git`, directory or worktree file, and a gitdir
  inside the checkout are read-only inside. `read_file` and `write_file`
  open files through `os.Root`; `write_file` refuses `.git` and any symlink
  at the final component. Long output goes to a directory the sandbox sees
  read-only; the sandbox's home and output are emptied at every start.

- **A checkout cannot give orders (#38, slice B).** In the coding profile
  the working directory's `AGENTS.md` and `CLAUDE.md` are delivered as the
  repository's notes — inside a `<checkout_notes>` fence, escaped so they
  cannot end it, after the rules — never as standing orders, and nothing in
  them changes the tools, the profile or what needs approval. `N` opens a
  panel listing them; `t` trusts the selected file, recording its path and
  the SHA-256 of the exact bytes shown in `trusted_checkout_notes.json`
  beside `config.yaml` (never in the checkout, and only that key writes
  it). Only a matching hash ranks as the operator's own instruction; a file
  that changes afterwards drops back to data at the next start, and the TUI
  says why. The mesh-only profile is unchanged.
- **Rings reach a headless controller.** A ring still awaiting an answer is
  sent on the control socket as `{"type":"ring_pending","ring_id",
  "from","purpose"}` the moment it arrives, not when the agent next idles.
  The Go SDK's `Turn.PendingRings` and the Python client's `pending_rings`
  collect the ones that arrive during a turn (#19).
- **Pre-tool hook on the control socket (G8).** A controller sends
  `{"type":"hook","event":"pre_tool"}` and then decides every tool call the
  session makes (`pre_tool` → `hook_answer`, with a reason on denial),
  ahead of the ask list. It fails closed on timeout, interrupt, or the
  controller disconnecting without `unhook`. One controller holds it at a
  time. `pre_tool_hook_required: true` refuses every tool call from start
  until the first controller hooks. Go SDK: `Client.HookPreTool`,
  `Client.ServeHook` (answers every turn's requests, not only `Say`'s) and
  `Client.Unhook` (#19).

### Changed

- **One keymap: the message box is always focused, commands behind `ctrl+x`
  (#62).** There is no normal/insert mode any more: a bare key is a letter in
  your message and `Enter` sends, so nothing has to be entered before typing
  and no word is lost to a stray command key. Every command moved behind the
  leader `ctrl+x` — `m` mesh view, `s` mesh services, `r` realms, `N` checkout
  notes, `l` the agent log, `E` recent errors, `e` tool detail, `v` verbose,
  `b` mute, `y` copy the last answer, `x` interrupt, `g`/`G` chat top and
  bottom, `q` quit, `?` the whole list. The hint row offers them all while the
  leader is armed, and a key pressed after `ctrl+x` is a command or a cancel,
  never a letter in the draft. `↑`/`↓` belong to the message box now: the chat
  scrolls with `shift+↑`/`shift+↓` a line and `pgup`/`pgdn` a page. `esc`
  closes what is open, cancels a pending leader or interrupts a running turn,
  and with nothing open does nothing at all. Inside a panel `enter` acts,
  replacing `i`. A ring, an approval request or a first error opens its prompt
  only after two seconds without a keystroke, so a prompt never takes the
  screen mid-word; the #59 guard that keeps a letter meant for a message from
  answering it is unchanged.

- **The macula-mcp floor is 0.48.0, the wire 1.0 release.** 0.48.0 is the
  first macula-mcp built on `@macula-io/ts` 0.31.0, which speaks wire 1.0
  and nothing older; the fleet has spoken wire 1.0 since the flag day of
  2026-10-19. A pre-1.0 macula-mcp is refused at the station's handshake
  anyway, so lazymesh now refuses it at startup and names both versions.

### Fixed

- **`lazymesh attach` can be interrupted (#49).** An attached TUI whose stdin
  is not a terminal — `timeout 5 lazymesh attach … < /dev/null`, a service
  stop, a supervisor — sometimes ignored SIGINT and SIGTERM and had to be
  SIGKILLed: bubbletea installs its own signal handler, and when attach's
  own shutdown won the race to end the event loop, bubbletea's handler was
  left blocked on an unbuffered channel that nothing read, with `Run`'s
  shutdown waiting for it. Attach now takes signal handling for itself and
  tells bubbletea not to, and it releases that handler the moment the first
  signal arrives: a second ctrl+c is then the terminal's own default action,
  so it ends a shutdown that will not finish instead of being swallowed by a
  handler whose reader has already returned. Quitting with `q` or ctrl+c from
  a real terminal was measured working, and is pinned by a new pty test
  alongside the signal case.
- **The approval prompt shows the whole call (#39).** It used to show the
  first 200 characters, so a long command could hide its tail. The prompt
  now wraps the full arguments, shows control characters as text, and
  allows only once the last line has been on screen; a call over 16 KiB is
  refused without asking.
- A controller disconnecting while events streamed could crash lazymesh
  (send on a closed channel): it now leaves the broadcast set before its
  queue closes.
- Go SDK: `Say` now holds the client's read lock as documented, so it no
  longer races `Query` or the hook calls; `Close` can end a blocked read.
- **Each cycle's usage line shows the tool-schema cost (G17).** The whole
  tool array is resent on every completion round, so `agent.log`'s
  `[usage]` line now says how many rounds the cycle ran, about how many
  tokens of tool schemas they resent, and that as a share of the cycle's
  prompt tokens. The status query carries the totals (`Rounds`,
  `ToolSchemaBytes`) (#19).
- **Prometheus metrics.** `metrics_listen: <addr>` serves `/metrics` in the
  Prometheus text format: the runtime counters (turns, tool calls and
  per-tool events, errors, backoffs, approvals) and the session's token,
  round and tool-schema totals. No client library; off unless configured
  (#19).
- **Subagent orchestration example (G15).** `examples/fanout` runs one
  headless worker per subtask in parallel, each gated by the pre-tool hook
  to the tools its parent allows. New flag `--idle-start`: no startup
  turn, the first turn is the first input, room, ring or wakeup (#19).
- README: "Driving lazymesh from another program".

### Fixed (examples)

- `examples/handoff` could print the startup turn's text instead of the
  handed task's: each session ran its startup turn first, and `Say`
  returned at that turn's settle point. It now starts sessions with
  `--idle-start`.

### Security

- **The control socket serves only controllers of the same user.** Each
  connection is checked against the kernel's record of who connected
  (SO_PEERCRED on Linux, LOCAL_PEERCRED on macOS); a peer of another uid,
  root's own processes included, is closed before the handshake. The default
  socket directory must be private to this user (owned by it, no group or
  other access), so another user cannot swap the socket a controller dials. Windows has no peer
  credentials on unix sockets, so `--socket` and `--headless` now refuse to
  start there instead of running unchecked (#19).
- **Agents started from different configs no longer share one contact
  policy file.** `contact_policy_file` now defaults to `contact_policy.json`
  beside the config file it was loaded from. Each agent writes its ring
  policy into that file at start, so with one shared file an agent set to
  `accept-everyone` opened a `do-not-disturb` agent to every caller. The
  default config keeps its old path, so an existing trust list stays put; an
  explicit `contact_policy_file` is honoured as before (#19).

## [0.1.0] - 2026-10-05

### Fixed

- **An old macula-mcp is refused at startup, by name.** On Node older than
  `@macula-io/mcp`'s engines floor (>= 24.18.1), npx resolved 0.16.0 without a
  word and lazymesh ran a macula-mcp that cannot reach the fleet (#17). The
  version macula-mcp reports in its MCP handshake must now be 0.38.0 or later
  (handshake v5, the fleet's wire), on every spawn and respawn; otherwise
  lazymesh stops, naming the version it got and pointing at `node --version`.

### Changed

- **The mesh service tools call the mcl-\* services**, the macula 12
  successors of the retired hecate ones: `hecate-rag.<name>` became
  `mcl-rag/<name>` and `hecate_graph.<name>` became `mcl-graph/<name>`. Tool
  names follow (`mesh_service_mcl_rag_answer_query`, and so on).
- Discovery matches an advertisement only by the exact `Org/Name` its
  provider advertised. The old catalog stripped a leading `_/` so dotted
  names matched; with `Org/Name` that would let an org-less record stand in
  for a real org's procedure.
- Results are returned exactly as the service sent them. The macula 12 wire
  carries text as text, so the hex-decoding pass is gone.
- Every tool description names the arguments its handler actually reads.
  `list_chunks_by_source` and `get_document_verbatim` take `source_path`
  (they said `source_id`), and `rerank_results` requires `query_id`,
  `query_text` and `hits`.

### Removed

- **Agora views removed: hecate-agora retired, no mcl successor.** Its four
  procedures (`search_posts`, `search_archive`, `get_posts_page`,
  `get_thread_by_post_id`) served a service that exists nowhere, so they
  could only fail.

### Not yet reachable

- Nothing here reaches the macula 12 fleet until macula-mcp, which
  lazymesh calls through, speaks the 12 handshake.
