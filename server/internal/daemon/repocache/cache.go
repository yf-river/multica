// Package repocache manages bare git clone caches for workspace repositories.
// The daemon uses these caches as the source for creating per-task worktrees.
package repocache

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// gitEnv returns an environment for git subprocesses that contact remotes.
// It passes the full daemon environment so credential helpers (e.g. gh) can
// locate their config, and disables TTY prompting so auth failures produce
// clear errors instead of blocking on a non-existent terminal.
//
// safe.directory=* is set via GIT_CONFIG_* env vars so git trusts all
// directories regardless of ownership. The daemon manages its own bare
// caches and worktrees, so the ownership check adds no security value
// and breaks CI environments where the runner UID differs from the
// directory owner.
func gitEnv() []string {
	base := os.Environ()

	// Find the existing GIT_CONFIG_COUNT so we append at the next index
	// rather than overwriting any env-scoped git config (auth, URL
	// rewrites, extra headers, etc.).
	existing := 0
	for _, e := range base {
		if strings.HasPrefix(e, "GIT_CONFIG_COUNT=") {
			if n, err := strconv.Atoi(strings.TrimPrefix(e, "GIT_CONFIG_COUNT=")); err == nil {
				existing = n
			}
		}
	}

	idx := strconv.Itoa(existing)
	return append(base,
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_COUNT="+strconv.Itoa(existing+1),
		"GIT_CONFIG_KEY_"+idx+"=safe.directory",
		"GIT_CONFIG_VALUE_"+idx+"=*",
	)
}

var agentGitExcludePatterns = []string{".agent_context", "CLAUDE.md", "AGENTS.md", ".claude", ".opencode", "artifacts"}

const (
	gitCloneTimeout    = 10 * time.Minute
	gitFetchTimeout    = 5 * time.Minute
	gitMetadataTimeout = time.Minute
)

// RepoInfo describes a repository to cache.
type RepoInfo struct {
	URL string
}

// CachedRepo describes a cached bare clone ready for worktree creation.
type CachedRepo struct {
	URL       string // remote URL
	LocalPath string // absolute path to the bare clone
}

// Cache manages bare git clones for workspace repositories.
type Cache struct {
	root   string // base directory for all caches (e.g. ~/multica_workspaces/.repos)
	logger *slog.Logger
	// repoLocks maps bare repo path → dedicated mutex. Any mutating operation
	// on a given bare repo (clone, fetch, worktree add, ref update) must
	// hold its lock — git's own lockfiles (packed-refs.lock, config.lock,
	// worktree admin dirs) don't tolerate parallel mutations on the same
	// repo. Separate repos are independent and run concurrently.
	repoLocks sync.Map // barePath -> *sync.Mutex
}

// New creates a new repo cache rooted at the given directory.
func New(root string, logger *slog.Logger) *Cache {
	return &Cache{root: root, logger: logger}
}

// lockForRepo returns the mutex dedicated to the given bare repo path. See
// the Cache.repoLocks field comment for semantics.
func (c *Cache) lockForRepo(barePath string) *sync.Mutex {
	if l, ok := c.repoLocks.Load(barePath); ok {
		return l.(*sync.Mutex)
	}
	newLock := &sync.Mutex{}
	actual, _ := c.repoLocks.LoadOrStore(barePath, newLock)
	return actual.(*sync.Mutex)
}

// Sync ensures all repos for a workspace are cloned (or fetched if already cached).
// Repos no longer in the list are left in place (cheap to keep, avoids re-cloning
// if a repo is temporarily removed and re-added).
//
// Per-repo mutation serializes against CreateWorktree on the same bare path
// via lockForRepo. Different repos run sequentially within a single Sync call
// but concurrent Sync calls (different workspaces, or the same workspace
// re-synced while checkouts are running) do not block each other.
func (c *Cache) Sync(workspaceID string, repos []RepoInfo) error {
	wsDir, err := c.workspaceCacheDir(workspaceID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		return fmt.Errorf("create workspace cache dir: %w", err)
	}

	var firstErr error
	for _, repo := range repos {
		if repo.URL == "" {
			continue
		}
		barePath := filepath.Join(wsDir, bareDirName(repo.URL))

		repoLock := c.lockForRepo(barePath)
		repoLock.Lock()
		if isBareRepo(barePath) {
			// Already cached — fetch latest.
			c.logger.Info("repo cache: fetching", "url", repo.URL, "path", barePath)
			if err := gitFetch(barePath); err != nil {
				c.logger.Warn("repo cache: fetch failed", "url", repo.URL, "error", err)
				if firstErr == nil {
					firstErr = err
				}
			}
		} else {
			// Not cached — bare clone.
			c.logger.Info("repo cache: cloning", "url", repo.URL, "path", barePath)
			if err := gitCloneBare(repo.URL, barePath); err != nil {
				c.logger.Error("repo cache: clone failed", "url", repo.URL, "error", err)
				if firstErr == nil {
					firstErr = err
				}
			}
		}
		repoLock.Unlock()
	}
	return firstErr
}

// Lookup returns the local bare clone path for a repo URL within a workspace.
// Returns "" if not cached.
func (c *Cache) Lookup(workspaceID, url string) string {
	wsDir, err := c.workspaceCacheDir(workspaceID)
	if err != nil {
		return ""
	}
	barePath := filepath.Join(wsDir, bareDirName(url))
	if isBareRepo(barePath) {
		return barePath
	}
	return ""
}

func (c *Cache) workspaceCacheDir(workspaceID string) (string, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" || workspaceID == "." || workspaceID == ".." || filepath.Base(workspaceID) != workspaceID {
		return "", fmt.Errorf("invalid workspace cache segment %q", workspaceID)
	}
	root, err := filepath.Abs(c.root)
	if err != nil {
		return "", fmt.Errorf("resolve repo cache root: %w", err)
	}
	result := filepath.Join(root, workspaceID)
	if !pathWithin(root, result) {
		return "", fmt.Errorf("workspace cache path escapes root: %q", workspaceID)
	}
	return result, nil
}

// WithRepoLock serializes caller-supplied mutations on a bare repo against all
// other same-repo operations that use the cache's lock (Sync, Fetch,
// CreateWorktree, and daemon GC maintenance).
func (c *Cache) WithRepoLock(barePath string, fn func() error) error {
	repoLock := c.lockForRepo(barePath)
	repoLock.Lock()
	defer repoLock.Unlock()
	return fn()
}

// Fetch runs `git fetch origin` on a cached bare clone to get latest refs.
func (c *Cache) Fetch(barePath string) error {
	return c.WithRepoLock(barePath, func() error {
		return gitFetch(barePath)
	})
}

// bareDirName returns a filesystem-safe, collision-free directory name for
// the bare clone of rawURL. The name is built from the host plus each
// path segment, joined by '+'. '+' is disallowed in GitHub and GitLab
// path segments, so two URLs produce the same name only if they point at
// the same repository on the same host.
//
// Examples:
//
//	https://github.com/org/my-repo.git           -> github.com+org+my-repo.git
//	git@github.com:org/my-repo                   -> github.com+org+my-repo.git
//	git@github.com:foo/bar-baz.git               -> github.com+foo+bar-baz.git
//	git@github.com:foo-bar/baz.git               -> github.com+foo-bar+baz.git
//	git@github.com:org/repo.git                  -> github.com+org+repo.git
//	git@gitlab.example.com:org/repo.git          -> gitlab.example.com+org+repo.git
//	ssh://git@gitlab.example.com:22/g/s/r.git    -> gitlab.example.com%3A22+g+s+r.git
//	git@gitlab.example.com-22:org/repo.git       -> gitlab.example.com-22+org+repo.git
//	my-repo                                      -> my-repo.git (bare name fallback)
func bareDirName(rawURL string) string {
	rawURL = strings.TrimRight(rawURL, "/")

	host, path := splitHostAndPath(rawURL)
	host = strings.ToLower(strings.TrimSpace(host))
	// Encode ':' as '%3A' so host:port is lossless. A naive ':'->'-' rewrite
	// would collapse `gitlab.example.com:22` onto a literal hostname
	// `gitlab.example.com-22`, reintroducing the silent wrong-remote class
	// this function exists to prevent. '%' is forbidden in valid hostnames
	// (RFC 952 / RFC 1123), and in GitHub/GitLab path segments, so the
	// encoded marker can never come from a legal input.
	host = strings.ReplaceAll(host, ":", "%3A")

	var parts []string
	if host != "" {
		parts = append(parts, host)
	}
	for _, seg := range strings.Split(path, "/") {
		if seg != "" {
			parts = append(parts, seg)
		}
	}

	name := strings.Join(parts, "+")
	if !strings.HasSuffix(name, ".git") {
		name += ".git"
	}
	if name == "" || name == ".git" {
		name = "repo.git"
	}
	return name
}

// splitHostAndPath extracts the host and path-with-namespace from the
// supported git URL forms:
//
//   - URL form (ssh://user@host[:port]/path, https://host/path) — returns
//     u.Host verbatim (may include :port) and u.Path without the leading slash.
//   - scp-style ([user@]host:path) — splits on the first ':' after the
//     optional 'user@'.
//   - Anything else (bare repo names, absolute filesystem paths) — returns
//     an empty host and the raw input as the path.
func splitHostAndPath(rawURL string) (host, path string) {
	if u, err := url.Parse(rawURL); err == nil && u.Scheme != "" && u.Host != "" {
		return u.Host, strings.TrimPrefix(u.Path, "/")
	}
	s := rawURL
	if i := strings.Index(s, "@"); i >= 0 {
		s = s[i+1:]
	}
	if i := strings.Index(s, ":"); i >= 0 {
		return s[:i], s[i+1:]
	}
	return "", s
}

// isBareRepo checks if a path looks like a bare git repository.
func isBareRepo(path string) bool {
	// A bare repo has a HEAD file at the root.
	_, err := os.Stat(filepath.Join(path, "HEAD"))
	return err == nil
}

// modernFetchRefspec is the remote-tracking refspec that keeps fetched heads
// out of the bare repo's refs/heads/* namespace. That namespace is reserved
// for per-task worktree branches created by `git worktree add -b ...`, and any
// mirror-style fetch that targets refs/heads/* can collide with those locked
// refs and abort the entire fetch.
const modernFetchRefspec = "+refs/heads/*:refs/remotes/origin/*"

func gitCloneBare(url, dest string) error {
	out, err := runRemoteGit(gitCloneTimeout, "clone", "--bare", url, dest)
	if err != nil {
		// Clean up partial clone.
		if cleanupErr := os.RemoveAll(dest); cleanupErr != nil {
			return fmt.Errorf("git clone --bare: %s; cleanup partial clone: %v: %w", strings.TrimSpace(string(out)), cleanupErr, err)
		}
		return fmt.Errorf("git clone --bare: %s: %w", strings.TrimSpace(string(out)), err)
	}
	// Establish the only cache layout supported by the current daemon before
	// exposing this clone through Lookup.
	if err := configureNewBareClone(dest); err != nil {
		if cleanupErr := os.RemoveAll(dest); cleanupErr != nil {
			return fmt.Errorf("configure fetch refspec: %w; cleanup partial clone: %v", err, cleanupErr)
		}
		return fmt.Errorf("configure fetch refspec: %w", err)
	}
	return nil
}

// gitFetch runs `git fetch origin` on a current-layout bare cache. After a
// successful fetch it refreshes refs/remotes/origin/HEAD so a remote
// default-branch change (e.g. master→main on an existing repo) actually
// takes effect in getRemoteDefaultBranch. Plain `git fetch origin` never
// touches that symref on its own, so without this call an existing cache
// would keep basing new worktrees on the original default branch forever
// after the remote flipped.
func gitFetch(barePath string) error {
	if err := validateBareCloneLayout(barePath); err != nil {
		return err
	}
	if err := runGitFetch(barePath); err != nil {
		return err
	}
	// Refreshing origin/HEAD is part of the fetch result. Ignoring this failure
	// can keep a valid but stale symref after the remote changes its default
	// branch, making the checkout silently select old code.
	return refreshRemoteHead(barePath)
}

// runGitFetch is the raw `git fetch origin` wrapper. Callers should go through
// gitFetch unless they are establishing a new clone's current layout.
func runGitFetch(barePath string) error {
	out, err := runRemoteGit(gitFetchTimeout, "-C", barePath, "fetch", "origin")
	if err != nil {
		return fmt.Errorf("git fetch: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

func refreshRemoteHead(barePath string) error {
	out, err := runRemoteGit(gitMetadataTimeout, "-C", barePath, "remote", "set-head", "origin", "--auto")
	if err != nil {
		return fmt.Errorf("refresh remote default branch: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

func runRemoteGit(timeout time.Duration, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return runRemoteGitContext(ctx, args...)
}

func runRemoteGitContext(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = gitEnv()
	out, err := cmd.CombinedOutput()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return out, fmt.Errorf("git remote command cancelled: %w", ctxErr)
	}
	return out, err
}

func configureNewBareClone(barePath string) error {
	if err := setFetchRefspec(barePath, modernFetchRefspec); err != nil {
		return err
	}
	if err := runGitFetch(barePath); err != nil {
		return fmt.Errorf("populate remote refs: %w", err)
	}
	return refreshRemoteHead(barePath)
}

func validateBareCloneLayout(barePath string) error {
	refspec, err := readFetchRefspec(barePath)
	if err != nil {
		return err
	}
	if refspec != modernFetchRefspec {
		return fmt.Errorf("unsupported repo cache layout at %s: remote.origin.fetch is %q, want %q; remove the cache and sync again", barePath, refspec, modernFetchRefspec)
	}
	return nil
}

// readFetchRefspec returns the current remote.origin.fetch config value, or
// the empty string if it's not set. Distinguishes "missing" (exit 1) from
// real git errors.
func readFetchRefspec(barePath string) (string, error) {
	cmd := exec.Command("git", "-C", barePath, "config", "--get", "remote.origin.fetch")

	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
			return "", nil // key missing, not an error
		}
		return "", fmt.Errorf("read remote.origin.fetch: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func setFetchRefspec(barePath, refspec string) error {
	cmd := exec.Command("git", "-C", barePath, "config", "remote.origin.fetch", refspec)

	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("set remote.origin.fetch: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// WorktreeParams holds inputs for creating a worktree from a cached bare clone.
type WorktreeParams struct {
	WorkspaceID         string // workspace that owns the repo
	RepoURL             string // remote URL to look up in the cache
	WorkDir             string // parent directory for the worktree (e.g. task workdir)
	Ref                 string // optional branch, tag, or commit to base the worktree on
	AgentName           string // for branch naming
	TaskID              string // for branch naming uniqueness
	BranchNameOverride  string // optional exact branch name for issue-scoped worktrees
	PreserveExisting    bool   // when true, reuse an existing worktree without reset/checkout
	CoAuthoredByEnabled bool   // install prepare-commit-msg hook for Co-authored-by trailer
}

// WorktreeResult describes a successfully created worktree.
type WorktreeResult struct {
	Path       string `json:"path"`        // absolute path to the worktree
	BranchName string `json:"branch_name"` // git branch created for this worktree
}

// CreateWorktree looks up the bare cache for a repo, fetches latest, and creates
// a git worktree in the agent's working directory. If a worktree already exists
// at the target path (reused environment), it updates the existing worktree to
// the latest remote default branch instead of failing.
func (c *Cache) CreateWorktree(params WorktreeParams) (*WorktreeResult, error) {
	worktreePath, err := c.managedWorktreePath(params.WorkDir, params.RepoURL)
	if err != nil {
		return nil, err
	}
	barePath := c.Lookup(params.WorkspaceID, params.RepoURL)
	if barePath == "" {
		return nil, fmt.Errorf("repo not found in cache: %s (workspace: %s)", params.RepoURL, params.WorkspaceID)
	}

	// Serialize concurrent CreateWorktree calls on the same bare repo. Git's
	// own lockfiles (packed-refs.lock, config.lock, worktree admin dirs)
	// can't tolerate parallel fetch + worktree mutations on the same repo.
	repoLock := c.lockForRepo(barePath)
	repoLock.Lock()
	defer repoLock.Unlock()

	// Fetch latest from origin. This also migrates the bare cache's refspec
	// to the modern remote-tracking layout on first run, so subsequent fetches
	// never collide with the refs/heads/agent/* branches that worktree creation
	// locks in this same bare repo.
	if err := gitFetch(barePath); err != nil {
		return nil, fmt.Errorf("fetch repo before checkout: %w", err)
	}

	// Determine the ref to base the worktree on. By default this is the verified
	// origin/HEAD refreshed by the current cache layout.
	// Callers may request a specific branch, tag, or commit so review/QA agents
	// can inspect the exact revision without trying to mutate the daemon-owned
	// worktree metadata themselves.
	baseRef, err := resolveBaseRef(barePath, params.Ref)
	if err != nil {
		return nil, err
	}

	// The requested-ref path returns an explicit error earlier. Empty here means
	// the cache lost its required origin/HEAD metadata, which must be repaired by
	// a successful sync rather than guessed from branch names.
	if baseRef == "" {
		return nil, fmt.Errorf("cannot resolve default branch for %s: cache at %s has no valid origin/HEAD; remove the cache and sync again", params.RepoURL, barePath)
	}

	// Build branch name: agent/{sanitized-name}/{short-task-id}, unless the
	// caller owns a stable issue-scoped branch name.
	branchName := strings.TrimSpace(params.BranchNameOverride)
	if branchName == "" {
		branchName = fmt.Sprintf("agent/%s/%s", sanitizeName(params.AgentName), shortID(params.TaskID))
	}

	// If worktree already exists (reused environment from a prior task),
	// update it to the latest remote code instead of creating a new one.
	if isGitWorktree(worktreePath) {
		if params.PreserveExisting {
			actualBranch := currentBranch(worktreePath)
			if actualBranch == "" {
				actualBranch = branchName
			}
			if branchName != "" && actualBranch != branchName {
				switchedBranch, err := switchExistingWorktreeBranch(worktreePath, branchName)
				if err != nil {
					return nil, fmt.Errorf("restore preserved worktree branch: %w", err)
				}
				actualBranch = switchedBranch
			}
			if err := configureWorktreeGit(worktreePath, params.CoAuthoredByEnabled); err != nil {
				return nil, fmt.Errorf("configure preserved worktree: %w", err)
			}
			c.logger.Info("repo checkout: existing worktree preserved",
				"url", params.RepoURL,
				"path", worktreePath,
				"branch", actualBranch,
				"base", baseRef,
			)
			return &WorktreeResult{
				Path:       worktreePath,
				BranchName: actualBranch,
			}, nil
		}
		actualBranch, err := updateExistingWorktree(worktreePath, branchName, baseRef)
		if err != nil {
			return nil, fmt.Errorf("update existing worktree: %w", err)
		}

		if err := configureWorktreeGit(worktreePath, params.CoAuthoredByEnabled); err != nil {
			return nil, fmt.Errorf("configure updated worktree: %w", err)
		}

		c.logger.Info("repo checkout: existing worktree updated",
			"url", params.RepoURL,
			"path", worktreePath,
			"branch", actualBranch,
			"base", baseRef,
		)

		return &WorktreeResult{
			Path:       worktreePath,
			BranchName: actualBranch,
		}, nil
	}

	// Create a new worktree. createWorktree may rename the branch to avoid
	// collisions with stale per-task refs left over from previous runs.
	actualBranch, err := createWorktree(barePath, worktreePath, branchName, baseRef)
	if err != nil {
		return nil, fmt.Errorf("create worktree: %w", err)
	}

	if err := configureWorktreeGit(worktreePath, params.CoAuthoredByEnabled); err != nil {
		if cleanupErr := rollbackNewWorktree(barePath, worktreePath, actualBranch); cleanupErr != nil {
			return nil, fmt.Errorf("configure new worktree: %w; rollback failed: %v", err, cleanupErr)
		}
		return nil, fmt.Errorf("configure new worktree: %w", err)
	}

	c.logger.Info("repo checkout: worktree created",
		"url", params.RepoURL,
		"path", worktreePath,
		"branch", actualBranch,
		"base", baseRef,
	)

	return &WorktreeResult{
		Path:       worktreePath,
		BranchName: actualBranch,
	}, nil
}

// configureWorktreeGit applies the repository hygiene required before an
// Agent can safely use a checkout. These settings are part of successful
// checkout creation, not optional decoration: context files must stay
// untracked and the commit hook must match the workspace setting.
func configureWorktreeGit(worktreePath string, coAuthoredByEnabled bool) error {
	for _, pattern := range agentGitExcludePatterns {
		if err := excludeFromGit(worktreePath, pattern); err != nil {
			return fmt.Errorf("configure git exclude %q: %w", pattern, err)
		}
	}
	if coAuthoredByEnabled {
		if err := installCoAuthoredByHook(worktreePath); err != nil {
			return fmt.Errorf("configure co-authored-by hook: %w", err)
		}
		return nil
	}
	if err := removeCoAuthoredByHook(worktreePath); err != nil {
		return fmt.Errorf("configure co-authored-by hook: %w", err)
	}
	return nil
}

func rollbackNewWorktree(barePath, worktreePath, branchName string) error {
	removeCmd := exec.Command("git", "-C", barePath, "worktree", "remove", "--force", worktreePath)
	if out, err := removeCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("remove worktree: %s: %w", strings.TrimSpace(string(out)), err)
	}
	branchCmd := exec.Command("git", "-C", barePath, "branch", "-D", branchName)
	if out, err := branchCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("delete worktree branch: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// managedWorktreePath confines daemon-owned checkouts to the workspaces root,
// which is the parent of the .repos cache. This protects reset/clean reuse from
// ever targeting an arbitrary user-supplied Git worktree through the localhost
// checkout endpoint.
func (c *Cache) managedWorktreePath(workDir, repoURL string) (string, error) {
	cacheRoot, err := filepath.Abs(c.root)
	if err != nil {
		return "", fmt.Errorf("resolve repo cache root: %w", err)
	}
	managedRoot := filepath.Dir(cacheRoot)
	managedRoot, err = filepath.Abs(managedRoot)
	if err != nil {
		return "", fmt.Errorf("resolve managed workspaces root: %w", err)
	}
	workDir, err = filepath.Abs(strings.TrimSpace(workDir))
	if err != nil {
		return "", fmt.Errorf("resolve worktree parent: %w", err)
	}
	if !pathWithin(managedRoot, workDir) || pathWithin(cacheRoot, workDir) {
		return "", fmt.Errorf("worktree parent must be inside managed workspaces root and outside repo cache: %s", workDir)
	}
	resolvedRoot, err := filepath.EvalSymlinks(managedRoot)
	if err != nil {
		return "", fmt.Errorf("resolve managed workspaces root symlinks: %w", err)
	}
	resolvedWorkDir, err := filepath.EvalSymlinks(workDir)
	if err != nil {
		return "", fmt.Errorf("resolve worktree parent symlinks: %w", err)
	}
	if !pathWithin(resolvedRoot, resolvedWorkDir) {
		return "", fmt.Errorf("worktree parent escapes managed root through symlink: %s", workDir)
	}
	dirName := repoNameFromURL(repoURL)
	if dirName == "" || dirName == "." || dirName == ".." || filepath.Base(dirName) != dirName {
		return "", fmt.Errorf("cannot derive safe worktree directory from repo URL %q", repoURL)
	}
	result := filepath.Join(workDir, dirName)
	if !pathWithin(workDir, result) {
		return "", fmt.Errorf("derived worktree path escapes parent: %s", result)
	}
	return result, nil
}

func pathWithin(root, target string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(target))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func resolveBaseRef(barePath, requestedRef string) (string, error) {
	ref := strings.TrimSpace(requestedRef)
	if ref == "" {
		return getRemoteDefaultBranch(barePath), nil
	}

	// Prefer remote-tracking branches for human branch names. Then allow full
	// local refs, tags, and raw commits that exist in the fetched bare cache.
	candidates := []string{
		"refs/remotes/origin/" + ref,
		"refs/tags/" + ref,
		ref,
	}
	for _, candidate := range candidates {
		if gitRefExists(barePath, candidate+"^{commit}") {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("cannot resolve requested ref %q in repo cache at %s", ref, barePath)
}

func gitRefExists(repoPath, ref string) bool {
	cmd := exec.Command("git", "-C", repoPath, "rev-parse", "--verify", "--quiet", ref)

	return cmd.Run() == nil
}

// createWorktree creates a git worktree at the given path with a new branch.
// Returns the actual branch name used — which may differ from the requested
// branchName if a collision was resolved by appending a timestamp suffix.
func createWorktree(gitRoot, worktreePath, branchName, baseRef string) (string, error) {
	// Pre-check: if the worktree path already exists we would get a confusing
	// "already exists" error from `git worktree add` — which used to be
	// misclassified as a branch collision, causing the retry to leak branches
	// into the bare repo. Fail cleanly here instead. The caller is expected
	// to route reused workdirs through updateExistingWorktree via isGitWorktree.
	if _, err := os.Stat(worktreePath); err == nil {
		return "", fmt.Errorf("worktree path already exists and is not a valid git worktree: %s", worktreePath)
	}

	err := runWorktreeAdd(gitRoot, worktreePath, branchName, baseRef)
	if err != nil && isBranchCollisionError(err) {
		// Branch name collision: append timestamp and retry once.
		branchName = fmt.Sprintf("%s-%d", branchName, time.Now().Unix())
		err = runWorktreeAdd(gitRoot, worktreePath, branchName, baseRef)
	}
	if err != nil {
		return "", err
	}
	return branchName, nil
}

func runWorktreeAdd(gitRoot, worktreePath, branchName, baseRef string) error {
	cmd := exec.Command("git", "-C", gitRoot, "worktree", "add", "-b", branchName, worktreePath, baseRef)

	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git worktree add: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// isBranchCollisionError returns true if err is specifically about a branch
// name already existing. Git's other "already exists" messages (notably path
// collisions from `git worktree add`) must NOT be treated as branch
// collisions, or the retry-with-timestamp logic will leak branches while
// still failing on the original path collision.
func isBranchCollisionError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	// Git's message is "fatal: a branch named 'X' already exists".
	return strings.Contains(msg, "a branch named")
}

// isGitWorktree checks if a path is an existing git worktree.
// Worktrees have a .git *file* (not directory) that points to the main repo.
func isGitWorktree(path string) bool {
	info, err := os.Stat(filepath.Join(path, ".git"))
	return err == nil && !info.IsDir()
}

func currentBranch(worktreePath string) string {
	cmd := exec.Command("git", "-C", worktreePath, "branch", "--show-current")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func switchExistingWorktreeBranch(worktreePath, branchName string) (string, error) {
	checkRef := exec.Command("git", "-C", worktreePath, "show-ref", "--verify", "--quiet", "refs/heads/"+branchName)
	if err := checkRef.Run(); err != nil {
		return "", fmt.Errorf("expected issue branch %q is missing", branchName)
	}
	checkoutCmd := exec.Command("git", "-C", worktreePath, "checkout", branchName)
	if out, err := checkoutCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("git checkout %s: %s: %w", branchName, strings.TrimSpace(string(out)), err)
	}
	actualBranch := currentBranch(worktreePath)
	if actualBranch == "" {
		actualBranch = branchName
	}
	return actualBranch, nil
}

// updateExistingWorktree resets the worktree to a clean state and checks out a
// new branch from the default branch. The caller is responsible for fetching
// the bare cache beforehand (worktrees share the same object store).
// Returns the actual branch name used (may differ from input on collision).
func updateExistingWorktree(worktreePath, branchName, baseRef string) (string, error) {
	// Discard any leftover uncommitted changes from the previous task.
	resetCmd := exec.Command("git", "-C", worktreePath, "reset", "--hard")
	if out, err := resetCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("git reset --hard: %s: %w", strings.TrimSpace(string(out)), err)
	}

	// Clean untracked files (e.g. build artifacts from previous task).
	cleanCmd := exec.Command("git", "-C", worktreePath, "clean", "-fd")
	if out, err := cleanCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("git clean -fd: %s: %w", strings.TrimSpace(string(out)), err)
	}

	// Create a new branch from the resolved default-branch ref and switch to
	// it. baseRef is the verified refs/remotes/origin/<branch> target.
	checkoutCmd := exec.Command("git", "-C", worktreePath, "checkout", "-b", branchName, baseRef)
	out, err := checkoutCmd.CombinedOutput()
	if err == nil {
		return branchName, nil
	}
	wrapped := fmt.Errorf("git checkout -b: %s: %w", strings.TrimSpace(string(out)), err)
	if !isBranchCollisionError(wrapped) {
		return "", wrapped
	}
	// Branch name collision: append timestamp and retry once.
	branchName = fmt.Sprintf("%s-%d", branchName, time.Now().Unix())
	checkoutCmd = exec.Command("git", "-C", worktreePath, "checkout", "-b", branchName, baseRef)
	if out2, err2 := checkoutCmd.CombinedOutput(); err2 != nil {
		return "", fmt.Errorf("git checkout -b (retry): %s: %w", strings.TrimSpace(string(out2)), err2)
	}
	return branchName, nil
}

// getRemoteDefaultBranch returns the verified origin/HEAD target established
// by every successful current clone and fetch. Missing or invalid metadata is
// an invariant violation; guessing main, master, or a local ref can select the
// wrong branch and is deliberately unsupported.
func getRemoteDefaultBranch(barePath string) string {
	symrefCmd := exec.Command("git", "-C", barePath, "symbolic-ref", "refs/remotes/origin/HEAD")
	out, err := symrefCmd.Output()
	if err != nil {
		return ""
	}
	ref := strings.TrimSpace(string(out))
	if ref == "" {
		return ""
	}
	verifyCmd := exec.Command("git", "-C", barePath, "rev-parse", "--verify", ref)
	if err := verifyCmd.Run(); err != nil {
		return ""
	}
	return ref
}

// multicaHookMarker is a sentinel comment embedded in every prepare-commit-msg
// hook installed by the daemon. removeCoAuthoredByHook uses it to recognize
// hooks it owns so it never deletes a hook installed by the user or another
// tool. Do not change without bumping the recognition logic.
const multicaHookMarker = "# multica:prepare-commit-msg:co-authored-by"

// prepareCommitMsgHook is the prepare-commit-msg hook script that appends a
// Co-authored-by trailer for the Multica Agent to every commit message.
const prepareCommitMsgHook = `#!/bin/sh
# multica:prepare-commit-msg:co-authored-by
# Multica: add Co-authored-by trailer for the Multica Agent.
# Installed by the Multica daemon. Do not edit — it will be overwritten.

COMMIT_MSG_FILE="$1"
COMMIT_SOURCE="$2"

# Skip merge and squash commits.
case "$COMMIT_SOURCE" in
  merge|squash) exit 0 ;;
esac

TRAILER="Co-authored-by: multica-agent <github@multica.ai>"

# Don't add if already present.
if grep -qF "$TRAILER" "$COMMIT_MSG_FILE"; then
  exit 0
fi

# Use git interpret-trailers for proper formatting.
git interpret-trailers --in-place --trailer "$TRAILER" "$COMMIT_MSG_FILE"
`

// installCoAuthoredByHook installs a prepare-commit-msg git hook that appends
// a Co-authored-by trailer for the Multica Agent. The hook is installed in the
// git common directory (the bare repo for worktrees) so it applies to all
// worktrees created from this cache.
func installCoAuthoredByHook(worktreePath string) error {
	cmd := exec.Command("git", "-C", worktreePath, "rev-parse", "--git-common-dir")

	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("resolve git common dir: %w", err)
	}
	commonDir := strings.TrimSpace(string(out))
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(worktreePath, commonDir)
	}

	hooksDir := filepath.Join(commonDir, "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		return fmt.Errorf("create hooks dir: %w", err)
	}

	hookPath := filepath.Join(hooksDir, "prepare-commit-msg")
	contents, err := os.ReadFile(hookPath)
	if err == nil && !isDaemonInstalledHook(contents) {
		return fmt.Errorf("prepare-commit-msg hook already exists and is not managed by Multica")
	}
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read prepare-commit-msg hook: %w", err)
	}
	if err := os.WriteFile(hookPath, []byte(prepareCommitMsgHook), 0o755); err != nil {
		return fmt.Errorf("write prepare-commit-msg hook: %w", err)
	}
	return nil
}

// isDaemonInstalledHook reports whether a prepare-commit-msg hook on disk was
// installed by the current Multica daemon. It returns false for hooks that do
// not carry the current marker, so user and third-party hooks are left alone.
func isDaemonInstalledHook(contents []byte) bool {
	return strings.Contains(string(contents), multicaHookMarker)
}

// removeCoAuthoredByHook removes the prepare-commit-msg hook installed by
// installCoAuthoredByHook. It only deletes the file when the content matches
// the current daemon marker, so a user-installed prepare-commit-msg hook is
// never touched.
// Returns nil when no hook is present or when an unrelated hook occupies
// the path.
func removeCoAuthoredByHook(worktreePath string) error {
	cmd := exec.Command("git", "-C", worktreePath, "rev-parse", "--git-common-dir")

	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("resolve git common dir: %w", err)
	}
	commonDir := strings.TrimSpace(string(out))
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(worktreePath, commonDir)
	}

	hookPath := filepath.Join(commonDir, "hooks", "prepare-commit-msg")
	contents, err := os.ReadFile(hookPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read prepare-commit-msg hook: %w", err)
	}
	if !isDaemonInstalledHook(contents) {
		// Unrelated hook (user or third-party): leave it alone.
		return nil
	}
	if err := os.Remove(hookPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove prepare-commit-msg hook: %w", err)
	}
	return nil
}

// excludeFromGit adds a pattern to the worktree's .git/info/exclude file.
func excludeFromGit(worktreePath, pattern string) error {
	cmd := exec.Command("git", "-C", worktreePath, "rev-parse", "--git-dir")

	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("resolve git dir: %w", err)
	}

	gitDir := strings.TrimSpace(string(out))
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(worktreePath, gitDir)
	}

	excludePath := filepath.Join(gitDir, "info", "exclude")

	if err := os.MkdirAll(filepath.Dir(excludePath), 0o755); err != nil {
		return fmt.Errorf("create info dir: %w", err)
	}

	existing, err := os.ReadFile(excludePath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read exclude file: %w", err)
	}
	if hasGitExcludePattern(existing, pattern) {
		return nil
	}

	f, err := os.OpenFile(excludePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open exclude file: %w", err)
	}
	if _, err := fmt.Fprintf(f, "\n%s\n", pattern); err != nil {
		_ = f.Close()
		return fmt.Errorf("write exclude pattern: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close exclude file: %w", err)
	}
	return nil
}

func hasGitExcludePattern(contents []byte, pattern string) bool {
	for _, line := range strings.Split(string(contents), "\n") {
		if strings.TrimSpace(line) == pattern {
			return true
		}
	}
	return false
}

// repoNameFromURL extracts a short directory name from a git remote URL.
// e.g. "https://github.com/org/my-repo.git" → "my-repo"
func repoNameFromURL(url string) string {
	url = strings.TrimRight(url, "/")
	url = strings.TrimSuffix(url, ".git")

	if i := strings.LastIndex(url, "/"); i >= 0 {
		url = url[i+1:]
	}
	if i := strings.LastIndex(url, ":"); i >= 0 {
		url = url[i+1:]
		if j := strings.LastIndex(url, "/"); j >= 0 {
			url = url[j+1:]
		}
	}

	name := strings.TrimSpace(url)
	if name == "" {
		return "repo"
	}
	return name
}

var nonAlphanumeric = regexp.MustCompile(`[^a-z0-9]+`)

// sanitizeName produces a git-branch-safe name from a human-readable string.
func sanitizeName(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	s = nonAlphanumeric.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 30 {
		s = s[:30]
		s = strings.TrimRight(s, "-")
	}
	if s == "" {
		s = "agent"
	}
	return s
}

// shortID returns the first 8 characters of a UUID string (dashes stripped).
func shortID(uuid string) string {
	s := strings.ReplaceAll(uuid, "-", "")
	s = nonAlphanumeric.ReplaceAllString(strings.ToLower(strings.TrimSpace(s)), "")
	if s == "" {
		return "manual"
	}
	if len(s) > 8 {
		return s[:8]
	}
	return s
}
