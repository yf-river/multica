package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/multica-ai/multica/server/internal/daemon/execenv"
	"github.com/multica-ai/multica/server/internal/daemon/repocache"
	"github.com/multica-ai/multica/server/pkg/taskfailure"
)

func (d *Daemon) handleTask(ctx context.Context, task Task, slot int) {
	d.mu.Lock()
	rt := d.runtimeIndex[task.RuntimeID]
	d.mu.Unlock()
	provider := rt.Provider

	// Task-scoped logger with short ID for readable concurrent logs.
	taskLog := d.logger.With("task", shortID(task.ID))
	agentName := "agent"
	if task.Agent != nil {
		agentName = task.Agent.Name
	}
	if task.ChatSessionID != "" {
		taskLog.Info("picked chat task", "chat_session", shortID(task.ChatSessionID), "agent", agentName, "provider", provider)
	} else {
		taskLog.Info("picked task", "issue", task.IssueID, "agent", agentName, "provider", provider)
	}
	taskLog.Debug("task context",
		"workspace_id", task.WorkspaceID,
		"runtime_id", task.RuntimeID,
		"agent_id", task.AgentID,
		"repos", len(task.Repos),
		"project_id", task.ProjectID,
		"autopilot_run_id", task.AutopilotRunID,
		"trigger_comment_id", task.TriggerCommentID,
		"resume_session", task.PriorSessionID != "",
		"reuse_workdir", task.PriorWorkDir != "",
	)

	// Hold a process-wide active-root guard for the rest of this task so
	// the GC loop never sees a window where the env root has neither the
	// in-process guard nor .gc_meta.json (issue #3999 race B). runTask
	// installs its own ref-counted mark/unmark internally; without this
	// outer guard the inner unmark fires when runTask returns, leaving
	// the directory protected only by the 72h orphan TTL through
	// reportTaskResult and execenv.WriteGCMeta below. markActiveEnvRoot
	// is reference-counted, so the duplicate marks runTask installs are
	// correctly nested within these.
	predictedEnvRoot := execenv.PredictRootDir(d.cfg.WorkspacesRoot, task.WorkspaceID, task.ID)
	if predictedEnvRoot != "" {
		d.markActiveEnvRoot(predictedEnvRoot)
		defer d.unmarkActiveEnvRoot(predictedEnvRoot)
	}
	if task.PriorWorkDir != "" {
		if priorRoot := filepath.Dir(task.PriorWorkDir); priorRoot != "" && priorRoot != predictedEnvRoot {
			d.markActiveEnvRoot(priorRoot)
			defer d.unmarkActiveEnvRoot(priorRoot)
		}
	}

	// Create a cancellable context so we can interrupt the running agent
	// when the server signals the task should stop — either the task reached
	// a terminal state (completed/failed/cancelled) or the task row is
	// deleted (404).
	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()

	// Poll interval is d.cancelPollInterval (5s in production, reduced in tests
	// via direct field override). Guard against zero so a misconfigured daemon
	// doesn't panic time.NewTicker.
	pollInterval := d.cancelPollInterval
	if pollInterval == 0 {
		pollInterval = 5 * time.Second
	}
	cancelledByPoll := d.watchTaskCancellation(runCtx, task.ID, pollInterval, taskLog)
	go func() {
		select {
		case <-cancelledByPoll:
			runCancel()
		case <-runCtx.Done():
		}
	}()

	artifactCollectionCutoff := time.Now().Add(-1 * time.Second)
	result, err := d.runner.run(runCtx, task, provider, slot, taskLog)

	// Report usage before any early return — the agent accumulates tokens
	// whether the task completes, errors, or is cancelled mid-run by the poll
	// goroutine. Both claude.go and codex.go populate result.Usage even when
	// runCtx is cancelled, so dropping this on the cancelled path silently
	// under-reports billing.
	if len(result.Usage) > 0 {
		if usageErr := d.client.ReportTaskUsage(ctx, task.ID, result.Usage); usageErr != nil {
			taskLog.Warn("report task usage failed", "error", usageErr)
		}
	}

	// Check if we were cancelled by the polling goroutine.
	select {
	case <-cancelledByPoll:
		taskLog.Info("task cancelled during execution, discarding result")
		return
	default:
	}

	if err != nil {
		if errors.Is(err, errTaskStartConflict) {
			taskLog.Info("task skipped before start", "error", err)
			return
		}
		taskLog.Error("task failed", "error", err)
		// runTask returned without a TaskResult, so we don't have a SessionID
		// to forward — best we can do is record the failure.
		// MUL-2946: route the bare error string through the canonical
		// classifier so the failure_reason column reflects the actual
		// shape of the failure (provider 5xx, network, process crash,
		// …) rather than the coarse legacy "agent_error" bucket.
		reason := taskfailure.Classify(err.Error())
		if failErr := d.client.FailTask(ctx, task.ID, err.Error(), "", "", reason.String()); failErr != nil {
			taskLog.Error("fail task callback failed", "error", failErr)
		}
		return
	}

	_ = d.client.ReportProgress(ctx, task.ID, "Finishing task", 2, 2)

	// Final pre-completion check: if the server already moved the task to a
	// terminal state (completed/failed/cancelled) or deleted the row
	// outright, skip reporting — the complete/fail callbacks would fail
	// anyway. Reuse shouldInterruptAgent so this guard honors the same
	// signals as the in-flight watcher.
	if status, err := d.client.GetTaskStatus(ctx, task.ID); shouldInterruptAgent(status, err) {
		taskLog.Info("task cancelled during execution, discarding result", "status", status, "error", err)
		return
	}

	artifactCommentOptions := d.persistFinalOutputArtifactIfNeeded(task, result, result.WorkDir, result.ArtifactDir, taskLog)
	d.collectAndPostTaskArtifacts(ctx, task, result.WorkDir, result.ArtifactDir, artifactCollectionCutoff, taskLog, artifactCommentOptions)

	d.reportTaskResult(ctx, task.ID, result, taskLog)

	// Write GC metadata after the task finishes so the periodic GC loop
	// can look up the parent record (issue / chat session / autopilot run /
	// task itself for quick-create) later. Written last so that a mid-task
	// crash leaves the directory as an orphan (cleaned up by GCOrphanTTL).
	if result.EnvRoot != "" {
		if meta, ok := gcMetaForTask(task); ok {
			if err := execenv.WriteGCMeta(result.EnvRoot, meta, taskLog); err != nil {
				taskLog.Warn("write gc meta failed (non-fatal)", "error", err)
			}
		}
	}
}

// acquireLocalDirectoryLockIfNeeded is a legacy compatibility helper. Normal
// issue tasks now run in issue-scoped managed worktrees and do not call this
// path.
//
// The helper inspects the task's project resources for
// a local_directory pinned to this daemon, validates the path, and takes the
// path mutex. Returns a release callback (nil when no local_directory
// resource applies) and abort=true when the caller must bail without
// starting the task (the helper has already reported the failure to the
// server).
//
// The helper covers four distinct failure modes:
//
//  1. The project_resource JSON is structurally broken — fail the task fast.
//  2. The path fails validation (missing, not a directory, no R/W, system
//     blacklist) — fail the task fast with a user-facing reason.
//  3. The mutex is held by another task — call MarkTaskWaitingLocalDirectory
//     so the row flips to waiting_local_directory while we block on the
//     lock, then return the release callback once we win.
//  4. The blocking wait is cancelled (daemon shutdown, server-side cancel)
//     — fail the task with the ctx error.
func (d *Daemon) acquireLocalDirectoryLockIfNeeded(ctx context.Context, task Task, taskLog *slog.Logger) (release func(), abort bool) {
	if len(task.ProjectResources) == 0 || d.cfg.DaemonID == "" {
		return nil, false
	}
	assignment, err := findLocalDirectoryAssignment(task.ProjectResources, d.cfg.DaemonID)
	if err != nil {
		taskLog.Error("local_directory: resolve resource failed", "error", err)
		if failErr := d.client.FailTask(ctx, task.ID, err.Error(), "", "", "local_directory_error"); failErr != nil {
			taskLog.Error("fail task after local_directory resolve error", "error", failErr)
		}
		return nil, true
	}
	if assignment == nil {
		return nil, false
	}
	taskLog = taskLog.With("local_directory", assignment.AbsPath)
	if err := validateLocalPath(assignment.AbsPath); err != nil {
		taskLog.Error("local_directory: path validation failed", "error", err)
		if failErr := d.client.FailTask(ctx, task.ID, err.Error(), "", "", "local_directory_error"); failErr != nil {
			taskLog.Error("fail task after local_directory validation error", "error", failErr)
		}
		return nil, true
	}

	// While the lock is contended the daemon would otherwise sit blocked on
	// the path mutex with no signal back from the server — the main
	// per-task watcher only starts after the lock is acquired. If the user
	// cancels the issue or it gets reassigned during the wait, we need to
	// notice promptly so the daemon slot isn't pinned by a phantom waiter.
	// We spin up the cancellation watcher lazily inside onWait so the
	// no-contention fast path still costs nothing.
	waitCtx, waitCancel := context.WithCancel(ctx)
	defer waitCancel()
	pollInterval := d.cancelPollInterval
	if pollInterval == 0 {
		pollInterval = 5 * time.Second
	}
	var (
		watcherOnce     sync.Once
		cancelledByPoll <-chan struct{}
	)

	onWait := func(holder string) {
		reason := fmt.Sprintf("local_directory %s", assignment.AbsPath)
		if holder != "" {
			reason = fmt.Sprintf("%s (held by task %s)", reason, shortID(holder))
		}
		taskLog.Info("local_directory: waiting on path mutex", "holder", shortID(holder))
		if waitErr := d.client.MarkTaskWaitingLocalDirectory(ctx, task.ID, reason); waitErr != nil {
			// Non-fatal: even if the server-side flag fails to update,
			// we still want to block on the lock and proceed when free.
			// The UI just won't see the explicit "waiting" badge.
			taskLog.Warn("local_directory: mark waiting status failed", "error", waitErr)
		}
		// Start polling once we actually park. shouldInterruptAgent inside
		// watchTaskCancellation already handles both server-side terminal
		// states (completed/failed/cancelled) and the row-deleted
		// reassignment case (404), which is the full set of "this task
		// shouldn't run anymore" signals we need to react to during the wait.
		watcherOnce.Do(func() {
			cancelledByPoll = d.watchTaskCancellation(waitCtx, task.ID, pollInterval, taskLog)
			go func() {
				select {
				case <-cancelledByPoll:
					waitCancel()
				case <-waitCtx.Done():
				}
			}()
		})
	}
	release, err = d.localPathLocks.Acquire(waitCtx, assignment.RealPath, task.ID, onWait)
	if err != nil {
		// If the wait was cut short because the server finalized the task
		// (terminal state) or deleted the row, the row is already in a
		// terminal state — return silently the same way the run-phase poller
		// does at lines ~2104. Issuing FailTask here would be a no-op at best
		// and a confusing redundant log line at worst.
		if cancelledByPoll != nil {
			select {
			case <-cancelledByPoll:
				taskLog.Info("local_directory: wait aborted by server-side terminal state")
				return nil, true
			default:
			}
		}
		taskLog.Error("local_directory: lock acquire failed", "error", err)
		failureReason := "local_directory_error"
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			failureReason = "cancelled"
		}
		if failErr := d.client.FailTask(ctx, task.ID, fmt.Sprintf("local_directory wait cancelled: %s", err.Error()), "", "", failureReason); failErr != nil {
			taskLog.Error("fail task after local_directory lock cancel", "error", failErr)
		}
		return nil, true
	}
	taskLog.Info("local_directory: lock acquired")
	return release, false
}

// reportTaskResult writes the final task disposition back to the server.
//
// Fail closed: only an explicit "completed" status is reported as success.
// Anything else — "blocked", "cancelled", or any future status we forget to
// enumerate — must go through FailTask, so a run that never produced a real
// result can never be displayed as "Completed" in the UI (e.g. provider 429 /
// out-of-credit / runtime crash). Forward SessionID/WorkDir on every path:
// the agent may have built a real session before getting stuck, and we want
// the next chat turn to resume there rather than start over and "forget"
// the conversation.
func (d *Daemon) reportTaskResult(ctx context.Context, taskID string, result TaskResult, taskLog *slog.Logger) {
	switch result.Status {
	case "completed":
		taskLog.Info("task completed", "status", result.Status)
		err := d.client.CompleteTask(ctx, taskID, result.Comment, result.BranchName, result.SessionID, result.WorkDir)
		if err == nil {
			return
		}
		// CompleteTask retries transient errors internally. A transient
		// error reaching us here means the schedule was exhausted while
		// the upstream was still 5xx / unreachable. Converting that into
		// a fail would lose the agent's actual result and surface a
		// misleading red badge in the UI — leave the task in running
		// instead so a future fix (server-side stuck-task reaper, or a
		// daemon-side persistent pending queue) can recover it. Only
		// permanent server-side rejections (4xx other than 408/429)
		// warrant the legacy fallback, because at that point the server
		// has already refused this task and the only useful UI signal
		// left is a concrete failure.
		if isTransientError(err) {
			taskLog.Error("complete task failed after retries; leaving task in running rather than falling back to fail", "error", err)
			return
		}
		taskLog.Error("complete task rejected by server, falling back to fail", "error", err)
		// MUL-2946: this fallback fires when a server-side complete
		// callback was permanently rejected (4xx other than 408/429)
		// — the agent itself succeeded, so the err here describes the
		// server response rather than an agent failure. The classifier
		// is unlikely to match anything in the server's error text and
		// will land at ReasonAgentUnknown ("agent_error.unknown"),
		// which is the canonical replacement for the legacy
		// "agent_error" coarse bucket.
		fallbackErrMsg := fmt.Sprintf("complete task failed: %s", err.Error())
		if failErr := d.client.FailTask(ctx, taskID, fallbackErrMsg, result.SessionID, result.WorkDir, taskfailure.Classify(fallbackErrMsg).String()); failErr != nil {
			taskLog.Error("fail task fallback also failed", "error", failErr)
		}
	default:
		failureReason := result.FailureReason
		if failureReason == "" {
			if result.Status == "cancelled" {
				// "cancelled" is a deliberate non-failure terminal
				// state masquerading as a failure_reason — preserved
				// outside the canonical taxonomy so the UI can render
				// it differently from a real failure.
				failureReason = "cancelled"
			} else {
				// MUL-2946: classify the agent's comment text so the
				// failure_reason lands in the refined taxonomy
				// (provider_auth_or_access, context_overflow,
				// process_failure, …) instead of the legacy coarse
				// "agent_error" bucket. Empty comment lands in
				// ReasonAgentUnknown.
				failureReason = taskfailure.Classify(result.Comment).String()
			}
		}
		taskLog.Info("task did not complete, reporting failure", "status", result.Status, "failure_reason", failureReason)
		if err := d.client.FailTask(ctx, taskID, result.Comment, result.SessionID, result.WorkDir, failureReason); err != nil {
			taskLog.Error("report failed task failed", "error", err)
		}
	}
}

// gcMetaForTask classifies a finished task and produces a GCMeta of the right
// kind. The discriminator order matters: a task carrying both an issue_id
// and a chat_session_id (theoretical, not produced today) should be treated
// as a chat task because the chat session is the longer-lived parent record.
//
// Returns ok=false when the task has no recognizable parent (e.g. an
// internal task with no IDs at all). The caller skips writing a meta file
// in that case so the directory falls back to mtime-based orphan cleanup.
func gcMetaForTask(task Task) (execenv.GCMeta, bool) {
	meta := execenv.GCMeta{WorkspaceID: task.WorkspaceID}
	switch {
	case task.ChatSessionID != "":
		meta.Kind = execenv.GCKindChat
		meta.ChatSessionID = task.ChatSessionID
	case task.AutopilotRunID != "":
		meta.Kind = execenv.GCKindAutopilotRun
		meta.AutopilotRunID = task.AutopilotRunID
	case task.IssueID != "":
		meta.Kind = execenv.GCKindIssue
		meta.IssueID = task.IssueID
	case task.QuickCreatePrompt != "":
		// Quick-create tasks reach WriteGCMeta before the server runs
		// LinkTaskToIssue, so IssueID is always empty here. Persist the
		// task ID instead and let the GC loop ask the server for terminal
		// state via the task gc-check endpoint.
		meta.Kind = execenv.GCKindQuickCreate
		meta.TaskID = task.ID
	default:
		return execenv.GCMeta{}, false
	}
	return meta, true
}

func providerNeedsInlineSystemPrompt(provider string) bool {
	switch provider {
	case "openclaw", "kiro", "kimi":
		return true
	default:
		return false
	}
}

func coordinatorNeedsInlineSystemPrompt(provider string, policy TaskExecutionPolicy) bool {
	return supportsClaudeFamilyToolEnvelope(provider) && isCoordinatorWithoutRepoAccess(policy)
}

// gateResumeToReusedWorkdir clears the task's prior session unless the task
// runs in the exact workdir the session was recorded against, and reports
// whether that workdir was reused. CLI backends key their session stores to
// the cwd (Claude Code looks sessions up under ~/.claude/projects/<encoded-cwd>/),
// so a session id from a different workdir can never resolve: the CLI exits
// within a second and the run fails before doing any work — permanently,
// because the failed run records no session and the next claim serves the
// same stale pointer again. This fires whenever the prior workdir no longer
// exists (GC'd after the issue went done, daemon reinstall, manual cleanup)
// and execenv.Reuse fell back to a fresh Prepare (GitHub #3854).
func gateResumeToReusedWorkdir(task *Task, taskCtx *execenv.TaskContextForEnv, envWorkDir string, taskLog *slog.Logger) bool {
	reused := task.PriorWorkDir != "" && envWorkDir == task.PriorWorkDir
	if !reused && task.PriorSessionID != "" {
		taskLog.Info("dropping prior session: workdir not reused, per-cwd session cannot resolve",
			"session_id", task.PriorSessionID,
			"prior_workdir", task.PriorWorkDir,
			"workdir", envWorkDir,
		)
		task.PriorSessionID = ""
		taskCtx.PriorSessionResumed = false
	}
	return reused
}

type preparedIssueExecutionSpace struct {
	RootDir        string
	ReposDir       string
	PrimaryRepoDir string
	ArtifactDir    string
	BranchName     string
}

func managedWorkDirForIssueSpace(issueSpace *preparedIssueExecutionSpace) string {
	if issueSpace == nil {
		return ""
	}
	return issueSpace.PrimaryRepoDir
}

func issueExecutionSpaceEnabled(task Task) bool {
	return task.IssueExecutionSpace != nil &&
		task.IssueExecutionSpace.Enabled &&
		strings.TrimSpace(task.IssueExecutionSpace.IssueID) != "" &&
		strings.TrimSpace(task.IssueExecutionSpace.PrimaryRepoURL) != ""
}

func (d *Daemon) prepareIssueExecutionSpace(ctx context.Context, task Task, agentName string) (*preparedIssueExecutionSpace, error) {
	if !issueExecutionSpaceEnabled(task) {
		return nil, nil
	}
	if d.repoCache == nil {
		return nil, fmt.Errorf("issue execution space requires repo cache")
	}
	space := task.IssueExecutionSpace
	issueID := strings.TrimSpace(space.IssueID)
	repoURL := strings.TrimSpace(space.PrimaryRepoURL)
	rootDir := filepath.Join(d.cfg.WorkspacesRoot, task.WorkspaceID, "issues", shortID(issueID))
	reposDir := filepath.Join(rootDir, "repos")
	artifactDir := filepath.Join(rootDir, "artifacts", "multica")
	if err := os.MkdirAll(reposDir, 0o755); err != nil {
		return nil, fmt.Errorf("create issue repos dir: %w", err)
	}
	if err := os.MkdirAll(artifactDir, 0o755); err != nil {
		return nil, fmt.Errorf("create issue artifact dir: %w", err)
	}

	if err := d.repoCache.Sync(task.WorkspaceID, []repocache.RepoInfo{{URL: repoURL}}); err != nil {
		return nil, fmt.Errorf("sync issue repo: %w", err)
	}

	branchName := fmt.Sprintf("agent/issue/%s", shortID(issueID))
	result, err := d.repoCache.CreateWorktree(repocache.WorktreeParams{
		WorkspaceID:         task.WorkspaceID,
		RepoURL:             repoURL,
		WorkDir:             reposDir,
		Ref:                 space.Ref,
		AgentName:           agentName,
		TaskID:              task.ID,
		BranchNameOverride:  branchName,
		PreserveExisting:    true,
		CoAuthoredByEnabled: d.workspaceCoAuthoredByEnabled(task.WorkspaceID),
	})
	if err != nil {
		return nil, fmt.Errorf("checkout issue worktree: %w", err)
	}
	if err := linkIssueArtifactDir(result.Path, artifactDir); err != nil {
		d.logger.Warn("issue execution space: link artifact dir failed", "error", err, "workdir", result.Path, "artifact_dir", artifactDir)
	}
	return &preparedIssueExecutionSpace{
		RootDir:        rootDir,
		ReposDir:       reposDir,
		PrimaryRepoDir: result.Path,
		ArtifactDir:    artifactDir,
		BranchName:     result.BranchName,
	}, nil
}

func linkIssueArtifactDir(repoDir, artifactDir string) error {
	parent := filepath.Join(repoDir, "artifacts")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	linkPath := filepath.Join(parent, "multica")
	if st, err := os.Lstat(linkPath); err == nil {
		if st.Mode()&os.ModeSymlink != 0 {
			target, readErr := os.Readlink(linkPath)
			if readErr == nil && target == artifactDir {
				return nil
			}
			_ = os.Remove(linkPath)
		} else {
			return nil
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	return os.Symlink(artifactDir, linkPath)
}

