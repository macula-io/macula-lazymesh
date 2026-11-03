package localtools

// Review r=1 of #46 (PR 69), findings 2 and 3: a patch that names the same
// file twice quietly lost the first edit, and a hunk line beginning with
// "-- " or "++ " was read as a file header, so git's own diff for such a
// file was refused and a patch's target could be spoofed.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func checkoutWithFile(t *testing.T, name, content string) (*Source, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return plainSource(t, dir), dir
}

// R2: two sections on one file. The second was planned from disk, not from
// the first section's result, so it overwrote it and the tool reported
// success for an edit that was not applied.
func TestAPatchThatNamesOneFileTwiceIsRefused(t *testing.T) {
	src, dir := checkoutWithFile(t, "f.txt", "one\ntwo\n")
	patch := "--- a/f.txt\n+++ b/f.txt\n@@ -1,2 +1,3 @@\n one\n two\n+THREE\n" +
		"--- a/f.txt\n+++ b/f.txt\n@@ -1,2 +1,3 @@\n one\n two\n+FOUR\n"
	if _, err := callApplyPatch(t, src, patch); err == nil {
		t.Fatal("a patch naming f.txt twice was accepted; the first edit is lost")
	}
	got, err := os.ReadFile(filepath.Join(dir, "f.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "one\ntwo\n" {
		t.Fatalf("the file changed under a refused patch: %q", got)
	}
}

// R3a: deleting a line whose text begins with "-- " is written "--- old
// note", which is exactly what git emits. It was read as a header, so
// git's own diff for such a file could not be applied at all.
func TestAPatchCanDeleteALineBeginningWithTwoDashes(t *testing.T) {
	src, dir := checkoutWithFile(t, "f.txt", "-- old note\nkeep\n")
	patch := "--- a/f.txt\n+++ b/f.txt\n@@ -1,2 +1,1 @@\n--- old note\n keep\n"
	if _, err := callApplyPatch(t, src, patch); err != nil {
		t.Fatalf("a deletion of a line beginning with \"-- \" was refused: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "f.txt"))
	if string(got) != "keep\n" {
		t.Fatalf("f.txt = %q, want %q", got, "keep\n")
	}
}

// R3b: adding a line whose text begins with "++ " is written "+++ x",
// which was read as a header for a file named x -- the patch's target
// could be spoofed by the text it carried.
func TestAPatchCanAddALineBeginningWithTwoPluses(t *testing.T) {
	src, dir := checkoutWithFile(t, "f.txt", "one\n")
	patch := "--- a/f.txt\n+++ b/f.txt\n@@ -1,1 +1,2 @@\n one\n+++ x\n"
	if _, err := callApplyPatch(t, src, patch); err != nil {
		t.Fatalf("an addition of a line beginning with \"++ \" was refused: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "f.txt"))
	if string(got) != "one\n++ x\n" {
		t.Fatalf("f.txt = %q, want %q", got, "one\n++ x\n")
	}
	if _, err := os.Stat(filepath.Join(dir, "x")); err == nil {
		t.Fatal("the patch created a file named by the added line's text")
	}
}

// A hunk ends when its line counts are spent, so a header that follows
// one is read as a header again.
func TestAHeaderAfterAFullHunkIsStillAHeader(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("one\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	src := plainSource(t, dir)
	patch := "--- a/a.txt\n+++ b/a.txt\n@@ -1,1 +1,2 @@\n one\n+++ a\n" +
		"--- a/b.txt\n+++ b/b.txt\n@@ -1,1 +1,2 @@\n one\n+++ b\n"
	if _, err := callApplyPatch(t, src, patch); err != nil {
		t.Fatalf("two files in one patch were refused: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "b.txt")); string(got) != "one\n++ b\n" {
		t.Fatalf("b.txt = %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "a.txt")); !strings.HasPrefix(string(got), "one\n++ a") {
		t.Fatalf("a.txt = %q", got)
	}
}
