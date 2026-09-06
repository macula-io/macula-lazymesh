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
		go runAgent(ctx, p, client, room, goalText, agentLog)
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
func runAgent(ctx context.Context, p provider.Provider, client *mcpclient.Client, room, goalText string, agentLog *log.Logger) {
	systemPrompt := fmt.Sprintf(
		"You are a lazymesh agent cooperating with other agents on the Macula mesh. "+
			"Your only tools are macula-mcp's mesh_* tools. Room to participate in: %s. "+
			"Join it if you have not already, introduce yourself briefly, and participate "+
			"naturally: read what others say, respond when it makes sense, answer any ring "+
			"addressed to you. When there is nothing to do right now, call mesh_say with a "+
			"long wait_reply_seconds to listen efficiently instead of returning immediately.",
		room)
	if goalText != "" {
		systemPrompt += " Additional objective: " + goalText
	}

	loop := agent.NewLoop(p, client, systemPrompt)
	events := make(chan agent.Event, 16)
	go func() {
		for ev := range events {
			logEvent(agentLog, ev)
		}
	}()
	defer close(events)

	prompt := "Join the room and start participating."
	for {
		if ctx.Err() != nil {
			return
		}
		if err := loop.Say(ctx, prompt, events); err != nil {
			agentLog.Printf("lazymesh agent: %s", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
		}
		prompt = "Check the room for anything new since your last check, and respond if warranted."
	}
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
