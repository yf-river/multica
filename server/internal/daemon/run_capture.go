package daemon

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/codediff"
	"github.com/multica-ai/multica/server/internal/daemon/execenv"
)

// runCapture is the daemon-side wrapper around codediff.Capture: it knows which
// directories a run may have touched and how to keep one task's capture from
// competing with another's.
type runCapture struct {
	daemon      *Daemon
	capture     *codediff.Capture
	workspaceID string
}

// captureOptions maps the daemon configuration onto the capture budget.
func (d *Daemon) captureOptions() codediff.Options {
	options := codediff.DefaultOptions()
	if len(d.cfg.RunDiffDependencyDirs) > 0 {
		options.DependencyDirs = d.cfg.RunDiffDependencyDirs
	}
	options.BuildOutputDirs = d.cfg.RunDiffBuildOutputDirs
	return options
}

// captureRepos lists the repositories a run's worktrees could belong to: the
// task's project directories first, then every local directory registered in
// the workspace. The second list is what covers a task with no project at all
// (GONG-57), whose repository is registered on a different project.
func (d *Daemon) captureRepos(task Task) []string {
	seen := map[string]struct{}{}
	var repos []string
	add := func(path string) {
		path = strings.TrimSpace(path)
		if path == "" {
			return
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			return
		}
		if _, ok := seen[abs]; ok {
			return
		}
		seen[abs] = struct{}{}
		repos = append(repos, abs)
	}
	for _, resource := range task.ProjectResources {
		if resource.ResourceType != localDirectoryResourceType {
			continue
		}
		var ref localDirectoryRef
		if err := json.Unmarshal(resource.ResourceRef, &ref); err != nil {
			continue
		}
		add(ref.LocalPath)
	}
	for _, repo := range task.WorkspaceRepos {
		add(repo.Path)
	}
	sort.Strings(repos)
	return repos
}

// captureExcludePaths is everything a capture must never walk: this run's own
// environment root, every other task's root under the daemon's workspace root,
// and the repository cache the daemon clones into.
func (d *Daemon) captureExcludePaths(env *execenv.Environment) []string {
	paths := []string{}
	if env != nil && env.RootDir != "" {
		paths = append(paths, env.RootDir)
	}
	if d.cfg.WorkspacesRoot != "" {
		paths = append(paths, d.cfg.WorkspacesRoot)
	}
	return paths
}

// enterRunCapture registers this run as active for its workspace and returns
// how many *other* runs of the same workspace are capturing right now. Above
// zero, a worktree discovered outside the task directory cannot be attributed
// with confidence.
func (d *Daemon) enterRunCapture(workspaceID string) int {
	if workspaceID == "" {
		return 0
	}
	d.captureMu.Lock()
	defer d.captureMu.Unlock()
	if d.captureRunning == nil {
		d.captureRunning = map[string]int{}
	}
	others := d.captureRunning[workspaceID]
	d.captureRunning[workspaceID] = others + 1
	return others
}

func (d *Daemon) leaveRunCapture(workspaceID string) {
	if workspaceID == "" {
		return
	}
	d.captureMu.Lock()
	defer d.captureMu.Unlock()
	if d.captureRunning[workspaceID] <= 1 {
		delete(d.captureRunning, workspaceID)
		return
	}
	d.captureRunning[workspaceID]--
}

// startRunCapture takes the baseline for a run before the agent starts.
func (d *Daemon) startRunCapture(ctx context.Context, task Task, env *execenv.Environment) *runCapture {
	kind := "workdir"
	if env.LocalDirectory {
		kind = localDirectoryResourceType
	}
	spec := codediff.CaptureSpec{
		WorkDir:              env.WorkDir,
		WorkDirKind:          kind,
		Repos:                d.captureRepos(task),
		ExcludePaths:         d.captureExcludePaths(env),
		ConcurrentLocalTasks: d.enterRunCapture(task.WorkspaceID),
		Options:              d.captureOptions(),
	}
	return &runCapture{
		daemon:      d,
		capture:     codediff.StartCapture(ctx, spec),
		workspaceID: task.WorkspaceID,
	}
}

// finish collects the run's changes. It is called from a deferred function, so
// it must not depend on the run's context still being alive: a cancelled or
// timed-out run is exactly when the user most wants to see what was written.
func (rc *runCapture) finish() *codediff.Diff {
	if rc == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	defer func() {
		if rc.daemon != nil && rc.workspaceID != "" {
			rc.daemon.leaveRunCapture(rc.workspaceID)
		}
	}()
	return rc.capture.Finish(ctx)
}
