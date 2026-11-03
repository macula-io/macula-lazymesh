package localtools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func newTestSource(t *testing.T) *Source {
	t.Helper()
	dir := t.TempDir()
	src, err := New(Config{WorkingDir: dir, ShellTimeout: 5 * time.Second, Shell: HostShell(dir)})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	return src
}

func TestNew_RequiresWorkingDir(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatalf("expected an error when WorkingDir is empty")
	}
}

func TestNew_CreatesWorkingDirIfMissing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "workspace")
	if _, err := New(Config{WorkingDir: dir}); err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatalf("expected working dir to be created at %s", dir)
	}
}

func TestShellExec_CapturesStdoutAndExitCode(t *testing.T) {
	src := newTestSource(t)
	result, err := src.CallToolRaw(context.Background(), toolShellExec, `{"command":"echo hi"}`)
	if err != nil {
		t.Fatalf("CallToolRaw returned error: %v", err)
	}
	var parsed struct {
		Stdout   string `json:"stdout"`
		Stderr   string `json:"stderr"`
		ExitCode int    `json:"exit_code"`
	}
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("failed to decode result: %v", err)
	}
	if parsed.Stdout != "hi\n" {
		t.Fatalf("expected stdout %q, got %q", "hi\n", parsed.Stdout)
	}
	if parsed.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", parsed.ExitCode)
	}
}

func TestShellExec_NonZeroExitIsNotAGoError(t *testing.T) {
	src := newTestSource(t)
	result, err := src.CallToolRaw(context.Background(), toolShellExec, `{"command":"exit 3"}`)
	if err != nil {
		t.Fatalf("CallToolRaw should not error on a non-zero exit, got: %v", err)
	}
	var parsed struct {
		ExitCode int `json:"exit_code"`
	}
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("failed to decode result: %v", err)
	}
	if parsed.ExitCode != 3 {
		t.Fatalf("expected exit code 3, got %d", parsed.ExitCode)
	}
}

func TestShellExec_RunsInWorkingDir(t *testing.T) {
	src := newTestSource(t)
	result, err := src.CallToolRaw(context.Background(), toolShellExec, `{"command":"pwd"}`)
	if err != nil {
		t.Fatalf("CallToolRaw returned error: %v", err)
	}
	var parsed struct {
		Stdout string `json:"stdout"`
	}
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("failed to decode result: %v", err)
	}
	got := filepath.Clean(parsed.Stdout[:len(parsed.Stdout)-1]) // trim trailing newline
	if got != src.workingDir {
		t.Fatalf("expected pwd %q, got %q", src.workingDir, got)
	}
}

func TestWriteThenReadFile_RoundTrips(t *testing.T) {
	src := newTestSource(t)
	ctx := context.Background()

	writeArgs, _ := json.Marshal(map[string]string{"path": "notes.txt", "content": "hello world"})
	if _, err := src.CallToolRaw(ctx, toolWriteFile, string(writeArgs)); err != nil {
		t.Fatalf("write_file returned error: %v", err)
	}

	readArgs, _ := json.Marshal(map[string]string{"path": "notes.txt"})
	got, err := src.CallToolRaw(ctx, toolReadFile, string(readArgs))
	if err != nil {
		t.Fatalf("read_file returned error: %v", err)
	}
	if got != "hello world" {
		t.Fatalf("expected content %q, got %q", "hello world", got)
	}
}

func TestWriteFile_CreatesNestedDirectories(t *testing.T) {
	src := newTestSource(t)
	writeArgs, _ := json.Marshal(map[string]string{"path": "a/b/c.txt", "content": "nested"})
	if _, err := src.CallToolRaw(context.Background(), toolWriteFile, string(writeArgs)); err != nil {
		t.Fatalf("write_file returned error: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(src.workingDir, "a", "b", "c.txt"))
	if err != nil {
		t.Fatalf("expected nested file to exist: %v", err)
	}
	if string(data) != "nested" {
		t.Fatalf("expected content %q, got %q", "nested", string(data))
	}
}

// The tests below are the ones that actually matter for this package:
// confirming the sandbox boundary holds against the standard escape
// attempts, not just the happy path.

func TestReadFile_RejectsParentDirectoryEscape(t *testing.T) {
	src := newTestSource(t)
	// Plant a real file just outside the sandbox to make sure a successful
	// escape would have been observable, not just a coincidental "file not
	// found" for an unrelated reason.
	outside := filepath.Join(filepath.Dir(src.workingDir), "secret.txt")
	if err := os.WriteFile(outside, []byte("should not be readable"), 0o600); err != nil {
		t.Fatalf("failed to plant outside file: %v", err)
	}
	defer os.Remove(outside)

	readArgs, _ := json.Marshal(map[string]string{"path": "../secret.txt"})
	if _, err := src.CallToolRaw(context.Background(), toolReadFile, string(readArgs)); err == nil {
		t.Fatalf("expected an error reading a path that escapes the working directory via ..")
	}
}

func TestReadFile_RejectsAbsolutePath(t *testing.T) {
	src := newTestSource(t)
	readArgs, _ := json.Marshal(map[string]string{"path": "/etc/passwd"})
	if _, err := src.CallToolRaw(context.Background(), toolReadFile, string(readArgs)); err == nil {
		t.Fatalf("expected an error reading an absolute path")
	}
}

func TestWriteFile_RejectsParentDirectoryEscape(t *testing.T) {
	src := newTestSource(t)
	writeArgs, _ := json.Marshal(map[string]string{"path": "../escaped.txt", "content": "x"})
	if _, err := src.CallToolRaw(context.Background(), toolWriteFile, string(writeArgs)); err == nil {
		t.Fatalf("expected an error writing a path that escapes the working directory via ..")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(src.workingDir), "escaped.txt")); err == nil {
		t.Fatalf("escape write should not have created a file outside the sandbox")
	}
}

func TestResolveInSandbox_RejectsSiblingDirWithSamePrefix(t *testing.T) {
	// Regression guard for the classic strings.HasPrefix trap: a sandbox
	// at /tmp/foo must not accept /tmp/foobar just because the string
	// "/tmp/foo" is a prefix of "/tmp/foobar".
	src := newTestSource(t)
	sibling := src.workingDir + "-sibling"
	if err := os.MkdirAll(sibling, 0o755); err != nil {
		t.Fatalf("failed to create sibling dir: %v", err)
	}
	defer os.RemoveAll(sibling)

	rel, err := filepath.Rel(src.workingDir, filepath.Join(sibling, "x.txt"))
	if err != nil {
		t.Fatalf("filepath.Rel: %v", err)
	}
	readArgs, _ := json.Marshal(map[string]string{"path": rel})
	if _, err := src.CallToolRaw(context.Background(), toolReadFile, string(readArgs)); err == nil {
		t.Fatalf("expected an error escaping into a sibling directory sharing a path prefix")
	}
}

func TestReadFile_MissingPathIsAnError(t *testing.T) {
	src := newTestSource(t)
	if _, err := src.CallToolRaw(context.Background(), toolReadFile, `{}`); err == nil {
		t.Fatalf("expected an error when path is missing")
	}
}

// TestReadFile_RefusesSymlinkEscape pins the G10 fix: a symlink sitting
// inside the working directory and pointing OUT of it is resolved and
// refused — the lexical containment check alone cannot see it.
func TestReadFile_RefusesSymlinkEscape(t *testing.T) {
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("outside secret"), 0o644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	inside := t.TempDir()
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(inside, "leak.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	src, err := New(Config{Enabled: true, WorkingDir: inside})
	if err != nil {
		t.Fatalf("new source: %v", err)
	}

	if _, err := src.CallToolRaw(context.Background(), "read_file", `{"path":"leak.txt"}`); err == nil {
		t.Fatal("expected a symlinked-out read to be refused")
	}
}

// TestWriteFile_RefusesSymlinkedDirectoryEscape pins the write half: a
// symlinked directory inside the sandbox pointing out is refused as a
// destination.
func TestWriteFile_RefusesSymlinkedDirectoryEscape(t *testing.T) {
	outside := t.TempDir()
	inside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(inside, "escape")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	src, err := New(Config{Enabled: true, WorkingDir: inside})
	if err != nil {
		t.Fatalf("new source: %v", err)
	}

	if _, err := src.CallToolRaw(context.Background(), "write_file", `{"path":"escape/pwned.txt","content":"pwned"}`); err == nil {
		t.Fatal("expected a write through a symlinked-out directory to be refused")
	}
	if _, err := os.Stat(filepath.Join(outside, "pwned.txt")); !os.IsNotExist(err) {
		t.Fatal("the write escaped the sandbox")
	}
}

// TestReadFile_AllowsSymlinkInsideSandbox pins that the fix is not
// blanket symlink refusal: a symlink whose target stays inside the
// working directory still resolves and reads.
func TestReadFile_AllowsSymlinkInsideSandbox(t *testing.T) {
	inside := t.TempDir()
	if err := os.WriteFile(filepath.Join(inside, "real.txt"), []byte("inside content"), 0o644); err != nil {
		t.Fatalf("write real file: %v", err)
	}
	if err := os.Symlink(filepath.Join(inside, "real.txt"), filepath.Join(inside, "alias.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	src, err := New(Config{Enabled: true, WorkingDir: inside})
	if err != nil {
		t.Fatalf("new source: %v", err)
	}

	got, err := src.CallToolRaw(context.Background(), "read_file", `{"path":"alias.txt"}`)
	if err != nil {
		t.Fatalf("in-sandbox symlink refused: %v", err)
	}
	if got != "inside content" {
		t.Fatalf("content = %q", got)
	}
}

// TestNoShellOffersNoShellExec: without a shell runner, shell_exec is
// not listed, and a call to it by name is refused with the reason.
func TestNoShellOffersNoShellExec(t *testing.T) {
	src, err := New(Config{Enabled: true, WorkingDir: t.TempDir(), NoShellReason: "bubblewrap is not installed"})
	if err != nil {
		t.Fatal(err)
	}
	tools, _ := src.ListTools(context.Background())
	for _, tool := range tools {
		if tool.Name == toolShellExec {
			t.Fatal("shell_exec is listed with no shell")
		}
	}
	_, err = src.CallToolRaw(context.Background(), toolShellExec, `{"command":"true"}`)
	if err == nil || !strings.Contains(err.Error(), "bubblewrap is not installed") {
		t.Fatalf("a call with no shell = %v, want a refusal naming the reason", err)
	}
}

// TestLongOutputIsPagedNotCut (#26): output longer than one result is
// kept whole in the output directory, and its last line is reachable.
func TestLongOutputIsPagedNotCut(t *testing.T) {
	dir, out := t.TempDir(), t.TempDir()
	src, err := New(Config{Enabled: true, WorkingDir: dir, Shell: HostShell(dir), OutputDir: out})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := src.CallToolRaw(context.Background(), toolShellExec, `{"command":"i=0; while [ $i -lt 5000 ]; do echo line-$i; i=$((i+1)); done; echo LAST-LINE FAIL"}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > 20_000 {
		t.Fatalf("one result is %d bytes, past the agent's 20 KB cap", len(raw))
	}
	var res struct {
		FirstPage  string `json:"first_page"`
		OutputFile string `json:"output_file"`
	}
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(res.FirstPage, "line-0\n") || !strings.HasPrefix(res.OutputFile, out) {
		t.Fatalf("result = %.200s", raw)
	}
	whole, err := os.ReadFile(res.OutputFile)
	if err != nil || !strings.Contains(string(whole), "LAST-LINE FAIL") {
		t.Fatalf("the last line is not in the kept output: %v", err)
	}
}

// ---- Slice D (#46): confined search and apply_patch, paging past the cap ----
//
// These are the invariants pinned in #46's threat-model comment. The cap
// they page past is agent.maxToolResultBytes (20,000 bytes), the ceiling
// on what the harness stores of a tool result for the model.

// newGitCheckout returns a Source over a fresh git checkout, plus that
// checkout's path. apply_patch's working-tree diff comes from git, so the
// cases that assert on the diff need a real repository.
func newGitCheckout(t *testing.T) (*Source, string) {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-q")
	runGit(t, dir, "config", "user.email", "test@example.invalid")
	runGit(t, dir, "config", "user.name", "test")
	src, err := New(Config{Enabled: true, WorkingDir: dir})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return src, src.workingDir
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func callSearch(t *testing.T, src *Source, args map[string]any) (string, error) {
	t.Helper()
	body, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	return src.CallToolRaw(context.Background(), toolSearch, string(body))
}

func callApplyPatch(t *testing.T, src *Source, patch string) (string, error) {
	t.Helper()
	body, err := json.Marshal(map[string]string{"patch": patch})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	return src.CallToolRaw(context.Background(), toolApplyPatch, string(body))
}

// TestListTools_AdvertisesTheLocalSet pins that new tools are advertised
// and nothing else is: the coding profile's allowlist is built from this
// list, so a tool missing here is silently unreachable.
func TestListTools_AdvertisesTheLocalSet(t *testing.T) {
	src := newTestSource(t)
	tools, err := src.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools returned error: %v", err)
	}
	want := map[string]bool{
		toolShellExec: false, toolReadFile: false, toolWriteFile: false,
		toolSearch: false, toolApplyPatch: false,
	}
	for _, tool := range tools {
		if _, ok := want[tool.Name]; !ok {
			t.Fatalf("unexpected tool advertised: %s", tool.Name)
		}
		want[tool.Name] = true
	}
	for name, seen := range want {
		if !seen {
			t.Fatalf("expected tool %s to be advertised", name)
		}
	}
}

// TestSearch_FindsLiteralAndRegexMatches covers the two modes: a literal
// pattern is matched as text (a regex metacharacter is not special), and a
// regex pattern is matched as a pattern.
func TestSearch_FindsLiteralAndRegexMatches(t *testing.T) {
	src := newTestSource(t)
	if err := os.WriteFile(filepath.Join(src.workingDir, "a.txt"), []byte("alpha\nfunc (x) {}\nbeta\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	literal, err := callSearch(t, src, map[string]any{"pattern": "func (x)"})
	if err != nil {
		t.Fatalf("literal search: %v", err)
	}
	if !strings.Contains(literal, "a.txt:2:") {
		t.Fatalf("literal search did not find the line, got %q", literal)
	}

	regex, err := callSearch(t, src, map[string]any{"pattern": `^f\w+ \(x\)`, "regex": true})
	if err != nil {
		t.Fatalf("regex search: %v", err)
	}
	if !strings.Contains(regex, "a.txt:2:") {
		t.Fatalf("regex search did not find the line, got %q", regex)
	}

	none, err := callSearch(t, src, map[string]any{"pattern": "not-there"})
	if err != nil {
		t.Fatalf("empty search: %v", err)
	}
	if !strings.Contains(none, "no matches") {
		t.Fatalf("empty search should say so, got %q", none)
	}
}

// TestSearch_CannotReadOutsideCheckout: only #46's invariant 3.
func TestSearch_CannotReadOutsideCheckout(t *testing.T) {
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("OUTSIDE-SECRET"), 0o644); err != nil {
		t.Fatalf("plant: %v", err)
	}
	inside := t.TempDir()
	src, err := New(Config{Enabled: true, WorkingDir: inside})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := os.WriteFile(filepath.Join(inside, "inside.txt"), []byte("inside text\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// (a) an escaping path is refused
	if got, err := callSearch(t, src, map[string]any{"pattern": "OUTSIDE-SECRET", "path": "../secret.txt"}); err == nil {
		t.Fatalf("search through .. was not refused, got %q", got)
	}
	if got, err := callSearch(t, src, map[string]any{"pattern": "OUTSIDE-SECRET", "path": filepath.Join(outside, "secret.txt")}); err == nil {
		t.Fatalf("search of an absolute outside path was not refused, got %q", got)
	}

	// (b) a symlinked directory inside the checkout is not walked through
	if err := os.Symlink(outside, filepath.Join(inside, "escape")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	got, err := callSearch(t, src, map[string]any{"pattern": "OUTSIDE-SECRET"})
	if err != nil {
		t.Fatalf("search of the checkout: %v", err)
	}
	if strings.Contains(got, "OUTSIDE-SECRET") {
		t.Fatalf("search walked through a symlink out of the checkout:\n%s", got)
	}

	// (c) the in-checkout file is still searchable, so (b) did not pass by
	// searching nothing at all
	if got, err := callSearch(t, src, map[string]any{"pattern": "inside text"}); err != nil || !strings.Contains(got, "inside.txt:1:") {
		t.Fatalf("in-checkout search broke: %q err=%v", got, err)
	}
}

// TestSearch_PagesPastTheCap: invariant 4. A result whose matches would
// exceed the 20 KB cap comes back as a page under it, with the offset for
// the next one, and the last match is reachable by paging.
func TestSearch_PagesPastTheCap(t *testing.T) {
	src := newTestSource(t)
	const total = 3000
	var body strings.Builder
	for i := 0; i < total; i++ {
		fmt.Fprintf(&body, "needle line %04d\n", i)
	}
	if err := os.WriteFile(filepath.Join(src.workingDir, "big.txt"), []byte(body.String()), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	seen := 0
	offset := 0
	for page := 0; page < 100; page++ {
		out, err := callSearch(t, src, map[string]any{"pattern": "needle", "offset": offset})
		if err != nil {
			t.Fatalf("page at offset %d: %v", offset, err)
		}
		if len(out) >= 20_000 {
			t.Fatalf("page at offset %d is %d bytes, at or over the 20 KB cap the harness stores", offset, len(out))
		}
		lines := 0
		for _, line := range strings.Split(out, "\n") {
			if strings.HasPrefix(line, "big.txt:") {
				lines++
			}
		}
		seen += lines
		if strings.Contains(out, "end of results") {
			if seen != total {
				t.Fatalf("paging ended after %d of %d matches", seen, total)
			}
			if !strings.Contains(out, fmt.Sprintf("needle line %04d", total-1)) {
				t.Fatalf("the last page does not hold the last match: %q", out)
			}
			return
		}
		next := nextOffsetFromFooter(t, out)
		if next <= offset {
			t.Fatalf("footer offset %d does not advance past %d: %q", next, offset, out)
		}
		offset = next
	}
	t.Fatalf("paging did not reach the end in 100 pages (%d matches so far)", seen)
}

// nextOffsetFromFooter pulls the offset out of a paging footer.
func nextOffsetFromFooter(t *testing.T, out string) int {
	t.Helper()
	const marker = `"offset":`
	i := strings.Index(out, marker)
	if i < 0 {
		t.Fatalf("paged result has no offset footer: %q", out)
	}
	rest := out[i+len(marker):]
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	if end == 0 {
		t.Fatalf("footer offset is not a number: %q", out)
	}
	n, err := strconv.Atoi(rest[:end])
	if err != nil {
		t.Fatalf("footer offset: %v", err)
	}
	return n
}

// TestReadFile_PagesPastTheCap: invariant 4 for read_file. A long file is
// returned as pages under the cap, and the last line is reachable.
func TestReadFile_PagesPastTheCap(t *testing.T) {
	src := newTestSource(t)
	const total = 4000
	var body strings.Builder
	for i := 1; i <= total; i++ {
		fmt.Fprintf(&body, "line %05d\n", i)
	}
	if err := os.WriteFile(filepath.Join(src.workingDir, "long.txt"), []byte(body.String()), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	first, err := src.CallToolRaw(context.Background(), toolReadFile, `{"path":"long.txt"}`)
	if err != nil {
		t.Fatalf("read_file: %v", err)
	}
	if len(first) >= 20_000 {
		t.Fatalf("first page is %d bytes, over the 20 KB cap", len(first))
	}
	if !strings.Contains(first, "line 00001") {
		t.Fatalf("first page does not start at the first line: %q", first[:120])
	}
	last := first
	offset := 1
	for page := 0; page < 100; page++ {
		next := nextReadOffset(t, last)
		if next < 0 {
			if !strings.Contains(last, "line 04000") {
				t.Fatalf("the last page does not hold the last line: %q", last)
			}
			return
		}
		offset = next
		out, err := src.CallToolRaw(context.Background(), toolReadFile,
			fmt.Sprintf(`{"path":"long.txt","offset":%d}`, offset))
		if err != nil {
			t.Fatalf("page at line %d: %v", offset, err)
		}
		if len(out) >= 20_000 {
			t.Fatalf("page at line %d is %d bytes, over the cap", offset, len(out))
		}
		last = out
	}
	t.Fatalf("read_file paging did not reach the end")
}

// nextReadOffset reads the next-page offset out of read_file's footer, or
// -1 when this was the last page.
func nextReadOffset(t *testing.T, out string) int {
	t.Helper()
	const marker = "offset "
	i := strings.LastIndex(out, marker)
	if i < 0 {
		return -1
	}
	rest := out[i+len(marker):]
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	if end == 0 {
		t.Fatalf("read_file footer has no offset number: %q", out[max(0, len(out)-120):])
	}
	n, err := strconv.Atoi(rest[:end])
	if err != nil {
		t.Fatalf("read_file footer offset: %v", err)
	}
	return n
}

// TestApplyPatch_RefusesPathOutsideCheckout: invariant 1, all three
// spellings plus the symlinked-directory variant, and nothing written.
func TestApplyPatch_RefusesPathOutsideCheckout(t *testing.T) {
	outside := t.TempDir()
	planted := filepath.Join(outside, "planted.txt")
	if err := os.WriteFile(planted, []byte("untouched\n"), 0o644); err != nil {
		t.Fatalf("plant: %v", err)
	}

	cases := []struct {
		name  string
		patch string
	}{
		{"parent directory", "--- a/../planted.txt\n+++ b/../planted.txt\n@@ -1 +1 @@\n-untouched\n+pwned\n"},
		{"absolute path", fmt.Sprintf("--- a/%s\n+++ b/%s\n@@ -1 +1 @@\n-untouched\n+pwned\n", planted, planted)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := newTestSource(t)
			if _, err := callApplyPatch(t, src, tc.patch); err == nil {
				t.Fatalf("patch escaping the checkout was applied")
			}
			assertFile(t, planted, "untouched\n")
		})
	}

	t.Run("symlinked directory", func(t *testing.T) {
		inside := t.TempDir()
		src, err := New(Config{Enabled: true, WorkingDir: inside})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if err := os.Symlink(outside, filepath.Join(inside, "escape")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		patch := "--- a/escape/planted.txt\n+++ b/escape/planted.txt\n@@ -1 +1 @@\n-untouched\n+pwned\n"
		if _, err := callApplyPatch(t, src, patch); err == nil {
			t.Fatalf("patch through a symlinked-out directory was applied")
		}
		assertFile(t, planted, "untouched\n")
	})
}

// TestApplyPatch_RefusesSymlinkCreation: a unified diff may carry git's
// extended header for a symlink; a link is a launchpad for the next call,
// so it is refused outright.
func TestApplyPatch_RefusesSymlinkCreation(t *testing.T) {
	src := newTestSource(t)
	patch := "diff --git a/link b/link\nnew file mode 120000\nindex 0000000..1234567\n--- /dev/null\n+++ b/link\n@@ -0,0 +1 @@\n+/etc/passwd\n"
	if _, err := callApplyPatch(t, src, patch); err == nil {
		t.Fatalf("a patch creating a symlink was applied")
	}
	if _, err := os.Lstat(filepath.Join(src.workingDir, "link")); !os.IsNotExist(err) {
		t.Fatalf("the symlink was created: %v", err)
	}
}

// TestApplyPatch_RefusesWholePatchWhenOneTargetEscapes: validation covers
// every target before anything is written, so a patch that touches one
// good and one escaping path writes nothing at all.
func TestApplyPatch_RefusesWholePatchWhenOneTargetEscapes(t *testing.T) {
	src := newTestSource(t)
	safe := filepath.Join(src.workingDir, "safe.txt")
	if err := os.WriteFile(safe, []byte("safe\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	patch := "--- a/safe.txt\n+++ b/safe.txt\n@@ -1 +1 @@\n-safe\n+changed\n" +
		"--- a/../escape.txt\n+++ b/../escape.txt\n@@ -1 +1 @@\n-x\n+y\n"
	if _, err := callApplyPatch(t, src, patch); err == nil {
		t.Fatalf("a patch with one escaping target was accepted")
	}
	assertFile(t, safe, "safe\n")
	if _, err := os.Stat(filepath.Join(filepath.Dir(src.workingDir), "escape.txt")); !os.IsNotExist(err) {
		t.Fatalf("the escaping half of the patch was written")
	}
}

// TestApplyPatch_ReturnsWorkingTreeDiff: invariant 5. The result carries
// the working tree's own diff for the paths the patch touched, which is
// what the TUI shows the operator.
func TestApplyPatch_ReturnsWorkingTreeDiff(t *testing.T) {
	src, dir := newGitCheckout(t)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	runGit(t, dir, "add", "a.txt")
	runGit(t, dir, "commit", "-q", "-m", "base")

	patch := "--- a/a.txt\n+++ b/a.txt\n@@ -1,2 +1,2 @@\n one\n-two\n+three\n"
	got, err := callApplyPatch(t, src, patch)
	if err != nil {
		t.Fatalf("apply_patch: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "a.txt")); err != nil || string(data) != "one\nthree\n" {
		t.Fatalf("the patch was not applied: %q err=%v", data, err)
	}
	want := strings.TrimRight(runGit(t, dir, "diff", "--", "a.txt"), "\n")
	for _, line := range strings.Split(want, "\n") {
		if strings.HasPrefix(line, "index ") || strings.HasPrefix(line, "diff --git ") {
			continue // git's own header varies with the object ids; the hunks are what matter
		}
		if !strings.Contains(got, line) {
			t.Fatalf("apply_patch's result is missing the working-tree diff line %q:\n%s", line, got)
		}
	}
	if !strings.Contains(got, "a.txt") {
		t.Fatalf("apply_patch's result does not name the file: %q", got)
	}
}

// TestApplyPatch_RefusesMalformedPatch: a patch that is not a unified diff
// is refused before touching anything, with a message a model can act on.
func TestApplyPatch_RefusesMalformedPatch(t *testing.T) {
	src := newTestSource(t)
	if _, err := callApplyPatch(t, src, "this is not a patch\n"); err == nil {
		t.Fatalf("a non-patch was accepted")
	}
}

func assertFile(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("expected %s to exist: %v", path, err)
	}
	if string(data) != want {
		t.Fatalf("%s = %q, want %q", path, data, want)
	}
}

// TestApplyPatch_WorksWithoutAGitRepository: a checkout need not be a git
// repository to be edited. The patch still applies; only the diff shown
// back to the operator is unavailable, and the result says so rather than
// pretending the working tree has no changes.
func TestApplyPatch_WorksWithoutAGitRepository(t *testing.T) {
	src := newTestSource(t)
	patch := "--- /dev/null\n+++ b/new.txt\n@@ -0,0 +1,2 @@\n+hello\n+world\n"
	got, err := callApplyPatch(t, src, patch)
	if err != nil {
		t.Fatalf("apply_patch in a non-git checkout: %v", err)
	}
	assertFile(t, filepath.Join(src.workingDir, "new.txt"), "hello\nworld\n")
	if !strings.Contains(got, "not a git repository") {
		t.Fatalf("the result should say the diff is unavailable, got %q", got)
	}
}

// TestApplyPatch_RefusesDanglingSymlinkTarget pins the hole Saturnus found
// reviewing slice C (#26): a symlink whose target does not exist yet slips
// past EvalSymlinks (it errors, and an error is not a resolution), and the
// write then follows the link out of the checkout. A shell in the checkout
// can plant exactly that link, so both write paths must refuse it before
// writing anything.
func TestApplyPatch_RefusesDanglingSymlinkTarget(t *testing.T) {
	outside := t.TempDir()
	src := newTestSource(t)
	link := filepath.Join(src.workingDir, "link.txt")
	if err := os.Symlink(filepath.Join(outside, "pwned.txt"), link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	patch := "--- a/link.txt\n+++ b/link.txt\n@@ -0,0 +1 @@\n+pwned\n"
	_, err := callApplyPatch(t, src, patch)
	if err == nil {
		t.Fatalf("a patch through a dangling symlink was applied")
	}
	// git refuses some of these on its own; the refusal has to be OURS, so
	// it cannot depend on which git version is installed.
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("the refusal did not come from the containment check: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "pwned.txt")); !os.IsNotExist(err) {
		t.Fatalf("the patch wrote outside the checkout through the dangling link")
	}
}

// TestWriteFile_RefusesDanglingSymlinkTarget is the same hole on
// write_file, which shares resolveForWrite with apply_patch's validation.
func TestWriteFile_RefusesDanglingSymlinkTarget(t *testing.T) {
	outside := t.TempDir()
	src := newTestSource(t)
	if err := os.Symlink(filepath.Join(outside, "pwned.txt"), filepath.Join(src.workingDir, "link.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if _, err := src.CallToolRaw(context.Background(), toolWriteFile, `{"path":"link.txt","content":"pwned"}`); err == nil {
		t.Fatalf("write_file through a dangling symlink was allowed")
	}
	if _, err := os.Stat(filepath.Join(outside, "pwned.txt")); !os.IsNotExist(err) {
		t.Fatalf("write_file wrote outside the checkout through the dangling link")
	}
}

// TestApplyPatch_AppliesMultipleFilesAndHunks: one patch touching two
// files, one of them twice, applies as the diff says.
func TestApplyPatch_AppliesMultipleFilesAndHunks(t *testing.T) {
	src := newTestSource(t)
	if err := os.WriteFile(filepath.Join(src.workingDir, "a.txt"), []byte("one\ntwo\nthree\nfour\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	patch := "--- a/a.txt\n+++ b/a.txt\n" +
		"@@ -1,2 +1,2 @@\n one\n-two\n+TWO\n" +
		"@@ -3,2 +3,2 @@\n three\n-four\n+FOUR\n" +
		"--- /dev/null\n+++ b/b.txt\n@@ -0,0 +1,2 @@\n+hello\n+world\n"
	if _, err := callApplyPatch(t, src, patch); err != nil {
		t.Fatalf("apply_patch: %v", err)
	}
	assertFile(t, filepath.Join(src.workingDir, "a.txt"), "one\nTWO\nthree\nFOUR\n")
	assertFile(t, filepath.Join(src.workingDir, "b.txt"), "hello\nworld\n")
}

// TestApplyPatch_DeletesAFile: a /dev/null target removes the file.
func TestApplyPatch_DeletesAFile(t *testing.T) {
	src := newTestSource(t)
	if err := os.WriteFile(filepath.Join(src.workingDir, "gone.txt"), []byte("bye\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	patch := "--- a/gone.txt\n+++ /dev/null\n@@ -1 +0,0 @@\n-bye\n"
	if _, err := callApplyPatch(t, src, patch); err != nil {
		t.Fatalf("apply_patch: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(src.workingDir, "gone.txt")); !os.IsNotExist(err) {
		t.Fatalf("the deleted file still exists: %v", err)
	}
}

// TestApplyPatch_WritesNothingWhenAHunkDoesNotMatch: the patch is planned
// over every file before any of it is written, so a later file's failure
// leaves the earlier file untouched.
func TestApplyPatch_WritesNothingWhenAHunkDoesNotMatch(t *testing.T) {
	src := newTestSource(t)
	if err := os.WriteFile(filepath.Join(src.workingDir, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src.workingDir, "b.txt"), []byte("different\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	patch := "--- a/a.txt\n+++ b/a.txt\n@@ -1 +1 @@\n-one\n+ONE\n" +
		"--- a/b.txt\n+++ b/b.txt\n@@ -1 +1 @@\n-not-there\n+nope\n"
	if _, err := callApplyPatch(t, src, patch); err == nil {
		t.Fatalf("a patch whose second hunk does not match was applied")
	}
	assertFile(t, filepath.Join(src.workingDir, "a.txt"), "one\n")
	assertFile(t, filepath.Join(src.workingDir, "b.txt"), "different\n")
}

// TestApplyPatch_RefusesGitDir: a patch into .git would plant a hook the
// operator's own git later runs, so it is refused whatever the diff says.
func TestApplyPatch_RefusesGitDir(t *testing.T) {
	src := newTestSource(t)
	patch := "--- /dev/null\n+++ b/.git/hooks/pre-commit\n@@ -0,0 +1 @@\n+#!/bin/sh\n"
	if _, err := callApplyPatch(t, src, patch); err == nil {
		t.Fatalf("a patch into .git/hooks was applied")
	}
	if _, err := os.Lstat(filepath.Join(src.workingDir, ".git", "hooks", "pre-commit")); !os.IsNotExist(err) {
		t.Fatalf("the hook was written: %v", err)
	}
}

// TestSearch_ScopedToASubdirectoryNamesTheCheckoutPath: a scoped search
// reports each match by its path in the checkout, and a file that shares a
// name with one outside the scope is not confused for it.
func TestSearch_ScopedToASubdirectoryNamesTheCheckoutPath(t *testing.T) {
	src := newTestSource(t)
	if err := os.MkdirAll(filepath.Join(src.workingDir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src.workingDir, "sub", "a.txt"), []byte("needle inside\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src.workingDir, "a.txt"), []byte("needle outside the scope\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := callSearch(t, src, map[string]any{"pattern": "needle", "path": "sub"})
	if err != nil {
		t.Fatalf("scoped search: %v", err)
	}
	if !strings.Contains(got, "sub/a.txt:1:needle inside") {
		t.Fatalf("scoped search did not label the match with its checkout path:\n%s", got)
	}
	if strings.Contains(got, "outside the scope") {
		t.Fatalf("scoped search read outside its scope:\n%s", got)
	}
}

// TestApplyPatch_AppliesGitsOwnDiff is the end-to-end shape check: a diff
// written by git itself (index lines, a/ b/ prefixes, hunks at real
// offsets) applies through apply_patch, and the result matches what git
// had in the working tree.
func TestApplyPatch_AppliesGitsOwnDiff(t *testing.T) {
	src, dir := newGitCheckout(t)
	original := "alpha\nbeta\ngamma\ndelta\nepsilon\n"
	if err := os.WriteFile(filepath.Join(dir, "doc.txt"), []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "doc.txt")
	runGit(t, dir, "commit", "-q", "-m", "base")

	edited := "alpha\nBETA\ngamma\ndelta\nEPSILON\nadded\n"
	if err := os.WriteFile(filepath.Join(dir, "doc.txt"), []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	patch := runGit(t, dir, "diff", "--", "doc.txt")
	runGit(t, dir, "checkout", "--", "doc.txt")
	assertFile(t, filepath.Join(dir, "doc.txt"), original)

	if _, err := callApplyPatch(t, src, patch); err != nil {
		t.Fatalf("applying git's own diff: %v\npatch:\n%s", err, patch)
	}
	assertFile(t, filepath.Join(dir, "doc.txt"), edited)
}
