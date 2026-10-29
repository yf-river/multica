package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/multica-ai/multica/server/internal/daemon/execenv"
)

const deliveryWorktreeMarker = ".delivery-worktree.json"

func saveDeliveryWorktree(task Task, env *execenv.Environment) error {
	checkpoint := releaseCheckpoint{Task: Task{ID: task.ID, AgentID: task.AgentID, IssueID: task.IssueID, WorkspaceID: task.WorkspaceID}, Root: env.RootDir, WorkDir: env.WorkDir, LocalDirectory: env.LocalDirectory, Worktree: env.LocalWorktree}
	data, err := json.Marshal(checkpoint)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(env.RootDir)
	if err != nil {
		return err
	}
	defer root.Close()
	if err = root.WriteFile(deliveryWorktreeMarker+".tmp", data, 0600); err != nil {
		return err
	}
	return root.Rename(deliveryWorktreeMarker+".tmp", deliveryWorktreeMarker)
}

func deliveryWorktreeForTask(task Task, workspacesRoot string) (*releaseCheckpoint, error) {
	if task.DeliveryContext == nil || task.DeliveryContext.Workflow != "project" || (task.DeliveryContext.Phase != "testing" && task.DeliveryContext.Phase != "approved") || !task.DeliveryContext.OwnsCheckout || task.ReviewCommit != "" {
		return nil, nil
	}
	if task.PriorWorkDir == "" {
		return nil, fmt.Errorf("验收任务缺少原开发目录")
	}
	base, err := filepath.EvalSymlinks(workspacesRoot)
	if err != nil {
		return nil, err
	}
	workdir, err := filepath.EvalSymlinks(task.PriorWorkDir)
	if err != nil {
		return nil, err
	}
	rootPath := filepath.Dir(workdir)
	rel, err := filepath.Rel(base, rootPath)
	if err != nil || rel == "." || !filepath.IsLocal(rel) || filepath.Base(workdir) != "worktree" {
		return nil, fmt.Errorf("验收目录不属于本 daemon")
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	info, err := root.Lstat(deliveryWorktreeMarker)
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("验收目录缺少可信归属记录")
	}
	data, err := root.ReadFile(deliveryWorktreeMarker)
	if err != nil {
		return nil, err
	}
	var saved releaseCheckpoint
	if err = json.Unmarshal(data, &saved); err != nil {
		return nil, err
	}
	if saved.Task.WorkspaceID != task.WorkspaceID || saved.Task.AgentID != task.AgentID || saved.Task.IssueID != task.IssueID || saved.Root != rootPath || saved.WorkDir != workdir || saved.Worktree == nil || saved.Worktree.Path != workdir {
		return nil, fmt.Errorf("验收目录归属与任务不一致")
	}
	return &saved, nil
}
