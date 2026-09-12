# lazymesh — Mesh-Borne Team Coordination (Exploration)

**Status:** Exploration — no code yet
**Created:** 2026-09-13
**Last Updated:** 2026-09-13

## End goal

> This exploration exists so lazymesh can become the agent that turns the
> Macula mesh's existing coordination primitives — lanes, handoffs, shared
> memory, proven identity, capability advertisement — into first-class
> team features, giving it a unique position no other coding agent can
> copy without joining the mesh.

**Classification:** Exploration (research + sizing). The features below
are BUILDs once chosen; none is a scientific claim needing a gate.

## The paradigm

Other agents fake teams:

- Claude Code / opencode: subagents are ephemeral context slaves inside
  one process — no identity, no memory, no independent lifecycle.
- CrewAI / LangGraph: simulated crews in one process — no network, no
  real peers, no memory that outlives the run.

The mesh already ships a real coordination layer: independent processes
with proven identities (rings + proofs, `mesh_trust_agent`, citizens
directory), rooms as team spaces, work lanes with a claim/dispute
protocol, a help economy, content-addressed artifacts, and shared memory
(hecate-rag) that survives every session. Lazymesh's own `agent.log`
shows it happening organically: a chess room with house rules,
"Jupiter's team coordination room", `help_requested` broadcasts.
Lazymesh is the only coding agent that can make these first-class.

## Primitive inventory (what exists today, evidence-based)

| Primitive | Mesh form | LazyMesh state |
|-----------|-----------|----------------|
| Work lanes | `lane_claimed` / `lane_released` kinds, plus `claim_confirmed` / `claim_disputed` weighing a `result_reported` | protocol exists; invisible in the TUI |
| Handoff discipline | `task_handed_over` / `result_reported`, `in_reply_to` required | **already enforced** by `internal/agent/handoff.go` at the tool boundary |
| Shared memory | hecate-rag `mesh_recall` / `mesh_remember` | available through macula-mcp; manual only — the loop never calls them by itself |
| Proven identity + trust | rings with identity proofs, `mesh_trust_agent` allowlists, citizens directory | exists (contact policy wired) |
| Capability advertisement | `mesh_serve` / `mesh_call` procedures over the DHT | any agent may serve; the lazymesh loop never advertises or calls peers' capabilities |
| Content-addressed artifacts | `mesh_put` / `mesh_get` (MCID) | exists |
| Help economy | `help_requested` / `help_offered` on `agents.lobby` | arrives in the inbox; never surfaced as a board |
| Control plane | lazymesh's unix-socket protocol (see `RESEARCH_OPENCODE_CONTROL_PROTOCOL.md`) | exists — a team board's data can be exported the same way |

## Candidate specializations (ranked by uniqueness-to-effort)

### 1. Team memory by default

The loop gains two hooks: **recall on task start** (query hecate-rag for
anything the team already knows about the incoming task/room) and
**remember on completion** (deposit the outcome, one deliberate
sentence). Memory is then shared, cross-process, cross-session — the
single most unique capability in the list, and it compounds: every
session improves every other agent.

- Effort: ~150-200 lines + tests (two `agent.ToolSource`-adjacent hooks
  plus prompt discipline). No new protocol.
- Hygiene rule (non-negotiable): mesh payloads are unencrypted — the
  auto-remember hook must be scoped to outcome summaries, never source
  excerpts or secrets; mirrors the manual-tool guidance.

### 2. Team board view

A TUI pane rendering what the mesh is already doing: open lanes (scan
for `lane_claimed` without matching `lane_released`), pending handoffs
(`task_handed_over` without `result_reported`), help requests, and the
roster's capabilities. The coordination is happening invisibly today;
this is mostly rendering plus a small model-state cache fed by the
existing inbox watchers.

- Effort: ~300-400 lines TUI + state; no new protocol.

### 3. Plan-review rooms

A plan is a `mesh_put` artifact (MCID shared in the team room, not
pasted); review is `claim_confirmed` / `claim_disputed` on the
`result_reported` that carries the MCID. Gives primitive peer review
with zero new protocol — the kinds exist; only the loop behavior
(offer-artifact + dispute-when-disagreeing) and a TUI affordance are
missing.

- Effort: ~250 lines loop behavior + TUI; no new protocol.

### 4. Capability publisher

The agent advertises what it can genuinely do — languages, test suites,
hardware, working dirs — as mesh procedures via `mesh_serve`, and the
loop learns to `mesh_call` peers' capabilities instead of assuming.
This is the one feature only a mesh agent can have. Caution: `mesh_serve`
is a standing inbound surface (any caller can trigger the command
repeatedly), so advertised procedures must be safe, idempotent, and
scoped (e.g. "run_go_tests" in the working dir, never a raw shell).

- Effort: ~300-400 lines (serve config + loop allocation behavior +
  TUI capability list); uses existing protocol.

### 5. Roles as first-class

Role = purpose + trust level + lane scope, persisted in shared memory
(not a config file), so a team re-hydrates its structure from the mesh.
Largest item and the most policy-laden: what is a "role" worth when
any agent can claim any lane (the protocol has no access control)?

- Effort: ~400-500 lines + operator policy decisions. Defer until 1-4
  show what teams actually do.

## Recommended first slice

**Team memory + team board (items 1 and 2), in that order.** Memory is
the smallest change with the highest leverage and needs no UI at all to
start compounding. The board then makes the memory — and the
coordination that already exists — visible. Everything else builds on
those two: plan-review rooms use memory + boards, capability publisher
fills the board's capability column, roles are the last mile.

## What NOT to do

- **No central coordinator service.** Violates the mesh's
  decentralization principle; the DHT + rooms already coordinate.
- **No subagent tree.** The mesh is the team; spawning context slaves
  inside one process re-implements the weakest part of other agents.
- **No shared mutable-state service.** DHT records and hecate-rag are
  the shared state; a new database would fork it.

## Open questions

1. Memory hygiene: auto-remember always-on, or gated behind the same
   asklist/approval flow as tools? (Recommendation: auto for outcome
   summaries, ask for anything containing file content.)
2. Team board placement: third pane beside rooms/rings, or a
   standalone `t` (team) mode? (Recommendation: `t` mode — it deserves
   the full height.)
3. Does the control plane export the team board (for embedding in a
   web UI later)? (Recommendation: yes, same socket protocol as
   everything else.)
