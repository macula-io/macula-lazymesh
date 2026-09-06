// Command lazymesh is a single binary that spawns macula-mcp as its only
// tool source, drives a configurable LLM agent loop against it, and shows
// a live TUI of mesh rooms, pending rings, and agent presence. See
// plans/PLAN_LAZYMESH_MVP.md for the full design.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/macula-io/macula-lazymesh/internal/agent"
	"github.com/macula-io/macula-lazymesh/internal/config"
	"github.com/macula-io/macula-lazymesh/internal/contactpolicy"
	"github.com/macula-io/macula-lazymesh/internal/localtools"
	"github.com/macula-io/macula-lazymesh/internal/mcpclient"
	"github.com/macula-io/macula-lazymesh/internal/meshservices"
	"github.com/macula-io/macula-lazymesh/internal/provider"
	"github.com/macula-io/macula-lazymesh/internal/roomwaiter"
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
	spikeRoomWaiters := flag.Bool("spike-room-waiters", false,
		"EXPERIMENTAL, macula-io/macula-lazymesh#14 spike: move room-listening out of the model's own "+
			"tool calls into loop-owned per-room goroutines blocked in mesh_wait_room, instead of the "+
			"model calling mesh_say/mesh_wait_room with a long wait itself. Off by default -- today's "+
			"behavior is the baseline this flag measures against.")
	flag.Parse()

	if *showVersion {
		fmt.Printf("lazymesh %s (commit %s, built %s)\n", version, commit, date)
		return
	}

	if err := run(*configPath, *room, *goalText, *spikeRoomWaiters); err != nil {
		fmt.Fprintln(os.Stderr, "lazymesh:", err)
		os.Exit(1)
	}
}

func run(configPath, room, goalText string, spikeRoomWaiters bool) error {
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
	userInputCh := make(chan string, 8)

	// The agent loop always runs -- Raf's explicit product decision
	// (macula-io/macula-lazymesh#1, 2026-09-06): "lazymesh should run
	// without that arguments ceremony. lazymesh starts and uses the
	// model, point final." --room/--goal are optional hints passed to
	// runAgent, never a switch for whether the model runs at all.
	p, err := buildProvider(cfg)
	if err != nil {
		return fmt.Errorf("build provider: %w", err)
	}
	tools, err := buildToolSource(cfg, client)
	if err != nil {
		return fmt.Errorf("build tool source: %w", err)
	}

	// #14 spike only, below this point: everything above is the
	// unmodified production path. waiterMgr stays nil in the default
	// (non-spike) run, and every spike-specific call site checks that
	// before doing anything -- this is what keeps today's behavior
	// available as the actual measurement baseline from the same binary.
	var waiterMgr *roomwaiter.Manager
	if spikeRoomWaiters {
		tools = agent.NewNoBlockingWaitSource(tools)
		waiterMgr = roomwaiter.New(client, "")
		defer waiterMgr.StopAll() // before client.Close() below, same ordering #6 already established
		initial, err := seedInitialRooms(ctx, client)
		if err != nil {
			return fmt.Errorf("seed initial rooms for room-waiter spike: %w", err)
		}
		waiterMgr.Sync(ctx, initial)
	}

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
	agentLog := log.New(logFile, "", log.LstdFlags)
	fmt.Fprintf(os.Stderr, "lazymesh: agent activity logged to %s\n", logPath)
	localToolsReachable := allowlistIncludes(resolveAllowlist(cfg), "shell_exec")
	go runAgent(ctx, p, tools, room, goalText, localToolsReachable, cfg.ExpressiveStyle, waiterMgr, agentLog, tuiEvents, userInputCh)
	agentModelLabel := providerLabel(cfg) + "/" + cfg.Model

	tuiModel := tui.New(client, tui.Options{
		AgentEvents:       tuiEvents,
		UserInputCh:       userInputCh,
		StatusBarPosition: cfg.StatusBarPosition,
		ContactPolicyFile: cfg.ContactPolicyFile,
		AutoAcceptKnown:   config.RingPolicyAutoAcceptsKnown(cfg.RingPolicy),
		AgentModel:        agentModelLabel,
	})
	program := tea.NewProgram(tuiModel, tea.WithAltScreen())
	_, err = program.Run()
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

// buildToolSource combines macula-mcp, Phase 3's mesh-service tools
// (always on -- real, curated, currently-discovered mesh procedures,
// dogfooding the mesh's own service directory per the plan's actual
// thesis), and Phase 2's local shell/file tools when explicitly enabled
// -- then wraps whatever that is in an AllowlistSource. The allowlist is
// the actual gate: an agent's entire conversation can be steered by
// arbitrary mesh peers (room messages, ring purposes), so what the model
// is ALLOWED to see or call matters independently of what sources merely
// exist. cfg.LocalTools.Enabled controls whether shell_exec/read_file/
// write_file are wired up at all; it does NOT put them on the allowlist
// by itself -- see config.ToolAllowlist and internal/agent/allowlist.go.
func buildToolSource(cfg config.Config, client *mcpclient.Client) (agent.ToolSource, error) {
	sources := []agent.ToolSource{client, meshservices.New(client)}
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

	return agent.NewAllowlistSource(combined, resolveAllowlist(cfg)), nil
}

// resolveAllowlist is the single place cfg.ToolAllowlist gets defaulted,
// so buildToolSource's actual enforcement and runAgent's system-prompt
// claim about available tools can never drift apart -- telling the model
// it has a tool the allowlist then refuses is worse than not mentioning
// it. The default is agent's own conversational primitives PLUS Phase 3's
// curated mesh-service tool names (meshservices stays out of package
// agent to avoid a reverse dependency; this is the single place the two
// defaults get merged).
func resolveAllowlist(cfg config.Config) []string {
	if len(cfg.ToolAllowlist) > 0 {
		return cfg.ToolAllowlist
	}
	return append(append([]string{}, agent.DefaultToolAllowlist...), meshservices.AllowedToolNames()...)
}

func allowlistIncludes(allowlist []string, name string) bool {
	for _, n := range allowlist {
		if n == name {
			return true
		}
	}
	return false
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
func buildSystemPrompt(room, goalText string, localToolsReachable, expressiveStyle, spikeRoomWaiters bool) string {
	toolsLine := "You have macula-mcp's mesh_* tools, plus mesh_service_* tools that call real " +
		"mesh services (search/knowledge-graph/forum capabilities, discovered live) -- prefer " +
		"a mesh_service_* tool over guessing at an answer when the task fits one. Their exact " +
		"arguments aren't advertised; if a call errors, read the error and retry with corrected " +
		"arguments rather than giving up after one attempt."
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
	waitingLine := "When there is nothing to do right now, call mesh_say with a long " +
		"wait_reply_seconds to listen efficiently instead of returning immediately."
	if spikeRoomWaiters {
		// macula-io/macula-lazymesh#14 spike: waiting is now the harness's
		// job, not the model's own tool call -- see roomwaiter.Manager.
		// Telling the model to still reach for a long wait_reply_seconds
		// would recreate the exact blocking bug this spike exists to
		// measure a fix for; NoBlockingWaitSource also clamps it as
		// defense in depth, but the prompt has to say the new thing too,
		// not just stop saying the old one.
		waitingLine = "You do not need to wait for messages yourself: the harness is watching " +
			"every room you are in and will prompt you the moment something new arrives there, " +
			"or when a human sends you a message directly. Do not call mesh_say/mesh_wait_room " +
			"just to wait for a reply -- when you genuinely have nothing to add right now, reply " +
			"briefly (or with nothing) and your turn simply ends until the harness wakes you again."
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
			"conversation memory, which gets trimmed. "+
			"IMPORTANT, every single time you are prompted (not just when told to): call "+
			"mesh_read_inbox with no room_topic argument (so it covers every room) and "+
			"check its rings.pending list for anything addressed to you from ANY peer, not "+
			"just people already in a room you're in. If one is pending, decide whether to "+
			"accept or decline based on its stated purpose and call mesh_answer_ring -- "+
			"never leave a ring sitting there unanswered just because it's not about a room "+
			"you were already in; accepting one puts you in a new room, which becomes part "+
			"of your scope from then on too. %s",
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

// runAgent drives the agent loop in the background, for as long as the
// program runs; room and goalText are optional hints, not a scope
// restriction -- the model discovers and participates in every room it is
// currently a member of via mesh_rooms, regardless of whether either is
// set. Every round the LLM decides what to do (join, talk, answer rings,
// wait on mesh_say's own wait_reply_seconds) -- this function only
// supplies the cadence of asking it to keep going, not any of the mesh
// actions themselves.
func runAgent(ctx context.Context, p provider.Provider, tools agent.ToolSource, room, goalText string, localToolsReachable, expressiveStyle bool, waiterMgr *roomwaiter.Manager, agentLog *log.Logger, tuiEvents chan<- agent.Event, userInputCh <-chan string) {
	spikeRoomWaiters := waiterMgr != nil
	systemPrompt := buildSystemPrompt(room, goalText, localToolsReachable, expressiveStyle, spikeRoomWaiters)

	loop := agent.NewLoop(p, tools, systemPrompt)
	events := make(chan agent.Event, 16)
	go func() {
		for ev := range events {
			logEvent(agentLog, ev)
			// #14 spike: reactive room-churn detection (never a poll loop
			// of its own) -- piggyback on the mesh_rooms result the model
			// already produces via its own normal cadence, rather than
			// waiterMgr ever calling mesh_rooms on a timer.
			if waiterMgr != nil && ev.Kind == agent.EventToolResult && ev.ToolName == "mesh_rooms" {
				waiterMgr.Sync(ctx, parseJoinedRooms(ev.Text))
			}
			// Non-blocking: the TUI is a slow, human-paced consumer and
			// must never be able to stall the agent loop by not reading
			// fast enough (or not running at all -- tuiEvents always
			// exists, but nothing drains it without a program running).
			select {
			case tuiEvents <- ev:
			default:
			}
		}
	}()
	defer close(events)

	// maxConsecutiveErrors bounds how long this keeps retrying after
	// repeated provider failures (found by an adversarial review,
	// 2026-09-06): unbounded retries meant a wedged provider -- or a peer
	// deliberately flooding the room to force context-overflow errors --
	// left this loop silently spinning forever while the TUI still looked
	// healthy. backoff grows between attempts instead of a fixed delay, so
	// a transient blip recovers fast but a persistent failure doesn't
	// hammer the provider every 5s for no reason.
	const maxConsecutiveErrors = 8
	consecutiveErrors := 0
	backoff := initialBackoff

	prompt := agentInitialPrompt
	for {
		if ctx.Err() != nil {
			return
		}
		if err := loop.Say(ctx, prompt, events); err != nil {
			consecutiveErrors++
			agentLog.Printf("lazymesh agent: %s (consecutive failures: %d/%d)", err, consecutiveErrors, maxConsecutiveErrors)
			if consecutiveErrors >= maxConsecutiveErrors {
				agentLog.Printf("lazymesh agent: stopping after %d consecutive failures -- not retrying forever silently", consecutiveErrors)
				events <- agent.Event{Kind: agent.EventMaxFailuresReached}
				return
			}
			events <- agent.Event{Kind: agent.EventBackoff}
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff = nextBackoff(backoff)
			var ok bool
			prompt, ok = nextEvent(ctx, userInputCh, waiterMgr)
			if !ok {
				return
			}
			continue
		}
		consecutiveErrors = 0
		backoff = initialBackoff
		var ok bool
		prompt, ok = nextEvent(ctx, userInputCh, waiterMgr)
		if !ok {
			return
		}
	}
}

// nextPrompt prefers a message the human composed in the TUI (drained
// non-blocking -- if nothing is waiting, the loop keeps its own ambient
// cadence) over the default "check for anything new" prompt. A message
// typed while the agent is mid-Say() is picked up once that call returns,
// not instantly -- no in-flight call gets interrupted for it; documented
// as a known lag, not treated as a bug, matching this project's own
// "no theatre" lean-MVP scope.
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

// nextEvent is #14's replacement for nextPrompt when waiterMgr is
// non-nil: BLOCKS until there is an actual reason to run another Say()
// round, rather than nextPrompt's instant fallback to a synthetic
// "check for anything new" prompt every cycle. That instant fallback is
// exactly right for TODAY's design (the model's own long mesh_say wait
// IS the pacing), but would busy-loop the provider for free once that
// wait is taken away from the model -- something has to supply the
// pacing, and it's this select instead.
//
// waiterMgr == nil (the default, non-spike run) preserves nextPrompt's
// existing behavior byte-for-byte -- this is what keeps today's design
// available unchanged as the actual A/B baseline, from the same binary.
//
// Fairness/priority policy (macula-io/macula-lazymesh#14, Atlas's flagged
// gap): human input is checked non-blockingly FIRST, so an already-
// pending human message always wins outright. Only once nothing is
// immediately pending does this block on a real select across both
// channels -- in the rare case a human message and a room arrival become
// ready at the exact same instant during that block, Go's own
// select-among-ready-cases randomization decides, not a strict priority.
// Documented here rather than silently assumed correct: closing this gap
// fully would mean a second, always-running non-blocking check loop
// (spin on userInputCh between every select wakeup instead of trusting
// the select itself), trading a rare, microsecond-scale tie for a
// permanently more complex loop -- judged not worth it for a spike (see
// plans/SPIKE_LAZYMESH_ROOM_WAITERS.md for the fuller writeup), but a
// real implementation should make that same call deliberately, not by
// inheriting this one.
// Room-arrival fairness AMONG rooms is roomwaiter.Manager's own job (see
// its doc comment): this function just consumes whatever it hands back.
//
// Returns ok=false only when ctx is done -- the caller should stop the
// loop, not call Say with an empty prompt.
func nextEvent(ctx context.Context, userInputCh <-chan string, waiterMgr *roomwaiter.Manager) (string, bool) {
	if waiterMgr == nil {
		return nextPrompt(userInputCh, agentDefaultPrompt), true
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

// seedInitialRooms calls mesh_rooms directly (bypassing the model, same
// deterministic-harness-plumbing posture as sayGoodbye/#6) so
// roomwaiter.Manager has a starting room set before the agent loop's own
// first cycle -- otherwise the spike would wait for the model to
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

// agentDefaultPrompt/agentInitialPrompt are runAgent's per-cycle nudges,
// hoisted to package level (rather than local consts inside runAgent) so
// their room-scoping content has a direct unit test.
//
// Investigated 2026-09-06 after a live "ring stuck deferred" report -- that
// specific incident turned out to be an instance Raf stopped himself
// mid-test, not a bug, but the underlying gap is real regardless: the
// system prompt alone saying "answer any ring addressed to you" wasn't
// reliably driving the model to actually check for one, since every
// per-turn prompt only ever mentioned "the room." Both prompts say it
// explicitly, every cycle, not just once at the start of the conversation.
// Extended macula-io/macula-lazymesh#1 (2026-09-06): "the room" is now
// "every room mesh_rooms reports," not one hardcoded topic -- a room
// joined later via an accepted ring must stay in scope too.
const (
	agentDefaultPrompt = "First, call mesh_read_inbox (no room_topic) and answer any pending ring " +
		"addressed to you via mesh_answer_ring, from any peer. Then call mesh_rooms and check " +
		"every room you are currently a member of for anything new since your last check, " +
		"responding if warranted -- not just the room you were originally pointed at, if any."
	agentInitialPrompt = "First, call mesh_read_inbox (no room_topic) and answer any pending ring " +
		"addressed to you via mesh_answer_ring. Then call mesh_rooms; join any room you were " +
		"pointed at only if its room_topic is not already in mesh_rooms's own joined list, and " +
		"start participating in every room you are a member of."
)

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
	}
}
