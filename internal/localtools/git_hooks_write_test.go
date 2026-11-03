package localtools

// Review r=1 of #46 (PR 69), finding 1: a symlinked directory inside the
// checkout let both file tools write into .git -- a hook or a config the
// operator's own git runs unconfined the next time it touches the
// checkout.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func plainSource(t *testing.T, dir string) *Source {
	t.Helper()
	src, err := New(Config{Enabled: true, WorkingDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	return src
}

// checkoutWithHooksLink is an untrusted repository: it commits a symlink
// whose target is the checkout's own .git/hooks.
func checkoutWithHooksLink(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git", "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(".git", "hooks"), filepath.Join(dir, "tools")); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestWriteFileCannotWriteIntoGitThroughASymlinkedDirectory(t *testing.T) {
	dir := checkoutWithHooksLink(t)
	src := plainSource(t, dir)
	_, err := src.CallToolRaw(context.Background(), "write_file", `{"path":"tools/pre-commit","content":"#!/bin/sh\nevil\n"}`)
	if err == nil {
		t.Error("write_file accepted a path through a symlinked directory into .git")
	}
	if _, statErr := os.Stat(filepath.Join(dir, ".git", "hooks", "pre-commit")); statErr == nil {
		t.Fatal("ESCAPE: write_file planted .git/hooks/pre-commit through the symlink")
	}
}

func TestApplyPatchCannotWriteIntoGitThroughASymlinkedDirectory(t *testing.T) {
	dir := checkoutWithHooksLink(t)
	src := plainSource(t, dir)
	patch := "--- /dev/null\n+++ b/tools/post-checkout\n@@ -0,0 +1,2 @@\n+#!/bin/sh\n+evil\n"
	if _, err := callApplyPatch(t, src, patch); err == nil {
		t.Error("apply_patch accepted a path through a symlinked directory into .git")
	}
	if _, statErr := os.Stat(filepath.Join(dir, ".git", "hooks", "post-checkout")); statErr == nil {
		t.Fatal("ESCAPE: apply_patch planted .git/hooks/post-checkout through the symlink")
	}
}

// A symlinked directory that leads somewhere harmless is still refused on
// a write: the tool cannot tell a committed link from a planted one, and
// the shell can plant either.
func TestAWriteThroughASymlinkedDirectoryIsRefused(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "real"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	src := plainSource(t, dir)
	if _, err := src.CallToolRaw(context.Background(), "write_file", `{"path":"link/file.txt","content":"x"}`); err == nil {
		t.Error("write_file wrote through a symlinked directory")
	}
	if _, err := os.Stat(filepath.Join(dir, "real", "file.txt")); err == nil {
		t.Fatal("write_file followed the link and wrote the target")
	}
}
