package confinedshell

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Options says where the shell may and may not look.
type Options struct {
	// Checkout is the working directory: the only operator path mounted
	// read-write.
	Checkout string
	// ReadOnly lists extra toolchain paths the operator wants mounted
	// (local_tools.read_only_paths).
	ReadOnly []string
	// Private lists paths no mount may expose or contain: lazymesh's
	// config and data directories, the API key file, the identity file.
	// ~/.ssh and ~/.gnupg are always private.
	Private []string
	// Bwrap, Home and CacheDir default to bubblewrap on PATH, the user's
	// home and cache directories; tests set them.
	Bwrap    string
	Home     string
	CacheDir string
}

// Prepare builds the sandbox for opts.Checkout and proves it with
// SelfTest. Any error means the coding profile offers no shell: there is
// no weaker fallback.
func Prepare(ctx context.Context, opts Options) (*Sandbox, error) {
	if runtime.GOOS != "linux" {
		return nil, fmt.Errorf("no confined shell on %s yet: the coding profile offers no shell here", runtime.GOOS)
	}
	opts, err := withDefaults(opts)
	if err != nil {
		return nil, err
	}
	fence := newFence(opts.Home, opts.Private)
	checkout, err := fence.check(opts.Checkout, "the working directory")
	if err != nil {
		return nil, err
	}
	gitRO, err := gitMounts(checkout)
	if err != nil {
		return nil, err
	}
	scratch, output, err := makeWorkspace(opts.CacheDir, checkout)
	if err != nil {
		return nil, err
	}
	ro, goEnv := toolchainPaths()
	ro = append(ro, opts.ReadOnly...)
	var mounts []string
	for _, p := range ro {
		clean, err := fence.check(p, "read-only path")
		if err != nil {
			return nil, err
		}
		mounts = append(mounts, clean)
	}
	sb := &Sandbox{Bwrap: opts.Bwrap, Checkout: checkout, GitReadOnly: gitRO, Scratch: scratch, Output: output, ReadOnly: mounts, Env: environment(scratch, goEnv)}
	if err := SelfTest(ctx, sb, Probe{Checkout: checkout, Scratch: scratch, Output: output, SecretDir: opts.Home}); err != nil {
		return nil, err
	}
	return sb, nil
}

func withDefaults(opts Options) (Options, error) {
	var err error
	if opts.Bwrap == "" {
		if opts.Bwrap, err = exec.LookPath("bwrap"); err != nil {
			return opts, fmt.Errorf("bubblewrap (bwrap) is not installed: the coding profile offers no shell until it is")
		}
	}
	if _, err := os.Stat(opts.Bwrap); err != nil {
		return opts, fmt.Errorf("bubblewrap at %s: %w", opts.Bwrap, err)
	}
	if opts.Home == "" {
		if opts.Home, err = os.UserHomeDir(); err != nil {
			return opts, err
		}
	}
	if opts.CacheDir == "" {
		if opts.CacheDir, err = os.UserCacheDir(); err != nil {
			return opts, err
		}
	}
	return opts, nil
}

// fence refuses a mount that is the home directory or holds it, or that
// overlaps a private path in either direction.
type fence struct {
	home    string
	private []string
}

func newFence(home string, private []string) fence {
	f := fence{home: resolve(home)}
	for _, p := range append([]string{filepath.Join(home, ".ssh"), filepath.Join(home, ".gnupg")}, private...) {
		if p != "" {
			f.private = append(f.private, resolve(p))
		}
	}
	return f
}

func (f fence) check(path, what string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("%s %q is not an absolute path", what, path)
	}
	p := resolve(path)
	if within(f.home, p) {
		return "", fmt.Errorf("%s %s is the home directory or holds it; the shell may not see home", what, p)
	}
	for _, private := range f.private {
		if within(p, private) || within(private, p) {
			return "", fmt.Errorf("%s %s overlaps %s, which the shell may not see", what, p, private)
		}
	}
	return p, nil
}

// resolve cleans path and follows its symlinks as far as they exist.
func resolve(path string) string {
	clean := filepath.Clean(path)
	if real, err := filepath.EvalSymlinks(clean); err == nil {
		return real
	}
	return clean
}

// within reports whether path is dir or inside it.
func within(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// makeWorkspace is one directory per checkout under the user's cache:
// rw/ (mounted read-write: home/ and the Go build cache) and out/
// (mounted read-only: long output the host writes). home/ and out/ are
// emptied at every start, so nothing a command left there carries into
// the next session; the build cache is kept, and is only ever used
// inside the sandbox.
func makeWorkspace(cacheDir, checkout string) (string, string, error) {
	sum := sha256.Sum256([]byte(checkout))
	dir := filepath.Join(cacheDir, "lazymesh", "confined", hex.EncodeToString(sum[:8]))
	rw, out := filepath.Join(dir, "rw"), filepath.Join(dir, "out")
	for _, stale := range []string{filepath.Join(rw, "home"), out} {
		if err := os.RemoveAll(stale); err != nil {
			return "", "", fmt.Errorf("clear %s: %w", stale, err)
		}
	}
	for _, sub := range []string{filepath.Join(rw, "home"), filepath.Join(rw, "gocache"), out} {
		if err := os.MkdirAll(sub, 0o700); err != nil {
			return "", "", fmt.Errorf("scratch directory: %w", err)
		}
	}
	return resolve(rw), resolve(out), nil
}

// gitMounts is what keeps the checkout's .git read-only inside: the .git
// directory, or the .git file and the gitdir it names when that lies in
// the checkout (with its commondir). A checkout with no .git, or with a
// symlink there, gets no shell: an empty slot or a link is something a
// command could fill or retarget, and the host's git would follow it.
func gitMounts(checkout string) ([]string, error) {
	dotGit := filepath.Join(checkout, ".git")
	info, err := os.Lstat(dotGit)
	switch {
	case os.IsNotExist(err):
		return nil, fmt.Errorf("the working directory %s has no .git; the coding profile's shell runs only in a git checkout, whose .git it keeps read-only", checkout)
	case err != nil:
		return nil, err
	case info.Mode()&os.ModeSymlink != 0:
		return nil, fmt.Errorf("%s is a symlink; the coding profile's shell needs a real .git it can keep read-only", dotGit)
	case info.IsDir():
		return []string{dotGit}, nil
	case !info.Mode().IsRegular():
		return nil, fmt.Errorf("%s is neither a directory nor a file", dotGit)
	}
	mounts := []string{dotGit}
	gitDir := pointedDir(checkout, dotGit, "gitdir:")
	if gitDir == "" {
		return mounts, nil
	}
	if within(gitDir, checkout) {
		mounts = append(mounts, gitDir)
	}
	if common := pointedDir(gitDir, filepath.Join(gitDir, "commondir"), ""); common != "" && within(common, checkout) {
		mounts = append(mounts, common)
	}
	return mounts, nil
}

// pointedDir reads a git pointer file (".git" with a "gitdir:" prefix,
// or "commondir") and returns the existing directory it names, resolved
// against base, or "" when there is none.
func pointedDir(base, file, prefix string) string {
	data, err := os.ReadFile(file)
	if err != nil {
		return ""
	}
	target := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(data)), prefix))
	if target == "" {
		return ""
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(base, target)
	}
	resolved := resolve(target)
	if info, err := os.Stat(resolved); err != nil || !info.IsDir() {
		return ""
	}
	return resolved
}

// goToolchain is what the Go toolchain needs inside: its root and the
// module cache, read-only.
type goToolchain struct {
	root     string
	modCache string
}

// toolchainPaths finds the Go toolchain on PATH, if any, and returns the
// paths outside the system directories it needs mounted.
func toolchainPaths() ([]string, goToolchain) {
	var tc goToolchain
	out, err := exec.Command("go", "env", "GOROOT", "GOMODCACHE").Output()
	if err != nil {
		return nil, tc
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 2 {
		return nil, tc
	}
	tc.root, tc.modCache = strings.TrimSpace(lines[0]), strings.TrimSpace(lines[1])
	var paths []string
	for _, p := range []string{tc.root, tc.modCache} {
		if p == "" || within(resolve(p), "/usr") {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			paths = append(paths, p)
		}
	}
	return paths, tc
}

func environment(scratch string, tc goToolchain) []string {
	path := "/usr/local/bin:/usr/bin:/bin"
	if tc.root != "" {
		path = filepath.Join(tc.root, "bin") + ":" + path
	}
	env := []string{
		"PATH=" + path,
		"HOME=" + filepath.Join(scratch, "home"),
		"TMPDIR=/tmp",
		"LANG=C.UTF-8",
		"TERM=dumb",
		"GOCACHE=" + filepath.Join(scratch, "gocache"),
		"GOPROXY=off",
		"GOTOOLCHAIN=local",
		"GOFLAGS=-mod=readonly",
	}
	if tc.modCache != "" {
		env = append(env, "GOMODCACHE="+tc.modCache)
	}
	return env
}

// OutputDir is where long command output is kept for paging.
func (s *Sandbox) OutputDir() string {
	return s.Output
}
