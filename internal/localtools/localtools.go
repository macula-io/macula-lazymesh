// Package localtools is lazymesh's Phase 2 tool source: a minimal
// shell/file tool set so an agent can actually do work arising from mesh
// coordination, not just talk about it. This is a second, separately
// configurable ToolSource alongside macula-mcp -- opt-in and off by
// default, per the plan's own stated rationale: the MVP's whole value is
// having NO extra tools unless the operator explicitly asks for them.
//
// This is honestly as dangerous as it sounds: shell_exec runs whatever
// command the LLM decides to run, scoped only to WorkingDir as a starting
// directory, not a real sandbox or container jail. read_file/write_file
// are confined to WorkingDir by path validation, but shell_exec is not --
// a command can still `cd` anywhere the operator's own user account can
// reach. Enabling this means trusting the configured provider/model with
// real local execution, the same trust an operator running commands
// themselves would carry.
package localtools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/macula-io/macula-lazymesh/internal/mcpclient"
)

const (
	maxReadFileBytes = 1 << 20 // 1MiB -- generous for config/log/source files, bounded so a huge file can't blow up the conversation context
	toolShellExec    = "shell_exec"
	toolReadFile     = "read_file"
	toolWriteFile    = "write_file"
)

// Config controls Phase 2's local tool set. Enabled defaults to false --
// callers must opt in explicitly.
type Config struct {
	Enabled      bool
	WorkingDir   string        // required if Enabled; shell_exec's cwd, and the sandbox root for read_file/write_file
	ShellTimeout time.Duration // defaults to 30s if zero
}

// Source implements agent.ToolSource, giving an agent loop shell_exec,
// read_file, and write_file, all scoped to Config.WorkingDir.
type Source struct {
	workingDir   string
	shellTimeout time.Duration
}

// New validates cfg and returns a Source. It errors rather than silently
// falling back if WorkingDir is missing or not a real directory --
// getting the sandbox root wrong here is exactly the kind of mistake that
// shouldn't fail quietly. The root is resolved through symlinks once, at
// construction, so every later containment check compares against the
// canonical directory, not whatever spelling the operator typed.
func New(cfg Config) (*Source, error) {
	if cfg.WorkingDir == "" {
		return nil, fmt.Errorf("localtools: working_dir is required")
	}
	abs, err := filepath.Abs(cfg.WorkingDir)
	if err != nil {
		return nil, fmt.Errorf("localtools: resolve working_dir %s: %w", cfg.WorkingDir, err)
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, fmt.Errorf("localtools: create working_dir %s: %w", abs, err)
	}
	canonical, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("localtools: resolve working_dir symlinks %s: %w", abs, err)
	}
	timeout := cfg.ShellTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &Source{workingDir: canonical, shellTimeout: timeout}, nil
}

func (s *Source) ListTools(ctx context.Context) ([]mcpclient.Tool, error) {
	return []mcpclient.Tool{
		{
			Name:        toolShellExec,
			Description: "Run a shell command in lazymesh's configured working directory. Returns stdout, stderr, and the exit code.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"command": map[string]any{
						"type":        "string",
						"description": "Command to run via `sh -c`.",
					},
				},
				"required": []string{"command"},
			},
		},
		{
			Name:        toolReadFile,
			Description: "Read a text file's contents. Path is relative to lazymesh's configured working directory; it cannot escape it.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "File path, relative to the working directory.",
					},
				},
				"required": []string{"path"},
			},
		},
		{
			Name:        toolWriteFile,
			Description: "Write (creating or overwriting) a text file. Path is relative to lazymesh's configured working directory; it cannot escape it.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "File path, relative to the working directory.",
					},
					"content": map[string]any{
						"type":        "string",
						"description": "Content to write.",
					},
				},
				"required": []string{"path", "content"},
			},
		},
	}, nil
}

func (s *Source) CallToolRaw(ctx context.Context, name string, argumentsJSON string) (string, error) {
	args := map[string]any{}
	if strings.TrimSpace(argumentsJSON) != "" {
		if err := json.Unmarshal([]byte(argumentsJSON), &args); err != nil {
			return "", fmt.Errorf("decode arguments for %s: %w", name, err)
		}
	}

	switch name {
	case toolShellExec:
		return s.shellExec(ctx, args)
	case toolReadFile:
		return s.readFile(args)
	case toolWriteFile:
		return s.writeFile(args)
	default:
		return "", fmt.Errorf("localtools: unknown tool %q", name)
	}
}

func (s *Source) shellExec(ctx context.Context, args map[string]any) (string, error) {
	command, _ := args["command"].(string)
	if command == "" {
		return "", fmt.Errorf("shell_exec: \"command\" is required")
	}

	ctx, cancel := context.WithTimeout(ctx, s.shellTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = s.workingDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	exitCode := 0
	if runErr != nil {
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return "", fmt.Errorf("shell_exec: %w", runErr)
		}
	}

	result, err := json.Marshal(map[string]any{
		"stdout":    stdout.String(),
		"stderr":    stderr.String(),
		"exit_code": exitCode,
	})
	if err != nil {
		return "", fmt.Errorf("shell_exec: marshal result: %w", err)
	}
	return string(result), nil
}

// resolveInSandbox resolves a caller-supplied relative path against
// s.workingDir and confirms the result is still inside it -- rejecting
// absolute paths and ../ escapes rather than trusting the caller.
func (s *Source) resolveInSandbox(relPath string) (string, error) {
	if relPath == "" {
		return "", fmt.Errorf("\"path\" is required")
	}
	if filepath.IsAbs(relPath) {
		return "", fmt.Errorf("path %q must be relative to the working directory, not absolute", relPath)
	}
	joined := filepath.Join(s.workingDir, relPath)
	cleaned := filepath.Clean(joined)
	if err := s.checkContained(cleaned); err != nil {
		return "", fmt.Errorf("path %q escapes the working directory", relPath)
	}
	return cleaned, nil
}

// resolveExisting is resolveInSandbox plus symlink resolution (G10): the
// lexical containment check cannot see a symlink sitting INSIDE the
// sandbox that points back out, so the final path is resolved through
// symlinks and re-checked. read_file uses this; the target must exist.
func (s *Source) resolveExisting(relPath string) (string, error) {
	cleaned, err := s.resolveInSandbox(relPath)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(cleaned)
	if err != nil {
		return "", err
	}
	if err := s.checkContained(resolved); err != nil {
		return "", fmt.Errorf("path %q escapes the working directory through a symlink", relPath)
	}
	return resolved, nil
}

// resolveForWrite resolves the DEEPEST EXISTING ancestor of a possibly
// not-yet-existing target through symlinks and re-checks containment,
// then rejoins the remainder: a write must not follow a symlinked
// directory out of the sandbox, while the not-yet-created parts
// legitimately have nothing to resolve yet. When the final component
// itself exists and is a symlink, it is resolved and re-checked too —
// os.WriteFile follows it, so it must be treated like the directories.
func (s *Source) resolveForWrite(relPath string) (string, error) {
	cleaned, err := s.resolveInSandbox(relPath)
	if err != nil {
		return "", err
	}
	existing := filepath.Dir(cleaned)
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		} else if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return "", fmt.Errorf("write_file: no existing ancestor of %q inside the working directory", relPath)
		}
		existing = parent
	}
	resolved, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return "", err
	}
	if err := s.checkContained(resolved); err != nil {
		return "", fmt.Errorf("path %q escapes the working directory through a symlinked directory", relPath)
	}
	remainder := strings.TrimPrefix(cleaned, existing)
	target := filepath.Join(resolved, remainder)
	if final, err := filepath.EvalSymlinks(target); err == nil {
		if err := s.checkContained(final); err != nil {
			return "", fmt.Errorf("path %q escapes the working directory through a symlink", relPath)
		}
		return final, nil
	}
	return target, nil
}

// checkContained verifies path stays within the canonical working
// directory.
func (s *Source) checkContained(path string) error {
	root := filepath.Clean(s.workingDir)
	if path != root && !strings.HasPrefix(path, root+string(os.PathSeparator)) {
		return fmt.Errorf("path %q escapes the working directory", path)
	}
	return nil
}

func (s *Source) readFile(args map[string]any) (string, error) {
	relPath, _ := args["path"].(string)
	path, err := s.resolveExisting(relPath)
	if err != nil {
		return "", fmt.Errorf("read_file: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("read_file: %w", err)
	}
	if info.Size() > maxReadFileBytes {
		return "", fmt.Errorf("read_file: %s is %d bytes, over the %d byte limit", relPath, info.Size(), maxReadFileBytes)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read_file: %w", err)
	}
	return string(data), nil
}

func (s *Source) writeFile(args map[string]any) (string, error) {
	relPath, _ := args["path"].(string)
	content, _ := args["content"].(string)
	path, err := s.resolveForWrite(relPath)
	if err != nil {
		return "", fmt.Errorf("write_file: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("write_file: create parent directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("write_file: %w", err)
	}
	return fmt.Sprintf("wrote %d bytes to %s", len(content), relPath), nil
}
