package repocache

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testLogger() *slog.Logger {
	return slog.Default()
}

func TestGitEnv(t *testing.T) {
	t.Parallel()
	env := gitEnv()

	// Must contain GIT_TERMINAL_PROMPT=0.
	found := false
	for _, entry := range env {
		if entry == "GIT_TERMINAL_PROMPT=0" {
			found = true
			break
		}
	}
	if !found {
		t.Error("gitEnv() must include GIT_TERMINAL_PROMPT=0")
	}

	// Must contain HOME from the current environment.
	home := os.Getenv("HOME")
	if home == "" {
		t.Skip("HOME not set in test environment")
	}
	foundHome := false
	for _, entry := range env {
		if entry == "HOME="+home {
			foundHome = true
			break
		}
	}
	if !foundHome {
		t.Error("gitEnv() must include HOME from os.Environ()")
	}

	// Must set safe.directory=* via GIT_CONFIG env vars.
	envHas := func(env []string, want string) bool {
		for _, e := range env {
			if e == want {
				return true
			}
		}
		return false
	}
	if !envHas(env, "GIT_CONFIG_KEY_0=safe.directory") {
		t.Error("gitEnv() must include GIT_CONFIG_KEY_0=safe.directory (no pre-existing config)")
	}
	if !envHas(env, "GIT_CONFIG_VALUE_0=*") {
		t.Error("gitEnv() must include GIT_CONFIG_VALUE_0=*")
	}
}

func TestRemoteGitCommandHonorsCancellation(t *testing.T) {
	binDir := t.TempDir()
	gitPath := filepath.Join(binDir, "git")
	if err := os.WriteFile(gitPath, []byte("#!/bin/sh\nexec /bin/sleep 10\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := runRemoteGitContext(ctx, "fetch", "origin")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error=%v; want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("cancelled git command returned after %s", elapsed)
	}
}

func TestSyncFailsWhenRemoteDefaultBranchRefreshFails(t *testing.T) {
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	gitPath := filepath.Join(binDir, "git")
	wrapper := fmt.Sprintf(`#!/bin/sh
case "$*" in
  *"remote set-head origin --auto"*) echo "set-head blocked" >&2; exit 42 ;;
esac
exec %q "$@"
`, realGit)
	if err := os.WriteFile(gitPath, []byte(wrapper), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	sourceRepo := createTestRepo(t)
	cache := New(t.TempDir(), testLogger())
	err = cache.Sync("ws-1", []RepoInfo{{URL: sourceRepo}})
	if err == nil || !strings.Contains(err.Error(), "refresh remote default branch") {
		t.Fatalf("Sync error=%v; want explicit default-branch refresh failure", err)
	}
	if cached := cache.Lookup("ws-1", sourceRepo); cached != "" {
		t.Fatalf("failed clone remained visible in cache: %s", cached)
	}
}

func TestGitEnvPreservesExistingConfig(t *testing.T) {
	// GIT_CONFIG_COUNT env vars are process-wide; cannot use t.Setenv in
	// parallel tests, so run sequentially.
	t.Setenv("GIT_CONFIG_COUNT", "2")
	t.Setenv("GIT_CONFIG_KEY_0", "url.https://github.com/.insteadOf")
	t.Setenv("GIT_CONFIG_VALUE_0", "gh:")
	t.Setenv("GIT_CONFIG_KEY_1", "http.extraHeader")
	t.Setenv("GIT_CONFIG_VALUE_1", "Authorization: Bearer tok")

	env := gitEnv()

	envHas := func(want string) bool {
		for _, e := range env {
			if e == want {
				return true
			}
		}
		return false
	}

	// safe.directory must be appended at index 2 (next available).
	if !envHas("GIT_CONFIG_COUNT=3") {
		t.Error("expected GIT_CONFIG_COUNT=3")
	}
	if !envHas("GIT_CONFIG_KEY_2=safe.directory") {
		t.Error("expected GIT_CONFIG_KEY_2=safe.directory")
	}
	if !envHas("GIT_CONFIG_VALUE_2=*") {
		t.Error("expected GIT_CONFIG_VALUE_2=*")
	}

	// Original entries must still be present.
	if !envHas("GIT_CONFIG_KEY_0=url.https://github.com/.insteadOf") {
		t.Error("existing GIT_CONFIG_KEY_0 was lost")
	}
	if !envHas("GIT_CONFIG_VALUE_0=gh:") {
		t.Error("existing GIT_CONFIG_VALUE_0 was lost")
	}
	if !envHas("GIT_CONFIG_KEY_1=http.extraHeader") {
		t.Error("existing GIT_CONFIG_KEY_1 was lost")
	}
}

func TestBareDirName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input, want string
	}{
		{"https://github.com/org/my-repo.git", "github.com+org+my-repo.git"},
		{"https://github.com/org/my-repo", "github.com+org+my-repo.git"},
		{"git@github.com:org/my-repo.git", "github.com+org+my-repo.git"},
		{"git@github.com:org/my-repo", "github.com+org+my-repo.git"},
		{"https://github.com/org/repo/", "github.com+org+repo.git"},
		{"ssh://git@gitlab.example.com:22/group/sub/repo.git", "gitlab.example.com%3A22+group+sub+repo.git"},
		// Basename collision: two repos sharing the basename must produce
		// distinct dirs (the original bug).
		{"ssh://git@gitlab.example.com:22/relisty/app.git", "gitlab.example.com%3A22+relisty+app.git"},
		{"ssh://git@gitlab.example.com:22/listbridge/app.git", "gitlab.example.com%3A22+listbridge+app.git"},
		{"my-repo", "my-repo.git"},
		{"", "repo.git"},
	}
	for _, tt := range tests {
		if got := bareDirName(tt.input); got != tt.want {
			t.Errorf("bareDirName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// TestBareDirNameDistinctsSegmentBoundaryColliders covers the collision class
// that a naive path-flattening-with-dashes scheme would miss: two repos whose
// path segments differ only at a segment boundary flatten to the same string
// once slashes become dashes. The '+' separator can't appear inside a
// GitHub/GitLab path segment, so the boundary stays visible in the output.
func TestBareDirNameDistinctsSegmentBoundaryColliders(t *testing.T) {
	t.Parallel()
	pairs := [][2]string{
		{"git@github.com:foo/bar-baz.git", "git@github.com:foo-bar/baz.git"},
		{"https://github.com/foo/bar-baz.git", "https://github.com/foo-bar/baz.git"},
	}
	for _, p := range pairs {
		a, b := bareDirName(p[0]), bareDirName(p[1])
		if a == b {
			t.Errorf("bareDirName collision: %q and %q both → %q", p[0], p[1], a)
		}
	}
}

// TestBareDirNameDistinctsSameRepoNameAcrossHosts covers the cross-host
// collision class: the same path-with-namespace on different hosts must
// produce distinct cache dirs so an agent configured for host A can't be
// served the clone from host B.
func TestBareDirNameDistinctsSameRepoNameAcrossHosts(t *testing.T) {
	t.Parallel()
	pairs := [][2]string{
		{"git@github.com:org/repo.git", "git@gitlab.example.com:org/repo.git"},
		{"https://github.com/org/repo.git", "https://gitlab.example.com/org/repo.git"},
		{"ssh://git@github.com/org/repo.git", "ssh://git@gitlab.example.com/org/repo.git"},
	}
	for _, p := range pairs {
		a, b := bareDirName(p[0]), bareDirName(p[1])
		if a == b {
			t.Errorf("bareDirName collision across hosts: %q and %q both → %q", p[0], p[1], a)
		}
	}
}

// TestBareDirNameDistinctsHostPortFromDashedHostname covers the lossy-port
// encoding regression: a naive ':' -> '-' rewrite would collapse
// `host:port` onto a hostname that literally contains the same dash pattern,
// silently reintroducing the wrong-remote bug. We URL-encode ':' to '%3A'
// so host+port is lossless — and '%' is forbidden in valid hostnames so the
// marker can never come from a legal literal hostname.
func TestBareDirNameDistinctsHostPortFromDashedHostname(t *testing.T) {
	t.Parallel()
	pairs := [][2]string{
		// Host-with-port vs a literal hostname that looks like `host-port`.
		{"ssh://git@gitlab.example.com:22/org/repo.git", "git@gitlab.example.com-22:org/repo.git"},
		// Same again but across the URL and scp-style forms, explicit ports
		// swapped to ensure we don't rely on order.
		{"ssh://git@host.example.com:443/a/b.git", "git@host.example.com-443:a/b.git"},
	}
	for _, p := range pairs {
		a, b := bareDirName(p[0]), bareDirName(p[1])
		if a == b {
			t.Errorf("bareDirName collision between host:port and host-port: %q and %q both → %q", p[0], p[1], a)
		}
	}
}

func TestIsBareRepo(t *testing.T) {
	t.Parallel()

	// A directory with a HEAD file should be detected as bare.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !isBareRepo(dir) {
		t.Error("expected bare repo to be detected")
	}

	// An empty directory should not.
	emptyDir := t.TempDir()
	if isBareRepo(emptyDir) {
		t.Error("expected empty dir to not be detected as bare repo")
	}
}

// createTestRepo creates a local git repo with an initial commit and returns its path.
func createTestRepo(t *testing.T) string {
	t.Helper()
	return createTestRepoAt(t, t.TempDir())
}

func createSyncedCache(t *testing.T) (*Cache, string) {
	t.Helper()
	sourceRepo := createTestRepo(t)
	cache := New(t.TempDir(), testLogger())
	if err := cache.Sync("ws-1", []RepoInfo{{URL: sourceRepo}}); err != nil {
		t.Fatalf("sync failed: %v", err)
	}
	return cache, sourceRepo
}

func issueWorktreeParams(t *testing.T, sourceRepo string) WorktreeParams {
	t.Helper()
	return WorktreeParams{
		WorkspaceID:        "ws-1",
		RepoURL:            sourceRepo,
		WorkDir:            t.TempDir(),
		AgentName:          "PM",
		TaskID:             "task-1",
		BranchNameOverride: "agent/issue/issue123",
		PreserveExisting:   true,
	}
}

// createTestRepoAt initializes a git repo at the given directory (which
// must already exist). Used to craft repo URLs at paths chosen by the test
// — e.g. to reproduce collision classes in name derivation.
func createTestRepoAt(t *testing.T, dir string) string {
	t.Helper()
	for _, args := range [][]string{
		{"init", dir},
		{"-C", dir, "commit", "--allow-empty", "-m", "initial"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git setup failed: %s: %v", out, err)
		}
	}
	return dir
}

func TestSyncAndLookup(t *testing.T) {
	t.Parallel()
	sourceRepo := createTestRepo(t)
	cacheRoot := t.TempDir()

	cache := New(cacheRoot, testLogger())

	// Sync should clone the repo.
	err := cache.Sync("ws-123", []RepoInfo{
		{URL: sourceRepo},
	})
	if err != nil {
		t.Fatalf("Sync failed: %v", err)
	}

	// Lookup should find the cached repo.
	path := cache.Lookup("ws-123", sourceRepo)
	if path == "" {
		t.Fatal("expected to find cached repo")
	}
	if !isBareRepo(path) {
		t.Fatalf("expected bare repo at %s", path)
	}

	// Lookup for unknown URL should return empty.
	if got := cache.Lookup("ws-123", "https://github.com/org/unknown"); got != "" {
		t.Fatalf("expected empty for unknown URL, got %q", got)
	}

	// Lookup for unknown workspace should return empty.
	if got := cache.Lookup("ws-999", sourceRepo); got != "" {
		t.Fatalf("expected empty for unknown workspace, got %q", got)
	}
}

// TestSyncKeepsDistinctCachesForSegmentBoundaryColliders proves that two
// URLs differing only at a path-segment boundary don't share a bare cache
// and don't silently reuse each other's origin. Both conditions would have
// failed under a plain slashes-to-dashes flattening scheme: the two URLs
// in this test produce the same dash-joined key even though they point at
// different source repositories.
func TestSyncKeepsDistinctCachesForSegmentBoundaryColliders(t *testing.T) {
	t.Parallel()

	// Build two real source repos under a shared parent. Their filesystem
	// paths are used directly as URLs (git accepts local paths as remote
	// URLs). The path pair ".../foo/bar-baz" and ".../foo-bar/baz" would
	// flatten to the same string under slashes-to-dashes — that's the
	// class of collision we want to rule out.
	parent := t.TempDir()
	srcA := filepath.Join(parent, "foo", "bar-baz")
	srcB := filepath.Join(parent, "foo-bar", "baz")
	if err := os.MkdirAll(srcA, 0o755); err != nil {
		t.Fatalf("mkdir srcA: %v", err)
	}
	if err := os.MkdirAll(srcB, 0o755); err != nil {
		t.Fatalf("mkdir srcB: %v", err)
	}
	createTestRepoAt(t, srcA)
	createTestRepoAt(t, srcB)
	// Distinct content so a silent-reuse bug would produce the wrong file
	// in the wrong cache.
	if err := os.WriteFile(filepath.Join(srcA, "A.txt"), []byte("A\n"), 0o644); err != nil {
		t.Fatalf("write A: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcB, "B.txt"), []byte("B\n"), 0o644); err != nil {
		t.Fatalf("write B: %v", err)
	}
	runGitAuthored(t, srcA, "add", ".")
	runGitAuthored(t, srcA, "commit", "-m", "A-content")
	runGitAuthored(t, srcB, "add", ".")
	runGitAuthored(t, srcB, "commit", "-m", "B-content")

	cache := New(t.TempDir(), testLogger())
	if err := cache.Sync("ws-1", []RepoInfo{{URL: srcA}, {URL: srcB}}); err != nil {
		t.Fatalf("Sync failed: %v", err)
	}

	pathA := cache.Lookup("ws-1", srcA)
	pathB := cache.Lookup("ws-1", srcB)
	if pathA == "" || pathB == "" {
		t.Fatalf("missing cache entry: A=%q B=%q", pathA, pathB)
	}
	if pathA == pathB {
		t.Fatalf("collider URLs share a bare cache path: %s", pathA)
	}

	// Each bare cache must carry the origin URL of the repo it was
	// cloned from — not the other one's. A silent-reuse bug would have
	// both caches pointing at whichever URL won the race in Sync.
	if got := gitConfigGet(t, pathA, "remote.origin.url"); got != srcA {
		t.Errorf("cacheA origin.url = %q, want %q", got, srcA)
	}
	if got := gitConfigGet(t, pathB, "remote.origin.url"); got != srcB {
		t.Errorf("cacheB origin.url = %q, want %q", got, srcB)
	}

	// And each cache's content must reflect the right source.
	if !cachedRepoHasFile(t, pathA, "A.txt") {
		t.Errorf("cacheA (%s) should contain A.txt from srcA", pathA)
	}
	if !cachedRepoHasFile(t, pathB, "B.txt") {
		t.Errorf("cacheB (%s) should contain B.txt from srcB", pathB)
	}
}

// gitConfigGet reads a git config value from repoPath. Fails the test if
// the key is missing or the command errors.
func gitConfigGet(t *testing.T, repoPath, key string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", repoPath, "config", "--get", key).Output()
	if err != nil {
		t.Fatalf("git config --get %s in %s: %v", key, repoPath, err)
	}
	return strings.TrimSpace(string(out))
}

// cachedRepoHasFile returns true if the bare cache at barePath exposes a
// file named filename anywhere in its remote-tracking default branch.
// Walks refs/remotes/origin/* since a bare clone stores fetched heads
// there under the modern refspec.
func cachedRepoHasFile(t *testing.T, barePath, filename string) bool {
	t.Helper()
	ref := getRemoteDefaultBranch(barePath)
	if ref == "" {
		return false
	}
	out, err := exec.Command("git", "-C", barePath, "ls-tree", "-r", "--name-only", ref).Output()
	if err != nil {
		t.Fatalf("git ls-tree %s in %s: %v", ref, barePath, err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.TrimSpace(line) == filename {
			return true
		}
	}
	return false
}

func TestSyncFetchesExisting(t *testing.T) {
	t.Parallel()
	sourceRepo := createTestRepo(t)
	cacheRoot := t.TempDir()

	cache := New(cacheRoot, testLogger())

	// First sync: clone.
	if err := cache.Sync("ws-1", []RepoInfo{{URL: sourceRepo}}); err != nil {
		t.Fatalf("first sync failed: %v", err)
	}

	// Record the remote-tracking default head in the cache. Under the modern
	// refspec layout, fetches write to refs/remotes/origin/*, not the bare
	// repo's own refs/heads/*, so reading the bare HEAD would return the
	// fossil snapshot from initial clone.
	barePath := cache.Lookup("ws-1", sourceRepo)
	oldHead := gitRefCommit(t, barePath, getRemoteDefaultBranch(barePath))

	// Add a commit to source.
	addEmptyCommit(t, sourceRepo, "second")
	sourceHead := gitHead(t, sourceRepo)
	if sourceHead == oldHead {
		t.Fatal("source HEAD should differ after new commit")
	}

	// Second sync: should fetch (not re-clone).
	if err := cache.Sync("ws-1", []RepoInfo{{URL: sourceRepo}}); err != nil {
		t.Fatalf("second sync failed: %v", err)
	}

	// Verify the cache remote-tracking ref was updated.
	newHead := gitRefCommit(t, barePath, getRemoteDefaultBranch(barePath))
	if newHead == oldHead {
		t.Fatal("expected cache remote-tracking head to be updated after fetch")
	}
	if newHead != sourceHead {
		t.Fatalf("expected cache head %s to match source head %s", newHead, sourceHead)
	}
}

func gitHead(t *testing.T, repoPath string) string {
	t.Helper()
	cmd := exec.Command("git", "-C", repoPath, "rev-parse", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse HEAD failed in %s: %v", repoPath, err)
	}
	return strings.TrimSpace(string(out))
}

func TestWorktreeFromCache(t *testing.T) {
	t.Parallel()
	cache, sourceRepo := createSyncedCache(t)

	barePath := cache.Lookup("ws-1", sourceRepo)
	if barePath == "" {
		t.Fatal("expected cached repo")
	}

	// Create a worktree from the bare cache — this is the actual use case.
	worktreeDir := filepath.Join(t.TempDir(), "work")
	cmd := exec.Command("git", "-C", barePath, "worktree", "add", "-b", "test-branch", worktreeDir, "HEAD")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("worktree add failed: %s: %v", out, err)
	}
	defer func() { _ = exec.Command("git", "-C", barePath, "worktree", "remove", "--force", worktreeDir).Run() }()

	// Verify worktree exists and is on the right branch.
	cmd = exec.Command("git", "-C", worktreeDir, "branch", "--show-current")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("show branch failed: %v", err)
	}
	if got := trimLine(string(out)); got != "test-branch" {
		t.Fatalf("expected branch 'test-branch', got %q", got)
	}
}

func TestCreateWorktree(t *testing.T) {
	t.Parallel()
	cache, sourceRepo := createSyncedCache(t)

	workDir := t.TempDir()
	result, err := cache.CreateWorktree(WorktreeParams{
		WorkspaceID: "ws-1",
		RepoURL:     sourceRepo,
		WorkDir:     workDir,
		AgentName:   "Code Reviewer",
		TaskID:      "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
	})
	if err != nil {
		t.Fatalf("CreateWorktree failed: %v", err)
	}

	// Verify the worktree was created.
	if _, err := os.Stat(result.Path); os.IsNotExist(err) {
		t.Fatalf("worktree path does not exist: %s", result.Path)
	}

	// Verify branch name format.
	if !strings.HasPrefix(result.BranchName, "agent/code-reviewer/") {
		t.Errorf("expected branch to start with 'agent/code-reviewer/', got %q", result.BranchName)
	}

	// Verify the worktree is on the correct branch.
	cmd := exec.Command("git", "-C", result.Path, "branch", "--show-current")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("show branch failed: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != result.BranchName {
		t.Errorf("expected branch %q, got %q", result.BranchName, got)
	}
}

func TestCreateWorktreeFailsWhenRemoteCannotBeRefreshed(t *testing.T) {
	cache, sourceRepo := createSyncedCache(t)
	offlinePath := sourceRepo + "-offline"
	if err := os.Rename(sourceRepo, offlinePath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Rename(offlinePath, sourceRepo) })
	workDir := t.TempDir()
	result, err := cache.CreateWorktree(WorktreeParams{
		WorkspaceID: "ws-1",
		RepoURL:     sourceRepo,
		WorkDir:     workDir,
		AgentName:   "Reviewer",
		TaskID:      "task-fetch-failure",
	})
	if err == nil || result != nil || !strings.Contains(err.Error(), "fetch repo before checkout") {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	entries, readErr := os.ReadDir(workDir)
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("failed checkout left worktree entries=%v err=%v", entries, readErr)
	}
}

func TestManagedWorktreePathRejectsOutsideAndSymlinkParents(t *testing.T) {
	managedRoot := t.TempDir()
	cacheRoot := filepath.Join(managedRoot, ".repos")
	if err := os.MkdirAll(cacheRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	cache := New(cacheRoot, testLogger())
	workDir := filepath.Join(managedRoot, "workspace", "task")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path, err := cache.managedWorktreePath(workDir, "https://github.com/acme/repo.git")
	if err != nil || path != filepath.Join(workDir, "repo") {
		t.Fatalf("path=%q err=%v", path, err)
	}
	if _, err := cache.managedWorktreePath(t.TempDir(), "https://github.com/acme/repo.git"); err == nil {
		t.Fatal("outside worktree parent was accepted")
	}
	if _, err := cache.managedWorktreePath(filepath.Join(cacheRoot, "work"), "https://github.com/acme/repo.git"); err == nil {
		t.Fatal("repo-cache child was accepted as worktree parent")
	}
	outside := t.TempDir()
	symlinkParent := filepath.Join(managedRoot, "symlink-parent")
	if err := os.Symlink(outside, symlinkParent); err == nil {
		if _, err := cache.managedWorktreePath(symlinkParent, "https://github.com/acme/repo.git"); err == nil {
			t.Fatal("symlinked outside parent was accepted")
		}
	}
	if _, err := cache.managedWorktreePath(workDir, "https://github.com/acme/.."); err == nil {
		t.Fatal("unsafe derived repository directory was accepted")
	}
}

func TestWorkspaceCacheDirRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	cache := New(root, testLogger())
	path, err := cache.workspaceCacheDir("workspace-1")
	if err != nil || path != filepath.Join(root, "workspace-1") {
		t.Fatalf("path=%q err=%v", path, err)
	}
	for _, invalid := range []string{"", ".", "..", "../outside", "nested/workspace"} {
		if _, err := cache.workspaceCacheDir(invalid); err == nil {
			t.Fatalf("invalid workspace segment %q was accepted", invalid)
		}
		if got := cache.Lookup(invalid, "https://github.com/acme/repo.git"); got != "" {
			t.Fatalf("Lookup(%q) escaped cache: %q", invalid, got)
		}
	}
}

func TestCreateWorktreePreserveExistingKeepsIssueWorktreeChanges(t *testing.T) {
	t.Parallel()
	cache, sourceRepo := createSyncedCache(t)

	params := issueWorktreeParams(t, sourceRepo)
	result, err := cache.CreateWorktree(params)
	if err != nil {
		t.Fatalf("CreateWorktree first call failed: %v", err)
	}
	if result.BranchName != "agent/issue/issue123" {
		t.Fatalf("branch name = %q, want stable issue branch", result.BranchName)
	}
	changedPath := filepath.Join(result.Path, "stage-change.txt")
	if err := os.WriteFile(changedPath, []byte("kept\n"), 0o644); err != nil {
		t.Fatalf("write change: %v", err)
	}

	second, err := cache.CreateWorktree(params)
	if err != nil {
		t.Fatalf("CreateWorktree preserve call failed: %v", err)
	}
	if second.Path != result.Path {
		t.Fatalf("preserved worktree path = %q, want %q", second.Path, result.Path)
	}
	if data, err := os.ReadFile(changedPath); err != nil || string(data) != "kept\n" {
		t.Fatalf("preserved change = %q, %v; want kept", string(data), err)
	}
}

func TestCreateWorktreeReportsExistingWorktreeExcludeFailure(t *testing.T) {
	cache, sourceRepo := createSyncedCache(t)

	params := WorktreeParams{
		WorkspaceID:      "ws-1",
		RepoURL:          sourceRepo,
		WorkDir:          t.TempDir(),
		AgentName:        "PM",
		TaskID:           "exclude-failure",
		PreserveExisting: true,
	}
	result, err := cache.CreateWorktree(params)
	if err != nil {
		t.Fatalf("initial CreateWorktree failed: %v", err)
	}

	gitDir := resolveGitDir(t, result.Path)
	infoPath := filepath.Join(gitDir, "info")
	if err := os.RemoveAll(infoPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(infoPath, []byte("blocks exclude directory"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := cache.CreateWorktree(params)
	if err == nil || got != nil || !strings.Contains(err.Error(), "configure git exclude") {
		t.Fatalf("result=%#v err=%v; want explicit exclude configuration failure", got, err)
	}
}

func TestCreateWorktreeRollsBackWhenHookConfigurationFails(t *testing.T) {
	cache, sourceRepo := createSyncedCache(t)
	barePath := cache.Lookup("ws-1", sourceRepo)
	hooksPath := filepath.Join(barePath, "hooks")
	if err := os.RemoveAll(hooksPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hooksPath, []byte("blocks hooks directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	branchesBefore := gitAgentBranches(t, barePath)
	workDir := t.TempDir()

	result, err := cache.CreateWorktree(WorktreeParams{
		WorkspaceID:         "ws-1",
		RepoURL:             sourceRepo,
		WorkDir:             workDir,
		AgentName:           "Test Agent",
		TaskID:              "hook-failure",
		CoAuthoredByEnabled: true,
	})
	if err == nil || result != nil || !strings.Contains(err.Error(), "configure co-authored-by hook") {
		t.Fatalf("result=%#v err=%v; want explicit hook configuration failure", result, err)
	}
	entries, readErr := os.ReadDir(workDir)
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("failed checkout left worktree entries=%v err=%v", entries, readErr)
	}
	if branchesAfter := gitAgentBranches(t, barePath); branchesAfter != branchesBefore {
		t.Fatalf("failed checkout leaked branch: before=%q after=%q", branchesBefore, branchesAfter)
	}
}

func resolveGitDir(t *testing.T, worktreePath string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", worktreePath, "rev-parse", "--git-dir").Output()
	if err != nil {
		t.Fatalf("resolve git dir: %v", err)
	}
	gitDir := strings.TrimSpace(string(out))
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(worktreePath, gitDir)
	}
	return filepath.Clean(gitDir)
}

func gitAgentBranches(t *testing.T, barePath string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", barePath, "for-each-ref", "--format=%(refname)", "refs/heads/agent/").Output()
	if err != nil {
		t.Fatalf("list agent branches: %v", err)
	}
	return string(out)
}

func TestCreateWorktreePreserveExistingRestoresIssueBranch(t *testing.T) {
	t.Parallel()
	cache, sourceRepo := createSyncedCache(t)

	params := issueWorktreeParams(t, sourceRepo)
	result, err := cache.CreateWorktree(params)
	if err != nil {
		t.Fatalf("CreateWorktree first call failed: %v", err)
	}
	if out, err := exec.Command("git", "-C", result.Path, "checkout", "-b", "v5.0.0_dev_sop").CombinedOutput(); err != nil {
		t.Fatalf("checkout target branch: %s: %v", string(out), err)
	}
	if got := currentBranch(result.Path); got != "v5.0.0_dev_sop" {
		t.Fatalf("setup branch = %q, want target branch", got)
	}

	second, err := cache.CreateWorktree(params)
	if err != nil {
		t.Fatalf("CreateWorktree preserve call failed: %v", err)
	}
	if second.Path != result.Path {
		t.Fatalf("preserved worktree path = %q, want %q", second.Path, result.Path)
	}
	if second.BranchName != "agent/issue/issue123" {
		t.Fatalf("branch name = %q, want restored issue branch", second.BranchName)
	}
	if got := currentBranch(result.Path); got != "agent/issue/issue123" {
		t.Fatalf("current branch = %q, want restored issue branch", got)
	}
}

func TestCreateWorktreeEmptyTaskIDUsesValidManualBranch(t *testing.T) {
	t.Parallel()
	cache, sourceRepo := createSyncedCache(t)

	result, err := cache.CreateWorktree(WorktreeParams{
		WorkspaceID: "ws-1",
		RepoURL:     sourceRepo,
		WorkDir:     t.TempDir(),
		AgentName:   "agent",
		TaskID:      "",
	})
	if err != nil {
		t.Fatalf("CreateWorktree failed: %v", err)
	}
	if result.BranchName != "agent/agent/manual" {
		t.Fatalf("branch name = %q, want agent/agent/manual", result.BranchName)
	}
}

func TestCreateWorktreeExcludesOpenCodeSkills(t *testing.T) {
	t.Parallel()
	cache, sourceRepo := createSyncedCache(t)

	workDir := t.TempDir()
	result, err := cache.CreateWorktree(WorktreeParams{
		WorkspaceID: "ws-1",
		RepoURL:     sourceRepo,
		WorkDir:     workDir,
		AgentName:   "OpenCode",
		TaskID:      "opencode-exclude-test",
	})
	if err != nil {
		t.Fatalf("CreateWorktree failed: %v", err)
	}

	exclude := gitInfoExclude(t, result.Path)
	if !strings.Contains(exclude, ".opencode\n") {
		t.Fatalf("expected .git/info/exclude to contain .opencode, got:\n%s", exclude)
	}
	if strings.Contains(exclude, ".config/opencode") {
		t.Fatalf("expected .git/info/exclude to not contain stale .config/opencode, got:\n%s", exclude)
	}
}

func gitInfoExclude(t *testing.T, worktreePath string) string {
	t.Helper()
	cmd := exec.Command("git", "-C", worktreePath, "rev-parse", "--git-dir")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse --git-dir failed in %s: %v", worktreePath, err)
	}
	gitDir := strings.TrimSpace(string(out))
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(worktreePath, gitDir)
	}
	data, err := os.ReadFile(filepath.Join(gitDir, "info", "exclude"))
	if err != nil {
		t.Fatalf("read .git/info/exclude failed: %v", err)
	}
	return string(data)
}

func TestHasGitExcludePatternMatchesWholeLines(t *testing.T) {
	contents := []byte(".agent_context_old\n# CLAUDE.md backup\nartifacts\n")
	if hasGitExcludePattern(contents, ".agent_context") {
		t.Fatal("substring was treated as an existing exclude pattern")
	}
	if hasGitExcludePattern(contents, "CLAUDE.md") {
		t.Fatal("comment was treated as an existing exclude pattern")
	}
	if !hasGitExcludePattern(contents, "artifacts") {
		t.Fatal("exact exclude line was not recognized")
	}
}

func TestCreateWorktreeNotCached(t *testing.T) {
	t.Parallel()
	cacheRoot := t.TempDir()
	cache := New(cacheRoot, testLogger())

	_, err := cache.CreateWorktree(WorktreeParams{
		WorkspaceID: "ws-1",
		RepoURL:     "https://github.com/org/nonexistent",
		WorkDir:     t.TempDir(),
		AgentName:   "Agent",
		TaskID:      "test-task-id",
	})
	if err == nil {
		t.Fatal("expected error for uncached repo")
	}
	if !strings.Contains(err.Error(), "not found in cache") {
		t.Errorf("expected 'not found in cache' error, got: %v", err)
	}
}

func TestCreateWorktreeWithRequestedBranchRef(t *testing.T) {
	t.Parallel()
	sourceRepo := createTestRepo(t)
	defaultHead := gitHead(t, sourceRepo)

	runGitAuthored(t, sourceRepo, "checkout", "-b", "review-branch")
	if err := os.WriteFile(filepath.Join(sourceRepo, "review.txt"), []byte("review\n"), 0o644); err != nil {
		t.Fatalf("write review file: %v", err)
	}
	runGitAuthored(t, sourceRepo, "add", ".")
	runGitAuthored(t, sourceRepo, "commit", "-m", "review branch commit")
	reviewHead := gitHead(t, sourceRepo)
	if reviewHead == defaultHead {
		t.Fatal("test setup failed: review branch did not advance")
	}

	cache := New(t.TempDir(), testLogger())
	if err := cache.Sync("ws-1", []RepoInfo{{URL: sourceRepo}}); err != nil {
		t.Fatalf("sync failed: %v", err)
	}

	result, err := cache.CreateWorktree(WorktreeParams{
		WorkspaceID: "ws-1",
		RepoURL:     sourceRepo,
		WorkDir:     t.TempDir(),
		Ref:         "review-branch",
		AgentName:   "Reviewer",
		TaskID:      "review-task-id",
	})
	if err != nil {
		t.Fatalf("CreateWorktree failed: %v", err)
	}

	if got := gitHead(t, result.Path); got != reviewHead {
		t.Fatalf("worktree HEAD = %s, want requested branch head %s", got, reviewHead)
	}
	if _, err := os.Stat(filepath.Join(result.Path, "review.txt")); err != nil {
		t.Fatalf("requested branch file missing: %v", err)
	}
}

func TestCreateWorktreeWithRequestedCommitRef(t *testing.T) {
	t.Parallel()
	sourceRepo := createTestRepo(t)
	firstCommit := gitHead(t, sourceRepo)
	addEmptyCommit(t, sourceRepo, "second commit")

	cache := New(t.TempDir(), testLogger())
	if err := cache.Sync("ws-1", []RepoInfo{{URL: sourceRepo}}); err != nil {
		t.Fatalf("sync failed: %v", err)
	}

	result, err := cache.CreateWorktree(WorktreeParams{
		WorkspaceID: "ws-1",
		RepoURL:     sourceRepo,
		WorkDir:     t.TempDir(),
		Ref:         firstCommit,
		AgentName:   "Reviewer",
		TaskID:      "commit-task-id",
	})
	if err != nil {
		t.Fatalf("CreateWorktree failed: %v", err)
	}

	if got := gitHead(t, result.Path); got != firstCommit {
		t.Fatalf("worktree HEAD = %s, want requested commit %s", got, firstCommit)
	}
}

func TestCreateWorktreeWithRequestedTagRef(t *testing.T) {
	t.Parallel()
	sourceRepo := createTestRepo(t)
	taggedCommit := gitHead(t, sourceRepo)
	runGitAuthored(t, sourceRepo, "tag", "v1")
	// Advance the default branch past the tag so worktree HEAD == taggedCommit
	// can only be true if the tag was actually resolved (vs falling back to
	// the default branch tip).
	addEmptyCommit(t, sourceRepo, "post-tag commit")

	cache := New(t.TempDir(), testLogger())
	if err := cache.Sync("ws-1", []RepoInfo{{URL: sourceRepo}}); err != nil {
		t.Fatalf("sync failed: %v", err)
	}

	result, err := cache.CreateWorktree(WorktreeParams{
		WorkspaceID: "ws-1",
		RepoURL:     sourceRepo,
		WorkDir:     t.TempDir(),
		Ref:         "v1",
		AgentName:   "Reviewer",
		TaskID:      "tag-task-id",
	})
	if err != nil {
		t.Fatalf("CreateWorktree failed: %v", err)
	}

	if got := gitHead(t, result.Path); got != taggedCommit {
		t.Fatalf("worktree HEAD = %s, want tagged commit %s", got, taggedCommit)
	}
}

func TestCreateWorktreeWithUnknownRequestedRef(t *testing.T) {
	t.Parallel()
	cache, sourceRepo := createSyncedCache(t)

	_, err := cache.CreateWorktree(WorktreeParams{
		WorkspaceID: "ws-1",
		RepoURL:     sourceRepo,
		WorkDir:     t.TempDir(),
		Ref:         "missing-ref",
		AgentName:   "Reviewer",
		TaskID:      "missing-ref-task-id",
	})
	if err == nil {
		t.Fatal("expected unknown ref error")
	}
	if !strings.Contains(err.Error(), "cannot resolve requested ref") {
		t.Fatalf("expected requested ref error, got: %v", err)
	}
}

func trimLine(s string) string {
	return strings.TrimSpace(s)
}

// gitRefCommit resolves a git ref to its commit SHA in repoPath.
func gitRefCommit(t *testing.T, repoPath, ref string) string {
	t.Helper()
	if ref == "" {
		t.Fatalf("empty ref in %s", repoPath)
	}
	cmd := exec.Command("git", "-C", repoPath, "rev-parse", ref)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse %s failed in %s: %v", ref, repoPath, err)
	}
	return strings.TrimSpace(string(out))
}

// addEmptyCommit adds an empty commit on the current branch of repoPath.
func addEmptyCommit(t *testing.T, repoPath, message string) {
	t.Helper()
	cmd := exec.Command("git", "-C", repoPath, "commit", "--allow-empty", "-m", message)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit failed in %s: %s: %v", repoPath, out, err)
	}
}

// runGitAuthored runs `git -C repoPath <args...>` with the test author env set.
func runGitAuthored(t *testing.T, repoPath string, args ...string) {
	t.Helper()
	full := append([]string{"-C", repoPath}, args...)
	cmd := exec.Command("git", full...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %s: %v", args, repoPath, out, err)
	}
}

// Agent branches and fetched remote branches occupy separate namespaces, so a
// pushed Agent branch cannot block a later default-branch refresh.
func TestCreateWorktreeFetchesDespiteAgentBranchOnRemote(t *testing.T) {
	t.Parallel()
	sourceRepo := createTestRepo(t)
	// Capture the default branch BEFORE any detach/commit/checkout dance — we
	// need its name later to add new commits to the correct branch.
	defaultBranch := currentBranchName(t, sourceRepo)

	// Put source repo on a detached HEAD so the first worktree's agent branch
	// can be pushed back to it as a regular update (non-bare repos refuse to
	// push to the currently checked-out branch).
	runGitAuthored(t, sourceRepo, "checkout", "--detach", "HEAD")

	cacheRoot := t.TempDir()
	cache := New(cacheRoot, testLogger())
	if err := cache.Sync("ws-1", []RepoInfo{{URL: sourceRepo}}); err != nil {
		t.Fatalf("sync failed: %v", err)
	}

	// First worktree creates refs/heads/agent/... inside the bare cache.
	workDir1 := t.TempDir()
	result1, err := cache.CreateWorktree(WorktreeParams{
		WorkspaceID: "ws-1",
		RepoURL:     sourceRepo,
		WorkDir:     workDir1,
		AgentName:   "agent",
		TaskID:      "t1111111-0000-0000-0000-000000000000",
	})
	if err != nil {
		t.Fatalf("first CreateWorktree failed: %v", err)
	}

	// Simulate the agent pushing its branch back to origin (i.e. opening a PR).
	// Now sourceRepo has refs/heads/agent/... matching the locked ref in the
	// bare cache, exercising the namespace separation invariant.
	if err := os.WriteFile(filepath.Join(result1.Path, "hello.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	runGitAuthored(t, result1.Path, "add", ".")
	runGitAuthored(t, result1.Path, "commit", "-m", "first task")
	runGitAuthored(t, result1.Path, "push", "origin", result1.BranchName)

	// Add a new commit to source's default branch (not the agent branch we
	// just pushed). Then re-detach so future pushes to other branches still work.
	runGitAuthored(t, sourceRepo, "checkout", defaultBranch)
	addEmptyCommit(t, sourceRepo, "new commit on default branch")
	sourceHead := gitRefCommit(t, sourceRepo, "refs/heads/"+defaultBranch)
	runGitAuthored(t, sourceRepo, "checkout", "--detach", "HEAD")

	// Second worktree: CreateWorktree fetches first and must see sourceHead.
	workDir2 := t.TempDir()
	result2, err := cache.CreateWorktree(WorktreeParams{
		WorkspaceID: "ws-1",
		RepoURL:     sourceRepo,
		WorkDir:     workDir2,
		AgentName:   "agent",
		TaskID:      "t2222222-0000-0000-0000-000000000000",
	})
	if err != nil {
		t.Fatalf("second CreateWorktree failed: %v", err)
	}

	if got := gitHead(t, result2.Path); got != sourceHead {
		t.Fatalf("second worktree HEAD = %s, want %s (remote default head after new commit)", got, sourceHead)
	}
}

// currentBranchName returns the branch name that HEAD points at in repoPath.
// Fails the test if HEAD is detached.
func currentBranchName(t *testing.T, repoPath string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", repoPath, "symbolic-ref", "--short", "HEAD").Output()
	if err != nil {
		t.Fatalf("symbolic-ref --short HEAD in %s: %v", repoPath, err)
	}
	name := strings.TrimSpace(string(out))
	if name == "" {
		t.Fatalf("empty branch name in %s", repoPath)
	}
	return name
}

func TestValidateBareCloneLayoutRejectsNonCurrentRefspec(t *testing.T) {
	t.Parallel()
	cache, sourceRepo := createSyncedCache(t)
	barePath := cache.Lookup("ws-1", sourceRepo)
	if err := validateBareCloneLayout(barePath); err != nil {
		t.Fatalf("new clone did not establish current layout: %v", err)
	}

	if err := setFetchRefspec(barePath, "+refs/heads/*:refs/heads/*"); err != nil {
		t.Fatalf("set non-current refspec: %v", err)
	}
	err := validateBareCloneLayout(barePath)
	if err == nil || !strings.Contains(err.Error(), "unsupported repo cache layout") {
		t.Fatalf("validateBareCloneLayout error=%v", err)
	}
	if err := gitFetch(barePath); err == nil || !strings.Contains(err.Error(), "remove the cache and sync again") {
		t.Fatalf("gitFetch did not reject non-current cache: %v", err)
	}
}

// TestCreateWorktreePathCollisionDoesNotLeakBranch verifies the secondary bug
// fix: when the worktree path already exists as a non-worktree (e.g. a plain
// directory), createWorktree must fail cleanly without leaking a branch into
// the bare repo. Previously the "already exists" retry logic would
// misclassify path collisions as branch collisions and create a second
// timestamp-suffixed branch before hitting the same path error.
func TestCreateWorktreePathCollisionDoesNotLeakBranch(t *testing.T) {
	t.Parallel()
	cache, sourceRepo := createSyncedCache(t)
	barePath := cache.Lookup("ws-1", sourceRepo)

	// Pre-create the target worktree path as a plain non-empty directory.
	workDir := t.TempDir()
	dirName := repoNameFromURL(sourceRepo)
	worktreePath := filepath.Join(workDir, dirName)
	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		t.Fatalf("pre-create worktree path: %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktreePath, "stray.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write stray file: %v", err)
	}

	_, err := cache.CreateWorktree(WorktreeParams{
		WorkspaceID: "ws-1",
		RepoURL:     sourceRepo,
		WorkDir:     workDir,
		AgentName:   "agent",
		TaskID:      "t1111111-0000-0000-0000-000000000000",
	})
	if err == nil {
		t.Fatal("expected CreateWorktree to fail when path exists as non-worktree")
	}

	// No agent/* branches should have been created in the bare repo as a
	// side effect of the failed call.
	out, runErr := exec.Command("git", "-C", barePath, "for-each-ref", "--format=%(refname)", "refs/heads/agent").Output()
	if runErr != nil {
		t.Fatalf("for-each-ref failed: %v", runErr)
	}
	if leaked := strings.TrimSpace(string(out)); leaked != "" {
		t.Errorf("branch leaked into bare repo after path-collision failure:\n%s", leaked)
	}
}

// A branch ref alone is not evidence that it is the remote default. Current
// caches require the origin/HEAD established by a successful remote refresh.
func TestGetRemoteDefaultBranchRequiresOriginHeadForCustomBranch(t *testing.T) {
	t.Parallel()
	cache, sourceRepo := createSyncedCache(t)
	barePath := cache.Lookup("ws-1", sourceRepo)

	// Resolve the existing default branch's commit so we can repoint a
	// custom-named ref at it, then wipe the standard refs to force the
	// missing-origin/HEAD rejection path.
	existing := getRemoteDefaultBranch(barePath)
	if existing == "" {
		t.Fatalf("precondition: cache should have a default branch right after sync")
	}
	commit := gitRefCommit(t, barePath, existing)

	// Create refs/remotes/origin/develop pointing at that commit.
	runGitAuthored(t, barePath, "update-ref", "refs/remotes/origin/develop", commit)
	// Now wipe origin/HEAD (symbolic-ref -d removes the symref file itself)
	// and the common defaults so steps 1 and 2 of the resolver miss and we
	// fall through to the for-each-ref scan.
	_ = exec.Command("git", "-C", barePath, "symbolic-ref", "-d", "refs/remotes/origin/HEAD").Run()
	_ = exec.Command("git", "-C", barePath, "update-ref", "-d", "refs/remotes/origin/main").Run()
	_ = exec.Command("git", "-C", barePath, "update-ref", "-d", "refs/remotes/origin/master").Run()

	if got := getRemoteDefaultBranch(barePath); got != "" {
		t.Fatalf("getRemoteDefaultBranch = %q, want no guessed custom branch", got)
	}
}

func TestGetRemoteDefaultBranchRejectsLocalBareHead(t *testing.T) {
	t.Parallel()
	cache, sourceRepo := createSyncedCache(t)
	barePath := cache.Lookup("ws-1", sourceRepo)

	// Remove current remote metadata while leaving an unrelated local HEAD.
	if err := exec.Command("sh", "-c", "rm -rf '"+filepath.Join(barePath, "refs", "remotes")+"'").Run(); err != nil {
		t.Fatalf("wipe refs/remotes: %v", err)
	}

	// Sanity: origin/* is gone, HEAD is still a symbolic ref to refs/heads/*.
	if out, err := exec.Command("git", "-C", barePath, "for-each-ref", "refs/remotes/origin/").Output(); err == nil && strings.TrimSpace(string(out)) != "" {
		t.Fatalf("precondition failed: refs/remotes/origin/* should be empty, got %s", out)
	}

	if got := getRemoteDefaultBranch(barePath); got != "" {
		t.Fatalf("getRemoteDefaultBranch = %q, want no local-head guess", got)
	}
}

// TestGitFetchRefreshesOriginHeadAfterDefaultChange verifies that an
// already-modern cache picks up a remote default-branch change. Plain `git
// fetch` never refreshes refs/remotes/origin/HEAD on its own, so without
// gitFetch's explicit `git remote set-head origin --auto` call the resolver
// would keep returning the original default branch forever after the
// upstream flipped (e.g. master → main on a long-lived repo). This guards
// against the "already-modern cache never refreshes origin/HEAD" regression.
func TestGitFetchRefreshesOriginHeadAfterDefaultChange(t *testing.T) {
	t.Parallel()
	sourceRepo := createTestRepo(t)
	initialBranch := currentBranchName(t, sourceRepo)

	cacheRoot := t.TempDir()
	cache := New(cacheRoot, testLogger())
	if err := cache.Sync("ws-1", []RepoInfo{{URL: sourceRepo}}); err != nil {
		t.Fatalf("sync failed: %v", err)
	}
	barePath := cache.Lookup("ws-1", sourceRepo)

	// Precondition: cache is already modern and origin/HEAD points at the
	// source's initial default branch.
	if got := getRemoteDefaultBranch(barePath); got != "refs/remotes/origin/"+initialBranch {
		t.Fatalf("precondition: getRemoteDefaultBranch = %q, want refs/remotes/origin/%s", got, initialBranch)
	}

	// Flip the source's default: create a new branch, commit on it, stay
	// checked out on it so the source's HEAD reflects the new default. A
	// subsequent `git ls-remote` against the source advertises this new
	// HEAD, which is what set-head --auto consumes.
	runGitAuthored(t, sourceRepo, "checkout", "-b", "new-default")
	addEmptyCommit(t, sourceRepo, "new-default commit")

	// Fetch via the cache's code path. Without the set-head call, origin/HEAD
	// would still point at the old default here.
	if err := gitFetch(barePath); err != nil {
		t.Fatalf("gitFetch failed: %v", err)
	}

	// refs/remotes/origin/HEAD must now point at the new default branch.
	out, err := exec.Command("git", "-C", barePath, "symbolic-ref", "refs/remotes/origin/HEAD").Output()
	if err != nil {
		t.Fatalf("symbolic-ref origin/HEAD after fetch: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "refs/remotes/origin/new-default" {
		t.Fatalf("origin/HEAD after fetch = %q, want refs/remotes/origin/new-default", got)
	}

	// And getRemoteDefaultBranch must resolve through step 1 (verified
	// origin/HEAD) to the new default — not through step 2 where origin/main
	// or origin/master could accidentally match the old branch.
	if got := getRemoteDefaultBranch(barePath); got != "refs/remotes/origin/new-default" {
		t.Fatalf("getRemoteDefaultBranch after fetch = %q, want refs/remotes/origin/new-default", got)
	}
}

func TestGetRemoteDefaultBranchDoesNotUseBareHeadHint(t *testing.T) {
	t.Parallel()
	cache, sourceRepo := createSyncedCache(t)
	barePath := cache.Lookup("ws-1", sourceRepo)

	existing := getRemoteDefaultBranch(barePath)
	if existing == "" {
		t.Fatalf("precondition: cache should have a default branch right after sync")
	}
	commit := gitRefCommit(t, barePath, existing)

	// Simulate a custom default branch: create refs/heads/trunk in the bare
	// repo and point HEAD at it. `git clone --bare` would do the equivalent
	// when the remote's default was "trunk", so this matches real-world
	// state for such remotes.
	runGitAuthored(t, barePath, "update-ref", "refs/heads/trunk", commit)
	runGitAuthored(t, barePath, "symbolic-ref", "HEAD", "refs/heads/trunk")

	// Populate two refs/remotes/origin/* entries. "feature-alpha" is
	// alphabetically earlier than "trunk" — a refname-order scan (the old
	// bug) would return feature-alpha, not trunk.
	runGitAuthored(t, barePath, "update-ref", "refs/remotes/origin/trunk", commit)
	runGitAuthored(t, barePath, "update-ref", "refs/remotes/origin/feature-alpha", commit)

	// Remove origin/HEAD; local and same-name refs must not replace it.
	_ = exec.Command("git", "-C", barePath, "symbolic-ref", "-d", "refs/remotes/origin/HEAD").Run()
	_ = exec.Command("git", "-C", barePath, "update-ref", "-d", "refs/remotes/origin/main").Run()
	_ = exec.Command("git", "-C", barePath, "update-ref", "-d", "refs/remotes/origin/master").Run()

	if got := getRemoteDefaultBranch(barePath); got != "" {
		t.Fatalf("getRemoteDefaultBranch = %q, want no bare-HEAD guess", got)
	}
}

// TestCreateWorktreeInstallsCoAuthoredByHook verifies that CreateWorktree
// installs a prepare-commit-msg hook that appends a Co-authored-by trailer
// for the Multica Agent to every commit made in the worktree.
func TestCreateWorktreeInstallsCoAuthoredByHook(t *testing.T) {
	t.Parallel()
	cache, sourceRepo := createSyncedCache(t)

	workDir := t.TempDir()
	result, err := cache.CreateWorktree(WorktreeParams{
		WorkspaceID:         "ws-1",
		RepoURL:             sourceRepo,
		WorkDir:             workDir,
		AgentName:           "Test Agent",
		TaskID:              "a1b2c3d4-0000-0000-0000-000000000000",
		CoAuthoredByEnabled: true,
	})
	if err != nil {
		t.Fatalf("CreateWorktree failed: %v", err)
	}

	// Make a commit in the worktree and verify the hook appends the trailer.
	if err := os.WriteFile(filepath.Join(result.Path, "test.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write test file: %v", err)
	}
	runGitAuthored(t, result.Path, "add", ".")
	runGitAuthored(t, result.Path, "commit", "-m", "test commit")

	// Read the commit message.
	out, err := exec.Command("git", "-C", result.Path, "log", "-1", "--format=%B").Output()
	if err != nil {
		t.Fatalf("git log failed: %v", err)
	}
	commitMsg := string(out)
	expectedTrailer := "Co-authored-by: multica-agent <github@multica.ai>"
	if !strings.Contains(commitMsg, expectedTrailer) {
		t.Errorf("commit message missing Co-authored-by trailer.\ngot:\n%s", commitMsg)
	}
}

// TestCoAuthoredByHookIdempotent verifies that the hook does not add a
// duplicate Co-authored-by trailer if one is already present in the message.
func TestCoAuthoredByHookIdempotent(t *testing.T) {
	t.Parallel()
	cache, sourceRepo := createSyncedCache(t)

	workDir := t.TempDir()
	result, err := cache.CreateWorktree(WorktreeParams{
		WorkspaceID:         "ws-1",
		RepoURL:             sourceRepo,
		WorkDir:             workDir,
		AgentName:           "Test Agent",
		TaskID:              "b2c3d4e5-0000-0000-0000-000000000000",
		CoAuthoredByEnabled: true,
	})
	if err != nil {
		t.Fatalf("CreateWorktree failed: %v", err)
	}

	// Commit with the trailer already in the message.
	trailer := "Co-authored-by: multica-agent <github@multica.ai>"
	if err := os.WriteFile(filepath.Join(result.Path, "test.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write test file: %v", err)
	}
	runGitAuthored(t, result.Path, "add", ".")
	runGitAuthored(t, result.Path, "commit", "-m", "test commit\n\n"+trailer)

	out, err := exec.Command("git", "-C", result.Path, "log", "-1", "--format=%B").Output()
	if err != nil {
		t.Fatalf("git log failed: %v", err)
	}
	commitMsg := string(out)

	// Count occurrences — should appear exactly once.
	count := strings.Count(commitMsg, trailer)
	if count != 1 {
		t.Errorf("expected exactly 1 Co-authored-by trailer, found %d.\ngot:\n%s", count, commitMsg)
	}
}

// TestCreateWorktreeRemovesCoAuthoredByHookWhenDisabled verifies the toggle-off
// path: a bare cache that already carries the Multica prepare-commit-msg hook
// (e.g. from a prior worktree created with the setting on) must drop the hook
// when the next CreateWorktree call passes CoAuthoredByEnabled=false.
// Otherwise commits keep getting the trailer even after the user disables the
// workspace setting.
func TestCreateWorktreeRemovesCoAuthoredByHookWhenDisabled(t *testing.T) {
	t.Parallel()
	cache, sourceRepo := createSyncedCache(t)

	// First worktree: setting enabled → hook installed in the bare cache's
	// shared hooks dir.
	workDir1 := t.TempDir()
	if _, err := cache.CreateWorktree(WorktreeParams{
		WorkspaceID:         "ws-1",
		RepoURL:             sourceRepo,
		WorkDir:             workDir1,
		AgentName:           "Test Agent",
		TaskID:              "11111111-0000-0000-0000-000000000000",
		CoAuthoredByEnabled: true,
	}); err != nil {
		t.Fatalf("CreateWorktree (enabled) failed: %v", err)
	}

	barePath := cache.Lookup("ws-1", sourceRepo)
	hookPath := filepath.Join(barePath, "hooks", "prepare-commit-msg")
	if _, err := os.Stat(hookPath); err != nil {
		t.Fatalf("precondition: expected hook to be installed at %s: %v", hookPath, err)
	}

	// Second worktree on the same bare cache: setting disabled → hook must
	// be removed and a commit in the new worktree must NOT carry the
	// trailer.
	workDir2 := t.TempDir()
	result, err := cache.CreateWorktree(WorktreeParams{
		WorkspaceID:         "ws-1",
		RepoURL:             sourceRepo,
		WorkDir:             workDir2,
		AgentName:           "Test Agent",
		TaskID:              "22222222-0000-0000-0000-000000000000",
		CoAuthoredByEnabled: false,
	})
	if err != nil {
		t.Fatalf("CreateWorktree (disabled) failed: %v", err)
	}

	if _, err := os.Stat(hookPath); !os.IsNotExist(err) {
		t.Errorf("expected hook to be removed at %s, stat err=%v", hookPath, err)
	}

	if err := os.WriteFile(filepath.Join(result.Path, "test.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write test file: %v", err)
	}
	runGitAuthored(t, result.Path, "add", ".")
	runGitAuthored(t, result.Path, "commit", "-m", "test commit")

	out, err := exec.Command("git", "-C", result.Path, "log", "-1", "--format=%B").Output()
	if err != nil {
		t.Fatalf("git log failed: %v", err)
	}
	commitMsg := string(out)
	if strings.Contains(commitMsg, "Co-authored-by: multica-agent") {
		t.Errorf("commit unexpectedly carries the Co-authored-by trailer with setting disabled.\ngot:\n%s", commitMsg)
	}
}

// TestRemoveCoAuthoredByHookPreservesUserHook verifies that the disable path
// only deletes hooks installed by the daemon. A prepare-commit-msg hook
// without the Multica marker (e.g. one a user added manually) must be left
// untouched even when CoAuthoredByEnabled=false.
func TestRemoveCoAuthoredByHookPreservesUserHook(t *testing.T) {
	t.Parallel()
	cache, sourceRepo := createSyncedCache(t)

	barePath := cache.Lookup("ws-1", sourceRepo)
	hooksDir := filepath.Join(barePath, "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatalf("create hooks dir: %v", err)
	}
	hookPath := filepath.Join(hooksDir, "prepare-commit-msg")
	userHook := "#!/bin/sh\n# user hook, not Multica\nexit 0\n"
	if err := os.WriteFile(hookPath, []byte(userHook), 0o755); err != nil {
		t.Fatalf("seed user hook: %v", err)
	}

	workDir := t.TempDir()
	if _, err := cache.CreateWorktree(WorktreeParams{
		WorkspaceID:         "ws-1",
		RepoURL:             sourceRepo,
		WorkDir:             workDir,
		AgentName:           "Test Agent",
		TaskID:              "33333333-0000-0000-0000-000000000000",
		CoAuthoredByEnabled: false,
	}); err != nil {
		t.Fatalf("CreateWorktree (disabled) failed: %v", err)
	}

	got, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatalf("user hook unexpectedly removed: %v", err)
	}
	if string(got) != userHook {
		t.Errorf("user hook contents changed.\nwant:\n%s\ngot:\n%s", userHook, string(got))
	}
}

func TestInstallCoAuthoredByHookDoesNotOverwriteUserHook(t *testing.T) {
	cache, sourceRepo := createSyncedCache(t)

	barePath := cache.Lookup("ws-1", sourceRepo)
	hookPath := filepath.Join(barePath, "hooks", "prepare-commit-msg")
	userHook := "#!/bin/sh\n# user hook, not Multica\nexit 0\n"
	if err := os.WriteFile(hookPath, []byte(userHook), 0o755); err != nil {
		t.Fatalf("seed user hook: %v", err)
	}
	workDir := t.TempDir()
	result, err := cache.CreateWorktree(WorktreeParams{
		WorkspaceID:         "ws-1",
		RepoURL:             sourceRepo,
		WorkDir:             workDir,
		AgentName:           "Test Agent",
		TaskID:              "user-hook-conflict",
		CoAuthoredByEnabled: true,
	})
	if err == nil || result != nil || !strings.Contains(err.Error(), "not managed by Multica") {
		t.Fatalf("result=%#v err=%v; want user-hook conflict", result, err)
	}
	got, readErr := os.ReadFile(hookPath)
	if readErr != nil || string(got) != userHook {
		t.Fatalf("user hook changed: got=%q err=%v", string(got), readErr)
	}
	entries, readErr := os.ReadDir(workDir)
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("failed checkout left worktree entries=%v err=%v", entries, readErr)
	}
}

// TestGetRemoteDefaultBranchAmbiguousOriginReturnsEmpty verifies step 4's
// safe-scan gating: when the cache has multiple refs/remotes/origin/*
// entries, none match the common defaults, and none match the bare HEAD
// either, the resolver must refuse to guess and return "". The caller
// surfaces this as a hard error instead of silently basing new agent work
// on an arbitrary refname-order-first candidate.
func TestGetRemoteDefaultBranchAmbiguousOriginReturnsEmpty(t *testing.T) {
	t.Parallel()
	cache, sourceRepo := createSyncedCache(t)
	barePath := cache.Lookup("ws-1", sourceRepo)

	existing := getRemoteDefaultBranch(barePath)
	if existing == "" {
		t.Fatalf("precondition: cache should have a default branch right after sync")
	}
	commit := gitRefCommit(t, barePath, existing)

	// Populate two unrelated origin branches.
	runGitAuthored(t, barePath, "update-ref", "refs/remotes/origin/feature-a", commit)
	runGitAuthored(t, barePath, "update-ref", "refs/remotes/origin/feature-b", commit)

	// Remove the required origin/HEAD and common branch refs.
	_ = exec.Command("git", "-C", barePath, "symbolic-ref", "-d", "refs/remotes/origin/HEAD").Run()
	_ = exec.Command("git", "-C", barePath, "update-ref", "-d", "refs/remotes/origin/main").Run()
	_ = exec.Command("git", "-C", barePath, "update-ref", "-d", "refs/remotes/origin/master").Run()
	got := getRemoteDefaultBranch(barePath)
	if got != "" {
		t.Fatalf("getRemoteDefaultBranch = %q, want \"\" (ambiguous origin/* must not guess)", got)
	}
}
