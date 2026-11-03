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
//
// The file tools are read_file, write_file, search and apply_patch. Every
// one of them reaches the checkout only through s.root, an os.Root over
// the working directory: a path that resolves outside is refused by
// construction, not by a check a caller can forget. apply_patch writes the
// patched files itself, through the same root, rather than handing the
// diff to a host process -- the diff is model-authored text, and a host
// `git apply` would write with host rights wherever the diff says.
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
	"regexp"
	"strconv"
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
	toolSearch       = "search"
	toolApplyPatch   = "apply_patch"

	// pageBudgetBytes is how much of a result's body a paged tool puts in
	// ONE page (#46). It sits below agent's 20 KB result cap, the ceiling
	// on what the harness stores of a tool result for the model: a page is
	// built to fit under that ceiling together with its footer, so the
	// ceiling never fires mid-result and the model is always told how to
	// reach the rest. Fable's observation on #38: a test run overflows the
	// cap, so results past it are paged, never cut.
	pageBudgetBytes = 18_000

	// read_file's page shape: a line count the model can reason about,
	// hard-capped so one call cannot pull an enormous slice at once even
	// when every line is short.
	defaultReadLines = 500
	maxReadLines     = 2000

	// search's page shape. max_results is a hint the model may lower; the
	// byte budget is what actually bounds a page.
	defaultSearchResults = 100
	maxSearchResults     = 500
	// maxSearchFileBytes skips a file too big to be worth scanning, and
	// maxMatchLineBytes keeps one pathological line (a minified bundle, a
	// generated file) from eating the whole page.
	maxSearchFileBytes = 1 << 20
	maxMatchLineBytes  = 400
)

// ToolNames is every tool this source serves: the names an operator can
// put on an allowlist for it.
func ToolNames() []string {
	return []string{toolShellExec, toolReadFile, toolWriteFile, toolSearch, toolApplyPatch}
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
			Description: "Read a text file's contents. Path is relative to lazymesh's configured working directory; it cannot escape it. A file too long for one result comes back in pages, with the offset for the next one.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "File path, relative to the working directory.",
					},
					"offset": map[string]any{
						"type":        "integer",
						"description": "First line to return, 1-based. Default 1.",
					},
					"limit": map[string]any{
						"type":        "integer",
						"description": "Most lines to return. Default 500, at most 2000.",
					},
				},
				"required": []string{"path"},
			},
		},
		{
			Name:        toolSearch,
			Description: "Search the working directory for a literal string or a regular expression, returning one line per match as path:line:text. Results come back in pages, with the offset for the next one.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"pattern": map[string]any{
						"type":        "string",
						"description": "What to look for. Literal text unless \"regex\" is true.",
					},
					"regex": map[string]any{
						"type":        "boolean",
						"description": "Treat pattern as a Go regular expression. Default false.",
					},
					"path": map[string]any{
						"type":        "string",
						"description": "File or directory to search, relative to the working directory. Default the whole working directory. .git is always skipped.",
					},
					"offset": map[string]any{
						"type":        "integer",
						"description": "How many matches to skip, for the next page. Default 0.",
					},
					"max_results": map[string]any{
						"type":        "integer",
						"description": "Most matches to return. Default 100, at most 500; a page is also bounded by size.",
					},
				},
				"required": []string{"pattern"},
			},
		},
		{
			Name:        toolApplyPatch,
			Description: "Apply a unified diff to the working directory. Every path it touches must resolve inside the working directory; a patch that would write outside it, into .git, or create a symlink is refused and nothing is written. Returns the working tree's diff for the files it touched.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"patch": map[string]any{
						"type":        "string",
						"description": "The unified diff to apply, headers included (--- a/file, +++ b/file, @@ hunks).",
					},
				},
				"required": []string{"patch"},
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
		// shell_exec is not offered at all without a runner, rather than
		// offered and refused on every call (#26): a tool the model can
		// see but never use wastes a round every time.
		offered := tools[:0:0]
		for _, tool := range tools {
			if tool.Name != toolShellExec {
				offered = append(offered, tool)
			}
		}
		return offered, nil
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
	case toolSearch:
		return s.search(ctx, args)
	case toolApplyPatch:
		return s.applyPatch(ctx, args)
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

	// A command may change the checkout -- a formatter, a test that writes
	// an artifact -- so the tree is read before and after it: the diff the
	// operator is shown has to be the checkout as it now stands, not the
	// patch's snapshot from earlier (#46).
	before, beforeOK := s.treeDiff(ctx, nil)
	res, runErr := s.shell.Run(ctx, command)
	fields := map[string]any{"stdout": res.Stdout, "stderr": res.Stderr, "exit_code": res.ExitCode}
	if s.outputDir != "" && len(res.Stdout)+len(res.Stderr) > shellPageBytes {
		paged, pageErr := s.pagedOutput(res)
		if pageErr != nil {
			return "", pageErr
		}
		fields = paged
	}
	// The after-read gets its own budget: a command that outlived the
	// timeout leaves ctx expired, and a read made under it would answer
	// nothing -- which is how a change became invisible (#46 review r=2).
	readCtx, cancelRead := context.WithTimeout(context.WithoutCancel(ctx), diffReadTimeout)
	after, afterOK := s.treeDiff(readCtx, nil)
	cancelRead()
	switch {
	case afterOK && after != before && strings.TrimSpace(after) != "":
		fields["working_tree_diff"] = s.capDiff(after)
	case beforeOK && !afterOK:
		fields["working_tree_diff_unread"] = "git could not read the checkout after this command, so any change it made is not shown here"
	}
	if runErr != nil {
		// The command failed or timed out; the tree may still have
		// changed, so the result is returned rather than only an error.
		fields["error"] = runErr.Error()
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

// diffReadTimeout is the budget for the after-command diff read, which
// runs on its own context: the shell's is expired exactly when a command
// outlives it, which is when the tree most needs reading (#46 review r=2).
const diffReadTimeout = 5 * time.Second

// capDiff keeps a working-tree diff under the result budget. A diff has
// no offset the model could page with, so the whole of it is written to
// the output directory when there is one, and the page says where.
func (s *Source) capDiff(diff string) string {
	if len(diff) <= shellPageBytes {
		return diff
	}
	cut := shellPageBytes
	for cut > 0 && diff[cut] != '\n' {
		cut--
	}
	if cut == 0 {
		cut = shellPageBytes
	}
	note := fmt.Sprintf("[diff cut at %d of %d bytes", cut, len(diff))
	if s.outputDir != "" {
		if f, err := s.createOutputFile(); err == nil {
			_, writeErr := f.WriteString(diff)
			path := f.Name()
			f.Close()
			if writeErr == nil {
				note += "; the whole diff is in " + path
			}
		}
	}
	return diff[:cut+1] + note + "]\n"
}

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

// refuseSymlinkedPath refuses rel when any one of its components, as the
// host would traverse it, is a symlink.
//
// os.Root keeps a symlink from reaching outside the checkout, but it
// follows a middle component whose target stays inside -- which is how a
// committed `tools -> .git/hooks` reached .git through both file tools
// (#46 review r=1). The tools are model-driven, so they refuse the whole
// class instead of reasoning about which links are harmless: an untrusted
// repository can commit one, and the confined shell can plant one.
func (s *Source) refuseSymlinkedPath(rel, display string) error {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	for i := 1; i <= len(parts); i++ {
		prefix := strings.Join(parts[:i], "/")
		info, err := s.root.Lstat(prefix)
		if err != nil {
			// Nothing there yet: no link to follow, and the open that
			// follows reports anything else.
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%q goes through the symlink %s; the file tools never write through one", display, prefix)
		}
	}
	return nil
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

// readFile returns a file's text, or one page of it (#46). A file that
// fits in a page comes back byte-for-byte as it is on disk; a longer one
// is cut at a line boundary with the offset of the next page in the
// footer, so a long file is paged rather than truncated.
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

	first, _ := intArg(args, "offset", 1)
	limit, _ := intArg(args, "limit", defaultReadLines)
	if first < 1 {
		return "", fmt.Errorf("read_file: offset is the 1-based line to start at, got %d", first)
	}
	if limit < 1 {
		return "", fmt.Errorf("read_file: limit must be at least 1, got %d", limit)
	}
	limit = min(limit, maxReadLines)

	text := string(data)
	lines, trailingNewline := splitLines(text)
	if first == 1 && len(lines) <= limit && len(text) <= pageBudgetBytes {
		return text, nil // the whole file fits: return it exactly, with no footer
	}
	if first > len(lines) {
		return "", fmt.Errorf("read_file: %s has %d lines; offset %d is past the end", relPath, len(lines), first)
	}

	var page strings.Builder
	shown := 0
	for i := first - 1; i < len(lines) && shown < limit; i++ {
		line := lines[i] + "\n"
		if shown > 0 && page.Len()+len(line) > pageBudgetBytes {
			break
		}
		page.WriteString(line)
		shown++
	}
	last := first + shown - 1
	if last < len(lines) {
		page.WriteString(fmt.Sprintf("[showing lines %d-%d of %d; call read_file again with offset %d for the next page]\n",
			first, last, len(lines), last+1))
	}
	body := page.String()
	if !trailingNewline {
		body = strings.TrimSuffix(body, "\n")
	}
	return body, nil
}

// splitLines splits text into lines without keeping the newline, and says
// whether the text ended with one, so a page reassembles into exactly what
// the file holds.
func splitLines(text string) ([]string, bool) {
	if text == "" {
		return nil, false
	}
	trailing := strings.HasSuffix(text, "\n")
	trimmed := strings.TrimSuffix(text, "\n")
	return strings.Split(trimmed, "\n"), trailing
}

// intArg reads an integer argument, which a JSON decoder hands over as
// float64, falling back to def when it is absent or not a number.
func intArg(args map[string]any, name string, def int) (int, bool) {
	switch v := args[name].(type) {
	case float64:
		return int(v), true
	case int:
		return v, true
	}
	return def, false
}

// search walks the working directory for a literal or a regex pattern and
// returns one page of matches (#46). It never follows a symlink: the walk
// skips them entirely, so a link planted in the checkout cannot take a
// search outside it, and .git is skipped. Each file it does read is opened
// through s.root, so a path that resolves outside is refused there too.
func (s *Source) search(ctx context.Context, args map[string]any) (string, error) {
	pattern, _ := args["pattern"].(string)
	if pattern == "" {
		return "", fmt.Errorf("search: \"pattern\" is required")
	}
	asRegex, _ := args["regex"].(bool)
	offset, _ := intArg(args, "offset", 0)
	limit, _ := intArg(args, "max_results", defaultSearchResults)
	if offset < 0 {
		return "", fmt.Errorf("search: offset must not be negative, got %d", offset)
	}
	if limit < 1 {
		return "", fmt.Errorf("search: max_results must be at least 1, got %d", limit)
	}
	limit = min(limit, maxSearchResults)

	root, err := s.searchRoot(args)
	if err != nil {
		return "", fmt.Errorf("search: %w", err)
	}
	matcher, err := newMatcher(pattern, asRegex)
	if err != nil {
		return "", fmt.Errorf("search: %w", err)
	}

	var page strings.Builder
	seen, shown := 0, 0
	more := false
	walkErr := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || ctx.Err() != nil {
			return nil // an unreadable entry is skipped, not fatal: the rest of the checkout is still searchable
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil // symlinks, devices, sockets: never followed
		}
		matches, ok := s.matchesInFile(path, d, matcher)
		if !ok {
			return nil
		}
		for _, m := range matches {
			seen++
			if seen <= offset {
				continue
			}
			line := m + "\n"
			if shown > 0 && page.Len()+len(line) > pageBudgetBytes {
				more = true
				return filepath.SkipAll
			}
			page.WriteString(line)
			shown++
			if shown == limit {
				more = true
				return filepath.SkipAll
			}
		}
		return nil
	})
	if walkErr != nil && walkErr != filepath.SkipAll {
		return "", fmt.Errorf("search: %w", walkErr)
	}
	if more && !s.hasMatchAfter(root, offset+shown, matcher) {
		more = false
	}

	switch {
	case seen == 0 && offset == 0:
		return "[no matches]\n", nil
	case shown == 0 && !more:
		return fmt.Sprintf("[no matches at or after offset %d]\n", offset), nil
	}
	body := page.String()
	if more {
		body += fmt.Sprintf("[showing matches %d-%d; call search again with \"offset\":%d for the next page]\n",
			offset+1, offset+shown, offset+shown)
	} else {
		body += fmt.Sprintf("[end of results: %d matches]\n", offset+shown)
	}
	return body, nil
}

// searchRoot resolves the optional "path" argument, which may name a file
// or a directory inside the checkout, and defaults to all of it.
func (s *Source) searchRoot(args map[string]any) (string, error) {
	relPath, _ := args["path"].(string)
	if relPath == "" || relPath == "." {
		return s.workingDir, nil
	}
	rel, err := s.relative(relPath)
	if err != nil {
		return "", err
	}
	info, err := s.root.Lstat(rel)
	if err != nil {
		return "", fmt.Errorf("%q cannot be read: %w", relPath, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("%q is a symlink; search does not follow links", relPath)
	}
	if !info.Mode().IsRegular() && !info.IsDir() {
		return "", fmt.Errorf("%q is neither a file nor a directory", relPath)
	}
	return filepath.Join(s.workingDir, rel), nil
}

// matchesInFile returns this file's matches in source order, already
// formatted as path:line:text, and whether the file was readable as text
// at all. A binary file (a NUL byte early on) and one over the size limit
// are skipped rather than searched.
func (s *Source) matchesInFile(path string, d os.DirEntry, matcher *regexp.Regexp) ([]string, bool) {
	info, err := d.Info()
	if err != nil || info.Size() > maxSearchFileBytes {
		return nil, false
	}
	// The label is relative to the checkout, never to whatever subdirectory
	// was searched, and it is the same spelling os.Root is asked for: a
	// path relative to the search root would open the wrong file.
	rel, err := filepath.Rel(s.workingDir, path)
	if err != nil {
		return nil, false
	}
	f, err := s.root.Open(rel)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	// The size above is the listing's word for it; the read is bounded by
	// the same number rather than trusted to agree (#46 review r=1).
	data, err := io.ReadAll(io.LimitReader(f, maxSearchFileBytes))
	if err != nil || isBinary(data) {
		return nil, false
	}
	var out []string
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if !matcher.MatchString(line) {
			continue
		}
		out = append(out, fmt.Sprintf("%s:%d:%s", rel, i+1, clipMatchLine(line)))
	}
	return out, true
}

// hasMatchAfter reports whether a match exists at or after index skip. It
// exists so a page says "call again with offset N" only when there really
// is a page after it: a page that ends exactly on the last match says
// "end of results" instead.
func (s *Source) hasMatchAfter(root string, skip int, matcher *regexp.Regexp) bool {
	seen := 0
	found := false
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		matches, ok := s.matchesInFile(path, d, matcher)
		if !ok {
			return nil
		}
		seen += len(matches)
		if seen > skip {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

// newMatcher compiles a literal pattern into an escaped regexp, so both
// modes share one matcher and a literal pattern's own metacharacters are
// never special.
func newMatcher(pattern string, asRegex bool) (*regexp.Regexp, error) {
	if !asRegex {
		return regexp.Compile(regexp.QuoteMeta(pattern))
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("pattern %q: %w", pattern, err)
	}
	return re, nil
}

// isBinary reports whether data looks like a binary file, by git's own
// rule: a NUL byte in the first 8000 bytes.
func isBinary(data []byte) bool {
	head := data
	if len(head) > 8000 {
		head = head[:8000]
	}
	return bytes.IndexByte(head, 0) >= 0
}

// clipMatchLine bounds one match's text so a minified file cannot eat a
// whole page with a single line.
func clipMatchLine(line string) string {
	if len(line) <= maxMatchLineBytes {
		return line
	}
	cut := maxMatchLineBytes
	for cut > 0 && !utf8.RuneStart(line[cut]) {
		cut--
	}
	return line[:cut] + "..."
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
	if err := s.refuseSymlinkedPath(rel, relPath); err != nil {
		return "", fmt.Errorf("write_file: %w", err)
	}
	if dir := filepath.Dir(rel); dir != "." {
		if err := s.root.MkdirAll(dir, 0o755); err != nil {
			return "", fmt.Errorf("write_file: create parent directory: %w", err)
		}
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

// ---- apply_patch (#46) -------------------------------------------------

// applyPatch applies a unified diff inside the working directory.
//
// Every path the diff touches is resolved through s.relative and opened
// through s.root, so a patch that reaches outside the checkout -- by "..",
// by an absolute path, or through a symlink -- is refused by construction,
// and a write never follows a final-component symlink, dangling or not.
// Writes into .git are refused for the same reason write_file refuses
// them. The patched files are written here rather than handed to a host
// `git apply`: the diff is model-authored text, and a host process would
// write with host rights wherever the diff says.
//
// Nothing is written until every file in the patch has been resolved, read
// and matched: one bad target refuses the whole patch, and the working
// tree is left exactly as it was.
func (s *Source) applyPatch(ctx context.Context, args map[string]any) (string, error) {
	patch, _ := args["patch"].(string)
	if strings.TrimSpace(patch) == "" {
		return "", fmt.Errorf("apply_patch: \"patch\" is required")
	}
	files, err := parsePatch(patch)
	if err != nil {
		return "", fmt.Errorf("apply_patch: %w", err)
	}

	plans := make([]patchPlan, 0, len(files))
	named := make(map[string]bool, len(files))
	for _, file := range files {
		plan, err := s.planPatchFile(file)
		if err != nil {
			return "", fmt.Errorf("apply_patch: %w", err)
		}
		// Every section is planned from what is on disk, so a second
		// section on the same file would overwrite the first and report
		// success for an edit that was never applied (#46 review r=1).
		if named[plan.rel] {
			return "", fmt.Errorf("apply_patch: the patch names %s twice; send one section per file", plan.display)
		}
		named[plan.rel] = true
		plans = append(plans, plan)
	}
	for _, plan := range plans {
		if err := s.commitPatchPlan(plan); err != nil {
			return "", fmt.Errorf("apply_patch: %w", err)
		}
	}

	paths := make([]string, 0, len(plans))
	hunks := 0
	for _, plan := range plans {
		paths = append(paths, plan.display)
		hunks += plan.hunks
	}
	summary := fmt.Sprintf("applied %d hunk(s) to %d file(s): %s\n\n", hunks, len(paths), strings.Join(paths, ", "))
	return summary + s.workingTreeDiff(ctx, paths), nil
}

// patchPlan is one file's pending change: what to write (or remove) and
// where, all resolved and matched, so committing cannot half-apply.
type patchPlan struct {
	rel      string // the path os.Root is asked for, relative to the checkout
	display  string // the path as the diff spelled it, for messages
	content  string
	remove   bool
	create   bool
	hunks    int
	symlinkR bool // the target exists and is a symlink (always refused)
}

// patchFile is one file's parsed diff.
type patchFile struct {
	oldRel, newRel string
	create, remove bool
	hunks          []patchHunk
}

type patchHunk struct {
	oldStart, oldCount int
	newCount           int
	// oldLeft and newLeft are how many lines of each side the header
	// still promises: a line belongs to the hunk until both reach zero,
	// which is how git knows where a hunk ends (#46 review r=1).
	oldLeft, newLeft int
	lines            []patchLine
}

// patchLine is one line of a hunk: its kind (' ', '-' or '+') and text.
type patchLine struct {
	kind byte
	text string
	// noNewline marks the "\ No newline at end of file" marker: this line
	// is the file's last and it carries no trailing newline.
	noNewline bool
}

// parsePatch reads a unified diff into per-file hunks, refusing anything it
// cannot apply exactly rather than guessing.
func parsePatch(patch string) ([]patchFile, error) {
	var files []patchFile
	var current *patchFile
	var hunk *patchHunk
	lines := strings.Split(strings.TrimSuffix(patch, "\n"), "\n")

	flushHunk := func() {
		if hunk != nil && current != nil {
			current.hunks = append(current.hunks, *hunk)
			hunk = nil
		}
	}
	flushFile := func() {
		flushHunk()
		if current != nil && (current.newRel != "" || current.oldRel != "") {
			files = append(files, *current)
		}
		current = nil
	}

	for _, raw := range lines {
		line := strings.TrimSuffix(raw, "\r")

		// Inside a hunk every line belongs to it until the header's counts
		// are spent -- git emits exactly that many. Read the other way, a
		// deletion of "-- old note" is written "--- old note" and an
		// addition of "++ x" is written "+++ x", so a patch could name a
		// file through the text it carries (#46 review r=1).
		if hunk != nil && (hunk.oldLeft > 0 || hunk.newLeft > 0) {
			switch {
			case strings.HasPrefix(line, "\\"):
				if len(hunk.lines) == 0 {
					return nil, fmt.Errorf("a \"no newline at end of file\" marker appears with no line before it")
				}
				hunk.lines[len(hunk.lines)-1].noNewline = true
			case line == "":
				// Some tools emit a truly empty line for an empty context line.
				hunk.lines = append(hunk.lines, patchLine{kind: ' '})
				hunk.oldLeft--
				hunk.newLeft--
			case line[0] == ' ' || line[0] == '-' || line[0] == '+':
				hunk.lines = append(hunk.lines, patchLine{kind: line[0], text: line[1:]})
				if line[0] != '+' {
					hunk.oldLeft--
				}
				if line[0] != '-' {
					hunk.newLeft--
				}
			default:
				return nil, fmt.Errorf("unexpected line in the patch: %q", line)
			}
			if hunk.oldLeft < 0 || hunk.newLeft < 0 {
				return nil, fmt.Errorf("a hunk carries more lines than its header counts")
			}
			continue
		}
		if hunk != nil {
			if strings.HasPrefix(line, "\\") {
				if len(hunk.lines) == 0 {
					return nil, fmt.Errorf("a \"no newline at end of file\" marker appears with no line before it")
				}
				hunk.lines[len(hunk.lines)-1].noNewline = true
				continue
			}
			flushHunk() // its counts are spent: the next line is not its own
		}
		switch {
		case strings.HasPrefix(line, "diff --git "):
			flushFile()
			current = &patchFile{}
		case strings.HasPrefix(line, "--- "):
			flushFile()
			current = &patchFile{}
			path, err := patchHeaderPath(line[len("--- "):])
			if err != nil {
				return nil, err
			}
			if path == "/dev/null" {
				current.create = true
			} else {
				current.oldRel = path
			}
		case strings.HasPrefix(line, "+++ "):
			if current == nil {
				return nil, fmt.Errorf("a +++ header appears before any --- header")
			}
			path, err := patchHeaderPath(line[len("+++ "):])
			if err != nil {
				return nil, err
			}
			if path == "/dev/null" {
				current.remove = true
			} else {
				current.newRel = path
			}
		case strings.HasPrefix(line, "new file mode "), strings.HasPrefix(line, "deleted file mode "),
			strings.HasPrefix(line, "old mode "), strings.HasPrefix(line, "new mode "):
			if strings.Contains(line, "120000") {
				return nil, fmt.Errorf("the patch creates or changes a symlink (%s), which apply_patch never does", strings.TrimSpace(line))
			}
		case strings.HasPrefix(line, "index "):
			if fields := strings.Fields(line); len(fields) == 3 && fields[2] == "120000" {
				return nil, fmt.Errorf("the patch carries a symlink mode (%s), which apply_patch never does", strings.TrimSpace(line))
			}
		case strings.HasPrefix(line, "rename from "), strings.HasPrefix(line, "rename to "),
			strings.HasPrefix(line, "copy from "), strings.HasPrefix(line, "copy to "),
			strings.HasPrefix(line, "similarity index "), strings.HasPrefix(line, "dissimilarity index "):
			return nil, fmt.Errorf("renames and copies are not applied; send a delete and a create instead")
		case strings.HasPrefix(line, "@@ "):
			flushHunk()
			if current == nil {
				return nil, fmt.Errorf("a @@ hunk appears before any file header")
			}
			parsed, err := parseHunkHeader(line)
			if err != nil {
				return nil, err
			}
			hunk = parsed
		case line == "":
			// Blank line between files.
		default:
			return nil, fmt.Errorf("unexpected line in the patch: %q", line)
		}
	}
	if hunk != nil && (hunk.oldLeft > 0 || hunk.newLeft > 0) {
		return nil, fmt.Errorf("the last hunk ends before its header's line count")
	}
	flushFile()
	if len(files) == 0 {
		return nil, fmt.Errorf("no file paths found: send a unified diff with ---/+++ headers")
	}
	return files, nil
}

// patchHeaderPath turns one ---/+++ header value into the path it names,
// dropping git's a/ or b/ prefix and any timestamp a plain diff appended.
func patchHeaderPath(value string) (string, error) {
	if tab := strings.IndexByte(value, '\t'); tab >= 0 {
		value = value[:tab]
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("a ---/+++ header has no path")
	}
	if strings.HasPrefix(value, `"`) {
		unquoted, err := strconv.Unquote(value)
		if err != nil {
			return "", fmt.Errorf("header path %s cannot be read: %w", value, err)
		}
		value = unquoted
	}
	return strings.TrimPrefix(strings.TrimPrefix(value, "a/"), "b/"), nil
}

// parseHunkHeader reads "@@ -oldStart,oldCount +newStart,newCount @@".
func parseHunkHeader(line string) (*patchHunk, error) {
	fields := strings.Fields(line)
	if len(fields) < 3 || !strings.HasPrefix(fields[1], "-") || !strings.HasPrefix(fields[2], "+") {
		return nil, fmt.Errorf("hunk header %q is not of the form @@ -a,b +c,d @@", line)
	}
	oldStart, oldCount, err := parseHunkRange(fields[1])
	if err != nil {
		return nil, err
	}
	_, newCount, err := parseHunkRange(fields[2])
	if err != nil {
		return nil, err
	}
	return &patchHunk{oldStart: oldStart, oldCount: oldCount, newCount: newCount, oldLeft: oldCount, newLeft: newCount}, nil
}

// parseHunkRange reads "start" or "start,count" (the count defaults to 1
// and may be 0 for a pure insertion).
func parseHunkRange(field string) (int, int, error) {
	body := field[1:]
	start, count := body, "1"
	if comma := strings.IndexByte(body, ','); comma >= 0 {
		start, count = body[:comma], body[comma+1:]
	}
	startN, err := strconv.Atoi(start)
	if err != nil {
		return 0, 0, fmt.Errorf("hunk range %q has a bad start", field)
	}
	countN, err := strconv.Atoi(count)
	if err != nil {
		return 0, 0, fmt.Errorf("hunk range %q has a bad count", field)
	}
	return startN, countN, nil
}

// planPatchFile resolves one file's target inside the checkout, reads what
// is there and applies the hunks in memory. It writes nothing.
func (s *Source) planPatchFile(file patchFile) (patchPlan, error) {
	relPath := file.newRel
	if relPath == "" {
		relPath = file.oldRel
	}
	rel, err := s.relative(relPath)
	if err != nil {
		return patchPlan{}, err
	}
	if insideGitDir(rel) {
		return patchPlan{}, fmt.Errorf("%q is inside .git; apply_patch never writes there", relPath)
	}
	plan := patchPlan{rel: rel, display: relPath, create: file.create, remove: file.remove, hunks: len(file.hunks)}
	if err := s.refuseSymlinkedPath(rel, relPath); err != nil {
		return patchPlan{}, err
	}

	old, trailing, err := s.readForPatch(rel)
	if err != nil {
		return patchPlan{}, err
	}
	if file.create && old != nil {
		return patchPlan{}, fmt.Errorf("%q already exists, so the patch's creation of it does not apply", relPath)
	}
	if !file.create && old == nil {
		return patchPlan{}, fmt.Errorf("%q does not exist, so the patch does not apply", relPath)
	}

	newLines, newTrailing, err := applyHunks(old, trailing, file.hunks)
	if err != nil {
		return patchPlan{}, fmt.Errorf("%s: %w", relPath, err)
	}
	if file.remove {
		if len(newLines) != 0 {
			return patchPlan{}, fmt.Errorf("%q is deleted by the patch but its hunks leave content behind", relPath)
		}
		plan.remove = true
		return plan, nil
	}
	plan.content = joinLines(newLines, newTrailing)
	return plan, nil
}

// readForPatch reads rel through the root, returning nil lines for a file
// that does not exist yet.
func (s *Source) readForPatch(rel string) ([]string, bool, error) {
	f, err := s.root.Open(rel)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("%s cannot be read: %w", rel, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, false, err
	}
	if info.Size() > maxReadFileBytes {
		return nil, false, fmt.Errorf("%s is over the %d byte limit apply_patch can edit", rel, maxReadFileBytes)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxReadFileBytes))
	if err != nil {
		return nil, false, err
	}
	lines, trailing := splitLines(string(data))
	return lines, trailing, nil
}

// applyHunks returns the patched lines. Each hunk is matched against the
// file: at its recorded position when the context is there, otherwise at
// the first place later in the file where it does match -- the same offset
// tolerance patch itself has, so a hunk still applies after an earlier one
// shifted the lines.
//
// The trailing newline comes from the file's own end when the patch leaves
// it alone, and from the patch's last line when the patch rewrites it: a
// "\ No newline at end of file" marker on that line means the result has
// none, and its absence means it has one (a patch that creates a file
// states its newline this way, with no old file to inherit from).
func applyHunks(lines []string, trailing bool, hunks []patchHunk) ([]string, bool, error) {
	out := make([]string, 0, len(lines))
	pos := 0
	var lastWritten *patchLine
	for i, hunk := range hunks {
		oldSide := make([]string, 0, len(hunk.lines))
		for _, line := range hunk.lines {
			if line.kind != '+' {
				oldSide = append(oldSide, line.text)
			}
		}
		want := hunk.oldStart - 1
		if hunk.oldCount == 0 {
			want = hunk.oldStart
		}
		start, ok := matchHunk(lines, oldSide, pos, want)
		if !ok {
			return nil, false, fmt.Errorf("hunk %d does not match the file (it looks for %q)", i+1, strings.Join(oldSide, "\\n"))
		}
		out = append(out, lines[pos:start]...)
		cursor := start
		for j := range hunk.lines {
			line := &hunk.lines[j]
			switch line.kind {
			case ' ':
				if cursor >= len(lines) || lines[cursor] != line.text {
					return nil, false, fmt.Errorf("hunk %d does not match the file at line %d", i+1, cursor+1)
				}
				out = append(out, line.text)
				lastWritten = line
				cursor++
			case '-':
				if cursor >= len(lines) || lines[cursor] != line.text {
					return nil, false, fmt.Errorf("hunk %d does not match the file at line %d", i+1, cursor+1)
				}
				cursor++
			case '+':
				out = append(out, line.text)
				lastWritten = line
			default:
				return nil, false, fmt.Errorf("hunk %d has a line that is neither context, an addition nor a deletion", i+1)
			}
		}
		pos = cursor
	}
	out = append(out, lines[pos:]...)
	if len(lines[pos:]) == 0 && lastWritten != nil {
		trailing = !lastWritten.noNewline
	}
	return out, trailing, nil
}

// matchHunk finds where oldSide matches in lines, preferring want and
// falling back to the first match at or after from.
func matchHunk(lines, oldSide []string, from, want int) (int, bool) {
	if matchesAt(lines, oldSide, want) && want >= from {
		return want, true
	}
	for i := from; i+len(oldSide) <= len(lines); i++ {
		if matchesAt(lines, oldSide, i) {
			return i, true
		}
	}
	return 0, false
}

func matchesAt(lines, oldSide []string, at int) bool {
	if at < 0 || at+len(oldSide) > len(lines) {
		return len(oldSide) == 0 && at >= 0 && at <= len(lines)
	}
	for i, want := range oldSide {
		if lines[at+i] != want {
			return false
		}
	}
	return true
}

// joinLines rebuilds a file's text from its lines, keeping whether the file
// ended with a newline.
func joinLines(lines []string, trailing bool) string {
	if len(lines) == 0 {
		return ""
	}
	body := strings.Join(lines, "\n")
	if trailing {
		body += "\n"
	}
	return body
}

// commitPatchPlan writes (or removes) one file through the root. The parent
// directory is created through the root too, so a missing directory cannot
// become an escape.
func (s *Source) commitPatchPlan(plan patchPlan) error {
	rel := plan.rel
	if plan.remove {
		if err := s.root.Remove(rel); err != nil {
			return fmt.Errorf("%s cannot be removed: %w", plan.display, err)
		}
		return nil
	}
	if dir := filepath.Dir(rel); dir != "." {
		if err := s.root.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("%s: create parent directory: %w", rel, err)
		}
	}
	f, err := s.root.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("%s cannot be written: %w", rel, err)
	}
	defer f.Close()
	if _, err := f.WriteString(plan.content); err != nil {
		return fmt.Errorf("%s cannot be written: %w", rel, err)
	}
	return nil
}

// workingTreeDiff is git's own diff of the patched paths, which is what the
// operator is shown: the change as the checkout now holds it, not the patch
// as the model wrote it. Read-only: nothing here writes.
func (s *Source) workingTreeDiff(ctx context.Context, targets []string) string {
	out, ok := s.treeDiff(ctx, targets)
	if !ok {
		return "[no working-tree diff: this checkout is not a git repository]\n"
	}
	if strings.TrimSpace(out) == "" {
		return "[the working tree shows no change in those paths]\n"
	}
	return out
}

// treeDiff runs git's own diff in the checkout and says whether git
// answered at all. The arguments and the environment are pinned so a
// checkout cannot make this read run code on the host: --no-ext-diff and
// --no-textconv (a diff driver), core.fsmonitor=false and
// --ignore-submodules=all (a nested repository's own config, which the
// sandbox leaves writable -- #46 review r=2), no inherited GIT_*
// variables, and no global or system config to read a filter or a
// fsmonitor from.
func (s *Source) treeDiff(ctx context.Context, targets []string) (string, bool) {
	args := append([]string{
		"-c", "core.fsmonitor=false",
		"--no-pager", "diff", "--no-color", "--no-ext-diff", "--no-textconv",
		"--ignore-submodules=all", "--",
	}, targets...)
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = s.workingDir
	cmd.Env = hostGitEnv()
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	return string(out), true
}

// hostGitEnv is the environment a host git invocation gets: none of the
// caller's GIT_* variables (GIT_DIR would point it at another repository)
// and no global or system config, so only the checkout's own tracked
// configuration applies.
func hostGitEnv() []string {
	env := make([]string, 0, len(os.Environ())+3)
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "GIT_") || strings.HasPrefix(kv, "GIT_CONFIG") {
			continue
		}
		env = append(env, kv)
	}
	return append(env,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_TERMINAL_PROMPT=0",
	)
}
