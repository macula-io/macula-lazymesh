// Command fanout is the G15 example: subagent orchestration as a
// controller concern. A parent process starts one headless lazymesh
// session per subtask, gates each with the pre-tool hook so a worker runs
// only the tools the parent allows (none by default: workers think, they
// do not act on the mesh), hands each its subtask in parallel and prints
// the answers in order.
//
// Usage:
//
//	fanout --binary ./lazymesh --config config.yaml \
//	       --subtask "name three uses of a mesh room" \
//	       --subtask "name three risks of answering rings from strangers" \
//	       [--allow-tool mesh_say ...]
//
// All workers share one config, which must set
// pre_tool_hook_required: true, so no worker can call a tool before the
// parent hooks it. Workers start with --idle-start: no startup turn, the
// first turn is the parent's subtask. Each session gets its own mesh
// identity unless the config pins identity_file, and they share the
// config's contact policy. The sockets live in a fresh private temp dir,
// removed on exit.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/macula-io/macula-lazymesh/internal/config"
	"github.com/macula-io/macula-lazymesh/sdk"
)

// listFlag collects a repeatable string flag.
type listFlag []string

func (l *listFlag) String() string     { return fmt.Sprint(*l) }
func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }

func main() {
	binary := flag.String("binary", "lazymesh", "path to the lazymesh binary")
	configPath := flag.String("config", "", "config.yaml every worker session starts from")
	timeout := flag.Duration("timeout", 2*time.Minute, "per-subtask deadline")
	var subtasks, allowed listFlag
	flag.Var(&subtasks, "subtask", "one subtask per worker (repeat)")
	flag.Var(&allowed, "allow-tool", "a tool workers may call (repeat; none by default)")
	flag.Parse()

	if *configPath == "" || len(subtasks) == 0 {
		fmt.Fprintln(os.Stderr, "fanout: --config and at least one --subtask are required")
		os.Exit(2)
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fatalf("%v", err)
	}
	if !cfg.PreToolHookRequired {
		fatalf("%s must set pre_tool_hook_required: true, or a worker woken by a room or ring before its parent hooks it would call tools ungated", *configPath)
	}

	dir, err := os.MkdirTemp("", "lazymesh-fanout-*")
	if err != nil {
		fatalf("temp dir: %v", err)
	}
	defer os.RemoveAll(dir)

	workers := make([]*worker, len(subtasks))
	for i := range subtasks {
		id := fmt.Sprintf("worker-%d", i+1)
		workers[i] = spawn(*binary, *configPath, filepath.Join(dir, id+".sock"), id)
		defer workers[i].kill()
	}

	ctx := context.Background()
	gate := toolGate(allowed)
	for _, w := range workers {
		w.client = dialWhenReady(ctx, w.socket)
		defer w.client.Close()
		if err := w.client.HookPreTool(ctx, gate); err != nil {
			fatalf("%s: hook pre_tool: %v", w.id, err)
		}
	}

	answers := make([]string, len(workers))
	var wg sync.WaitGroup
	for i, w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			turnCtx, cancel := context.WithTimeout(ctx, *timeout)
			defer cancel()
			turn, err := w.client.Say(turnCtx, subtasks[i])
			switch {
			case err != nil:
				answers[i] = "error: " + err.Error()
			case turn.Text == "":
				answers[i] = "(no answer)"
			default:
				answers[i] = turn.Text
			}
		}()
	}
	wg.Wait()

	for i, w := range workers {
		fmt.Printf("%s: %s\n  -> %s\n", w.id, subtasks[i], answers[i])
		w.client.Shutdown()
	}
}

// toolGate is the parent's policy for every worker: only the listed tools
// run, everything else is refused with a reason the worker's model sees.
func toolGate(allowed []string) sdk.PreToolFunc {
	set := make(map[string]bool, len(allowed))
	for _, name := range allowed {
		set[name] = true
	}
	return func(tool, args string) (bool, string) {
		if set[tool] {
			return true, ""
		}
		return false, "this worker answers from its own reasoning; " + tool + " is not allowed by its parent"
	}
}

type worker struct {
	id     string
	socket string
	cmd    *exec.Cmd
	client *sdk.Client
}

// spawn starts one idle headless session; its config requires the
// pre-tool hook, so it cannot call a tool before the parent hooks it.
func spawn(binary, configPath, socket, id string) *worker {
	cmd := exec.Command(binary, "--headless", "--idle-start", "--config", configPath, "--unix-socket", socket, "--session-id", id)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		fatalf("start %s: %v", id, err)
	}
	return &worker{id: id, socket: socket, cmd: cmd}
}

func (w *worker) kill() {
	if w.cmd.Process != nil {
		w.cmd.Process.Kill()
	}
}

// dialWhenReady waits up to 30 s for a session's socket to serve.
func dialWhenReady(ctx context.Context, socket string) *sdk.Client {
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		if c, err := sdk.Dial(ctx, socket); err == nil {
			return c
		}
	}
	fatalf("session at %s never served its control socket", socket)
	return nil
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "fanout: "+format+"\n", args...)
	os.Exit(1)
}
