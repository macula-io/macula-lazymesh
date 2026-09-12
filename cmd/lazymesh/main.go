// Command lazymesh is a single binary that spawns macula-mcp as its only
// tool source, drives a configurable LLM agent loop against it, and shows
// a live TUI of mesh rooms, pending rings, and agent presence. See
// plans/PLAN_LAZYMESH_MVP.md for the full design.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"ergo.services/ergo/gen"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/macula-io/macula-lazymesh/internal/agent"
	"github.com/macula-io/macula-lazymesh/internal/config"
	"github.com/macula-io/macula-lazymesh/internal/contactpolicy"
	"github.com/macula-io/macula-lazymesh/internal/frontend"
	"github.com/macula-io/macula-lazymesh/internal/localtools"
	"github.com/macula-io/macula-lazymesh/internal/logging"
	"github.com/macula-io/macula-lazymesh/internal/mcpclient"
	"github.com/macula-io/macula-lazymesh/internal/meshservices"
	"github.com/macula-io/macula-lazymesh/internal/provider"
	"github.com/macula-io/macula-lazymesh/internal/realmjoin"
	"github.com/macula-io/macula-lazymesh/internal/ringwaiter"
	"github.com/macula-io/macula-lazymesh/internal/roomwaiter"
	"github.com/macula-io/macula-lazymesh/internal/sessionhost"
	"github.com/macula-io/macula-lazymesh/internal/sessionstore"
	"github.com/macula-io/macula-lazymesh/internal/tui"
	"github.com/macula-io/macula-lazymesh/internal/updatecheck"
)

// version, commit, and date are set via -ldflags by .goreleaser.yml at
// release build time; "dev" is what `go build`/`go run` without those
// flags produces, which is the honest answer for a local build.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	showVersion := flag.Bool("version", false, "print version and exit")
	configPath := flag.String("config", "", "path to config.yaml (default: ~/.config/lazymesh/config.yaml)")
	room := flag.String("room", "", "mesh room topic to prioritize joining, in addition to whatever rooms the agent is already a member of (optional -- the agent loop always runs)")
	goalText := flag.String("goal", "", "additional objective for the agent, beyond ordinary mesh participation (optional)")
	headless := flag.Bool("headless", false, "run without the TUI (a unix control socket or a signal ends the session)")
	socketPath := flag.String("unix-socket", "", "serve the control plane on this unix socket (default: $XDG_RUNTIME_DIR/lazymesh/<session-id>.sock)")
	sessionID := flag.String("session-id", "", "session id reported to controllers (default: lazymesh-<pid>)")
	resumeRef := flag.String("resume", "", "resume a previous session: a session id, or \"latest\"/\"last\" for the newest one")
	continueLatest := flag.Bool("continue", false, "resume the newest session (same as --resume latest)")
	flag.Parse()

	if *showVersion {
		fmt.Printf("lazymesh %s (commit %s, built %s)\n", version, commit, date)
		return
	}

	if err := run(*configPath, *room, *goalText, *headless, *socketPath, *sessionID, *resumeRef, *continueLatest); err != nil {
		fmt.Fprintln(os.Stderr, "lazymesh:", err)
		os.Exit(1)
	}
}

func run(configPath, room, goalText string, headless bool, socketPath, sessionID, resumeRef string, continueLatest bool) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Best-effort only: bounded by updatecheck.DefaultTimeout, silent on
	// any failure (offline use included), and never changes
	// cfg.MaculaMCPVersion -- see package updatecheck's own doc comment.
	// Must run and print here, before the TUI's alt screen takes over
	// stderr below.
	if notice := (updatecheck.Checker{}).Notice(ctx, cfg.MaculaMCPVersion); notice != "" {
		fmt.Fprintln(os.Stderr, "lazymesh:", notice)
	}

	// Sets the isolated contact_policy.json's own contact_policy field
	// before macula-mcp ever reads it, translating cfg.RingPolicy the one
	// place that mapping happens (config.RingPolicyContactPolicyFileValue's
	// own doc comment explains why this isn't a direct 1:1 map). Preserves
	// any existing allowlist -- an operator's own past "Answer + Trust"
	// choices are never clobbered by this.
	if cfg.ContactPolicyFile != "" {
		if err := contactpolicy.Ensure(cfg.ContactPolicyFile, config.RingPolicyContactPolicyFileValue(cfg.RingPolicy)); err != nil {
			return fmt.Errorf("set up contact policy file: %w", err)
		}
	}

	client, err := mcpclient.Spawn(ctx, mcpclient.SpawnOptions{
		Version:           cfg.MaculaMCPVersion,
		IdentityFile:      cfg.IdentityFile,
		ContactPolicyFile: cfg.ContactPolicyFile,
	})
	if err != nil {
		return fmt.Errorf("spawn macula-mcp: %w", err)
	}
	defer client.Close()

	// Buffered generously since the TUI side is the slow consumer (a human
	// reading, not a tight loop) and the agent side must never block on it.
	tuiEvents := make(chan agent.Event, 64)
	// The control plane's own event fan-out: the event bridge and the
	// driver broadcast every event here too, and the frontend server
	// drains it. Without a socket there is no reader, and the non-blocking
	// sends simply drop — the TUI's channel is unaffected.
	frontendEvents := make(chan agent.Event, 64)
	userInputCh := make(chan string, 8)

	// Moved ahead of buildToolSource (2026-09-07, R2): meshservices.Source
	// needs a logger for its one-time discovery line, and buildToolSource
	// is where that gets wired in.
	logPath, err := agentLogPath()
	if err != nil {
		return fmt.Errorf("resolve agent log path: %w", err)
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open agent log %s: %w", logPath, err)
	}
	defer logFile.Close()
	// Never write agent activity to stderr/stdout once the TUI's alt
	// screen takes over the terminal -- interleaved log lines would
	// corrupt the display. A separate file the operator can tail
	// (fmt.Fprintln below, before the TUI starts) is the live view
	// for agent internals; the chat pane shows a collapsed line per
	// event live, but agent.log keeps the full verbose detail.
	// Records reach agent.log through internal/logging: levelled, with
	// structured attributes and a component tag on every line. agentLog
	// stays a *log.Logger so every existing call site below, and the three
	// SetLogger seams, are unchanged -- see logging.Stack.StdFor.
	// LevelDebug because the previous logger had no levels at all and
	// dropped nothing; this must not start filtering out what used to be
	// written.
	logStack := logging.New(logFile, slog.LevelDebug)
	// slog's package-level functions write to STDERR by default, which
	// would corrupt the alt screen. Point them at agent.log instead,
	// before anything can log.
	logStack.SetDefault()
	agentLog := logStack.StdFor("agent")
	fmt.Fprintf(os.Stderr, "lazymesh: agent activity logged to %s\n", logPath)
	// 2026-09-08: a respawn attempt/success/failure is otherwise
	// completely invisible -- see mcpclient.Client's own doc comment on
	// SetLogger for why that matters (same "quietly healthy" vs
	// "silently failing" ambiguity today's ringwaiter/meshservices
	// logging fixes already closed elsewhere).
	client.SetLogger(logStack.StdFor("mcp"))
	// The `r` panel's join runs as its own subprocess and its only other
	// trace is a panel the operator dismisses; log it so a failed join
	// is still diagnosable afterwards.
	realmjoin.SetLogger(logStack.StdFor("realm"))

	// The agent loop always runs -- Raf's explicit product decision
	// (macula-io/macula-lazymesh#1, 2026-09-06): "lazymesh should run
	// without that arguments ceremony. lazymesh starts and uses the
	// model, point final." --room/--goal are optional hints passed to
	// runAgent, never a switch for whether the model runs at all.
	p, err := buildProvider(cfg)
	if err != nil {
		return fmt.Errorf("build provider: %w", err)
	}
	// Constructed here, not inside buildToolSource, so the TUI's own `s`
	// (MeshServices panel) can read the exact same Source's Snapshot --
	// found 2026-09-08 (Raf, via Jupiter): this used to be built and
	// wrapped entirely inside buildToolSource, invisible to anything
	// outside it. nil when cfg.MeshServicesEnabled is false, matching
	// buildToolSource's own existing gate -- the panel handles a nil
	// Source as its own disabled state, not a crash.
	var meshSvc *meshservices.Source
	if cfg.MeshServicesEnabled {
		meshSvc = meshservices.New(client)
		meshSvc.SetLogger(logStack.StdFor("mesh"))
	}
	tools, err := buildToolSource(cfg, client, meshSvc)
	if err != nil {
		return fmt.Errorf("build tool source: %w", err)
	}
	tools = agent.NewNoBlockingWaitSource(tools)

	// Startup budget check (2026-09-07, R2): computed here, before
	// anything else starts, so a genuinely undersized context window
	// fails fast with a clear message instead of the same class of
	// crash the runaway-context incident already produced once.
	localToolsReachable := allowlistIncludes(resolveAllowlist(cfg), "shell_exec")
	systemPrompt := buildSystemPrompt(room, goalText, localToolsReachable, cfg.ExpressiveStyle, cfg.MeshServicesEnabled)
	if err := checkStartupBudget(ctx, systemPrompt, tools, p, agentLog); err != nil {
		return err
	}

	// Loop-owned room listening (macula-io/macula-lazymesh#14/#15): the Go
	// loop itself, not the model, blocks in mesh_wait_room per joined
	// room. defer waiterMgr.StopAll() before defer client.Close() above
	// runs (LIFO -- registered after, so it fires first), same ordering
	// #6's sayGoodbye already established for exactly this reason.
	waiterMgr := roomwaiter.New(client, "")
	defer waiterMgr.StopAll()
	initialRooms, err := seedInitialRooms(ctx, client)
	if err != nil {
		return fmt.Errorf("seed initial rooms for room-waiter: %w", err)
	}
	waiterMgr.Sync(ctx, initialRooms)

	// Loop-owned ring listening (2026-09-07, replacing the system
	// prompt's own former blanket ring-check mandate): same teardown
	// ordering as waiterMgr above, deferred before client.Close().
	ringMgr := ringwaiter.New(client, "")
	// SetLogger before Start (2026-09-08, a real live incident that was
	// hard to diagnose from agent.log alone): see ringwaiter.Manager's own
	// doc comment on SetLogger for why this matters -- without it,
	// "ringwaiter is quietly healthy" and "ringwaiter's poll has been
	// failing since startup" were indistinguishable from the log alone.
	ringMgr.SetLogger(logStack.StdFor("ring"))
	defer ringMgr.Stop()
	ringMgr.Start(ctx)

	// The actor core (D4): one embedded node, one root supervisor, one
	// supervised session actor owning the conversation, and a bridge
	// process handing loop events to the TUI, the agent log and the
	// room-waiter. A provider panic now restarts the session instead of
	// killing lazymesh — the whole point of the supervision tree.
	node, err := sessionhost.Node()
	if err != nil {
		return fmt.Errorf("start actor node: %w", err)
	}
	rootPid, err := sessionhost.StartRoot(node)
	if err != nil {
		return fmt.Errorf("start session root: %w", err)
	}

	// Session persistence (D2): every run resolves its session id, wires
	// the JSONL store, and either starts fresh or resumes. --continue and
	// --resume resolve against the workspace-fingerprinted session dir;
	// without them the default id (lazymesh-<pid>) starts a new log.
	resolvedSessionID := sessionID
	if resolvedSessionID == "" {
		resolvedSessionID = fmt.Sprintf("lazymesh-%d", os.Getpid())
	}
	dataDir, err := sessionstore.DefaultDataDir()
	if err != nil {
		return fmt.Errorf("resolve session store dir: %w", err)
	}
	wd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve working directory: %w", err)
	}
	sessionStore, err := sessionstore.New(dataDir, sessionstore.Fingerprint(wd))
	if err != nil {
		return fmt.Errorf("open session store: %w", err)
	}
	if continueLatest {
		if resumeRef != "" {
			return fmt.Errorf("--continue and --resume are the same thing; give one")
		}
		resumeRef = "latest"
	}
	if resumeRef != "" {
		resolvedSessionID, err = sessionstore.Resolve(resumeRef, sessionStore, resolvedSessionID)
		if err != nil {
			return err
		}
	}

	sessPid, err := sessionhost.StartSession(node, rootPid, sessionhost.SessionArgs{
		Provider:     p,
		Tools:        tools,
		SystemPrompt: systemPrompt,
		Store:        sessionStore,
		SessionID:    resolvedSessionID,
		AskTools:     cfg.ToolAsklist,
	})
	if err != nil {
		return fmt.Errorf("start session: %w", err)
	}
	defer sessionhost.StopSession(node, sessPid)
	if _, err := startEventBridge(node, ctx, sessPid, tuiEvents, frontendEvents, agentLog, waiterMgr); err != nil {
		return fmt.Errorf("start event bridge: %w", err)
	}

	// The interrupt bus (Phase 3): the TUI's `x` key and the control
	// socket's interrupt message both land here and cancel the session's
	// in-flight turn via its per-turn context. One goroutine per signal;
	// a turn that isn't running makes Interrupt a no-op.
	interruptCh := make(chan struct{}, 4)
	go func() {
		for range interruptCh {
			sessionhost.Interrupt(sessPid)
		}
	}()

	// The approval bus (G9): the TUI's approval popup and the control
	// socket's approve message both land here and answer the session's
	// outstanding consent question.
	approvalCh := make(chan tui.ApprovalAnswer, 4)
	go func() {
		for answer := range approvalCh {
			sessionhost.AnswerApproval(sessPid, answer.ID, answer.Allow)
		}
	}()

	// The control plane (D1): a unix socket speaking NDJSON both ways,
	// attached to the same bus the TUI uses. input lands on userInputCh
	// exactly like the compose line; queries read live session and mesh
	// state; shutdown ends the headless run.
	var ctrl *frontend.Server
	if socketPath != "" || headless {
		path := socketPath
		if path == "" {
			path = frontend.DefaultSocketPath(resolvedSessionID)
			if path == "" {
				return fmt.Errorf("resolve control socket path for session %s", resolvedSessionID)
			}
		}
		ctrl, err = frontend.Start(frontend.Options{
			Path:      path,
			Input:     userInputCh,
			Events:    frontendEvents,
			Query:     buildQueryHandler(node, sessPid, client),
			Interrupt: func() { sessionhost.Interrupt(sessPid) },
			Approve:   func(id string, allow bool) { sessionhost.AnswerApproval(sessPid, id, allow) },
			SessionID: resolvedSessionID,
			Model:     providerLabel(cfg) + "/" + cfg.Model,
			Log:       logStack.StdFor("frontend"),
		})
		if err != nil {
			return fmt.Errorf("start control plane: %w", err)
		}
		defer ctrl.Close()
	}

	go runAgent(ctx, node, rootPid, sessPid, waiterMgr, ringMgr, agentLog, tuiEvents, frontendEvents, userInputCh)
	agentModelLabel := providerLabel(cfg) + "/" + cfg.Model

	tuiModel := tui.New(client, tui.Options{
		AgentEvents:       tuiEvents,
		UserInputCh:       userInputCh,
		InterruptCh:       interruptCh,
		ApprovalCh:        approvalCh,
		StatusBarPosition: cfg.StatusBarPosition,
		ContactPolicyFile: cfg.ContactPolicyFile,
		AutoAcceptKnown:   config.RingPolicyAutoAcceptsKnown(cfg.RingPolicy),
		AgentModel:        agentModelLabel,
		MeshServices:      meshSvc, // nil when cfg.MeshServicesEnabled is false -- see Options.MeshServices' own doc
		// The l key opens this file in $EDITOR. The same logPath the file
		// was opened from above, so there is one derivation of the path.
		LogPath: logPath,
		// The `r` panel execs macula-mcp-realm directly (internal/realmjoin),
		// never through client -- MaculaMCPVersion is the same value both
		// this and the persistent server's own Spawn resolve their npx
		// invocation from, so (pinned) they run the exact same
		// @macula-io/mcp release, or (empty, the default -- see
		// config.Config.MaculaMCPVersion's own doc comment) both float to
		// npm's latest independently, which is the same release barring a
		// new one landing in the narrow window between the two npx calls.
		// client.IdentityFile() is the RESOLVED identity path that server
		// actually ended up using (not cfg.IdentityFile, which
		// config.Default() deliberately leaves empty most of the time --
		// see mcpclient.Client.IdentityFile's own doc comment), so a fresh
		// join's credential lands under the same node_id this operator's
		// agent is actually presenting on the mesh.
		MaculaMCPVersion:  cfg.MaculaMCPVersion,
		RealmIdentityFile: client.IdentityFile(),
	})

	if headless {
		// No TUI: the session runs until a controller says shutdown or a
		// signal lands. The control socket is the deliberate primary exit
		// path; SIGINT/SIGTERM (ctx) still work without one.
		if ctrl == nil {
			<-ctx.Done()
		} else {
			select {
			case <-ctx.Done():
			case <-ctrl.Shutdown():
			}
		}
	} else {
		program := tea.NewProgram(tuiModel, tea.WithAltScreen())
		_, err = program.Run()
	}
	cancel()

	// Every deliberate exit path converges here: the TUI's own Quit ("q")
	// and ForceQuit (ctrl+c) key bindings both return tea.Quit, and an
	// external SIGINT/SIGTERM is caught by bubbletea's own signal handling
	// (see handleSignals in its source), which also ends program.Run() --
	// independently of this function's own signal.NotifyContext above,
	// which only governs ctx. So a single call here, after program.Run()
	// returns and before the deferred client.Close() below runs, covers
	// mesh_goodbye for all of them; ctx itself must not be reused since
	// it's already cancelled by now on every path (see sayGoodbye's doc).
	sayGoodbye(client, goodbyeTimeout)

	return err
}

// goodbyeTimeout bounds the deliberate mesh_goodbye call at shutdown --
// same posture as every other bounded operation in this codebase
// (maxConsecutiveErrors, maxHistoryMessages): a slow or unresponsive
// macula-mcp must never hang shutdown indefinitely. 20s matches
// meshservices' own callTimeoutMS convention for a bounded mesh
// operation -- verified live: mesh_goodbye does real work (leaves every
// room, publishes agent.goodbye, tears down every subscription), and an
// initial 5s bound was measured too short against the real mesh, timing
// out instead of completing cleanly.
const goodbyeTimeout = 20 * time.Second

// goodbyeCaller is the minimal subset of *mcpclient.Client sayGoodbye
// needs, so its ordering/timeout/error-tolerance behavior has a direct
// unit test against a fake, independent of spawning a real macula-mcp
// subprocess (see main_live_test.go for the real-spawn version).
type goodbyeCaller interface {
	CallTool(ctx context.Context, name string, args map[string]any) (string, error)
}

// sayGoodbye calls mesh_goodbye directly against client -- a deterministic
// shutdown step, not an LLM tool-call decision, so it deliberately bypasses
// the agent loop and DefaultToolAllowlist entirely (macula-io/macula-
// lazymesh#6). Uses a fresh context.Background()-derived timeout rather
// than run()'s own ctx, which is already cancelled by the time every
// caller reaches this point. Best-effort: a failure is logged, never
// returned -- shutdown must complete regardless of whether the mesh got
// the message.
func sayGoodbye(client goodbyeCaller, timeout time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if _, err := client.CallTool(ctx, "mesh_goodbye", nil); err != nil {
		fmt.Fprintln(os.Stderr, "lazymesh: mesh_goodbye failed:", err)
	}
}

func buildProvider(cfg config.Config) (provider.Provider, error) {
	switch cfg.Provider {
	case "", "deepseek":
		key, err := cfg.APIKey()
		if err != nil {
			return nil, err
		}
		return provider.NewDeepSeek(cfg.BaseURL, cfg.Model, key, nil), nil
	case "anthropic":
		key, err := cfg.APIKey()
		if err != nil {
			return nil, err
		}
		return provider.NewAnthropic(cfg.BaseURL, cfg.Model, key), nil
	case "nvidia":
		key, err := cfg.APIKey()
		if err != nil {
			return nil, err
		}
		return provider.NewNVIDIA(cfg.BaseURL, cfg.Model, key, nil), nil
	case "groq":
		key, err := cfg.APIKey()
		if err != nil {
			return nil, err
		}
		return provider.NewGroq(cfg.BaseURL, cfg.Model, key, nil), nil
	default:
		return nil, fmt.Errorf("unknown provider %q", cfg.Provider)
	}
}

// providerLabel resolves cfg.Provider the same way buildProvider's own
// switch does ("" means deepseek), so the status strip's provider/model
// label matches what actually built -- never shows a blank provider name
// just because the operator left config.yaml's provider field unset.
func providerLabel(cfg config.Config) string {
	if cfg.Provider == "" {
		return "deepseek"
	}
	return cfg.Provider
}

// buildToolSource combines macula-mcp, Phase 3's mesh-service tools when
// meshSvc is non-nil (real, curated, currently-discovered mesh
// procedures, dogfooding the mesh's own service directory per the plan's
// actual thesis -- but OFF by default since R2, 2026-09-07: see
// config.MeshServicesEnabled's own doc comment for why), and Phase 2's
// local shell/file tools when explicitly enabled -- then wraps whatever
// that is in an AllowlistSource. The allowlist is the actual gate: an
// agent's entire conversation can be steered by arbitrary mesh peers (room
// messages, ring purposes), so what the model is ALLOWED to see or call
// matters independently of what sources merely exist. cfg.LocalTools.Enabled
// controls whether shell_exec/read_file/write_file are wired up at all; it
// does NOT put them on the allowlist by itself -- see config.ToolAllowlist
// and internal/agent/allowlist.go.
//
// meshSvc is constructed by the caller (run), not here, as of 2026-09-08 --
// see run's own comment on why: the TUI's `s` panel needs the identical
// Source instance to read via Snapshot, not a second one wrapping the
// same client. Must be nil exactly when cfg.MeshServicesEnabled is false
// (the caller's job to keep those in sync -- see run).
func buildToolSource(cfg config.Config, client *mcpclient.Client, meshSvc *meshservices.Source) (agent.ToolSource, error) {
	sources := []agent.ToolSource{client}
	if meshSvc != nil {
		sources = append(sources, meshSvc)
	}
	if cfg.LocalTools.Enabled {
		local, err := localtools.New(localtools.Config{
			Enabled:      cfg.LocalTools.Enabled,
			WorkingDir:   cfg.LocalTools.WorkingDir,
			ShellTimeout: time.Duration(cfg.LocalTools.ShellTimeoutSeconds) * time.Second,
		})
		if err != nil {
			return nil, fmt.Errorf("local tools: %w", err)
		}
		sources = append(sources, local)
	}
	combined := agent.NewMultiSource(sources...)
	allowed := agent.NewAllowlistSource(combined, resolveAllowlist(cfg))

	// Terse-ified last, after allowlisting -- see internal/agent/terse.go's
	// own doc comment (R2, 2026-09-07): only shortens what actually
	// reaches the model, never wastes work rewriting a tool the allowlist
	// would filter out anyway.
	return agent.NewTerseDescriptionSource(allowed), nil
}

// resolveAllowlist is the single place cfg.ToolAllowlist gets defaulted,
// so buildToolSource's actual enforcement and runAgent's system-prompt
// claim about available tools can never drift apart -- telling the model
// it has a tool the allowlist then refuses is worse than not mentioning
// it. The default is agent's own conversational primitives, PLUS Phase 3's
// curated mesh-service tool names only when cfg.MeshServicesEnabled is set
// (meshservices stays out of package agent to avoid a reverse dependency;
// this is the single place the two defaults get merged) -- this condition
// MUST match buildToolSource's own cfg.MeshServicesEnabled check, or the
// allowlist and the actual wired-up sources drift apart exactly the way
// this function's own job is to prevent.
func resolveAllowlist(cfg config.Config) []string {
	if len(cfg.ToolAllowlist) > 0 {
		return cfg.ToolAllowlist
	}
	names := append([]string{}, agent.DefaultToolAllowlist...)
	if cfg.MeshServicesEnabled {
		names = append(names, meshservices.AllowedToolNames()...)
	}
	return names
}

func allowlistIncludes(allowlist []string, name string) bool {
	for _, n := range allowlist {
		if n == name {
			return true
		}
	}
	return false
}

// fixedPrefixTargetTokens is R2's own design target (Fable's review,
// 2026-09-07): what the fixed prefix (system prompt + tool schemas)
// SHOULD fit under, engineered toward directly by the tool-schema work
// in this same change (dropping mesh_hello, TerseDescriptionSource,
// meshservices' once-per-session discovery). Not itself a hard-fail
// threshold -- see checkStartupBudget's own doc comment for that -- just
// what gets logged alongside the real measurement so a regression that
// creeps back over it is visible without needing to know this number by
// heart.
const fixedPrefixTargetTokens = 1500

// estimateTokens is a rough, deliberately-approximate byte/4 heuristic,
// not a real tokenizer -- same reasoning as internal/agent's
// maxHistoryBytes: no tokenizer dependency exists in this codebase, and
// an approximate, provider-agnostic budget is the deliberate choice
// (real per-provider token counts vary and would need one per backend).
// Good enough for "is this roughly on target," not exact accounting.
func estimateTokens(byteLen int) int {
	return byteLen / 4
}

// checkStartupBudget computes the fixed prefix's real size (system
// prompt + every tool's Description/InputSchema, marshaled exactly as
// agent.Loop.Say sends them) against the configured provider's real
// ContextWindow(), and refuses to start if it doesn't leave enough room
// to function at all.
//
// Hard-fail, not just a log line (2026-09-07, Fable's own R2 spec: fail
// when the actual budget check comes up short for whatever's
// configured, don't hard-fail generically) -- conditional on the
// PROVIDER's real window, not the fixedPrefixTargetTokens design target
// above: neither DeepSeek nor NVIDIA (both ~1M tokens today) will ever
// trip this, since even an untrimmed fixed prefix is a tiny fraction of
// a million-token window. The threshold this actually enforces: the
// fixed prefix must not consume more than half the configured window --
// a genuine "would this even be able to hold one real exchange" floor,
// not an optimization target, so a legitimately small-context model
// (the whole reason this check exists) isn't refused just for being
// small, only for being too small to function with what's currently
// configured.
func checkStartupBudget(ctx context.Context, systemPrompt string, tools agent.ToolSource, p provider.Provider, agentLog *log.Logger) error {
	listed, err := tools.ListTools(ctx)
	if err != nil {
		return fmt.Errorf("startup budget check: list tools: %w", err)
	}
	specs := agent.ToolSpecsFrom(listed)
	toolsJSON, err := json.Marshal(specs)
	if err != nil {
		return fmt.Errorf("startup budget check: marshal tool specs: %w", err)
	}
	fixedPrefixTokens := estimateTokens(len(systemPrompt) + len(toolsJSON))
	window := p.ContextWindow()

	line := fmt.Sprintf(
		"[budget] fixed prefix ~%d tokens (target <%d) against a %d-token context window (%d tools)",
		fixedPrefixTokens, fixedPrefixTargetTokens, window, len(listed),
	)
	if agentLog != nil {
		agentLog.Print(line)
	}

	if window > 0 && fixedPrefixTokens*2 > window {
		fmt.Fprintln(os.Stderr, "lazymesh:", line)
		return fmt.Errorf(
			"startup budget check failed: fixed prefix (~%d tokens) leaves too little of this model's %d-token context window to function -- reduce the tool allowlist or configure a larger-context model",
			fixedPrefixTokens, window,
		)
	}
	return nil
}

// agentLogPath is where agent activity is logged instead of stderr, since
// stderr is unsafe to write to once the TUI's alt screen is active.
func agentLogPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".config", "lazymesh")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(dir, "agent.log"), nil
}

// buildSystemPrompt is the agent's fixed opening instruction, pulled out of
// runAgent as its own pure function so the room-scoping behavior (macula-
// io/macula-lazymesh#1) has a direct unit test independent of the loop's
// I/O. room and goalText are both optional hints, never a restriction: the
// model is told to discover its actual participation scope live via
// mesh_rooms, since a room joined later via an accepted ring must be
// covered too, not just whatever room this function was called with.
func buildSystemPrompt(room, goalText string, localToolsReachable, expressiveStyle, meshServicesEnabled bool) string {
	// Deliberately says nothing about why this changed (macula-io/macula-
	// lazymesh#14/#15's own history) -- that belongs in code comments and
	// the issue tracker, not in tokens sent to the model on every single
	// cycle; the model only needs the current instruction.
	//
	// No longer mentions rings (2026-09-07, replacing the former blanket
	// "every single time, call mesh_read_inbox with no room_topic and
	// check rings.pending" mandate here): internal/ringwaiter now watches
	// for a pending ring itself, at zero LLM cost, and wakes the model
	// with a ring-specific prompt (see ringArrivalPrompt) only when one
	// actually exists -- the model no longer needs to be told to go
	// looking for one on every unrelated turn.
	const waitingLine = "You do not need to wait for messages yourself: the harness is watching " +
		"every room you are in (and separately watching for any ring addressed to you) and will " +
		"prompt you the moment something new arrives. Do not call mesh_say/mesh_wait_room just to " +
		"wait for a reply -- when you genuinely have nothing to add right now, reply briefly (or " +
		"with nothing) and your turn simply ends until the harness wakes you again."
	// mesh_service_* is mentioned ONLY when actually wired up (2026-09-07,
	// R2's close-out): config.MeshServicesEnabled defaults false, and
	// telling the model about a tool buildToolSource never built is worse
	// than not mentioning it -- resolveAllowlist's own doc comment already
	// requires this file and buildToolSource stay in lockstep for the same
	// reason.
	toolsLine := "You have macula-mcp's mesh_* tools."
	if meshServicesEnabled {
		toolsLine = "You have macula-mcp's mesh_* tools, plus mesh_service_* tools that call real " +
			"mesh services (search/knowledge-graph/forum capabilities, discovered live) -- prefer " +
			"a mesh_service_* tool over guessing at an answer when the task fits one. Their exact " +
			"arguments aren't advertised; if a call errors, read the error and retry with corrected " +
			"arguments rather than giving up after one attempt."
	}
	if localToolsReachable {
		toolsLine += " You also have shell_exec/read_file/write_file scoped to a local working " +
			"directory -- use those only when actual local work (not just mesh conversation or " +
			"a mesh service) is genuinely called for."
	}
	roomHint := ""
	if room != "" {
		roomHint = fmt.Sprintf(
			" In particular, prioritize this room: %s -- check mesh_rooms's own "+
				"joined list first and only call mesh_join_room if that room_topic is "+
				"not already there, introduce yourself briefly, and participate "+
				"naturally: read what others say, respond when it makes sense.",
			room,
		)
	}
	systemPrompt := fmt.Sprintf(
		"You are a lazymesh agent cooperating with other agents on the Macula mesh. "+
			"%s%s "+
			"Your participation scope is every room you are currently a member of, not "+
			"just one you were pointed at -- call mesh_rooms (no arguments) to find out "+
			"which rooms that is. Its joined list is the definitive record of what you "+
			"have already joined -- never call mesh_join_room for a room_topic already "+
			"in that list, even if you don't recall joining it earlier in this "+
			"conversation; check mesh_rooms fresh each time instead of relying on "+
			"conversation memory, which gets trimmed. %s",
		toolsLine, roomHint, waitingLine)
	if expressiveStyle {
		systemPrompt += " Feel free to be expressive in room conversation (mesh_say text) when " +
			"talking to other agents -- emoji and status markers like ❌/⏳/✅ are welcome where " +
			"they fit naturally. Don't force it into every message, and keep it to conversation " +
			"text, not tool arguments."
	}
	if goalText != "" {
		systemPrompt += " Additional objective: " + goalText
	}
	return systemPrompt
}

// runAgent drives the supervised session actor for as long as the program
// runs: it blocks on one Say at a time (the session runs the loop; the
// driver supplies prompts and cadence), reports cycle usage, and waits on
// nextEvent between turns — the same sequential shape the old
// goroutine-owned Loop had, now with the conversation itself owned by a
// supervised actor.
//
// A Say whose Call fails means the session PROCESS died (a panic-restart);
// the driver reattaches to its supervised replacement and retries the same
// prompt, because a crashed turn was never answered — that recovery path
// is exactly what supervision buys. When no replacement appears, the
// failure falls into the ordinary backoff accounting. Room/ring waiting
// stays waiterMgr's and ringMgr's job (macula-io/macula-lazymesh#14/#15):
// this function supplies the cadence, driven by real events.
func runAgent(ctx context.Context, n gen.Node, root, sessPid gen.PID, waiterMgr *roomwaiter.Manager, ringMgr *ringwaiter.Manager, agentLog *log.Logger, tuiEvents, frontendEvents chan<- agent.Event, userInputCh <-chan string) {
	consecutiveErrors := 0
	backoff := initialBackoff
	prompt := agentInitialPrompt

	for {
		if ctx.Err() != nil {
			return
		}

		usageBefore, err := sessionhost.SessionStatus(n, sessPid)
		if err != nil {
			// The session died before this turn even started. Reattach
			// and start over with the same prompt — Say never ran, so
			// nothing was answered twice.
			newPid, rerr := reattachSession(ctx, n, root, sessPid, agentLog)
			if rerr != nil {
				if !failCycle(ctx, &consecutiveErrors, &backoff, agentLog, tuiEvents, frontendEvents, rerr) {
					return
				}
				prompt, _ = nextEvent(ctx, userInputCh, waiterMgr, ringMgr)
				continue
			}
			sessPid = newPid
			continue
		}

		reply, err := sessionhost.SayTurn(n, sessPid, prompt)
		if err != nil {
			// The session died mid-turn. Its replacement gets the SAME
			// prompt: the crashed turn was never answered, and the fresh
			// conversation has no record of it.
			newPid, rerr := reattachSession(ctx, n, root, sessPid, agentLog)
			if rerr != nil {
				if !failCycle(ctx, &consecutiveErrors, &backoff, agentLog, tuiEvents, frontendEvents, rerr) {
					return
				}
				prompt, _ = nextEvent(ctx, userInputCh, waiterMgr, ringMgr)
				continue
			}
			sessPid = newPid
			continue
		}
		if errors.Is(reply.Err, context.Canceled) {
			// An interrupt (Phase 3): the turn was deliberately cut
			// short, not a failure. The loop already emitted the
			// EventError and the session its turn-complete marker; the
			// interrupted message stays in the conversation history as
			// the fact it is, and whatever the operator typed next (or
			// the next room/ring event) picks the cadence back up.
			agentLog.Printf("lazymesh agent: turn interrupted")
			prompt, _ = nextEvent(ctx, userInputCh, waiterMgr, ringMgr)
			continue
		}
		if reply.Err != nil {
			// An ordinary failed turn: the session survived, the loop
			// already emitted the EventError. Count it, back off, wait
			// for the next reason to run.
			if !failCycle(ctx, &consecutiveErrors, &backoff, agentLog, tuiEvents, frontendEvents, reply.Err) {
				return
			}
			prompt, _ = nextEvent(ctx, userInputCh, waiterMgr, ringMgr)
			continue
		}

		consecutiveErrors = 0
		backoff = initialBackoff
		usageAfter, err := sessionhost.SessionStatus(n, sessPid)
		if err != nil {
			// The session died between Say's reply and this status read —
			// rare, but the loop below handles it like any death.
			usageAfter = sessionhost.Status{}
		}
		logCycleUsage(agentLog, usageBefore.Usage, usageAfter.Usage)
		// The session itself emitted the turn's EventListening (it must
		// ride the same delivery path as the turn's deltas — see
		// session.runSay); this driver emits it only on the failure
		// path, where no session events are in flight.
		var ok bool
		prompt, ok = nextEvent(ctx, userInputCh, waiterMgr, ringMgr)
		if !ok {
			return
		}
	}
}

// reattachSession finds the supervised replacement of a session whose
// process died (panic → supervisor restart) and verifies it answers a
// status probe before handing it back. Bounded so a crash-looping session
// — whose restarts the supervisor's own intensity limit eventually
// exhausts — cannot keep the driver busy forever. With one session per
// root, any other pid in the root's list IS the replacement; per-session
// identity arrives with OD2's session ids, not with pid matching.
func reattachSession(ctx context.Context, n gen.Node, root, oldPid gen.PID, agentLog *log.Logger) (gen.PID, error) {
	agentLog.Printf("lazymesh agent: session %s died -- reattaching to its supervised replacement", oldPid)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return gen.PID{}, ctx.Err()
		}
		pids, err := sessionhost.Sessions(n, root)
		if err != nil {
			return gen.PID{}, err
		}
		for _, pid := range pids {
			if pid == oldPid {
				continue
			}
			if _, err := sessionhost.SessionStatus(n, pid); err != nil {
				continue
			}
			agentLog.Printf("lazymesh agent: reattached to session %s", pid)
			return pid, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return gen.PID{}, fmt.Errorf("no replacement session appeared for %s", oldPid)
}

// buildQueryHandler answers the control plane's query messages: status
// reads the supervised session; rooms/inbox/agents/realms call the mesh
// directly through the client, bypassing the model — the same
// deterministic-harness-plumbing posture as seedInitialRooms. Mesh tool
// results travel as parsed JSON when they parse, raw text otherwise.
func buildQueryHandler(n gen.Node, sessPid gen.PID, client *mcpclient.Client) func(ctx context.Context, what string) (any, error) {
	meshTool := map[string]string{
		"rooms":  "mesh_rooms",
		"inbox":  "mesh_read_inbox",
		"agents": "mesh_agents",
		"realms": "mesh_list_realms",
	}
	return func(ctx context.Context, what string) (any, error) {
		switch what {
		case "status":
			st, err := sessionhost.SessionStatus(n, sessPid)
			if err != nil {
				return nil, err
			}
			return map[string]any{
				"message_count": st.MessageCount,
				"total_tokens":  st.Usage.TotalTokens,
			}, nil
		}
		if tool, ok := meshTool[what]; ok {
			result, err := client.CallTool(ctx, tool, nil)
			if err != nil {
				return nil, err
			}
			var data any
			if err := json.Unmarshal([]byte(result), &data); err != nil {
				return result, nil
			}
			return data, nil
		}
		return nil, fmt.Errorf("unknown query %q (want status|rooms|inbox|agents|realms)", what)
	}
}

// maxConsecutiveErrors bounds how long the driver keeps retrying after
// repeated failures (found by an adversarial review, 2026-09-06):
// unbounded retries meant a wedged provider -- or a peer deliberately
// flooding the room to force context-overflow errors -- left the loop
// silently spinning forever while the TUI still looked healthy.
const maxConsecutiveErrors = 8

// failCycle is the ordinary failure path, verbatim from the old runAgent:
// count, report, back off with growth, and stop after
// maxConsecutiveErrors so a wedged provider never retries forever
// silently. Returns false when the driver should stop.
func failCycle(ctx context.Context, consecutiveErrors *int, backoff *time.Duration, agentLog *log.Logger, tuiEvents, frontendEvents chan<- agent.Event, err error) bool {
	*consecutiveErrors++
	agentLog.Printf("lazymesh agent: %s (consecutive failures: %d/%d)", err, *consecutiveErrors, maxConsecutiveErrors)
	if *consecutiveErrors >= maxConsecutiveErrors {
		agentLog.Printf("lazymesh agent: stopping after %d consecutive failures -- not retrying forever silently", *consecutiveErrors)
		emitTui(tuiEvents, frontendEvents, agent.Event{Kind: agent.EventMaxFailuresReached})
		return false
	}
	emitTui(tuiEvents, frontendEvents, agent.Event{Kind: agent.EventBackoff})
	select {
	case <-ctx.Done():
		return false
	case <-time.After(*backoff):
	}
	*backoff = nextBackoff(*backoff)
	emitTui(tuiEvents, frontendEvents, agent.Event{Kind: agent.EventListening})
	return true
}

// emitTui forwards a driver-level event to the TUI without ever blocking
// on it — the TUI is a slow, human-paced consumer and must never stall
// the agent's cadence.
func emitTui(tuiEvents, frontendEvents chan<- agent.Event, ev agent.Event) {
	select {
	case tuiEvents <- ev:
	default:
	}
	select {
	case frontendEvents <- ev:
	default:
	}
}

// logCycleUsage reports both this cycle's own token cost (the delta since
// usageBefore) and the running total, so an operator watching agent.log
// sees cost accumulate live instead of only discovering it after a hard
// context-length failure (found investigating the 2026-09-07 runaway-
// context incident: Loop.Usage() existed already but had zero call sites
// anywhere in this codebase). cacheHit/cacheMiss are 0/0 on a backend
// that doesn't report the split (NVIDIA, currently) -- printed anyway
// rather than omitted, so their being zero is visible as a fact about
// that backend, not silently missing output a reader might mistake for a
// bug.
func logCycleUsage(agentLog *log.Logger, before, after provider.Usage) {
	agentLog.Printf(
		"[usage] this cycle: %d prompt + %d completion = %d total tokens (cache hit=%d miss=%d) -- running total: %d tokens",
		after.PromptTokens-before.PromptTokens,
		after.CompletionTokens-before.CompletionTokens,
		after.TotalTokens-before.TotalTokens,
		after.PromptCacheHitTokens-before.PromptCacheHitTokens,
		after.PromptCacheMissTokens-before.PromptCacheMissTokens,
		after.TotalTokens,
	)
}

// nextPrompt prefers a message the human composed in the TUI (drained
// non-blocking -- if nothing is waiting, the loop keeps its own ambient
// cadence) over the default "check for anything new" prompt. A message
// typed while the agent is mid-Say() is picked up once that call returns,
// not instantly -- no in-flight call gets interrupted for it; documented
// as a known lag, not treated as a bug, matching this project's own
// "no theatre" lean-MVP scope. Only reachable today as nextEvent's
// defensive fallback if waiterMgr is ever nil (production always
// constructs a real one) -- kept as its own tested function rather than
// inlined, since that fallback still needs to behave correctly.
func nextPrompt(userInputCh <-chan string, defaultPrompt string) string {
	select {
	case msg := <-userInputCh:
		return msg
	default:
		return defaultPrompt
	}
}

// roomArrivalPrompt is what a room's Arrival becomes as the next Say()
// prompt -- reuses the model's existing, already-allowlisted
// mesh_read_inbox tool-use pattern rather than carrying raw envelope
// content through roomwaiter.Arrival itself.
func roomArrivalPrompt(room string) string {
	return fmt.Sprintf(
		"New activity in room %s. Call mesh_read_inbox for that room_topic and respond if "+
			"warranted, same as you would on any other cycle.",
		room,
	)
}

// ringArrivalPrompt is what a ringwaiter.Ring becomes as the next Say()
// prompt (2026-09-07, replacing the system prompt's own former blanket
// ring-check mandate): unlike roomArrivalPrompt, this carries the ring's
// own details directly rather than pointing the model at another tool
// call first -- ringwaiter already read them, and there is no cheaper,
// ring-scoped follow-up call to make instead the way
// mesh_read_inbox(room_topic=...) is for a room.
//
// Deliberately does not try to resolve the race with the TUI's own
// human-facing ring pop-up (internal/tui/ringpopup.go) -- that relationship
// (human-popup-primary, model as fallback answerer) already exists today
// and is out of scope here; see ringpopup.go's own comment on Esc leaving
// a ring for runAgent's per-cycle checking to still pick up.
func ringArrivalPrompt(r ringwaiter.Ring) string {
	return fmt.Sprintf(
		"A ring is pending from %s: %q (ring_id=%s). Decide whether to accept or decline based on "+
			"its stated purpose and call mesh_answer_ring for this ring_id -- no need to check any "+
			"other room or ring, just this one. Accepting puts you in a new room, which becomes "+
			"part of your scope from then on too.",
		r.FromPetname, r.Purpose, r.RingID,
	)
}

// nextEvent replaced nextPrompt's instant fallback (macula-io/macula-
// lazymesh#14/#15): it BLOCKS until there is an actual reason to run
// another Say() round -- a human message, a room arrival, or a ring
// arrival -- rather than looping the provider for free on a synthetic
// "check for anything new" prompt every cycle. That instant fallback was
// exactly right for the OLD design (the model's own long mesh_say wait
// supplied the pacing); once that wait moved out of the model's own
// tool-calling turn, something has to supply it here instead.
//
// No periodic idle tick (2026-09-07, removed once ringwaiter covered
// rings the same zero-LLM-cost way roomwaiter already covers rooms):
// with both signal types now event-driven, a periodic "check anyway"
// fallback had nothing left to justify it -- Fable's own review of a
// live instance found 388/464 cycles were purely mechanical, empty
// read_inbox+rooms checks, exactly the cost this removes. A quiet mesh
// now produces zero ChatCompletion calls.
//
// Fairness/priority policy (Atlas's flagged gap in #14, made a real,
// deliberate decision for #15 rather than left as a spike-only
// tradeoff): human input is checked non-blockingly FIRST, so an
// already-pending human message always wins outright. Only once nothing
// is immediately pending does this block on a real select across every
// channel -- in the rare case two sources become ready at the exact same
// instant during that block, Go's own select-among-ready-cases
// randomization decides, not a strict priority. Closing this fully would
// mean a second, always-running non-blocking check loop (spin on
// userInputCh between every select wakeup instead of trusting the select
// itself), trading a rare, microsecond-scale tie for a permanently more
// complex loop -- judged not worth it: the spike measured human-input
// pickup at ~1 microsecond when nothing else was ready, so the window
// where a genuine tie is even possible is only ever a few microseconds
// wide. Room-arrival fairness AMONG rooms is roomwaiter.Manager's own
// job (see its doc comment); ring fairness is ringwaiter.Manager's own
// (surfaced-set keyed on ring_id) -- this function just consumes
// whatever each hands back.
//
// waiterMgr == nil or ringMgr == nil is a defensive fallback, never hit
// in production (cmd/lazymesh always constructs real Managers) --
// preserves nextPrompt's old behavior so a future caller that somehow
// doesn't have one yet still gets a safe, tested default rather than a
// nil-pointer panic.
//
// Returns ok=false only when ctx is done -- the caller should stop the
// loop, not call Say with an empty prompt.
func nextEvent(ctx context.Context, userInputCh <-chan string, waiterMgr *roomwaiter.Manager, ringMgr *ringwaiter.Manager) (string, bool) {
	if waiterMgr == nil || ringMgr == nil {
		return nextPrompt(userInputCh, "check for anything new"), true
	}

	select {
	case msg := <-userInputCh:
		return msg, true
	default:
	}

	select {
	case <-ctx.Done():
		return "", false
	case msg := <-userInputCh:
		return msg, true
	case arrival := <-waiterMgr.Arrivals():
		waiterMgr.Ack(arrival.RoomTopic)
		return roomArrivalPrompt(arrival.RoomTopic), true
	case ring := <-ringMgr.Arrivals():
		return ringArrivalPrompt(ring), true
	}
}

// parseJoinedRooms extracts room_topic values from mesh_rooms's own
// {"joined": [{"room_topic": ...}, ...], ...} result shape -- the single
// place that shape is depended on, so a future mesh_rooms change only
// needs updating here.
func parseJoinedRooms(resultJSON string) []string {
	var parsed struct {
		Joined []struct {
			RoomTopic string `json:"room_topic"`
		} `json:"joined"`
	}
	if err := json.Unmarshal([]byte(resultJSON), &parsed); err != nil {
		return nil
	}
	rooms := make([]string, 0, len(parsed.Joined))
	for _, j := range parsed.Joined {
		rooms = append(rooms, j.RoomTopic)
	}
	return rooms
}

// parseRoomTopic extracts a top-level "room_topic" string field --
// mesh_join_room/mesh_leave_room/mesh_ring's own result shape (unlike
// mesh_rooms's own {"joined": [...]} array, each of these reports exactly
// one room). "" on anything unparseable or absent, e.g. an error result
// from a failed call -- never misfires waiterMgr.Add/Remove on those.
func parseRoomTopic(resultJSON string) string {
	var parsed struct {
		RoomTopic string `json:"room_topic"`
	}
	if err := json.Unmarshal([]byte(resultJSON), &parsed); err != nil {
		return ""
	}
	return parsed.RoomTopic
}

// parseAnsweredRingRoom extracts mesh_answer_ring's own room_topic, but
// only when answer 1 (accept) actually joined it -- ring_service.ts's
// answerPendingRing always reports room_topic in its result, accept or
// decline, but only calls joinRoom on accept. Reporting a decline's
// room_topic here would tell waiterMgr to watch a room this agent was
// never actually added to.
func parseAnsweredRingRoom(resultJSON string) (string, bool) {
	var parsed struct {
		Answer    int    `json:"answer"`
		RoomTopic string `json:"room_topic"`
	}
	if err := json.Unmarshal([]byte(resultJSON), &parsed); err != nil || parsed.Answer != 1 || parsed.RoomTopic == "" {
		return "", false
	}
	return parsed.RoomTopic, true
}

// seedInitialRooms calls mesh_rooms directly (bypassing the model, same
// deterministic-harness-plumbing posture as sayGoodbye/#6) so
// roomwaiter.Manager has a starting room set before the agent loop's own
// first cycle -- otherwise the loop would wait for the model to
// spontaneously call mesh_rooms before watching anything.
func seedInitialRooms(ctx context.Context, client *mcpclient.Client) ([]string, error) {
	result, err := client.CallTool(ctx, "mesh_rooms", nil)
	if err != nil {
		return nil, err
	}
	return parseJoinedRooms(result), nil
}

const (
	initialBackoff = 5 * time.Second
	maxBackoff     = 2 * time.Minute
)

// agentInitialPrompt is runAgent's one-time startup nudge, hoisted to
// package level (rather than a local const inside runAgent) so its
// room-scoping content has a direct unit test. Runs exactly once, when
// there is genuinely no prior context yet to catch up on -- every cycle
// after this one is driven by a specific human message, room arrival, or
// ring arrival instead (see nextEvent), not a repeated generic nudge.
//
// Still mentions rings explicitly, unlike everything after it (2026-09-07,
// once internal/ringwaiter took over ongoing ring-checking): ringwaiter's
// own watch() does the same startup catch-up read now too (2026-09-08,
// switched from polling to a real blocking mesh_wait_ring call -- see
// that package's own doc comment), but this stays as a second, one-time,
// low-cost redundancy at the model level rather than being removed --
// not the ongoing per-cycle waste that made the old blanket mandate a
// problem, just startup-correctness belt and suspenders.
//
// Investigated 2026-09-06 after a live "ring stuck deferred" report -- that
// specific incident turned out to be an instance Raf stopped himself
// mid-test, not a bug, but the underlying gap is real regardless: the
// system prompt alone saying "answer any ring addressed to you" wasn't
// reliably driving the model to actually check for one, since every
// per-turn prompt only ever mentioned "the room." Extended macula-io/
// macula-lazymesh#1 (2026-09-06): "the room" is now "every room
// mesh_rooms reports," not one hardcoded topic -- a room joined later via
// an accepted ring must stay in scope too.
const agentInitialPrompt = "First, call mesh_read_inbox (no room_topic) and answer any pending ring " +
	"addressed to you via mesh_answer_ring. Then call mesh_rooms; join any room you were " +
	"pointed at only if its room_topic is not already in mesh_rooms's own joined list, and " +
	"start participating in every room you are a member of."

// nextBackoff doubles d, capped at maxBackoff -- pulled out as a pure
// function so the growth/cap behavior has its own test independent of the
// retry loop's I/O.
func nextBackoff(d time.Duration) time.Duration {
	d *= 2
	if d > maxBackoff {
		return maxBackoff
	}
	return d
}

func logEvent(agentLog *log.Logger, ev agent.Event) {
	switch ev.Kind {
	case agent.EventAssistantMessage:
		agentLog.Printf("[agent] %s", ev.Text)
	case agent.EventToolCall:
		agentLog.Printf("[tool call] %s(%s)", ev.ToolName, ev.Text)
	case agent.EventToolResult:
		agentLog.Printf("[tool result] %s -> %s", ev.ToolName, ev.Text)
	case agent.EventError:
		agentLog.Printf("[error] %s: %s", ev.ToolName, ev.Err)
	case agent.EventBackoff:
		agentLog.Printf("[backoff] agent hit an error, backing off before retrying")
	case agent.EventMaxFailuresReached:
		agentLog.Printf("[stopped] agent stopped after repeated failures")
	case agent.EventListening:
		agentLog.Printf("[listening] waiting for the next human message, room arrival, or periodic check")
	}
}
