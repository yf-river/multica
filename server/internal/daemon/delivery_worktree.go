package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

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
	if saved.Task.ID == "" || saved.Task.WorkspaceID != task.WorkspaceID || saved.Task.AgentID != task.AgentID || saved.Task.IssueID != task.IssueID || saved.Root != rootPath || saved.WorkDir != workdir || saved.Worktree == nil || saved.Worktree.Path != workdir {
		return nil, fmt.Errorf("验收目录归属与任务不一致")
	}
	return &saved, nil
}

// 仅对已核验并实际复用的工作树查询原始身份，避免独立目录继承别人的租约。
// 验收执行结束不代表源码交付结束，目录归属仍由原始开发执行确认。
func resolveDeliveryWorktreeRetention(ctx context.Context, client *Client, task Task, source *Task) (deliveryWorktreeRetention, Task, error) {
	if task.IssueID == "" {
		return deliveryWorktreeRetention{}, task, nil
	}
	query := func(id string) (deliveryWorktreeRetention, error) {
		check, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		retention, err := client.getDeliveryWorktreeRetention(check, id)
		var requestErr *requestError
		if errors.As(err, &requestErr) && requestErr.StatusCode == http.StatusNotFound {
			return deliveryWorktreeRetention{}, nil
		}
		return retention, err
	}
	retention, err := query(task.ID)
	if err != nil || retention.Retain || source == nil || source.ID == "" || source.ID == task.ID {
		return retention, task, err
	}
	retention, err = query(source.ID)
	if retention.Retain && err == nil {
		return retention, *source, nil
	}
	return retention, task, err
}
