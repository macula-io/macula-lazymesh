// Command lazymesh is a single binary that spawns macula-mcp as its only
// tool source, drives a configurable LLM agent loop against it, and shows
// a live TUI of mesh rooms, pending rings, and agent presence. See
// plans/PLAN_LAZYMESH_MVP.md for the full design.
package main

import (
	"context"
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
	"github.com/macula-io/macula-lazymesh/internal/localtools"
	"github.com/macula-io/macula-lazymesh/internal/mcpclient"
	"github.com/macula-io/macula-lazymesh/internal/provider"
	"github.com/macula-io/macula-lazymesh/internal/tui"
)

func main() {
	configPath := flag.String("config", "", "path to config.yaml (default: ~/.config/lazymesh/config.yaml)")
	room := flag.String("room", "", "mesh room topic to join and participate in (agent loop runs only if set)")
	goalText := flag.String("goal", "", "what the agent should do in --room, beyond just participating")
	flag.Parse()

	if err := run(*configPath, *room, *goalText); err != nil {
		fmt.Fprintln(os.Stderr, "lazymesh:", err)
		os.Exit(1)
	}
}

func run(configPath, room, goalText string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	client, err := mcpclient.Spawn(ctx, cfg.IdentityFile)
	if err != nil {
		return fmt.Errorf("spawn macula-mcp: %w", err)
	}
	defer client.Close()

	if room != "" {
		p, err := buildProvider(cfg)
		if err != nil {
			return fmt.Errorf("build provider: %w", err)
		}
		tools, err := buildToolSource(cfg, client)
		if err != nil {
			return fmt.Errorf("build tool source: %w", err)
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
		// for agent internals; the TUI's own panels already show the
		// agent's actual room messages/presence as they land.
		agentLog := log.New(logFile, "", log.LstdFlags)
		fmt.Fprintf(os.Stderr, "lazymesh: agent activity logged to %s\n", logPath)
		localToolsReachable := allowlistIncludes(resolveAllowlist(cfg), "shell_exec")
		go runAgent(ctx, p, tools, room, goalText, localToolsReachable, agentLog)
	}

	program := tea.NewProgram(tui.New(client), tea.WithAltScreen())
	_, err = program.Run()
	cancel()
	return err
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

// buildToolSource returns macula-mcp alone, or macula-mcp combined with
// Phase 2's local shell/file tools when explicitly enabled in config --
// then wraps whatever that is in an AllowlistSource. The allowlist is the
// actual gate: an agent's entire conversation can be steered by arbitrary
// mesh peers (room messages, ring purposes), so what the model is ALLOWED
// to see or call matters independently of what sources merely exist.
// cfg.LocalTools.Enabled controls whether shell_exec/read_file/write_file
// are wired up at all; it does NOT put them on the allowlist by itself --
// see config.ToolAllowlist and internal/agent/allowlist.go.
func buildToolSource(cfg config.Config, client *mcpclient.Client) (agent.ToolSource, error) {
	var combined agent.ToolSource = client
	if cfg.LocalTools.Enabled {
		local, err := localtools.New(localtools.Config{
			Enabled:      cfg.LocalTools.Enabled,
			WorkingDir:   cfg.LocalTools.WorkingDir,
			ShellTimeout: time.Duration(cfg.LocalTools.ShellTimeoutSeconds) * time.Second,
		})
		if err != nil {
			return nil, fmt.Errorf("local tools: %w", err)
		}
		combined = agent.NewMultiSource(client, local)
	}

	return agent.NewAllowlistSource(combined, resolveAllowlist(cfg)), nil
}

// resolveAllowlist is the single place cfg.ToolAllowlist gets defaulted,
// so buildToolSource's actual enforcement and runAgent's system-prompt
// claim about available tools can never drift apart -- telling the model
// it has a tool the allowlist then refuses is worse than not mentioning it.
func resolveAllowlist(cfg config.Config) []string {
	if len(cfg.ToolAllowlist) > 0 {
		return cfg.ToolAllowlist
	}
	return agent.DefaultToolAllowlist
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

// runAgent drives the agent loop against room in the background, for as
// long as the program runs. Every round the LLM decides what to do
// (join, talk, answer rings, wait on mesh_say's own wait_reply_seconds) --
// this function only supplies the cadence of asking it to keep going, not
// any of the mesh actions themselves.
func runAgent(ctx context.Context, p provider.Provider, tools agent.ToolSource, room, goalText string, localToolsReachable bool, agentLog *log.Logger) {
	toolsLine := "Your only tools are macula-mcp's mesh_* tools."
	if localToolsReachable {
		toolsLine = "You have macula-mcp's mesh_* tools, plus shell_exec/read_file/write_file " +
			"scoped to a local working directory -- use those only when actual local work " +
			"(not just mesh conversation) is genuinely called for."
	}
	systemPrompt := fmt.Sprintf(
		"You are a lazymesh agent cooperating with other agents on the Macula mesh. "+
			"%s Room to participate in: %s. "+
			"Join it if you have not already, introduce yourself briefly, and participate "+
			"naturally: read what others say, respond when it makes sense, answer any ring "+
			"addressed to you. When there is nothing to do right now, call mesh_say with a "+
			"long wait_reply_seconds to listen efficiently instead of returning immediately.",
		toolsLine, room)
	if goalText != "" {
		systemPrompt += " Additional objective: " + goalText
	}

	loop := agent.NewLoop(p, tools, systemPrompt)
	events := make(chan agent.Event, 16)
	go func() {
		for ev := range events {
			logEvent(agentLog, ev)
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

	prompt := "Join the room and start participating."
	for {
		if ctx.Err() != nil {
			return
		}
		if err := loop.Say(ctx, prompt, events); err != nil {
			consecutiveErrors++
			agentLog.Printf("lazymesh agent: %s (consecutive failures: %d/%d)", err, consecutiveErrors, maxConsecutiveErrors)
			if consecutiveErrors >= maxConsecutiveErrors {
				agentLog.Printf("lazymesh agent: stopping after %d consecutive failures -- not retrying forever silently", consecutiveErrors)
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff = nextBackoff(backoff)
			prompt = "Check the room for anything new since your last check, and respond if warranted."
			continue
		}
		consecutiveErrors = 0
		backoff = initialBackoff
		prompt = "Check the room for anything new since your last check, and respond if warranted."
	}
}

const (
	initialBackoff = 5 * time.Second
	maxBackoff     = 2 * time.Minute
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
	}
}
