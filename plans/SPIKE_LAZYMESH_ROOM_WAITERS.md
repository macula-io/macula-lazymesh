# Spike Report: Loop-Owned Per-Room Mesh Listeners

**Issue:** macula-io/macula-lazymesh#14
**Status:** Complete
**Branch:** `spike/room-waiters` (isolated worktree, never touched shared `main`)
**Author:** Juno (github-com-39), synthesizing Atlas/Nova/Vega/Fable's design work from #10/#13/team room
**Timebox:** ~1 day of the 1-2 day budget

## One-line goal

Prove or disprove that moving room-listening out of the model's own tool-calling
loop, into loop-owned per-room goroutines, fixes "a long mesh wait blocks
everything else" -- without the cost/coherence problems either full-isolation
proposal had.

## Recommendation

**Proceed to full implementation.** The core mechanism is real, measured, and
clean: concurrent `mesh_wait_room` calls over one MCP session work correctly in
practice (not just in principle), and the fix takes human-input latency from
~7 seconds (bounded by whatever's left of the model's own wait) to about one
microsecond. Resource cost at a realistic room count is negligible. The
remaining open questions (below) are things to verify *during* the build, not
reasons to iterate on the design further or abandon it.

## What was built

All in a git worktree on branch `spike/room-waiters`, off `origin/main` at
`0355224` -- the shared checkout other sessions were using stayed on `main`
throughout, untouched.

1. **`internal/roomwaiter`** (new package). `Manager` runs one goroutine per
   joined room, each in a real `mesh_wait_room` call (`wait_seconds=3600`,
   re-armed immediately on return -- one continuous call per mesh_wait_room's
   own doc guidance, not a poll loop). Cancellation is via `context.Context`
   per room, not by shortening `wait_seconds`, so room-leave/shutdown is
   responsive regardless of how long a wait was requested.
   - **Fairness policy** (Atlas's flagged gap): a room already pending (its
     last arrival not yet `Ack`'d) is never re-queued. This caps any one
     room's influence to one outstanding "check me" slot -- a chatty room
     cannot flood the channel and starve others by volume. What happens
     among several simultaneously-pending rooms is then whatever Go's own
     `select` does among ready cases, which is fair over time but not a
     deliberate priority order -- documented as a real, acceptable-for-now
     limitation, not solved further.
   - **Room churn** (Vega's item): `Sync(ctx, joined []string)` reconciles
     watched rooms against a list the caller provides -- it never calls
     `mesh_rooms` itself. `cmd/lazymesh` feeds it by watching the *existing*
     event stream for `mesh_rooms` tool results the model already produces on
     its own normal cadence, so no new poll loop was added anywhere.
2. **`internal/agent/nowait.go`**: `NoBlockingWaitSource` clamps any
   `mesh_say` `wait_reply_seconds` argument above 10s down to 10s, regardless
   of what the model asks for -- defense in depth matching `allowlist.go`'s
   own posture, so a prompt regression can't quietly recreate the bug.
   `mesh_wait_room` itself needs no clamping: it was never on
   `agent.DefaultToolAllowlist` to begin with (confirmed by reading the
   allowlist source directly, not assumed) -- only `roomwaiter.Manager` ever
   calls it, directly against the client, same as `sayGoodbye` (#6) bypasses
   the allowlist entirely for its own deterministic teardown call.
3. **`cmd/lazymesh/main.go`**: a new `--spike-room-waiters` flag (default
   `false`) threads a `*roomwaiter.Manager` through `run`/`runAgent`. When nil
   (the default), every code path is byte-for-byte identical to today --
   that's what makes today's behavior the actual A/B baseline from the same
   binary, not a remembered "before" state.
   - `buildSystemPrompt` gained a `spikeRoomWaiters bool` parameter: true
     drops the existing "call mesh_say with a long wait_reply_seconds to
     listen efficiently" instruction and replaces it with an explanation that
     the harness now handles waiting -- confirmed this was necessary, not
     optional: shipping the goroutine alone without also rewriting this line
     would leave the model still trying to do the old thing.
   - `nextPrompt` (today's instant, non-blocking "check for anything new"
     fallback) is preserved unchanged; a new `nextEvent` wraps it for the
     spike path, blocking (no default case) on human input, a room arrival,
     or `ctx.Done()` when nothing is immediately pending. This blocking is
     itself necessary and easy to miss: once the model stops supplying its
     own wait, *something* has to pace the loop or it would call the
     provider back-to-back for free-standing "anything new?" prompts forever.

## What was measured (real, against the live mesh)

All live tests are `//go:build live`, run with `go test -tags live`; none were
merged to `main`.

### 1. Does one `*mcpclient.Client` actually support concurrent outstanding calls?

**Confirmed, not just "no serializing lock in the source."**
`internal/mcpclient/mcpclient_live_test.go`'s
`TestLiveConcurrentCallTool_DoesNotSerialize`: 5 real rooms, one client fires 5
concurrent `mesh_wait_room` calls, a separate publisher client sends one
message per room staggered 1s apart after a 5s head start. If the client
serialized calls internally, elapsed times would balloon toward the *sum* of
every stagger, or later calls would simply miss their own message and time
out. Actual result:

```
room=...195835... elapsed=5.260481483s
room=...baae8854... elapsed=6.261249906s
room=...60b11ead... elapsed=7.264061801s
room=...43ab2049... elapsed=8.266107922s
room=...ecde53bc... elapsed=9.268171913s
```

Each call returned within ~2ms of its own expected time, spaced almost
exactly 1.002s apart. No serialization, no missed messages. Passed under
`-race` too.

### 2. Human-input latency, baseline vs. spike

`cmd/lazymesh/spike_live_test.go`, two tests against the real mesh:

- **Baseline** (`TestLiveBaseline_HumanInputInvisibleDuringLongMeshSayWait`):
  a scripted provider deterministically issues one real `mesh_say` call with
  `wait_reply_seconds=8` in a private test room nobody else is in. A "human"
  message is pushed to `userInputCh` 1s into that call. Measured: the message
  was invisible for **7.19s** -- exactly the remaining wait, because nothing
  re-checks `userInputCh` until `loop.Say()` itself returns. This is the bug,
  reproduced mechanically and quantified, not inferred from reading the code.
- **Spike** (`TestLiveSpike_HumanInputSeenImmediatelyDuringRoomWait`): a real
  `roomwaiter.Manager` is deep inside a real `mesh_wait_room(wait_seconds=3600)`
  call on one room. A "human" message pushed to `userInputCh` is picked up by
  `nextEvent` in **1.04 microseconds** -- human input is its own `select` case,
  never nested inside a room's wait.

### 3. macula-mcp resource usage under concurrent waiters

Fable flagged 64 concurrent `REPLY_POLL_MS=250` polls (verified directly
against `macula-mcp`'s own `rooms.ts` source, not assumed) as worth measuring.
`internal/roomwaiter/resource_live_test.go` opened 30 real rooms and held all
30 `mesh_wait_room` waiters open concurrently for 20s. External `ps` sample on
the actual macula-mcp node process during the hold: **3.1% CPU, ~140MB RSS**,
all 30 waiters still healthy at the end. This is at n=30, not Fable's full 64
-- scaling looks comfortably sub-concerning at this size, but the exact
n=64 number was not separately re-run; a straightforward follow-up if the team
wants that specific figure confirmed.

### 4. Token usage instrumentation

`provider.ChatResponse` gained a `Usage` field (prompt/completion/total
tokens), parsed from DeepSeek's own OpenAI-compatible response (it was already
being discarded, unparsed, before this). `agent.Loop.Usage()` accumulates it
across a conversation. Confirmed working against the real API
(`TestLiveDeepSeek_ReportsRealTokenUsage`): a trivial one-word exchange
reported `{PromptTokens:90 CompletionTokens:11 TotalTokens:101}`.

**What this does NOT include**: an actual long-running soak comparing
aggregate token cost, baseline vs. spike, over realistic multi-room chatter.
Reasoning instead of measurement here: waiting itself costs zero tokens in
*either* design (mesh_say/mesh_wait_room are MCP calls, not LLM calls).
`wait_reply_seconds` already returns on the *first* reply today, so baseline
already produces roughly one `Say()` cycle per event too, not one per
long-wait-window -- the two designs likely aren't as different in per-event
cost as "cost scales with events, not context count" implies on its own; the
real difference measured above is human-input responsiveness specifically,
not aggregate spend. The instrumentation now exists for the team to run that
longer soak directly rather than trust this reasoning indefinitely.

### 5. `trimHistory` consumption rate under higher event volume

Not measured with real multi-room conversation data -- reasoned only.
`maxHistoryMessages=200`, and a typical tool-calling cycle (user prompt +
assistant-with-tool-call + tool-result + final assistant reply) is roughly 4
messages, so ~50 cycles of history survive the window regardless of design.
If 3+ rooms are each independently chatty, the spike design's "one cycle per
arrival" would burn through that budget measurably faster than baseline's
"one cycle roughly per long-wait-window" -- flagged as a real, plausible risk
worth watching during full implementation, not disproven or confirmed here.

## Known prerequisite, checked

`mesh_wait_room` shipped in macula-mcp 0.24.0 (confirmed by reading
`macula-mcp/CHANGELOG.md` and `src/mesh_wait_room.ts`/`src/rooms.ts` directly)
-- already covered by lazymesh's own pinned default (`config.Default()`'s
`0.24.2`), so no version bump was needed. It was never on
`agent.DefaultToolAllowlist` (checked the source, not assumed) -- Atlas's
flagged prerequisite ("don't just add it there and call it done") turned out
to already be satisfied by omission; the actual remaining work was the prompt
rewrite and the `NoBlockingWaitSource` clamp, both built above.

## What this spike deliberately did not do

Per the issue's own non-goals: no TUI polish, no full test coverage (unit
tests cover the pure logic -- dedup, priority, clamping -- live tests cover the
mechanism; nothing exhaustive), no macula-mcp changes, no voting/polling
feature work.

## Honest byproduct

Building and running these live tests opened ~37 real, private test rooms and
left several fresh, un-`mesh_goodbye`'d identities behind -- the exact "stray
room/participant" pattern this team was independently discussing in the
shared room earlier today. Fixed going forward (every live test in this
branch now calls `mesh_goodbye` before `client.Close()`), but the rooms
already created during earlier measurement runs are still sitting there
(harmless: private topics, never announced anywhere a person would see them,
and roster entries self-prune after 15 minutes regardless).

## Public-reference-material review pass

Per Raf's standing rule (all macula-io implementation work is public and read
as reference material by outside developers), did a pass over this spike's
own code for unexplained non-standard choices after the fact, since a spike
branch is still public even though it isn't merged: strengthened
`maxModelWaitSeconds`'s doc comment (10 was picked but not weighed on the
page -- now explains the actual short-vs-long tradeoff and flags it as
unmeasured, not settled) and `roomwaiter.errorBackoff`'s (now ties it
explicitly to `cmd/lazymesh`'s existing `initialBackoff`, same value reused
rather than picked independently). No other magic numbers found undocumented
on this pass; the rest of the spike already inherited this codebase's own
existing convention of explaining "why," not just "what."

## Suggested next steps if proceeding

1. A longer real live session (many real cycles, not the scripted/mechanical
   tests here) to confirm deepseek-v4-flash's behavior holds up across a real
   multi-turn conversation under the new prompt wording, and to get a real
   `trimHistory` rotation-rate number instead of the estimate above.
2. Decide whether the fairness policy's narrow simultaneous-arrival race
   (documented in `nextEvent`'s own doc comment) needs hardening or is
   acceptable as-is.
3. If promoted to a real feature: a `config.Config` field (matching
   `LocalTools`/`RingPolicy`'s own pattern) rather than a bare CLI flag, so an
   operator doesn't have to remember to pass `--spike-room-waiters` every run.
4. Optionally re-run the resource probe at n=64 to cover Fable's exact number.
