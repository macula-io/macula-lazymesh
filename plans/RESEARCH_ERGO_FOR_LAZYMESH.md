# RESEARCH_ERGO_FOR_LAZYMESH.md

**Status:** Deep study complete — survey only, no code changed
**Created:** 2026-09-12
**Last Updated:** 2026-09-12

> This exists so lazymesh can host N supervised agent sessions in one process
> without losing tab isolation, without dragging in a network stack macula
> already provides, and without a panic in one session taking down the others.

This document is the gate for decision D4 in `PLAN_LAZYMESH_AGENT_QUALITY_GAPS.md`
(sessions = supervised actors; supervisor tree for tab isolation; monitors for
OD1's local peer channel; frontend bus → actor mailboxes). It is a deep study of
Ergo (ergo-services/ergo) at tag `v1.999.330` (= release **v3.3.0**), shallow
clone at `/tmp/opencode/ergo`. Only that line was studied; the clone is throwaway.

---

## What Ergo is

**Version studied:** v3.3.0, tag `v1.999.330`, published 2026-09-04. The tag
naming scheme is odd — releases are named `v3.x.y` in the CHANGELOG but tagged
`v1.999.3xx` (`CHANGELOG.md:4`, `CHANGELOG.md:231`). Master HEAD (2026-09-07,
`301097b`) is v3.3.0 plus documentation/benchmark moves only, so studying master
equals studying v3.3.0.

**License:** MIT (`LICENSE`, "Copyright (c) Taras Halturin"). **Go requirement:**
`go 1.21` (`go.mod:3`, `README.md:251`) — lazymesh on go 1.27.0 is fine. **Zero
external dependencies, pure Go** (`README.md:12`).

**Health:** Actively developed (last commit 2026-09-07). Docs are comprehensive
(a full GitBook at docs.ergo.services mirrored in `docs/`: basics, actors,
meta-processes, networking, testing, advanced, tools). In-repo test suite is
substantial, including a purpose-built four-layer testing framework
(`testing/{unit,stage,check,mock}`). **Bus factor is effectively 1**: 143 of
~210 counted commits are by `halturin`, 50 by `Zert`, everyone else ≤ 4
(GitHub contributors API). Breaking-change cadence is real: v1 → v2 (2021-10-12,
overhaul, `CHANGELOG.md:347`) and v2 → v3 (2024-09-04, ground-up rewrite,
`CHANGELOG.md:231`). Within the v3 line (3.0 → 3.3) changes have been additive
(tracing, EDF evolution, router, testing framework). Extra components (observer,
loggers, websocket/sse meta processes, registrars, Erlang protocol) live in
separate repos and are not needed for lazymesh.

Ergo is the OTP-faithful Go option the plan names it as: isolated processes with
priority mailboxes, supervision trees, links/monitors, and transparent
networking — but the networking is strictly optional, which is what makes D4
viable.

---

## Q1 — Single-node/embedded operation

**Verdict: YES, fully embedded is possible, but NOT by default.**
`ergo.StartNode` is the only public entry point (`ergo.go:12`); there is no
node-less spawn API. But `Network.Mode = NetworkModeDisabled` turns the node
into a pure in-process runtime: the network stack starts, sees the mode, and
returns immediately with no listeners and no network goroutines
(`node/network.go:1303-1313`; modes in `gen/network.go:345-358`).

Evidence that the default is NOT embedded-safe:

- `NetworkModeEnabled` is the default (`NetworkModeEnabled NetworkMode = 0`,
  `gen/network.go:348-351`).
- With default options and no acceptors configured, the framework **auto-adds an
  acceptor on `gen.DefaultPort` = 11144** (`node/network.go:1383-1393`,
  `gen/default.go:26`). So the README's own hello-world
  (`ergo.StartNode("mynode@localhost", gen.NodeOptions{})`) binds a TCP listener.
- Local-only message routing never touches the network: local PIDs resolve
  in-process (`node/core.go:90-141`), and the `CoreEvent` bus is node-local by
  design (`gen/core_events.go:1-6`).

Node-side overhead beyond actors is small and passive: a minute-granularity cron
ticker (`node/node.go:320`, `node/cron.go:52-86`), loggers, and a target manager
(`node/node.go:297`). Processes are goroutines that sleep with no goroutine
held; there is no pool.

**Condition for lazymesh:** `ergo.StartNode("lazymesh@localhost",
gen.NodeOptions{Network: gen.NetworkOptions{Mode: gen.NetworkModeDisabled}})`.
Never spawn with zero options — that opens port 11144.

---

## Q2 — Panic semantics

**Verdict: panics are recovered by default, the process dies with
`TerminateReasonPanic`, and supervisors/monitors get the panic as the exit
reason. Two nested recover layers plus recovery around Terminate.**

- Layer 1 (behavior): `act.Actor.ProcessRun` wraps the whole message loop in
  `defer recover()` and converts a panic into a returned
  `gen.TerminateReasonPanic` (`act/actor.go:137-145`). Panics in `Init` are
  recovered the same way (`act/actor.go:117-125`).
- Layer 2 (runtime): the run-loop goroutine itself has a `defer recover()` that
  logs `"process terminated - %#v at <origin>"`, flips state to Terminated,
  and finishes the process with `gen.TerminateReasonPanic`
  (`node/process_run.go:21-37`). Origin is reported as
  `function[file:line]` via `lib.PanicOrigin` (`lib/panic.go:9-24`).
- A panic in the `ProcessTerminate` callback is recovered and logged, and does
  not crash the node (`node/node.go:3242-3249`).
- The exit reason then flows to linked/monitoring parties via
  `cleanupProcess → RouteTerminatePID → tm.TerminatedTargetPID`
  (`node/node.go:3197-3198`, `node/tm/terminate.go:7-11`), so a supervisor
  restarts the child with reason = panic and a monitor receives
  `MessageDownPID{Reason: TerminateReasonPanic}`.
- The panic does NOT reach Go's default recover handler (there is no
  process-level one) and does NOT propagate to the caller's goroutine.
- Recovery is a build tag: `-tags=norecover` disables it globally
  (`lib/recover.go:1-7` vs `lib/norecover.go:1-7`, `README.md:294`). lazymesh
  must never build with that tag, since one process hosts all tabs (OD2's own
  failure-isolation concern).

The four termination reasons are first-class errors:
`TerminateReasonNormal/Kill/Panic/Shutdown` (`gen/process.go:206-231`).

---

## Q3 — Supervision

**Verdict: complete OTP supervision semantics with static and dynamic child
registration. Nothing supervises the root — lazymesh must start its own root
supervisor.**

- Strategies: OneForOne (default), AllForOne, RestForOne, SimpleOneForOne
  (`act/supervisor.go:96-117`).
- Restart policies per child: Transient (default), Temporary, Permanent, plus an
  Inherit sentinel that falls back to the supervisor-level strategy
  (`act/supervisor.go:136-156`).
- Restart intensity/window: defaults 5 restarts per 5 seconds
  (`act/supervisor.go:14-17`), enforced by `supCheckRestartIntensity`
  (`act/supervisor.go:786-805`). Per-child override possible
  (`act/supervisor.go:219-224`).
- On intensity exceeded: terminate the supervisor (default) or disable the
  offending child (`OnExceedDisable`) leaving the supervisor alive
  (`act/supervisor.go:172-183`).
- Static children: declared in `SupervisorSpec.Children` returned from `Init`
  (`act/supervisor.go:186-234`). Dynamic: `AddChild`, `StartChild`,
  `EnableChild`, `DisableChild` at runtime (`act/supervisor.go:249-308`).
- Children are automatically linked both directions (`LinkParent` +
  `LinkChild`, `act/supervisor.go:658-659`), so the supervisor sees child
  deaths as exit messages in its Urgent queue and applies the strategy
  (`act/supervisor.go:547-560`).
- **Clean state on restart: yes.** A restarted child is a fresh factory
  instance — factories are documented as "must return a new instance on each
  call" (`gen/process.go:53-55`) — spawned via `Spawn`/`SpawnRegister`
  (`act/supervisor.go:664-672`). Optional `ProcessOptions.PreserveMailbox`
  captures the dying process's mailbox into the exit reason and the supervisor
  hands it to the next incarnation (`gen/process.go:1054-1068`,
  `act/supervisor.go:660-662`) — crash-safe session inboxes if lazymesh wants
  them.
- **Root process: unsupervised.** Node-spawned processes get the node core as
  parent (`node/node.go:451-452`) but no restarter. Applications don't help
  either: `ApplicationModePermanent` *stops the application* when a member dies
  — it does not restart anything (`node/application.go:591-615`). So the
  lazymesh tree must be: node → root supervisor (node-spawned, the only
  unsupervised process) → tab supervisors → session actors.

---

## Q4 — Monitors and links

**Verdict: full OTP monitor/link semantics, directed and demonitorable. A
monitor sees the real reason (`TerminateReasonPanic` on panic,
`TerminateReasonNormal` on clean stop); a link kills the linked process on any
exit unless it traps exits.**

- Monitor → non-fatal `MessageDownPID{PID, Reason}` delivered as a
  regular message at High priority (`gen/message.go:5-9`;
  dispatch in `node/tm/terminate.go:198-232`). Variants exist for ProcessID,
  Alias, Event, Node (`gen/message.go:11-39`).
- Link → fatal exit signal `MessageExitPID{PID, Reason}`
  (`gen/message.go:41-45`); an actor without trap-exit terminates with the
  reason (`act/actor.go:299-306`). `SetTrapExit(true)` converts exit signals
  into regular messages — except one from the parent, which always kills
  (`act/actor.go:76-84`).
- Monitor/Demonitor API: `gen/process.go:805-848`.
- The exit reason is the actual termination reason, panic included
  (`node/node.go:3197-3198`), so a monitor can distinguish
  panic (`TerminateReasonPanic`) from normal stop (`TerminateReasonNormal`)
  from node shutdown (`TerminateReasonShutdown`) — exactly the deathwatch
  signal OD1's local peer channel needs.

---

## Q5 — Blocking in handlers

**Verdict: safe for the node, but it stalls that actor and the framework
assumes it won't happen. Scheduler = the Go runtime, one goroutine per active
process — there is no shared scheduler for a blocked handler to starve.**

- A process runs on a goroutine spawned on wakeup and reused while the mailbox
  is non-empty (`node/process_run.go:14-105`; wakeup triggered by the sender at
  `node/core.go:140`). A blocking `HandleMessage` blocks only that process's
  goroutine and mailbox; other actors run on other goroutines and are
  unaffected. There is no pool, no fixed worker set.
- The API docs nonetheless state the actor-model contract: "NEVER use blocking
  primitives (mutexes, channels, sync.WaitGroup) in callbacks" and "NEVER spawn
  goroutines in callbacks" (`gen/process.go:13-17`). Blocking I/O belongs in
  meta processes, which run in their own goroutines (`gen/process.go:292-297`,
  `README.md:174`).
- **Kill cannot interrupt a blocked handler.** `Kill` marks the state Zombee
  and waits for the goroutine to stop (`node/node.go:1949-1970`); the actor
  notices at the top of its loop (`act/actor.go:148-151`). So a hung LLM call
  without context cancellation is a stuck tab until the call returns; node
  shutdown escalation force-kills and ultimately `os.Exit(1)`s
  (`node/node.go:1326-1367`). lazymesh's Phase-3 interrupt must cancel the
  provider context — Kill is not an interrupt.
- Asynchronous request handling is built in: `HandleCall` returning `(nil, nil)`
  leaves the caller waiting and the actor free to keep processing its mailbox;
  the answer comes later via `SendResponse` (`act/actor.go:273-288`). This is
  the right shape for "run this turn" commands.

---

## Q6 — Mailbox

**Verdict: unbounded by default, FIFO per queue with a strict priority order,
no silent drops ever.**

- Four lock-free MPSC queues: Urgent / System / Main / Log
  (`gen/process.go:1310-1326`, implementation `lib/mpsc.go:1-92`).
- Pop order is strict priority: Urgent → System → Main → Log
  (`act/actor.go:158-191`); FIFO within each queue.
- Unbounded by default (`MailboxSize` 0 = unlimited; `Size()` reports -1,
  `lib/mpsc.go:107-113`, `gen/process.go:993-997`).
- Bounded opt-in: `MailboxSize` swaps in a bounded queue; on overflow the
  sender gets `ErrProcessMailboxFull`, or the message is wrapped in
  `MessageFallback` and forwarded to a configured fallback process
  (`node/core.go:97-115`, `gen/process.go:1026-1030`).
- There is no drop-policy dial — full mailboxes are always surfaced as errors
  or fallbacks. For socket-fed lazymesh actors, set `MailboxSize` + `Fallback`
  to get backpressure instead of unbounded growth.

---

## Q7 — gen_statem / session-lifecycle FSM

**Verdict: NO gen_statem exists. `ProcessKindFSM` is only a classification
label, not a behavior (`gen/process.go:86-87`). An FSM must be hand-rolled on
`act.Actor`, and that is sufficient for the session lifecycle.**

- No `statem`/`StateMachine` type anywhere in `act/` or `gen/` (grep-confirmed).
- Timers are first-class and cancellable: `SendAfter` / `SendWithPriorityAfter`
  / `SendEvery` return a `CancelFunc` that "returns false if the timer already
  expired" (`gen/process.go:516-542`); exit timers `SendExitAfter`
  (`gen/process.go:556-562`). Cancellable timeouts are therefore trivial:
  `cancel, _ := p.SendAfter(p.PID(), msgTimeoutWaitingRing, 30*time.Minute)`,
  cancel on state change.
- Plus node-level cron for wall-clock jobs (`node/cron.go`).
- The `listening → running → waiting_ring → paused → archived` machine is a
  state field + type switch in `HandleMessage`, with one cancellable timer per
  timed state. That is DIY code (a ~100-line state machine), not framework
  machinery — acceptable, but the state table and its transitions become
  lazymesh's own correctness surface to test.

---

## Q8 — Stop semantics

**Verdict: graceful stop is a message, not a kill — Terminate callback runs,
links/monitors get the real reason. No mailbox drain on stop (capture is
opt-in). Node shutdown has an escalation ladder ending in os.Exit(1).**

- Four reasons: Normal, Kill, Panic, Shutdown (`gen/process.go:206-231`).
- `SendExit(pid, reason)` → exit message → actor terminates with the reason
  wrapped (`act/actor.go:299-320`); trap-exit can intercept it (parent's exit
  excepted).
- **No message-queue drain on stop**: the loop returns on the exit message and
  the remaining mailbox is abandoned, unless `PreserveMailbox` was set
  (`gen/process.go:1054-1068`, `node/process_run.go:56`) — the supervisor then
  re-injects the captured mailbox into the next incarnation
  (`act/supervisor.go:660-662`).
- `ProcessTerminate` runs after unregistration; async `Send` family remains
  available during it (`gen/process.go:39-44, 196-198`;
  `node/node.go:3228-3251`).
- Node `Stop()`: exit-signals every process with `TerminateReasonShutdown`
  (sent via parent PID so it can't be trapped), waits, escalates to kill-all on
  timeout, then `os.Exit(1)` as last resort (`node/node.go:1224-1296`,
  `1326-1367`). `StopForce()` kills immediately (`node/node.go:1220-1222`).

---

## Q9 — Performance footprint

**Verdict: ~2 allocations and ~190ns per local Send on an M4 Max — roughly 2-4×
a raw channel handoff, and trivially sufficient for N sessions each doing
seconds-long LLM turns.**

- In-repo benchmark: `testing/benchmarks/ping/pingpong_test.go` measures
  end-to-end send→handle (not the Send call alone).
- Published numbers (`README.md:84-135`): local 1:1 191.4 ns/op (M4 Max) / 459
  ns/op (Threadripper), **58 B/op, 2 allocs/op**; network messages 6 allocs/op.
  Aggregate: 15.4–25.6M msg/s across cores.
- The 2 allocs are the per-message envelope (a `MailboxMessage` from a
  `sync.Pool`, `gen/mailbox.go:28-50`) plus the payload interface boxing.
- Comparison to a plain Go channel: a buffered-channel ping-pong typically
  lands at ~50–100 ns/op with 1–2 allocs, so Ergo's `Send` costs roughly 2–4× a
  hand-rolled channel while adding PID/name resolution, mailbox push, wakeup
  CAS, and priority. For lazymesh's message rates (user input, ring events,
  JSONL appends) this is noise. It would only matter if the frontend bus became
  a high-frequency event stream — the benchmark numbers say even that is fine.

---

## Q10 — Version/license/health

**Verdict: healthy project, MIT, active — with two caveats: bus factor 1 and a
history of breaking major rewrites.**

- Version studied: v3.3.0, tag `v1.999.330` (2026-09-04). Go `1.21`+
  (`go.mod:3`). MIT license (`LICENSE`). Zero dependencies.
- Commits: latest 2026-09-07 (this week). Contributors: `halturin` 143, `Zert`
  50, everyone else ≤ 4 → **bus factor 1** in practice.
- Breaking cadence: v1 → v2 overhaul (2021-10-12, `CHANGELOG.md:347`), v2 → v3
  ground-up rewrite (2024-09-04, `CHANGELOG.md:231`). The v3 line itself (3.0 →
  3.3 over two years) has been additive. Expect the next breaking change to be
  a major rewrite; pin `v1.999.330` (module resolution via semver is the odd
  tag anyway) and budget for an upgrade migration.
- Docs: excellent and current (`docs/` GitBook mirror in-repo, comprehensive
  coverage of actors/supervision/meta/network/testing). Testing framework is
  itself a headline feature of v3.3.0 (`CHANGELOG.md:3-25`).

---

## Q11 — Testing patterns

**Verdict: Ergo ships a real testing discipline lazymesh can copy directly —
in-process unit harness with a mock node, egress recording, ingress driving,
and self-send draining.**

- `testing/unit` spawns exactly one behavior against a **mock node**: outbound
  operations are recorded as `check.Records` and stubbable (negative paths via
  `OnCall`, `OnSpawn`, ...), inbound signals are driven explicitly —
  `SendMessage`, `Call`, `DeliverExit`, `DeliverDown`, `DeliverEvent`,
  `FireTimers`, `FireCron` (`testing/unit/unit.go:1-21, 281-520`).
- `Drain`/`Step` replay the actor's messages to itself, which is how
  "Init posts to itself" actors are tested (`testing/unit/unit.go:574-657`).
- The harness **replicates the production panic recovery**: `runBehavior`
  recovers a panic into `TerminateReasonPanic` exactly like
  `node/process_run.go` (`testing/unit/unit.go:237-249`).
- `testing/stage` runs live multi-node integration with the same assertion
  grammar; `testing/check` is the shared matcher core.
- Repo's own supervisor coverage lives beside the code:
  `act/supervisor_ofo_test.go`, `act/supervisor_arfo_test.go`,
  `act/supervisor_sofo_test.go` (one file per strategy).
- For lazymesh: session actors get unit-harness tests (drive ring/pause/
  interrupt messages, assert egress + termination reason), the supervision
  tree gets stage-style integration tests, and panic-path tests mirror
  `testing/unit`'s recover replication.

---

## Q12 — Integration sketch for lazymesh

```
main
 └── bubbletea program (own goroutine, the TUI loop — NOT an actor)
      └── uiBridge actor        (owns the tea<->actor channel pair)
 └── ergo node (NetworkModeDisabled)            ← Q1 condition
      └── rootSupervisor (act.Supervisor, OneForOne, Permanent children)
           ├── tabHostSupervisor₁..N (OneForOne, per-tab isolation)
           │     └── sessionActor₁ (act.Actor + hand-rolled FSM)   ← Q7
           │           └── JSONL writer (SpawnMeta or own goroutine at boundary)
           ├── socketServerActor (unix socket accept, D1 headless)
           │     └── per-connection actors (or meta processes)
           └── meshBridgeActor   (macula-mcp / QUIC boundary)
```

- **Session = actor with an FSM** (`listening → running → waiting_ring →
  paused → archived`). State field + type switch in `HandleMessage`, one
  cancellable `SendAfter` per timed state (ring timeout, pause auto-resume).
  Kind label `ProcessKindSession` (`gen/process.go:124-125`).
- **Supervision = tab isolation.** Each tab gets a supervisor (OneForOne,
  Transient, default 5-in-5s intensity). A panicking session dies with
  `TerminateReasonPanic` (Q2) and is respawned clean (Q3); a crash-looping tab
  kills only its own supervisor, not the process. Root supervisor is the only
  unsupervised process — keep it trivial.
- **TUI stays bubbletea, not an actor.** A bridge actor owns a `tea.Cmd`-safe
  channel pair: TUI events → `Send` into the bus; actor outbound (assistant
  deltas, ring popups) → tea messages. The frontend bus becomes actor
  mailboxes (D4's promise), with the TUI as one more frontend beside the
  socket.
- **Boundary rule (Q5):** anything that blocks lives OUTSIDE actors — LLM calls
  (provider goroutine), QUIC/mesh I/O, JSONL `O_APPEND` writes, socket writes.
  Session actor requests a turn via async `HandleCall` (`act/actor.go:273-288`)
  or a fire-and-forget `Send` to the provider worker; results return as
  messages. The session actor therefore stays responsive: `interrupt`,
  ring-answer, and pause messages keep flowing during a turn.
- **Interrupt = context cancellation, never Kill** (Q5). Phase-3's cancellable
  ctx is the mechanism; `Kill`/`SendExit` remain supervisor-grade tools.
- **OD1 local peer channel:** the socket server actor monitors each connection
  actor (Q4); `MessageDownPID{Reason}` tells it whether a peer socket died
  (normal close vs panic vs kill) and drives the ring/publish fallback rules.
- **Backpressure:** bounded mailboxes (`MailboxSize` + `Fallback`) on the
  socket-fed and TUI-fed actors (Q6); session inboxes unbounded but
  `PreserveMailbox` on if crash-resume without JSONL replay is ever wanted.
- **Persistence stays D2**: JSONL is an append-only log written by the
  boundary worker; a respawned session replays from JSONL, not from mailbox
  capture. Don't double-track state in both.

---

## Verdict for D4

**GO-WITH-CONDITIONS.**

Ergo v3.3.0 confirms all three gate questions from the plan: single-node
embedded operation is real (network fully disableable, Q1), panic →
supervisor-restart semantics are real and recovered-by-default (Q2, Q3), and
blocking I/O is tolerated by the goroutine-per-process scheduler as long as it
is kept at the boundary (Q5). Supervision, monitors, priority mailboxes, and
the testing harness are all strictly better than what a hand-rolled channel
bus would give Phase 0.

Conditions:

1. **Embedded-only node**: always `Network.Mode = NetworkModeDisabled`. Default
   options silently bind TCP :11144 (`node/network.go:1383-1393`) — a
   hard-wired acceptance test should assert no listener.
2. **Boundary rule is law**: LLM/QUIC/socket/JSONL I/O never executes inside an
   actor callback; results come back as messages. Enforce in review.
3. **Interrupt = context cancel, not Kill**: Kill cannot preempt a blocked
   handler (`node/node.go:1949-1970`).
4. **FSM is hand-rolled**: there is no gen_statem. Keep the session state
   machine small and unit-tested via `testing/unit`.
5. **Never build with `-tags=norecover`** — one process hosts all tabs.
6. **Pin the minor** (`v1.999.330`); budget for a breaking-major migration
   eventually (v1→v2→v3 history) and accept bus factor 1.
7. **Bounded mailboxes on every socket-fed actor** (no silent drops, but
   unbounded growth is the default).
8. **Root supervisor only, nothing above it**: Ergo restarts nothing above the
   tree root; lazymesh's main loop owns starting/stopping the node.

## Risks & open questions

- **Bus factor 1** (`halturin`). A project fork is a realistic contingency;
  MIT makes that legal.
- **Breaking-major cadence.** v2 and v3 were both rewrites; an Ergo v4 could
  arrive during lazymesh's Phase 0. Mitigate by isolating Ergo behind
  lazymesh's own bus abstraction so actors are swappable.
- **No gen_statem** means the session lifecycle's correctness (illegal
  transitions, timer leaks on state change) is lazymesh code, not framework
  code. The `testing/unit` harness (FireTimers, DeliverExit) is the right tool
  to pin it.
- **Hung-handler semantics**: a provider call that ignores its cancelled
  context yields a zombie session until the goroutine returns; the node's
  shutdown escalation (`os.Exit(1)`) exists but is not a per-tab answer. This
  is a lazymesh discipline problem (always ctx-plumbed providers), not an Ergo
  one.
- **Two crash-recovery mechanisms**: `PreserveMailbox` (in-actor) vs JSONL
  replay (D2). The sketch picks JSONL as the single source of truth; a session
  crash between `O_APPEND` and actor-state update can still lose the tail of a
  turn. Reconcile in Phase 4.
- **Goroutine-per-wakeup model** spawns a goroutine per message burst; fine at
  N-session scale, untested by lazymesh at high event rates (benchmarks suggest
  headroom of 3+ orders of magnitude).
- **Open question**: whether the socket server should be an actor at all, or a
  plain listener whose accepted conns are meta processes (`SpawnMeta`) — Ergo's
  own answer to blocking I/O. Decide in Phase 0; both are supported.
