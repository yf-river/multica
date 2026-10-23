package codediff

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// CaptureSpec describes one run's capture: the work directory it owns and the
// repositories whose worktrees it may have created outside that directory.
type CaptureSpec struct {
	// WorkDir is the directory the agent ran in.
	WorkDir string
	// WorkDirKind is "workdir" for a disposable directory and
	// "local_directory" for a user directory used in place.
	WorkDirKind string
	// Repos are candidate repositories (project and workspace local
	// directories). Their worktrees are inspected at the end of the run.
	Repos []string
	// ExcludePaths are never captured: other tasks' environment roots and this
	// task's own scratch space.
	ExcludePaths []string
	// ConcurrentLocalTasks counts other tasks of the same workspace running on
	// this daemon. Above zero, a new worktree cannot be attributed with
	// confidence and is reported as unassigned.
	ConcurrentLocalTasks int
	Options              Options
}

type externalRepo struct {
	path      string
	head      string
	worktrees map[string]string
}

// Capture is the run-scoped collector. Start takes the baseline, Finish diffs
// against it; keeping them separate is what makes "before the agent ran"
// meaningful.
type Capture struct {
	spec         CaptureSpec
	before       *Snapshot
	external     []externalRepo
	workDirState string
}

// StartCapture records the baseline for a run. A failure here is not fatal:
// Finish still reports whatever it can, and the reason travels with the result
// instead of leaving a silent blank.
func StartCapture(ctx context.Context, spec CaptureSpec) *Capture {
	capture := &Capture{spec: spec}
	before, err := TakeSnapshot(ctx, spec.WorkDir, spec.Options)
	if err != nil {
		capture.workDirState = fmt.Sprintf("运行开始时无法读取任务目录：%v", err)
	} else {
		capture.before = before
	}
	seen := map[string]struct{}{}
	for _, repo := range spec.Repos {
		repo = strings.TrimSpace(repo)
		if repo == "" {
			continue
		}
		abs, err := filepath.Abs(repo)
		if err != nil {
			continue
		}
		if _, ok := seen[abs]; ok {
			continue
		}
		seen[abs] = struct{}{}
		if !isGitRepo(ctx, abs) {
			continue
		}
		capture.external = append(capture.external, externalRepo{
			path:      abs,
			head:      gitHead(ctx, abs),
			worktrees: worktreePaths(ctx, abs),
		})
	}
	return capture
}

// Finish produces the stored value. Work directory changes and worktrees that
// appeared during the run are reported as separate roots.
func (c *Capture) Finish(ctx context.Context) *Diff {
	if c == nil {
		return nil
	}
	defer func() {
		if c.before != nil {
			c.before.Close()
			c.before = nil
		}
	}()
	roots := []Root{}
	workRoot := Root{Path: c.spec.WorkDir, Kind: c.spec.WorkDirKind, Label: "任务目录"}
	if c.workDirState != "" {
		workRoot.Unavailable = true
		workRoot.Reason = c.workDirState
		roots = append(roots, workRoot)
		return Assembly(roots, nil, nil)
	}
	kind := c.spec.WorkDirKind
	if kind == "" {
		kind = "workdir"
	}
	workRoot = BuildRoot(ctx, RootSpec{
		Path:        c.spec.WorkDir,
		Kind:        kind,
		Label:       "任务目录",
		Before:      c.before,
		Attribution: "task",
	}, c.spec.Options)
	// A user directory used in place is shared with the user's own editor, so
	// the run cannot claim every change in it.
	if kind == "local_directory" {
		workRoot.Label = "用户目录（就地运行）"
		workRoot.Attribution = "shared"
	}
	roots = append(roots, workRoot)

	dropped := 0
	// 修改 1. The work directory already occupies one slot, so the cap counts it;
	//         dropped worktrees are counted and reported instead of disappearing
	//         behind a boundary that rendered as "no change".
	remaining := MaxRoots - 1
	for _, repo := range c.external {
		now := worktreePaths(ctx, repo.path)
		var created []string
		for path := range now {
			if _, existed := repo.worktrees[path]; !existed {
				created = append(created, path)
			}
		}
		sort.Strings(created)
		eligible := make([]string, 0, len(created))
		for _, worktree := range created {
			if pathInside(worktree, c.spec.WorkDir) || pathInside(c.spec.WorkDir, worktree) {
				continue
			}
			if excluded(worktree, c.spec.ExcludePaths) {
				continue
			}
			eligible = append(eligible, worktree)
		}
		if len(eligible) > remaining {
			dropped += len(eligible) - max(remaining, 0)
			eligible = eligible[:max(remaining, 0)]
		}
		remaining -= len(eligible)
		for _, worktree := range eligible {
			base, _, branch := WorktreeStart(ctx, worktree)
			source := "reflog"
			if base == "" {
				base = MergeBase(ctx, repo.path, gitHead(ctx, worktree), repo.head)
				source = "merge-base"
			}
			if base == "" {
				source = "unknown"
			}
			attribution := "task"
			reason := ""
			if c.spec.ConcurrentLocalTasks > 0 {
				attribution = "unassigned"
				reason = "同一仓库有多个任务同时在运行，无法确定这个工作树属于谁"
			}
			root := BuildRoot(ctx, RootSpec{
				Path:         worktree,
				Kind:         "external",
				Label:        "任务目录外的工作树",
				Attribution:  attribution,
				BaseOverride: base,
				BaseSource:   source,
				Commits:      CommitCount(ctx, worktree, base, gitHead(ctx, worktree)),
			}, c.spec.Options)
			root.Branch = branch
			if root.Reason == "" {
				root.Reason = reason
			}
			if source == "unknown" {
				root.Reason = "无法确定该工作树的基线，只列出相对空树的改动"
			}
			roots = append(roots, root)
		}
	}
	diff := Assembly(roots, c.before, nil)
	// 修改 2. Say what the cap hid: the user asked for every change to be
	//         visible, so a silently dropped group is worse than a bounded one.
	if dropped > 0 {
		diff.Notes = append(diff.Notes, fmt.Sprintf("任务目录外还有 %d 个工作树超出展示上限（最多 %d 个分组）", dropped, MaxRoots))
	}
	return diff
}

func isGitRepo(ctx context.Context, dir string) bool {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return false
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		return true
	}
	out, err := GitCommand(ctx, dir, "rev-parse", "--is-inside-work-tree")
	return err == nil && strings.TrimSpace(out) == "true"
}

// worktreePaths lists the worktrees of a repository keyed by absolute path.
func worktreePaths(ctx context.Context, repo string) map[string]string {
	out, err := GitCommand(ctx, repo, "worktree", "list", "--porcelain")
	if err != nil {
		return nil
	}
	paths := map[string]string{}
	current := ""
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			current = strings.TrimSpace(strings.TrimPrefix(line, "worktree "))
			if abs, err := filepath.Abs(current); err == nil {
				current = abs
			}
			paths[current] = ""
		case strings.HasPrefix(line, "HEAD ") && current != "":
			paths[current] = strings.TrimSpace(strings.TrimPrefix(line, "HEAD "))
		}
	}
	return paths
}

func pathInside(path, parent string) bool {
	if parent == "" {
		return false
	}
	cleanParent := filepath.Clean(parent)
	cleanPath := filepath.Clean(path)
	return cleanPath == cleanParent || strings.HasPrefix(cleanPath, cleanParent+string(filepath.Separator))
}
