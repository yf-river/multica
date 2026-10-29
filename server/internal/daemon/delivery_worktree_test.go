package daemon

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

func TestDeliveryWorktreeRealLifecycle(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	root := filepath.Join(base, "env", "source")
	if err := os.MkdirAll(repo, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	git := func(dir string, args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git: %v %s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git(repo, "init", "-b", "main")
	git(repo, "config", "user.name", "Test")
	git(repo, "config", "user.email", "test@example.test")
	if err := os.WriteFile(filepath.Join(repo, "file"), []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	git(repo, "add", ".")
	git(repo, "commit", "-m", "initial")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	wt, err := execenv.PrepareLocalWorktree(execenv.LocalWorktreeParams{LocalPath: repo, EnvRoot: root, TaskID: "source", AgentName: "test", WorkspaceID: "ws", AgentID: "agent", ConversationID: "issue"}, log)
	if err != nil {
		t.Fatal(err)
	}
	commit := git(wt.Path, "rev-parse", "HEAD")
	source := Task{ID: "source", WorkspaceID: "ws", AgentID: "agent", IssueID: "issue"}
	env := &execenv.Environment{RootDir: root, WorkDir: wt.WorkDir, LocalWorktree: wt}
	if _, err = wt.FinalizeRetained(log, wt.Branch, commit); err != nil {
		t.Fatal(err)
	}
	if err = saveDeliveryWorktree(source, env); err != nil {
		t.Fatal(err)
	}
	next := Task{ID: "next", WorkspaceID: "ws", AgentID: "agent", IssueID: "issue", PriorWorkDir: wt.WorkDir, DeliveryContext: &DeliveryContextData{Workflow: "project", Phase: "testing", OwnsCheckout: true}}
	saved, err := deliveryWorktreeForTask(next, filepath.Join(base, "env"))
	if err != nil || saved == nil {
		t.Fatalf("复用失败: %v", err)
	}
	statuses := map[string]int{}
	retained := map[string]bool{"source": true}
	calls := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.Split(r.URL.Path, "/")[4]
		calls = append(calls, id)
		if statuses[id] != 0 {
			w.WriteHeader(statuses[id])
			return
		}
		json.NewEncoder(w).Encode(deliveryWorktreeRetention{Retain: retained[id], SourceBranch: wt.Branch, SourceCommit: commit})
	}))
	defer srv.Close()
	client := NewClient(srv.URL)
	resolve := func(original *Task) (deliveryWorktreeRetention, Task, error) {
		return resolveDeliveryWorktreeRetention(context.Background(), client, next, original)
	}
	r, marker, err := resolve(&saved.Task)
	if err != nil || !r.Retain || marker.ID != "source" {
		t.Fatalf("原身份未保留: %+v %+v %v", r, marker, err)
	}
	if _, err = saved.Worktree.FinalizeRetained(log, r.SourceBranch, r.SourceCommit); err != nil {
		t.Fatal(err)
	}
	if err = saveDeliveryWorktree(marker, env); err != nil {
		t.Fatal(err)
	}
	saved, err = deliveryWorktreeForTask(next, filepath.Join(base, "env"))
	if err != nil || saved.Task.ID != "source" {
		t.Fatalf("再次接续失败: %+v %v", saved, err)
	}
	retained["next"] = true
	calls = nil
	r, marker, err = resolve(&saved.Task)
	if err != nil || !r.Retain || marker.ID != "next" || len(calls) != 1 {
		t.Fatalf("当前身份未优先: %v %v %v", marker, calls, err)
	}
	retained["next"] = false
	for _, code := range []int{403, 500} {
		statuses["source"] = code
		_, _, err = resolve(&saved.Task)
		if err == nil {
			t.Fatalf("状态 %d 未报错", code)
		}
		if _, err = os.Stat(wt.Path); err != nil {
			t.Fatal(err)
		}
		delete(statuses, "source")
		statuses["next"] = code
		_, _, err = resolve(&saved.Task)
		if err == nil {
			t.Fatalf("当前身份状态 %d 未报错", code)
		}
		delete(statuses, "next")
	}
	calls = nil
	_, _, err = resolve(nil)
	if err != nil || len(calls) != 1 {
		t.Fatalf("独立目录借用原身份: %v %v", calls, err)
	}
	changed := next
	changed.AgentID = "other"
	if _, err = deliveryWorktreeForTask(changed, filepath.Join(base, "env")); err == nil {
		t.Fatal("不同归属被接受")
	}
	if _, err = saved.Worktree.FinalizeRetained(log, wt.Branch, strings.Repeat("0", 40)); err == nil {
		t.Fatal("变更提交被接受")
	}
	if _, err = os.Stat(wt.Path); err != nil {
		t.Fatal(err)
	}
	retained["source"] = false
	r, _, err = resolve(&saved.Task)
	if err != nil || r.Retain {
		t.Fatalf("发布后仍保留: %v", err)
	}
	if _, err = saved.Worktree.Finalize(log); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(wt.Path); !os.IsNotExist(err) {
		t.Fatalf("目录未删除: %v", err)
	}
}
