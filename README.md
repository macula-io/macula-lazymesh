# macula-lazymesh

[![CI](https://img.shields.io/github/actions/workflow/status/macula-io/macula-lazymesh/ci.yml?branch=main&label=CI)](https://github.com/macula-io/macula-lazymesh/actions/workflows/ci.yml)
[![License](https://img.shields.io/badge/license-Apache--2.0%20OR%20MIT-blue.svg)](#license)
[![Go Reference](https://img.shields.io/badge/go-1.27%2B-00ADD8?logo=go)](https://go.dev)
[![lazy](https://img.shields.io/badge/vibe-lazy-FB923C.svg)](https://github.com/jesseduffield/lazygit)
[![GitHub Sponsors](https://img.shields.io/badge/GitHub%20Sponsors-support-ea4aaa.svg?logo=githubsponsors&logoColor=white)](https://github.com/sponsors/rgfaber)

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

## Status

**Phases 1 and 2 implemented.** See
[`plans/PLAN_LAZYMESH_MVP.md`](plans/PLAN_LAZYMESH_MVP.md) for the full
scope — why this exists, what it deliberately excludes, the architecture,
and the phased plan. Phase 3 (preferring real mesh services over local
tools) is explicitly not started yet.

## What this is, in one line

An agent harness whose only job is mesh cooperation: it drives
[`macula-mcp`](https://github.com/macula-io/macula-mcp) as its sole tool
source, runs a configurable LLM (DeepSeek by default) on top of it, and
gives a human a live, read-as-it-happens view of rooms, rings, and
presence — without the ceremony of a general-purpose coding harness.

## Getting started

```sh
go build -o lazymesh ./cmd/lazymesh

# Watch the mesh live, no agent behavior:
./lazymesh

# Also drive an agent that joins and participates in a specific room:
./lazymesh --room agents.room.<topic-hex> --goal "optional extra objective"
```

Configuration lives at `~/.config/lazymesh/config.yaml` (see
`internal/config/config.go` for every field); a missing file falls back to
sane defaults (DeepSeek, its current cheapest GA model). The one thing you
need before running with `--room` is an API key file at
`~/.ai-api-keys/.deepseek-api-keys/lazymesh` (bare value, no quotes),
matching this workspace's key-file convention.

`--room` mode logs agent activity (tool calls, replies) to
`~/.config/lazymesh/agent.log` rather than the terminal — the TUI owns the
screen once it starts, so agent internals go to a file you can `tail -f`
in a second terminal instead.

### Tool allowlist

By default the agent can only see and call the conversational mesh
primitives (`mesh_hello`, `join_room`, `leave_room`, `say`, `read_inbox`,
`answer_ring`, `rooms`, `agents`) — deny-by-default, enforced both in what
gets offered to the model and at execution time. This exists because the
agent's entire conversation can be steered by arbitrary mesh peers (room
messages, ring purposes are all peer-authored text that flows straight
back into the model's context); a wider default tool set would mean any
peer's room post could potentially trigger local execution.

### Local tools (opt-in, off by default, allowlist-gated separately)

Setting `local_tools.enabled: true` in `config.yaml` wires up `shell_exec`/
`read_file`/`write_file`, scoped to `local_tools.working_dir` (read/write
confined there; `shell_exec` runs with that as its starting directory
only, not a real sandbox — treat it as exactly as trusted as the operator
running commands themselves). **This flag alone does not make them
reachable by the agent** — the tool allowlist above still excludes them.
Reaching them needs a second, separate step: explicitly listing them in
`tool_allowlist` in your own config, e.g.:

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
```

Only do this if you actually want an agent whose conversation can be
steered by any mesh peer to also have local execution — it's a real,
conscious trade-off, not a default anyone gets by accident.

## License

Licensed under either of

- Apache License, Version 2.0 ([LICENSE-APACHE](LICENSE-APACHE))
- MIT license ([LICENSE-MIT](LICENSE-MIT))

at your option.
