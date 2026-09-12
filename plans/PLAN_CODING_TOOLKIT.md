# lazymesh — Coding Toolkit Plan

**Status:** Planning — awaiting operator decisions (open questions at the end)
**Created:** 2026-09-13
**Last Updated:** 2026-09-13

## End goal

> This plan exists so an operator can point lazymesh at a repository and
> have it behave like a real coding agent — read, search, edit, and run
> code — with the same per-tool approval discipline it already applies to
> mesh tools, without giving up its mesh-resident identity.

**Classification:** BUILD (tool plumbing and policy defaults — no claim
about the world to gate).

## Current state (evidence-based, 2026-09-13)

The tool *architecture* is already a coding-agent architecture. What's
missing is the tool list and the policy defaults.

- **`agent.ToolSource`** (`internal/agent/agent.go:17`) is the interface:
  advertise tools + execute by name. Three implementations exist and are
  chained in `buildToolSource` (`cmd/lazymesh/main.go:551-578`):
  `mcpclient.Client` (macula-mcp), `webfetch` (SSRF-guarded, always
  wired, allowlist-gated), `meshservices.Source`, and — off by default —
  `localtools.Source`.
- **`internal/localtools`** (`localtools.go`) already ships
  `shell_exec`, `read_file`, `write_file`: `shell_exec` runs `sh -c` in
  the configured `WorkingDir` (default `~/.config/lazymesh/workspace`);
  the two file tools are confined to it by canonical-path validation
  (symlink-resolved, `localtools.go:189-271`). Honest limitation, stated
  in the package doc: `shell_exec` is NOT a jail — it can `cd` anywhere
  the operator's account can reach.
- **Permissions already exist in two layers**: the deny-by-default
  allowlist (`internal/agent/allowlist.go:70-79`, override via
  `tool_allowlist` / `tool_allowlist_extends`) and per-call approval via
  `tool_asklist` (config) + approve-all mode, surfaced as the TUI
  approval popup and control-socket `approval_request` events.
- **`mcpclient.Spawn`** hardcodes macula-mcp (`mcpclient.go:34-40`) —
  there is no way to attach a second MCP server today.

What lazymesh lacks against opencode/Claude Code: `edit_file`, `glob`,
`grep`, `ls`, git as a first-class tool, and subagents.

## Phases

### Phase A — First-class coding tools in localtools (small, mostly additive)

Each tool is a `ListTools` entry + a `CallToolRaw` case + tests, all
reusing the existing WorkingDir confinement and the existing approval
stack. No new plumbing.

- [ ] **`edit_file`** — `path`, `old_string`, `new_string` (opencode's
      shape): exact-match replace with a must-be-unique old_string,
      atomic write via temp file + rename. This is THE tool that makes
      agents reliably edit code without `shell_exec` sed/vim abuse.
      ~150 lines + tests.
- [ ] **`glob`** — pattern (`**/*.go`) → matching file list, capped.
      ~60 lines.
- [ ] **`grep`** — regex search over the sandbox with optional context
      lines, file-name filter, output cap (1 MiB, same posture as
      `readFile`). ~120 lines.
- [ ] **`ls`** — directory listing (names + sizes + kind), single level.
      ~50 lines.
- [ ] **git stays via `shell_exec` for v1** — no `git` tool; the
      `edit_file`/`glob`/`grep` trio removes the *editing* need for the
      shell, and running git is exactly what `shell_exec` is for.
- [ ] Optional **`run`** alias/helper — deferred; `shell_exec` covers
      tests and builds. Noted so it isn't accidentally re-planned.

### Phase B — MCP server list (medium)

- [ ] Generalize `mcpclient.Spawn` from "spawn macula-mcp" to "spawn
      the servers named in config" (`mcp_servers:` list, macula-mcp
      default entry), each with its own env allowlist. A separate
      `ToolSource` per server, merged in `buildToolSource`. This is how
      browser/filesystem MCP servers plug in without touching agent code.

### Phase C — Policy defaults (the real product work)

- [ ] Decide and ship the default allowlist/asklist split when
      `LocalTools.Enabled` (see open questions): read-only tools
      (`read_file`, `glob`, `grep`, `ls`) auto-run by default;
      `write_file`/`edit_file`/`shell_exec` ask by default (approve-all
      mode already exists for turning asks off).
- [ ] `shell_exec` sandboxing decision (open question 2): either keep
      the documented trust model, or add an optional `sandbox_command:`
      config (bwrap/firejail) wrapping shell execution.
- [ ] Tool-result size policy: grep/shell output caps feed the existing
      token-budget machinery (`internal/agent`, "[budget]" lines) —
      verify one MiB cap is compatible, lower if needed.

### Phase D — Operation modes (BUILD / ASK / PLAN), Claude Code parity

The input bar gets a mode badge cycled with Shift+Tab (arrives intact
through termkeys: `CSI 9;2u` → `\x1b[Z` → `KeyShiftTab`), mirroring
Claude Code's three:

- [ ] **BUILD** (default): the current behavior — allowlist +
      asklist exactly as configured.
- [ ] **ASK** (auto-accept): the existing ask handler answers yes
      automatically for the session; toggled, not config.
- [ ] **PLAN**: a session-scoped read-only allowlist (read_file,
      glob, grep, ls, web_fetch, all mesh reads) plus a system-prompt
      hint ("you are in plan mode: research and present a plan; do not
      modify anything"). Mutating tool calls are refused by the
      allowlist gate, which already runs at both list and execute time
      (`internal/agent/allowlist.go:103-121`) — no new enforcement
      code. v1 does NOT do Claude Code's "show the refused mutation as
      a suggestion that exits plan mode on accept"; that is a follow-up.
- [ ] Badge rendering: mode word in the chatbox border or status
      strip; the mode is model state in `internal/tui/model.go`,
      threaded to the session's allowlist + ask function
      (`internal/sessionhost`, `internal/agent`).

Scope note: modes are orthogonal to the coding toolkit — plan mode is
useful for pure mesh work too. They land here because the trigger was
PromptEditor parity, not because they depend on Phase A.

### Phase E — Subagents (out of scope of this plan)

`task`-style subagents are the one genuinely hard item (fresh context,
result-only return channel, supervision). Deferred to a dedicated plan;
this plan deliberately does NOT sketch them.

## Files to Create/Modify

| File | Purpose | Status |
|------|---------|--------|
| `internal/localtools/localtools.go` | add `edit_file`, `glob`, `grep`, `ls` | Pending |
| `internal/localtools/*_test.go` | per-tool tests incl. confinement | Pending |
| `internal/mcpclient/mcpclient.go` | server-list spawn (Phase B) | Pending |
| `internal/config/config.go` | `mcp_servers:` list; default ask/allow split | Pending |
| `internal/agent/allowlist.go` | default allowlist entries for read-only tools | Pending |
| `cmd/lazymesh/main.go` | buildToolSource merges per-server sources | Pending |
| `internal/tui/model.go` | mode state, Shift+Tab cycle, badge (Phase D) | Pending |
| `internal/sessionhost/`, `internal/agent/ask.go` | mode-aware ask + read-only allowlist (Phase D) | Pending |
| `plans/PLAN_CODING_TOOLKIT.md` | this plan | Done |

## Success Criteria

- [ ] `edit_file`/`glob`/`grep`/`ls` work inside the configured
      WorkingDir; every escape attempt fails the canonical-path check
      (tests, not just docs).
- [ ] Read-only coding tools run without prompts under the default
      config; write/exec tools prompt (and approve-all suppresses them,
      exactly like mesh tools today).
- [ ] An agent asked to "find where X is defined and fix the typo" can
      complete it with zero `shell_exec` calls.
- [ ] All existing suites still green (22 packages) and the localtools
      opt-out posture is preserved (Disabled by default unless the
      operator enables `LocalTools.Enabled`).

## Open Questions (operator decisions needed before Phase A lands)

1. **Default posture**: keep coding tools opt-in via `LocalTools.Enabled`
   (status quo, recommended), or make the read-only quartet on by
   default?
2. **`shell_exec` jail**: accept the documented trust model (status quo,
   simplest) or wire optional `bwrap`/`firejail` via config?
3. **`edit_file` shape**: `old_string`/`new_string` exact replace
   (opencode style, recommended) vs unified-diff patch?
4. **Approval granularity**: per-tool ask (asklist, status quo) is
   enough for v1 — confirm no per-command pattern matching (e.g. "ask
   before any `rm`") is wanted now.
