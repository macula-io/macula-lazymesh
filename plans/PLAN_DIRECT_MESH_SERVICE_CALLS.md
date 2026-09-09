# Direct (non-AI) mesh service calls from the `s` panel

**Status:** Implemented, all phases complete, not yet committed. Both open
questions from Phase 0 confirmed favorable against the actual code (not
assumed) before anything was written: `mcpclient.Client.CallTool` is safe
for concurrent callers (standard JSON-RPC request/response correlation,
`internal/jsonrpc2/conn.go`'s own mutex-guarded connection state — the SDK
is built for exactly this), and `CallToolRaw` already checks the shared
rate limiter internally, so a human-triggered call and the agent's own
tool-calling path automatically share one budget with no extra wiring.
**Created:** 2026-09-09
**Last updated:** 2026-09-09

## Why this exists

So an operator who already knows which curated mesh procedure they want can
call it directly from the `s` panel, without enabling `mesh_services_enabled`
and paying its 7,931-token fixed-prefix cost on every turn just to have the
AI make one deterministic call on their behalf. `internal/meshservices.Source
.CallToolRaw` already does exactly this call, with no LLM involved — it's
just never wired to anything but the agent's own tool-calling loop. This
plan wires it to the human instead.

## What this is NOT (out of scope for v1)

- Not a per-procedure argument form (one field per parameter, typed inputs,
  validation against each procedure's own schema). v1 takes one raw JSON
  object, typed by hand, matching `CallToolRaw`'s own signature exactly. A
  friendlier form is a real, separate follow-on if this gets real use — not
  designed here, because the 16 curated procedures don't share a parameter
  shape and building 16 forms (or one schema-driven generic form) is a
  materially bigger piece of work than this plan's actual goal.
- Not a change to when `mesh_services_enabled` matters for the AI path —
  the agent's own tool-calling loop is untouched. This only adds a second,
  independent door into the same `Source`.
- Not available when mesh services are disabled entirely (`meshServices ==
  nil` in the Model, i.e. `mesh_services_enabled: false` and no `Source`
  was ever constructed) — there's nothing to call. The panel already shows
  "disabled -- set mesh_services_enabled: true" in that state; the new
  keybinding is simply inert there too, same posture as everywhere else in
  this app (a capability nobody configured is never silently available).

## Open questions to resolve before writing code, not assumed

- **Concurrent `CallTool` safety.** `mcpclient.Client.CallTool` goes through
  `c.currentSession()` to the same live MCP session the agent's own
  tool-calling loop uses. MCP/JSON-RPC over stdio generally supports
  concurrent in-flight requests (that's what request IDs are for), but this
  needs to be *confirmed* against this specific SDK/session implementation
  early in implementation, not assumed — a human-triggered call landing
  mid-flight of an agent tool call is exactly the kind of race that looks
  fine in a quick manual test and breaks under real concurrent use. If it
  turns out not to be safe, the fix is serializing both paths through one
  place (a mutex or a single-worker queue in `Source`), not disabling the
  feature.
- **Rate limiting.** `Source` already has a `fixedWindowLimiter`
  (`maxCallsPerMinute`, `meshservices.go:78`) bounding real `mesh_call` RPCs.
  A human-triggered call must go through the *same* limiter instance as the
  agent path, not a separate budget — they're hitting the same real mesh
  infrastructure. Confirm `CallToolRaw` already checks it (it should, being
  a method on the same `Source`) before assuming this is free.

## Design

**Selection.** The `s` panel's table is currently rebuilt fresh on every
render call (`renderMeshServices`, no persistent cursor) — nothing in this
app tracks row selection today. Add a `meshServicesCursor int` field to
`Model`, bounded to `[0, len(entries))`. `Up`/`Down` currently no-op when
`meshServicesExpanded` is true (`model.go`'s Up/Down handlers explicitly
exclude it) — a genuinely free, currently-inert affordance; repurpose it to
move the cursor instead of adding a new keybinding. Render the selected row
highlighted (`table.WithStyles` already exists for this; check whether
`bubbles/table` needs the cursor set on the table instance itself each
render, or whether a custom highlight in the row-building loop is simpler
given the table is rebuilt fresh each time regardless).

**Invocation.** Same interaction shape as realm-join (`ModeRealmJoin` in
`model.go`/`realmjoin.go`) precedent, not invented fresh: `i` already means
different things depending on which panel is expanded (compose message,
realm name entry) — when the `s` panel is expanded, `i` starts a new mode
(`ModeMeshServiceCall` or similar) that prompts for a raw JSON arguments
string for the currently-selected row's procedure, defaulting to `{}`.
`Enter` submits: call `m.meshServices.CallToolRaw(ctx, procedure,
argumentsJSON)` (needs a `tea.Cmd`, not inline — this is a real network
call, same async-command shape as `startRealmJoin`, never block the Update
loop). `Esc` cancels, same as every other modal input in this app.

**Result.** Append a new chat entry showing the call and its result —
`chatToolCall`/`chatToolResult`-shaped (collapsed by default, `e` expands,
matches `truncateForChat`'s existing convention exactly) but distinguished
from an actual agent-initiated tool call, since conflating "the AI decided
to do this" with "the operator did this directly" in the transcript would
be misleading history. Either a new `chatEntryKind` (e.g. `chatDirectCall`)
or reuse `chatToolCall`/`chatToolResult` with a marker in `tool` (e.g.
prefixed `"[direct] "`) — pick whichever reads cleaner once the render code
is in front of you; both are cheap, this is not a decision worth blocking
on.

## Phases

- [x] Phase 0: Both open questions confirmed favorable against the actual
      SDK/Source code — see Status line above. No design change needed.
- [x] Phase 1: `meshServicesCursor` field, Up/Down repurposed for row
      selection when the `s` panel is expanded (previously a genuine
      no-op there), `meshServiceTableStyles()` (a real `Selected` style,
      scoped to this one table only — every other panel's `tableStyles()`
      stays deliberately blank, see that function's own comment on why).
      Cursor resets to 0 on panel close. Verified RED (new symbols
      undefined) before the implementation existed, GREEN after.
- [x] Phase 2: `ModeMeshServiceCall` added to the `Mode` enum,
      `meshServiceCallInput`/`meshServiceCallProcedure`/
      `meshServiceCallInFlight` added to `Model`, `i` on the `s` panel
      (services enabled, nothing in flight) captures the selected row's
      procedure and enters the mode; `Esc`/`Enter` handled with the exact
      same shape as `ModeRealmJoin`'s own block. Mode indicator
      (`-- MESH CALL --`) and hint row wired too.
- [x] Phase 3: `startMeshServiceCall` (an async `tea.Cmd`, `refreshCmd`'s
      single-request/single-response shape, not `waitForRealmJoinEvent`'s
      channel-of-events one — `CallToolRaw` is one RPC, not a stream) plus
      `meshServiceCallResultMsg`/`handleMeshServiceCallResult`. Result
      renders as a `chatToolResult`/`chatError` entry with `"[direct] "`
      prefixed onto the tool name, so it's never mistaken in the
      transcript for something the AI decided to do.
- [x] Phase 4: 9 new tests across `internal/tui/model_test.go`
      (`TestMeshServicesPanel_CursorMovesWithinBoundsOnly`,
      `TestUpDown_StillScrollsChatWhenNoPanelExpanded`,
      `TestStartMeshServiceCall_CallsCallToolRawWithExactArgsAndWrapsTheResult`,
      `TestStartMeshServiceCall_WrapsAnErrorToo`,
      `TestHandleMeshServiceCallResult_SuccessAndErrorRenderDifferentEntryKinds`,
      `TestModeMeshServiceCall_SubmitWithNoProcedureCapturedDoesNothing`,
      `TestModeMeshServiceCall_EscCancelsAndClearsTheDraft`,
      `TestNormalMode_IDoesNotEnterMeshServiceCallModeWhenServicesDisabled`,
      `TestNormalMode_IEntersMeshServiceCallModeAndCapturesSelectedProcedure`),
      each confirmed RED before the corresponding implementation landed,
      GREEN after — not written after the fact against already-passing
      code. Malformed-JSON-rejected-before-the-network-call is
      `CallToolRaw`'s own existing, already-tested property
      (`internal/meshservices`'s own suite); `startMeshServiceCall`'s
      tests confirm the string is passed through verbatim, not
      re-validated at this layer.

## Success criteria

- [x] With `mesh_services_enabled: false`, an operator can open the `s`
      panel, select a curated procedure, invoke it with hand-typed JSON
      arguments, and see the real result in chat — with the AI never
      loading any `mesh_service_*` tool schema and never making the
      decision to call it. (Verified at the unit level: the `i`-guard
      requires `m.meshServices != nil` — set only when the caller passes
      `Options.MeshServices` regardless of `mesh_services_enabled` — and
      `startMeshServiceCall`'s own test confirms the exact procedure/args
      typed reach `CallToolRaw` untouched. Not yet exercised in a live
      interactive terminal session — see note below.)
- [x] The agent's own tool-calling path (when `mesh_services_enabled:
      true`) is unaffected — nothing in `meshservices.Source` itself was
      touched, only new TUI-side callers added.
- [x] Full test suite green (`go test ./...`), `go vet`/`gofmt` clean.

**Not yet done:** committing. Also not yet done: driving this interactively
in a real terminal (a raw bubbletea TUI isn't something the available
tooling can drive/observe the way a browser-based UI change would be
click-tested) — the verification above is real (RED/GREEN unit tests
against the actual key-handling and rendering code paths, not just "it
compiles"), but it is not the same as an operator's own eyes on the actual
screen. Worth a real interactive pass before calling this fully done, not
just code-complete.
