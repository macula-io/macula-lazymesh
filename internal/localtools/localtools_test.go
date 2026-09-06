package localtools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newTestSource(t *testing.T) *Source {
	t.Helper()
	dir := t.TempDir()
	src, err := New(Config{WorkingDir: dir, ShellTimeout: 5 * time.Second})
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

func TestListTools_AdvertisesAllThree(t *testing.T) {
	src := newTestSource(t)
	tools, err := src.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools returned error: %v", err)
	}
	want := map[string]bool{toolShellExec: false, toolReadFile: false, toolWriteFile: false}
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
