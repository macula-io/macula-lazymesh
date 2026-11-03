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
//
// That describes the mesh profile, which passes HostShell. The coding
// profile passes a confinedshell.Sandbox instead, or no shell at all
// when the sandbox cannot be proven on this machine (#26).
package localtools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/macula-io/macula-lazymesh/internal/agent"
	"github.com/macula-io/macula-lazymesh/internal/confinedshell"
	"github.com/macula-io/macula-lazymesh/internal/mcpclient"
)

const (
	maxReadFileBytes = 1 << 20 // 1MiB -- generous for config/log/source files, bounded so a huge file can't blow up the conversation context
	toolShellExec    = "shell_exec"
	toolReadFile     = "read_file"
	toolWriteFile    = "write_file"
)

// ToolNames is every tool this source serves: the names an operator can
// put on an allowlist for it.
func ToolNames() []string {
	return []string{toolShellExec, toolReadFile, toolWriteFile}
}

// Config controls Phase 2's local tool set. Enabled defaults to false --
// callers must opt in explicitly.
type Config struct {
	Enabled      bool
	WorkingDir   string        // required if Enabled; shell_exec's cwd, and the sandbox root for read_file/write_file
	ShellTimeout time.Duration // defaults to 30s if zero
	// Shell runs shell_exec's commands: HostShell for the mesh profile,
	// a confinedshell.Sandbox for the coding profile. nil means no
	// shell: shell_exec is not offered, and a call to it is refused with
	// NoShellReason.
	Shell         confinedshell.Runner
	NoShellReason string
	// OutputDir, when set, keeps a command's whole output in a file once
	// it is longer than one result, and the result carries the first
	// page and the file's path (#26: a cut test run reads as a pass).
	OutputDir string
}

// HostShell is the mesh profile's shell: sh -c in dir with the
// operator's own rights, unconfined.
func HostShell(dir string) confinedshell.Runner {
	return hostShell{dir: dir}
}

type hostShell struct{ dir string }

func (h hostShell) Run(ctx context.Context, command string) (confinedshell.Result, error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = h.dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	res := confinedshell.Result{Stdout: stdout.String(), Stderr: stderr.String()}
	if runErr != nil {
		exitErr, ok := runErr.(*exec.ExitError)
		if !ok {
			return res, runErr
		}
		res.ExitCode = exitErr.ExitCode()
	}
	return res, nil
}

// Source implements agent.ToolSource, giving an agent loop shell_exec,
// read_file, and write_file, all scoped to Config.WorkingDir.
type Source struct {
	workingDir string
	// root confines every file the host opens for read_file and
	// write_file to workingDir: os.Root resolves symlinks only inside it,
	// so a link the shell plants cannot lead a host write outside (#26).
	root          *os.Root
	shellTimeout  time.Duration
	shell         confinedshell.Runner
	noShellReason string
	outputDir     string
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
	root, err := os.OpenRoot(canonical)
	if err != nil {
		return nil, fmt.Errorf("localtools: open working_dir %s: %w", canonical, err)
	}
	return &Source{workingDir: canonical, root: root, shellTimeout: timeout, shell: cfg.Shell, noShellReason: cfg.NoShellReason, outputDir: cfg.OutputDir}, nil
}

func (s *Source) ListTools(ctx context.Context) ([]mcpclient.Tool, error) {
	tools := []mcpclient.Tool{
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
	}
	if s.shell == nil {
		return tools[1:], nil
	}
	return tools, nil
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
	if s.shell == nil {
		return "", agent.Refused(fmt.Errorf("shell_exec is not offered: %s", s.noShellReason))
	}
	command, _ := args["command"].(string)
	if command == "" {
		return "", fmt.Errorf("shell_exec: \"command\" is required")
	}

	ctx, cancel := context.WithTimeout(ctx, s.shellTimeout)
	defer cancel()

	res, err := s.shell.Run(ctx, command)
	if err != nil {
		return "", fmt.Errorf("shell_exec: %w", err)
	}
	fields := map[string]any{"stdout": res.Stdout, "stderr": res.Stderr, "exit_code": res.ExitCode}
	if s.outputDir != "" && len(res.Stdout)+len(res.Stderr) > shellPageBytes {
		if fields, err = s.pagedOutput(res); err != nil {
			return "", err
		}
	}
	result, err := json.Marshal(fields)
	if err != nil {
		return "", fmt.Errorf("shell_exec: marshal result: %w", err)
	}
	return string(result), nil
}

// createOutputFile makes a new file in the output directory through
// os.Root, after refusing an output directory that is a symlink: the
// coding profile mounts it read-only in the sandbox, and the host still
// does not trust the path (#26).
func (s *Source) createOutputFile() (*os.File, error) {
	info, err := os.Lstat(s.outputDir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("output directory %s is not a plain directory", s.outputDir)
	}
	root, err := os.OpenRoot(s.outputDir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return root.OpenFile(fmt.Sprintf("shell-%d.log", time.Now().UnixNano()), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
}

// shellPageBytes is the most output one shell_exec result carries; it
// leaves room under the agent's 20 KB result cap for the JSON around it.
const shellPageBytes = 16_000

// pagedOutput writes a long output whole to a file in the output
// directory and returns its first page, its size and its path.
func (s *Source) pagedOutput(res confinedshell.Result) (map[string]any, error) {
	whole := res.Stdout
	if res.Stderr != "" {
		whole += "\n--- stderr ---\n" + res.Stderr
	}
	file, err := s.createOutputFile()
	if err != nil {
		return nil, fmt.Errorf("shell_exec: keep long output: %w", err)
	}
	defer file.Close()
	if _, err := file.WriteString(whole); err != nil {
		return nil, fmt.Errorf("shell_exec: keep long output: %w", err)
	}
	cut := shellPageBytes
	for cut > 0 && !utf8.RuneStart(whole[cut]) {
		cut--
	}
	return map[string]any{
		"exit_code":    res.ExitCode,
		"first_page":   whole[:cut],
		"output_file":  filepath.Join(s.outputDir, filepath.Base(file.Name())),
		"output_bytes": len(whole),
		"lines":        strings.Count(whole, "\n") + 1,
		"note":         "The output is longer than one result. All of it is in output_file; read further pages with shell_exec, for example sed -n '200,400p' on that file.",
	}, nil
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

// relative is resolveInSandbox as a path relative to the working
// directory, the form os.Root takes.
func (s *Source) relative(relPath string) (string, error) {
	cleaned, err := s.resolveInSandbox(relPath)
	if err != nil {
		return "", err
	}
	return filepath.Rel(s.workingDir, cleaned)
}

// insideGitDir reports whether rel names .git or anything under it, in
// any letter case: a host write there plants a hook or a config that the
// operator's own git later runs unconfined (#26).
func insideGitDir(rel string) bool {
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if strings.EqualFold(part, ".git") {
			return true
		}
	}
	return false
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
	rel, err := s.relative(relPath)
	if err != nil {
		return "", fmt.Errorf("read_file: %w", err)
	}
	f, err := s.openForRead(rel)
	if err != nil {
		return "", fmt.Errorf("read_file: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("read_file: %w", err)
	}
	if info.Size() > maxReadFileBytes {
		return "", fmt.Errorf("read_file: %s is %d bytes, over the %d byte limit", relPath, info.Size(), maxReadFileBytes)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxReadFileBytes))
	if err != nil {
		return "", fmt.Errorf("read_file: %w", err)
	}
	return string(data), nil
}

// openForRead opens rel through os.Root. A symlink whose absolute target
// lies inside the working directory (the path is the same inside the
// sandbox) is resolved, re-checked and opened again through the root;
// one that leads out is refused.
func (s *Source) openForRead(rel string) (*os.File, error) {
	f, err := s.root.Open(rel)
	if err == nil {
		return f, nil
	}
	resolved, evalErr := filepath.EvalSymlinks(filepath.Join(s.workingDir, rel))
	if evalErr != nil {
		return nil, err
	}
	if containErr := s.checkContained(resolved); containErr != nil {
		return nil, fmt.Errorf("%q escapes the working directory through a symlink", rel)
	}
	inner, relErr := filepath.Rel(s.workingDir, resolved)
	if relErr != nil {
		return nil, err
	}
	return s.root.Open(inner)
}

// writeFile writes inside the working directory only: through os.Root,
// never into .git, and never through a symlink at the final component,
// dangling or not, since the sandboxed shell can plant one (#26).
func (s *Source) writeFile(args map[string]any) (string, error) {
	relPath, _ := args["path"].(string)
	content, _ := args["content"].(string)
	rel, err := s.relative(relPath)
	if err != nil {
		return "", fmt.Errorf("write_file: %w", err)
	}
	if insideGitDir(rel) {
		return "", fmt.Errorf("write_file: %q is inside .git; write_file never writes there", relPath)
	}
	if dir := filepath.Dir(rel); dir != "." {
		if err := s.root.MkdirAll(dir, 0o755); err != nil {
			return "", fmt.Errorf("write_file: create parent directory: %w", err)
		}
	}
	if info, err := s.root.Lstat(rel); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("write_file: %q is a symlink; write_file does not follow symlinks", relPath)
	}
	f, err := s.root.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return "", fmt.Errorf("write_file: %w", err)
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		return "", fmt.Errorf("write_file: %w", err)
	}
	return fmt.Sprintf("wrote %d bytes to %s", len(content), relPath), nil
}
