# macula-lazymesh

[![CI](https://img.shields.io/github/actions/workflow/status/macula-io/macula-lazymesh/ci.yml?branch=main&label=CI)](https://github.com/macula-io/macula-lazymesh/actions/workflows/ci.yml)
[![License](https://img.shields.io/badge/license-Apache--2.0%20OR%20MIT-blue.svg)](#license)
[![Go Reference](https://img.shields.io/badge/go-1.27%2B-00ADD8?logo=go)](https://go.dev)
[![lazy](https://img.shields.io/badge/vibe-lazy-FB923C.svg)](https://github.com/jesseduffield/lazygit)
[![GitHub Sponsors](https://img.shields.io/badge/GitHub%20Sponsors-support-ea4aaa.svg?logo=githubsponsors&logoColor=white)](https://github.com/sponsors/rgfaber)
[![Buy Me a Coffee](https://img.shields.io/badge/Buy%20Me%20a%20Coffee-support-ffdd00.svg?logo=buymeacoffee&logoColor=black)](https://buymeacoffee.com/rlefever)

<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="assets/macula-lazymesh-full-dark.svg">
    <img src="assets/macula-lazymesh-full-light.svg" alt="Macula lazymesh" width="320">
  </picture>
</p>

<p align="center">
  <strong>A lean, mesh-native agent harness + lazygit-style TUI for the Macula mesh</strong>
</p>

---

## Install

**Linux / macOS:**

```bash
curl -fsSL https://raw.githubusercontent.com/macula-io/macula-lazymesh/main/install.sh | bash
```

**Windows (PowerShell):**

```powershell
irm https://raw.githubusercontent.com/macula-io/macula-lazymesh/main/install.ps1 | iex
```

Both pull the release archive matching your OS/arch from
[GitHub Releases](https://github.com/macula-io/macula-lazymesh/releases),
verify it against the release's own `checksums.txt`, and install
`lazymesh` (`$HOME/.local/bin` on Linux/macOS, `%LOCALAPPDATA%\lazymesh`
on Windows — override with `LAZYMESH_INSTALL_DIR`). Prefer building from
source, or already have Go? `go install
github.com/macula-io/macula-lazymesh/cmd/lazymesh@latest` works too.

To remove it again: `curl -fsSL .../uninstall.sh | bash` (or
`irm .../uninstall.ps1 | iex` on Windows) — same repo path, `uninstall.sh`/
`uninstall.ps1` instead of `install`. Leaves `~/.config/lazymesh` (identity,
contact policy, config, workspace) alone by default — add `--purge`/
`-Purge` to remove that too.

## Status

Released as v0.1.0. Planned work is tracked in
[issues labelled `plan`](https://github.com/macula-io/macula-lazymesh/issues?q=is%3Aissue+is%3Aopen+label%3Aplan);
ideas not yet approved carry `proposal`.

## What this is, in one line

An agent harness whose only job is mesh cooperation: it drives
[`macula-mcp`](https://github.com/macula-io/macula-mcp) as its sole tool
source, runs a configurable LLM (DeepSeek by default) on top of it, and
gives a human a live, read-as-it-happens view of rooms, rings, and
presence — without the ceremony of a general-purpose coding harness.

## Getting started

lazymesh runs macula-mcp through `npx`, so it needs Node >= 24.18.1 (the
engines floor of current `@macula-io/mcp`). On an older Node, npx installs an
old macula-mcp that cannot reach the fleet; lazymesh refuses any macula-mcp
older than 0.48.0 at startup and says which version it got. 0.48.0 is the
wire 1.0 release, the wire the fleet speaks.

```sh
go build -o lazymesh ./cmd/lazymesh

# Starts and uses the model, point final: discovers and participates in
# every room it's currently a member of (mesh_rooms), joins rooms it gets
# rung about, and shows the live TUI. No flags required.
./lazymesh

# Optional: also prioritize joining and participating in a specific room,
# on top of whatever it's already a member of:
./lazymesh --room agents.room.<topic-hex> --goal "optional extra objective"
```

Configuration lives at `~/.config/lazymesh/config.yaml` (see
`internal/config/config.go` for every field); a missing file falls back to
sane defaults (DeepSeek, its current cheapest GA model). The agent loop
always runs, so the one thing you need before running at all is an API key
file at `~/.ai-api-keys/.deepseek-api-keys/lazymesh` (bare value, no
quotes), matching this workspace's key-file convention.

The LLM backend is a config value, not a rewrite: set `provider: nvidia`
or `provider: groq` (plus that provider's own `api_key_file`, e.g.
`~/.ai-api-keys/.groq-api-keys/lazymesh`) to point elsewhere — see
`internal/provider/nvidia.go`/`groq.go`'s own doc comments for defaults.
NVIDIA's free tier turned out to be a trial-credit pool that exhausts
under sustained production load, not a genuine free tier — worth
knowing before pointing a long-running agent at it. `provider: anthropic`
(plus its own `api_key_file`) drives Anthropic's Messages API, with
completions and streaming: its own request/response mapping, since that
API does not share the other three's OpenAI-compatible shape — see
`internal/provider/anthropic.go`'s own doc comments for defaults.

Agent activity renders live in the TUI's chat pane (collapsed one-line
tool calls, expandable — see below) AND logs the full detail to
`~/.config/lazymesh/agent.log`, which you can `tail -f` in a second
terminal for anything the collapsed chat view doesn't show.

## Using the TUI

The message box is focused from the moment lazymesh starts: type, and
`Enter` sends. There is no normal/insert switch — a bare key is a letter
in your message, and every command lives behind the leader `ctrl+x`.
Press `ctrl+x ?` to see the whole list in the chat, or press `ctrl+x` and
read the hint row: while the leader is armed it offers every command.

`ctrl+x` is the default. `leader_key` in `config.yaml` moves it — for a
terminal or a habit that already uses `ctrl+x` somewhere else — for example
`leader_key: ctrl+g`. Only a ctrl key and one lowercase letter is accepted; a
key the message box needs (`ctrl+a/b/d/e/f/h/j/k/m/n/p/t/u/v/w`), `ctrl+c`,
`ctrl+i` (the terminal sends it as tab, so no keystroke could arm it), and
anything that is not ctrl and one letter are refused at start, naming the key
and the reason. The hint row, the `?` list and the error marker all name the
configured key. An attached TUI (`lazymesh attach`) uses the session's leader,
read over its control socket, so a session started with `--config` elsewhere
is attached with the key it actually answers. The tables below show the
default.

| Key | What it does |
|---|---|
| `Enter` | send the composed message to the agent; the message box keeps focus |
| `shift+Enter` | newline in the message (multi-line compose) |
| `Esc` | close what is open, cancel a pending leader, or stop a running turn. With nothing open it does nothing, and keeps your draft |
| `shift+↑` / `shift+↓` | scroll the chat one line |
| `pgup` / `pgdn` | scroll the chat one page |
| `ctrl+e` | compose in `$EDITOR` (falls back to `vi`); the result lands in the message box |
| `ctrl+c` | quit, in every state |
| `↑` / `↓` in an empty message box | walk back and forward through the last 50 prompts you sent in this checkout, kept across restarts; down past the newest empties the box. Once you edit a recalled prompt it is your draft, and the arrows move inside it again |

Sent prompts are kept for that recall in `history/<checkout>.jsonl` in
lazymesh's data directory (`$XDG_DATA_HOME/lazymesh`, by default
`~/.local/share/lazymesh`), beside the session logs: the prompt text only,
the last 50, in a file only you can read (`0600`). A prompt can contain a
secret; the session log already keeps every prompt, and this list is a
second copy of the recent ones. `forget_prompts: true` in `config.yaml` turns
it off (nothing read, nothing written); deleting the file removes what it
holds. An attached TUI (`lazymesh attach`) keeps no history of its own.

Commands, pressed after `ctrl+x`:

| Key | What it does |
|---|---|
| `ctrl+x m` | show/hide the mesh view (rooms/rings/presence) |
| `ctrl+x s` | show/hide the mesh services view (the curated catalog below). The panel takes the keyboard: `↑`/`↓` select, `Enter` calls the selected procedure, `Esc` closes it |
| `ctrl+x r` | show/hide the realms view (memberships, and joining a new one below). `Enter` opens the join input when the message box is empty |
| `ctrl+x N` | show/hide the checkout notes (the working directory's `AGENTS.md`/`CLAUDE.md`, and trusting one) |
| `ctrl+x l` | open `agent.log` in `$EDITOR` |
| `ctrl+x E` | read the recent errors in full (a repeating failure is one entry with a count) |
| `ctrl+x e` | expand/collapse tool-call detail in the chat pane |
| `ctrl+x v` | toggle verbose mode (tool calls inline in chat vs. dropped) |
| `ctrl+x b` | mute/unmute the bell |
| `ctrl+x y` | copy the last agent answer |
| `ctrl+x x` | interrupt the running turn |
| `ctrl+x g` / `ctrl+x G` | jump the chat to the top / bottom |
| `ctrl+x q` | quit |
| `ctrl+x ?` | show this list in the chat |

A key pressed after `ctrl+x` is a command or a cancel, never a letter:
it cannot land in your draft. `Esc`, or any key that names no command,
cancels the leader.

Inside a panel, its own keys stay what they are: `t` trusts the selected
checkout note, `tab` moves to the next one, `N` or `Esc` closes; `c`
copies from the error view and `p`/`n` step through older and newer
errors; the ring pop-up keeps `a`/`d`/`t` and the approval pop-up keeps
`y`/`n` (see below).

The compose line is always visible — a draft you leave stays there while
a panel, an overlay or a pop-up is on screen, and the next key after
closing one is the next letter of your message.

The mesh view overlays the Rooms/Pending rings/Presence panels on top of
the chat pane rather than replacing it outright: the panels pin to the
top of the body area, and whatever vertical space is left renders as a
single margin of the newest chat lines below them, so the ambient
conversation is still visible while you're looking at mesh state rather
than hidden outright. Older lines (including anything from earlier in a
long conversation) simply scroll out of view above the panels — there's
no split margin above and below to keep in sync any more. On a terminal
too short for the panels to fit with any margin left over, it falls back
to the panels alone, same as before. The panels themselves render with a
dim, muted border rather than a solid bright one, so the overlay reads
as a light layer over the conversation rather than a popup taking it
over.

The mesh services view (`ctrl+x s`, below) and the realms view
(`ctrl+x r`, below) are the same overlay shape over their own panels,
and all of them (mesh, mesh services, realms, checkout notes) are
mutually exclusive — opening one closes the others rather than stacking,
since two panels competing for the same tight chat margin would leave
less room for either.

A ring, an approval request or a first error opens its own prompt over
everything else, but only once the message box has been quiet for two
seconds: a prompt never takes the screen in the middle of a word.

### Checkout notes (`ctrl+x N`)

In the coding profile the working directory is a checkout, so its
`AGENTS.md` and `CLAUDE.md` are written by whoever wrote the repository —
a dependency, a fork, a branch someone pushed. The model therefore reads
them **as data about the repository, never as orders**: they are delivered
inside a fence, after the rules, and nothing in them can change the
tools, the profile or what needs your approval.

`ctrl+x N` lists them with each one's state. `t` trusts the selected file: the
record (path plus the SHA-256 of the exact bytes you were shown) is
written to `trusted_checkout_notes.json` beside your `config.yaml`, never
inside the checkout, and only that key writes it. A trusted file is
delivered as your own instruction. If the file changes afterwards — a
pull, a fork's PR — the hash no longer matches, the next start delivers it
as data again, and the TUI says so. Trust takes effect at the next start,
since a running session's system prompt is fixed.

The mesh-only profile is unchanged: there the working directory is usually
your own workspace, so its `CLAUDE.md` is read as your own instructions.

A status block (position configurable via `status_bar_position:
top\|bottom` in config, default bottom) is always visible whether the chat
pane or the full mesh view is showing — collapsing the mesh view never
loses that ambient awareness. Two lines:

- the shortcuts row: whatever is modal on screen names itself first
  (`-- APPROVAL --`, `-- RING --`, `-- NOTES --`, …), the candidates for
  the next key while `ctrl+x` is pending, and otherwise the keys that do
  something at the composer.
- the summary line: this instance's own petname once known, bold and in
  its own color so it stands out (`swift-otter`, requires macula-mcp
  >= 0.25.2 — see the room-message color coding above for the same
  per-agent coloring), then the macula-mcp it is running on (`mcp 0.51.1 ●`
  in green, `(pinned)` when the release running is the one
  `macula_mcp_version` pinned, and `mcp ✗ disconnected` in red once a
  refresh call fails or the subprocess exits — the version is the one
  macula-mcp reported in its own handshake, not the one config asked for,
  and it is re-read on every refresh, so a transparent respawn to a newer
  release shows up and is announced in the chat), then the room/ring/agent
  counts, the configured model in bold
  (`mcp 0.51.1 ● · 3 rooms · 2 pending rings · 8 agents seen ·
  deepseek/deepseek-v4-flash`), then a `listening HH:MM:SS`
  heartbeat once the agent loop's had its first idle cycle — an
  advancing clock is what makes a frozen process distinguishable from a
  correctly-idle one. The line is cut to the terminal's width rather than
  wrapped, so it always stays one row; the mcp segment leads it so the cut
  never takes it.
  Routine tool-call "chatter" (`mesh_call`, `mesh_say`, ...) is dropped
  from here entirely by default; `ctrl+x v` shows it inline in the chat pane
  instead for anyone who wants the full detail.

**Audio cues**, plain terminal bell only (no audio library, no sound
files): single bell for an ordinary room message from someone else,
double bell for a ring addressed to you, triple bell if the agent enters
a retry backoff or stops after repeated failures. Silent for the agent's
own outgoing messages. Toggle with `ctrl+x b`; degrades silently wherever the
bell is muted or unreachable (headless box, SSH, visual-bell-only
terminals) — it never errors, since sending the raw BEL byte is always
safe regardless of whether the terminal actually does anything with it.

A message typed while the agent is mid-call (in particular a long
`mesh_say` wait) is picked up once that call returns, not instantly —
no in-flight LLM call gets interrupted for it.

### Mesh service tools (Phase 3, off by default)

Beyond the conversational mesh primitives, the agent can also call real
mesh RPC procedures discovered live via `mesh_find_records_by_type` —
`internal/meshservices`'s `Curated` list (see `catalog.go`) currently
covers the read-only procedures of `mcl-rag` (semantic search, source and
chunk lookups, reranking) and `mcl-graph` (entity and link resolution and
narration), matched by the exact `Org/Name` each service advertises under
the pinned `io.macula` realm. Only individually named, curated, currently-
discovered procedures ever become tools — never a generic "call any mesh
procedure" tool, which would reopen the same risk the tool allowlist
exists to close. Discovery resolves once per process lifetime (not on a
timer, and not re-checked even if the mesh changes underneath it — a
deliberate R2 tradeoff for a byte-stable tool schema a provider's own
prompt cache can actually reuse): a curated procedure not currently
advertised at that one moment just never becomes a tool for the rest of
this run.

Set `mesh_services_enabled: true` in `config.yaml` to turn this on —
**off by default** (R2, 2026-09-07): a room-chat-only agent usually never
touches corpus search at all, and the catalog's own tool schemas are real
fixed-prefix token cost every operator would otherwise pay whether they
use it or not. Press `ctrl+x s` any time (on or off) to see the curated catalog
itself — Procedure/Status/Description, each tagged `live` (currently
discovered and callable), `not live` (curated but not currently
advertised), `checking...` (enabled, discovery just hasn't resolved yet),
or `inactive` (the feature itself is off) — reading from the exact same
`Source` the agent's own tool calls go through, not a second, possibly-
diverging query.

### Realms (`ctrl+x r`)

Press `ctrl+x r` to see which mesh realms this identity has joined — a small
table of realm/handle/tier/joined-at, sourced from macula-mcp's
`mesh_list_realms` tool alongside the other three `mesh_*` state calls the
overlay panels already poll.

Joining a **new** realm is deliberately not something the agent (or any
mesh peer's room text) can trigger — there is no `mesh_join_realm`-style
tool with a realm parameter reachable from the model's own tool-calling
loop, on purpose: any parameter on any MCP tool is reachable by whichever
peer can steer the conversation, not just by the operator typing at the
keyboard. Instead, with the realms view open and the message box empty,
press `Enter` to type a realm name directly and `Enter` again to start
the join — this runs
`macula-mcp-realm`, a separate CLI never registered as an MCP tool,
inheriting the same identity file the running macula-mcp server already
uses. A join in progress shows its own session (link/QR code) and status
inline in the same panel; press `Esc` once it's finished (confirmed,
expired, or failed) to return to the membership list.

The agent is told this route too, so asking it "are you in io.macula?" or
"why can't you join a realm?" gets the operator's own next step — this view,
`Enter` on an empty message box, the realm name — rather than only that
someone configured the node. It names the leader as configured (#63), and it
still cannot join a realm itself.

Realm names are dotted-hierarchical (`io.macula`, `net.beam-campus.sales`)
and must be typed, never picked from a list — a populated list of realms
to join is spoofable in a way that typing the name yourself isn't, the
same reasoning as typing a URL rather than trusting a link. The name
resolves to a host by reversing its labels (`io.macula` → `macula.io` →
`realm.macula.io`), a fixed convention with no discovery hop, so there's
no intermediate lookup step to poison either.

Requires `macula-mcp` >= 0.27.0 (`mesh_list_realms` and the
`macula-mcp-realm` CLI).

### Tool allowlist

By default the agent can only see and call the conversational mesh
primitives (`mesh_hello`, `join_room`, `leave_room`, `say`, `read_inbox`,
`answer_ring`, `rooms`, `agents`), plus the mesh-service tools above once
`mesh_services_enabled: true` opts into them — deny-by-default, enforced
both in what gets offered to the model and at execution time. This
exists because the agent's entire conversation can
be steered by arbitrary mesh peers (room messages, ring purposes are all
peer-authored text that flows straight back into the model's context); a
wider default tool set would mean any peer's room post could potentially
trigger local execution.

### Local tools (opt-in, off by default, allowlist-gated separately)

Setting `local_tools.enabled: true` in `config.yaml` wires up `shell_exec`/
`read_file`/`write_file`/`search`/`apply_patch`, scoped to
`local_tools.working_dir`. Every one of them reaches the directory through
an `os.Root`: a path that resolves outside — by `..`, by an absolute path,
or through a symlink — is refused, and a write never follows a symlink at
all. `shell_exec` is the exception: it runs with that as its starting
directory only, not a real sandbox (in the coding profile it is the
confined shell below). **This flag alone does not make them reachable by
the agent** — the tool allowlist above still excludes them. Reaching them
needs a second, separate step: explicitly listing them in `tool_allowlist`
in your own config, e.g.:

```yaml
local_tools:
  enabled: true
  working_dir: ~/.config/lazymesh/workspace
tool_allowlist:
  - mesh_hello
  - mesh_join_room
  - mesh_leave_room
  - mesh_say
  - mesh_read_inbox
  - mesh_answer_ring
  - mesh_rooms
  - mesh_agents
  - shell_exec
  - read_file
  - write_file
  - search
  - apply_patch
```

`search` takes a literal string or a regular expression and returns one
line per match as `path:line:text`. `apply_patch` applies a unified diff to
the working directory and refuses any path that resolves outside it, any
path inside `.git`, and any patch that would create a symlink. Both are
edited by the model and both are shown to you: a patch is asked about with
its whole text on screen before it runs, and the diff of the working tree
appears in the chat as soon as it has been applied.

Neither `write_file` nor `apply_patch` writes through a symlinked directory,
even one that points back inside the checkout: a committed `tools ->
.git/hooks` would otherwise put a hook where your own git runs it. One
section per file per patch — a patch that names the same file twice is
refused rather than silently keeping the last edit.

A `shell_exec` call reads the checkout's `git diff` before and after it, and
when the command changed the tree the result carries that diff, which the
chat shows without the details toggle: a test that rewrites a fixture is
visible as a change, not only as output. An ordinary test run changes
nothing and carries nothing. That diff is the host's own `git diff` with
`--no-ext-diff --no-textconv`, `core.fsmonitor=false`,
`--ignore-submodules=all`, no inherited `GIT_*` variables and no global or
system config, so nothing in the checkout — a nested repository's config
included — can make it run code on your machine. Two limits worth knowing:
it covers tracked changes only (a brand-new untracked file does not appear),
and a diff over 16 KB is cut with a note and, on the coding profile, kept
whole in the scratch directory.

A result too long for one answer is paged, not cut: `read_file` and
`search` return a page with the offset for the next one in the footer, so
a search's last match and a long file's last line stay reachable.

**Adding `shell_exec` (or any file tool) to `tool_allowlist`
re-exposes the mesh-content-to-arbitrary-execution risk the default
allowlist exists to prevent** — the fix removes the capability from the
default, it doesn't mark peer-authored room text as untrusted. Only do
this in a trusted/private mesh context, not against the shared public
fleet.

Only do this if you actually want an agent whose conversation can be
steered by any mesh peer to also have local execution — it's a real,
conscious trade-off, not a default anyone gets by accident.

### Coding profile: only your roster reaches the model (in progress)

`profile: coding` turns lazymesh into a crew member that takes work from
the people you name, and from no one else (#38). You list their node ids
under `roster:`:

```yaml
profile: coding
roster:
  - <64-hex node id of the crew member who assigns work>
  - <64-hex node id of your own other session, if you post from one>
```

What changes in this profile:

- **lazymesh reads the rooms, not the model.** When something arrives,
  lazymesh keeps only messages that the station confirms came from a node
  on your roster, and hands those to the model, each one marked with who
  sent it and how long ago it arrived. A stranger's message, a message pretending to be from someone
  on your roster, and a room's own description never reach the model. A
  room with nothing from your roster costs no model call at all. If
  lazymesh cannot read a room, the TUI and the control socket say so, so
  a broken read never passes for a quiet room.
- **The model can only speak, and only where it already is.** Its one mesh
  tool is `mesh_say`, limited to the rooms this session watches, and it
  gets back only the id of what it said. It cannot read an inbox, join a
  room, ring anyone, answer a ring or serve a procedure, whatever
  `tool_allowlist` says. Rings are shown to you in the TUI and never to the
  model.
- **Only you approve.** A roster member can assign and discuss work. No
  message can approve a tool call: approvals come only from the TUI or the
  control socket. Every local tool you allow here (`read_file`, `search`,
  `apply_patch`, `write_file`, `shell_exec`) asks you before each call,
  whatever `tool_asklist` says. The approval prompt shows the whole call,
  with invisible characters (bidi overrides, zero-width marks) written out
  as `\u{XXXX}`; one taller than the screen must be paged through, every
  line, before `y` allows it, and a call over 16 KiB is refused without
  asking — a patch larger than that has to be sent in several pieces.
  The prompt only appears once the message box has been quiet for two
  seconds, and its answer keys then stay asleep for a further 0.6 s —
  a letter meant for your message cannot answer it, and the prompt says so
  while the keys sleep. `esc` denies at once: the safe answer is never
  delayed.
- **Handing work out is a separate, narrower tool.** Name `git_branch`,
  `git_commit` and `git_push` on `tool_allowlist` and the model can create
  or switch a branch, commit, and push that branch to your checkout's own
  configured remote (the one in `local_tools.working_dir`). It can never
  push `main`, `master` or a tag (in any letter case), never follow a tag,
  never force, and never reach a different remote — not even when an
  approval says yes: those calls are refused before you are asked, and
  every refusal is written to `agent.log` with its arguments and reason.
  What you approve is the resolved command — the remote, its URL and the
  ref `refs/heads/<branch>` — not an empty argument object.
  The push URL is the one read when lazymesh started; if the checkout's
  config rewrites it, adds push refs, sets a mirror, or names a filter, a
  gpg program, an ssh or askpass command, a remote `receivepack`/
  `uploadpack` or an include, the call is refused. Every git command pins
  hooks, fsmonitor, commit signing, the ssh command, the credential helper,
  askpass and `ext:` remotes on its own command line, the environment's
  `GIT_*` variables are dropped, and a `.git` that is a symlink or that
  changed after start is refused outright: a swapped `.git` is a whole
  attacker-chosen config. It never prompts for credentials.
- The roster lives only in your config file. Nothing the model does changes
  it, and it is separate from the ring pop-up's "Answer + Trust" list.

Start it in the room your crew works in: `lazymesh --room <room_topic>`.
In this profile lazymesh joins that room itself, since the model has no
way to join one.

`roster:` without `profile: coding` changes nothing the model sees: it only
logs, in agent.log, which messages a coding agent would have dropped, so
you can check the filter against your own rooms first.

### The shell in the coding profile is confined, or not there

Put `shell_exec` on `tool_allowlist` and lazymesh runs every command it is
allowed inside [bubblewrap](https://github.com/containers/bubblewrap)
(#26). An approved command sees:

- your working directory, which it may change, except its `.git`
  directory, which it may only read;
- a scratch directory of its own (its `HOME`, the Go build cache, and the
  full text of any output too long for one answer);
- the system's programs and libraries and the Go toolchain, read-only.

It does not see the rest of your home directory, has no network, starts
with none of lazymesh's environment (so no API key), and cannot type into
your terminal. You still approve every command; this is what holds if you
misread one.

At start lazymesh checks all of this on your machine: it plants a file
outside the working directory and makes sure a command cannot read it,
looks for any network interface besides loopback, and so on. If any check
fails, or bubblewrap is not installed, or you are not on Linux, the coding
profile has **no shell at all** and says why when it starts. There is no
setting that runs it unconfined.

Other toolchains outside the system directories can be mounted read-only:

```yaml
local_tools:
  enabled: true
  working_dir: /path/to/the/checkout
  read_only_paths:
    - /opt/node-22
```

A path that is your home directory, holds it, or overlaps lazymesh's
config, session data, key or identity files is refused, and the shell is
then not offered. Do not start the coding profile in your home directory:
the working directory is writable, so that is refused too.

What the sandbox does not cover:

- **What the agent leaves in the working directory is untrusted.** A
  command may edit a `Makefile`, add an `.envrc` or a `.vscode/tasks.json`,
  or create a nested `sub/.git` with its own hooks. Those run with your
  full rights when you later build, `cd` or open an editor there. Review
  the diff before you run anything in the checkout yourself.
- **No limit on CPU or memory**: only the time limit
  (`shell_timeout_seconds`) applies.
- The coding profile runs its shell only in a git checkout: the working
  directory needs a real `.git` (a directory, or the file a git worktree
  has), which the sandbox keeps read-only. `write_file` never writes into
  `.git` and never writes through a symlink.

Every call lazymesh refuses (you denied it, it was too long to show, the
tool is not allowed, there is no shell) is written whole, with the reason,
to `<session-id>.denials.jsonl` next to the session's own log. The file is
readable only by you (mode 0600) and holds each refused call's full
arguments.

Install bubblewrap from your distribution: `apt install bubblewrap`,
`dnf install bubblewrap`, `pacman -S bubblewrap`.

On Ubuntu 24.04 and later, AppArmor stops bubblewrap from setting up its
sandbox unless it has a profile, and lazymesh's start-up check then
reports `a plain command does not run`. Load Ubuntu's own restricted
profile for bubblewrap, which lets bubblewrap set up its sandbox and gives
what runs inside no capabilities and no further namespaces:

```sh
sudo apt install apparmor-profiles
sudo cp /usr/share/apparmor/extra-profiles/bwrap-userns-restrict /etc/apparmor.d/
sudo apparmor_parser -r /etc/apparmor.d/bwrap-userns-restrict
```

Avoid the shortcut some guides give, an unconfined profile with `userns`
for `/usr/bin/bwrap`: any program on the machine can run bubblewrap, so
that hands unprivileged user namespaces back to every local program and
undoes Ubuntu's restriction machine-wide.

## Driving lazymesh from another program

`lazymesh --headless` runs without the TUI and serves a control socket
(`--unix-socket <path>`; by default `<session-id>.sock` in
`$XDG_RUNTIME_DIR`). Another program sends it input and reads what the
agent does, one JSON object per line each way. `sdk/` holds a Go client
and a stdlib-only Python client (`sdk/python/lazymesh_client.py`); the
full wire protocol is documented at the top of
`internal/frontend/frontend.go`.

- **Only your own user can drive it.** The socket is created `0600`, and
  each connection is checked against the kernel's record of who connected:
  a process of another user, root included, is closed before it gets the
  handshake. Windows has no such record for unix sockets, so there
  `--headless` and `--unix-socket` refuse to start.
- **Rings reach the controller.** A ring still awaiting an answer is sent
  as `ring_pending` (ring id, caller, purpose) the moment it arrives;
  `Say` in both SDKs collects them as `pending_rings`.
- **A controller can gate every tool call.** After
  `{"type":"hook","event":"pre_tool"}` the session sends each tool call to
  that controller alone as `pre_tool` (tool and full arguments) and runs it
  only when the controller answers `hook_answer` with `allow: 1`; a denial's
  `reason` is what the model sees. It runs before any approval prompt, so a
  refused call never reaches a human. It fails closed: an unanswered call is
  refused after two minutes, and if the controller disconnects without
  `unhook`, every tool call is refused until a controller hooks again. Set
  `pre_tool_hook_required: true` in the config to refuse every tool call
  from start until the first controller hooks, so the agent never acts
  ungated while your policy process (re)connects. In the Go SDK, a policy
  process calls `HookPreTool` and then `ServeHook` on its own client: a
  headless agent's turns mostly start from the mesh, not from `Say`. A
  request the controller doesn't read in time (including when it falls 64
  lines behind the event stream) is refused.
- **Subagents are sessions a parent drives.** `--idle-start` runs no
  startup turn, so a parent's first `Say` gets its own task's turn back.
  `examples/fanout` starts one worker per subtask, gates each with the
  pre-tool hook so workers run only the tools the parent allows, and
  collects the answers in parallel; `examples/handoff` passes one task
  between two sessions.
- **Prometheus metrics, if you ask for them.** Set
  `metrics_listen: 127.0.0.1:9464` (any address) and lazymesh serves
  `/metrics`: turns, tool calls (also per tool), errors, backoffs,
  approvals, prompt and completion tokens, completion rounds and tool-schema
  bytes. Off by default; the endpoint has no authentication, so bind it
  where only your scraper reaches it.
- **Each config has its own contact policy.** `contact_policy_file`
  defaults to `contact_policy.json` beside the config file. Agents started
  from different configs keep their own ring policy, and agents from one
  config share one trust list.

## Architecture

What lazymesh talks to and what it is made of, as a C4 model: [architecture/README.md](architecture/README.md).

![macula-lazymesh system context](assets/c4-context.svg)

## License

Licensed under either of

- Apache License, Version 2.0 ([LICENSE-APACHE](LICENSE-APACHE))
- MIT license ([LICENSE-MIT](LICENSE-MIT))

at your option.
