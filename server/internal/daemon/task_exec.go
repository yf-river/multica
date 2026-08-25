package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/daemon/execenv"
	"github.com/multica-ai/multica/server/internal/daemon/repocache"
	"github.com/multica-ai/multica/server/internal/executionpolicy"
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

	// Keep the environment protected until result reporting and GC metadata
	// finish. The guard is reference-counted with runTask's inner guard.
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

	// Guard against a zero interval before constructing the cancellation ticker.
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
	result, err := d.runner(runCtx, task, provider, slot, taskLog)

	// Usage is billable even when execution is cancelled or fails.
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
		// runTask returned without session state; classify the transport error.
		reason := taskfailure.Classify(err.Error())
		if failErr := d.client.FailTask(ctx, task.ID, err.Error(), "", "", reason.String()); failErr != nil {
			taskLog.Error("fail task callback failed", "error", failErr)
		}
		return
	}

	_ = d.client.ReportProgress(ctx, task.ID, "Finishing task", 2, 2)

	// Recheck terminal state after the runner to avoid reporting a stale result.
	if status, err := d.client.GetTaskStatus(ctx, task.ID); shouldInterruptAgent(status, err) {
		taskLog.Info("task cancelled during execution, discarding result", "status", status, "error", err)
		return
	}

	artifactCommentOptions := d.persistFinalOutputArtifactIfNeeded(task, result, result.WorkDir, result.ArtifactDir, taskLog)
	d.collectAndPostTaskArtifacts(ctx, task, result.WorkDir, result.ArtifactDir, artifactCollectionCutoff, taskLog, artifactCommentOptions)

	d.reportTaskResult(ctx, task.ID, result, taskLog)

	// Metadata is written last; a mid-task crash remains an orphan for TTL GC.
	if result.EnvRoot != "" {
		if meta, ok := gcMetaForTask(task); ok {
			if err := execenv.WriteGCMeta(result.EnvRoot, meta); err != nil {
				taskLog.Warn("write gc meta failed (non-fatal)", "error", err)
			}
		}
	}
}

// reportTaskResult fails closed: only completed is success. Every failure path
// preserves session/workdir so a recoverable conversation can resume.
func (d *Daemon) reportTaskResult(ctx context.Context, taskID string, result TaskResult, taskLog *slog.Logger) {
	switch result.Status {
	case "completed":
		taskLog.Info("task completed", "status", result.Status)
		err := d.client.CompleteTask(ctx, taskID, result.Comment, result.BranchName, result.SessionID, result.WorkDir)
		if err == nil {
			return
		}
		// A transient callback failure leaves the successful result recoverable;
		// only a permanent server rejection becomes a failed task.
		if isTransientError(err) {
			taskLog.Error("complete task failed after retries; leaving task in running rather than falling back to fail", "error", err)
			return
		}
		taskLog.Error("complete task rejected by server, falling back to fail", "error", err)
		fallbackErrMsg := fmt.Sprintf("complete task failed: %s", err.Error())
		if failErr := d.client.FailTask(ctx, taskID, fallbackErrMsg, result.SessionID, result.WorkDir, taskfailure.Classify(fallbackErrMsg).String()); failErr != nil {
			taskLog.Error("fail task fallback also failed", "error", failErr)
		}
	default:
		failureReason := result.FailureReason
		if failureReason == "" {
			if result.Status == "cancelled" {
				// Cancellation stays outside the failure taxonomy for UI semantics.
				failureReason = "cancelled"
			} else {
				failureReason = taskfailure.Classify(result.Comment).String()
			}
		}
		taskLog.Info("task did not complete, reporting failure", "status", result.Status, "failure_reason", failureReason)
		if err := d.client.FailTask(ctx, taskID, result.Comment, result.SessionID, result.WorkDir, failureReason); err != nil {
			taskLog.Error("report failed task failed", "error", err)
		}
	}
}

// gcMetaForTask records the longest-lived parent, with Chat and Autopilot
// taking precedence over Issue. Unclassified roots fall back to TTL cleanup.
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
		// Quick Create has no Issue ID until after this callback.
		meta.Kind = execenv.GCKindQuickCreate
		meta.TaskID = task.ID
	default:
		return execenv.GCMeta{}, false
	}
	return meta, true
}

func needsInlineSystemPrompt(provider string, policy executionpolicy.Policy) bool {
	if provider == "kiro" || provider == "kimi" {
		return true
	}
	return supportsClaudeFamilyToolEnvelope(provider) && policy.IsCoordinatorWithoutRepo()
}

// gateResumeToReusedWorkdir drops a cwd-scoped provider session unless its
// original workdir was actually reused.
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

func (d *Daemon) prepareIssueExecutionSpace(task Task, agentName string) (*preparedIssueExecutionSpace, error) {
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
