package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/multica-ai/multica/server/internal/daemon/execenv"
)

func TestDeliveryWorktreeRequiresSameIssueAgentAndWorkspace(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "workspace", "task")
	work := filepath.Join(root, "worktree")
	if err := os.MkdirAll(work, 0700); err != nil {
		t.Fatal(err)
	}
	task := Task{ID: "task-1", AgentID: "agent-1", IssueID: "issue-1", WorkspaceID: "ws-1"}
	env := &execenv.Environment{RootDir: root, WorkDir: work, LocalWorktree: &execenv.LocalWorktree{Path: work}}
	if err := saveDeliveryWorktree(task, env); err != nil {
		t.Fatal(err)
	}
	next := Task{AgentID: task.AgentID, IssueID: task.IssueID, WorkspaceID: task.WorkspaceID, PriorWorkDir: work, DeliveryContext: &DeliveryContextData{Workflow: "project", Phase: "testing", OwnsCheckout: true}}
	if got, err := deliveryWorktreeForTask(next, base); err != nil || got == nil || got.WorkDir != work {
		t.Fatalf("未恢复原目录: %+v %v", got, err)
	}
	for _, mutate := range []func(*Task){func(v *Task) { v.AgentID = "other" }, func(v *Task) { v.IssueID = "other" }, func(v *Task) { v.WorkspaceID = "other" }} {
		changed := next
		mutate(&changed)
		if _, err := deliveryWorktreeForTask(changed, base); err == nil {
			t.Fatal("不属于该任务的目录被接受")
		}
	}
	if err := os.Remove(filepath.Join(root, deliveryWorktreeMarker)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(work, "missing"), filepath.Join(root, deliveryWorktreeMarker)); err != nil {
		t.Fatal(err)
	}
	if _, err := deliveryWorktreeForTask(next, base); err == nil {
		t.Fatal("不能使用符号链接作为目录归属记录")
	}
}
