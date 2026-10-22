package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/multica-ai/multica/server/internal/daemon/execenv"
)

type releasePauseKey struct{}
type releaseCheckpoint struct {
	Task           Task                   `json:"task"`
	Root           string                 `json:"root"`
	WorkDir        string                 `json:"work_dir"`
	LocalDirectory bool                   `json:"local_directory"`
	Worktree       *execenv.LocalWorktree `json:"worktree,omitempty"`
}

func releasePaused(ctx context.Context) bool {
	p, _ := ctx.Value(releasePauseKey{}).(*atomic.Bool)
	return p != nil && p.Load()
}

func (d *Daemon) preserveReleaseTask(ctx context.Context, task Task, env *execenv.Environment) error {
	if !releasePaused(ctx) {
		check, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		status, err := d.client.GetTaskStatus(check, task.ID)
		if err != nil {
			return fmt.Errorf("确认发布暂停状态: %w", err)
		}
		if status != "release_paused" {
			return nil
		}
		if p, ok := ctx.Value(releasePauseKey{}).(*atomic.Bool); ok {
			p.Store(true)
		}
	}
	checkpoint := releaseCheckpoint{task, env.RootDir, env.WorkDir, env.LocalDirectory, env.LocalWorktree}
	data, err := json.Marshal(checkpoint)
	if err != nil {
		return err
	}
	path := filepath.Join(env.RootDir, ".release-pause.json")
	if err = os.WriteFile(path+".tmp", data, 0600); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

// releaseCheckpointForTask 只读取服务端绑定的原运行和 daemon 受管根目录。
func releaseCheckpointForTask(task Task, workspacesRoot string) (*releaseCheckpoint, error) {
	if task.ReleaseResumeTaskID == "" {
		return nil, nil
	}
	root, err := filepath.EvalSymlinks(task.ReleaseResumeRoot)
	if err != nil {
		return nil, err
	}
	base, err := filepath.EvalSymlinks(workspacesRoot)
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(base, root)
	if err != nil || rel == "." || !filepath.IsLocal(rel) {
		return nil, fmt.Errorf("发布恢复目录不属于本 daemon")
	}
	data, err := os.ReadFile(filepath.Join(root, ".release-pause.json"))
	if err != nil {
		return nil, err
	}
	var saved releaseCheckpoint
	if err = json.Unmarshal(data, &saved); err != nil {
		return nil, err
	}
	if saved.Task.ID != task.ReleaseResumeTaskID || saved.Task.AgentID != task.AgentID || saved.Task.WorkspaceID != task.WorkspaceID || saved.Task.IssueID != task.IssueID || saved.Task.ChatSessionID != task.ChatSessionID || saved.Root != root || saved.WorkDir != task.PriorWorkDir {
		return nil, fmt.Errorf("发布恢复执行归属不匹配")
	}
	if _, err = os.Stat(saved.WorkDir); err != nil {
		return nil, err
	}
	return &saved, nil
}

func releaseCheckpointReady(task Task, result TaskResult) bool {
	if result.EnvRoot == "" {
		return result.WorkDir == ""
	}
	data, err := os.ReadFile(filepath.Join(result.EnvRoot, ".release-pause.json"))
	if err != nil {
		return false
	}
	var saved releaseCheckpoint
	return json.Unmarshal(data, &saved) == nil && saved.Task.ID == task.ID && saved.WorkDir == result.WorkDir
}
