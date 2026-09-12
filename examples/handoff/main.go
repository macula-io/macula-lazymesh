// Command handoff is the G5 example: a parent process starts two
// headless lazymesh sessions on this box and hands a task back and forth
// between them over their unix control sockets — the agents never call
// each other through the mesh, so the handoff costs zero mesh
// round-trips. The parent is the courier; the sockets are the channel.
//
// Usage:
//
//	handoff --binary ./lazymesh --config-a config-a.yaml --config-b config-b.yaml \
//	        --task "write a one-line greeting for a mesh agent"
//
// The two configs MUST give each session its own identity file (two
// lazymesh processes under one node_id would fight for the same mesh
// identity) and, of course, working provider keys. The sockets are
// created in a fresh temp dir and removed on exit.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/macula-io/macula-lazymesh/sdk"
)

func main() {
	binary := flag.String("binary", "lazymesh", "path to the lazymesh binary")
	configA := flag.String("config-a", "", "config.yaml for the worker session")
	configB := flag.String("config-b", "", "config.yaml for the reviewer session")
	task := flag.String("task", "write a one-line greeting for a mesh agent", "the task handed to the worker")
	timeout := flag.Duration("timeout", 2*time.Minute, "per-turn deadline")
	flag.Parse()

	if *configA == "" || *configB == "" {
		fmt.Fprintln(os.Stderr, "handoff: --config-a and --config-b are required (each with its own identity file)")
		os.Exit(2)
	}

	dir, err := os.MkdirTemp("", "lazymesh-handoff-*")
	if err != nil {
		fatalf("temp dir: %v", err)
	}
	defer os.RemoveAll(dir)
	sockA := filepath.Join(dir, "worker.sock")
	sockB := filepath.Join(dir, "reviewer.sock")

	worker := spawn(*binary, *configA, sockA, "worker")
	defer worker.kill()
	reviewer := spawn(*binary, *configB, sockB, "reviewer")
	defer reviewer.kill()

	ctx := context.Background()

	// Wait for both sockets to appear, then dial.
	var clientA, clientB *sdk.Client
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); {
		if clientA == nil {
			if c, err := sdk.Dial(ctx, sockA); err == nil {
				clientA = c
			}
		}
		if clientB == nil {
			if c, err := sdk.Dial(ctx, sockB); err == nil {
				clientB = c
			}
		}
		if clientA != nil && clientB != nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if clientA == nil || clientB == nil {
		fatalf("sessions never served their control sockets")
	}
	defer clientA.Close()
	defer clientB.Close()

	// Hand the task to the worker; block until its turn completes.
	turnCtx, cancel := context.WithTimeout(ctx, *timeout)
	work, err := clientA.Say(turnCtx, *task)
	cancel()
	if err != nil {
		fatalf("worker turn: %v", err)
	}
	if work.Text == "" {
		fatalf("worker produced no answer")
	}
	fmt.Printf("worker: %s\n", work.Text)

	// Hand the worker's answer to the reviewer; block until it completes.
	reviewCtx, cancel := context.WithTimeout(ctx, *timeout)
	review, err := clientB.Say(reviewCtx, "review this one-line answer for tone and correctness, and reply with an improved version only: "+work.Text)
	cancel()
	if err != nil {
		fatalf("reviewer turn: %v", err)
	}
	fmt.Printf("reviewer: %s\n", review.Text)

	clientA.Shutdown()
	clientB.Shutdown()
	fmt.Println("handoff complete — zero mesh round-trips")
}

// spawn starts one headless lazymesh session and returns its handle.
func spawn(binary, config, socket, sessionID string) *session {
	cmd := exec.Command(binary,
		"--headless",
		"--config", config,
		"--unix-socket", socket,
		"--session-id", sessionID,
	)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		fatalf("start %s session: %v", sessionID, err)
	}
	return &session{cmd: cmd, id: sessionID}
}

type session struct {
	cmd *exec.Cmd
	id  string
}

func (s *session) kill() {
	if s.cmd.Process != nil {
		s.cmd.Process.Kill()
	}
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "handoff: "+format+"\n", args...)
	os.Exit(1)
}
