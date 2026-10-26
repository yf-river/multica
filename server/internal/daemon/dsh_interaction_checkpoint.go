package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/multica-ai/multica/server/internal/daemon/execenv"
)

// 只存执行归属和原生环境，不把任务令牌、模型配置或完整任务快照写入检查点。
type dshInteractionCheckpoint struct {
	TaskID         string                 `json:"task_id"`
	WorkspaceID    string                 `json:"workspace_id"`
	AgentID        string                 `json:"agent_id"`
	IssueID        string                 `json:"issue_id"`
	ChatSessionID  string                 `json:"chat_session_id"`
	Root           string                 `json:"root"`
	WorkDir        string                 `json:"work_dir"`
	SessionID      string                 `json:"session_id"`
	LocalDirectory bool                   `json:"local_directory"`
	Worktree       *execenv.LocalWorktree `json:"worktree,omitempty"`
}

func saveDSHInteractionCheckpoint(task Task, result TaskResult, env *execenv.Environment, checkpointRoot string) error {
	saved := dshInteractionCheckpoint{TaskID: task.ID, WorkspaceID: task.WorkspaceID, AgentID: task.AgentID, IssueID: task.IssueID, ChatSessionID: task.ChatSessionID,
		Root: env.RootDir, WorkDir: env.WorkDir, SessionID: result.SessionID, LocalDirectory: env.LocalDirectory, Worktree: env.LocalWorktree}
	data, err := json.Marshal(saved)
	if err != nil {
		return err
	}
	path := filepath.Join(checkpointRoot, ".dsh-interaction.json")
	if err := os.WriteFile(path+".tmp", data, 0600); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

func loadDSHInteractionCheckpoint(task Task, workspacesRoot string) (*dshInteractionCheckpoint, error) {
	if !task.InteractionContinuation {
		return nil, nil
	}
	if task.InteractionSourceTaskID == "" || task.PriorSessionID == "" || task.PriorWorkDir == "" {
		return nil, fmt.Errorf("原生交互缺少会话恢复位置")
	}
	checkpointRoot, err := execenv.ResolveRootDir(execenv.RootDirParams{WorkspacesRoot: workspacesRoot, WorkspaceID: task.WorkspaceID, WorkspaceSlug: task.WorkspaceSlug, TaskID: task.InteractionSourceTaskID, IssueIdentifier: task.IssueIdentifier})
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(checkpointRoot, ".dsh-interaction.json"))
	if err != nil {
		return nil, err
	}
	var saved dshInteractionCheckpoint
	if err = json.Unmarshal(data, &saved); err != nil {
		return nil, err
	}
	if saved.TaskID != task.InteractionSourceTaskID || saved.WorkspaceID != task.WorkspaceID || saved.AgentID != task.AgentID || saved.IssueID != task.IssueID || saved.ChatSessionID != task.ChatSessionID || saved.WorkDir != task.PriorWorkDir || saved.SessionID != task.PriorSessionID {
		return nil, fmt.Errorf("原生交互检查点归属不匹配")
	}
	root, err := filepath.EvalSymlinks(saved.Root)
	if err != nil {
		return nil, err
	}
	base, err := filepath.EvalSymlinks(workspacesRoot)
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(base, root)
	if err != nil || rel == "." || !filepath.IsLocal(rel) {
		return nil, fmt.Errorf("原生交互环境不属于本 daemon")
	}
	if root != saved.Root {
		return nil, fmt.Errorf("原生交互环境路径已变化")
	}
	workdir, err := filepath.EvalSymlinks(saved.WorkDir)
	if err != nil {
		return nil, err
	}
	if workdir != saved.WorkDir {
		return nil, fmt.Errorf("原生交互工作目录已变化")
	}
	if !saved.LocalDirectory {
		rel, err := filepath.Rel(root, workdir)
		if err != nil || !filepath.IsLocal(rel) {
			return nil, fmt.Errorf("原生交互工作目录超出执行环境")
		}
	}
	return &saved, nil
}
