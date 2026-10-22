package daemon

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/multica-ai/multica/server/internal/daemon/execenv"
)

func TestReleasePausePreservesDirtyWorktreeAndRejectsWrongOwner(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	root := filepath.Join(base, "environments", "original")
	if err := os.MkdirAll(repo, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		raw, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git: %v %s", err, raw)
		}
		return string(raw)
	}
	git("init", "-b", "main")
	git("config", "user.name", "Test")
	git("config", "user.email", "test@example.test")
	os.WriteFile(filepath.Join(repo, "notes.txt"), []byte("original\n"), 0600)
	git("add", ".")
	git("commit", "-m", "initial")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	wt, err := execenv.PrepareLocalWorktree(execenv.LocalWorktreeParams{LocalPath: repo, EnvRoot: root, TaskID: "original", AgentName: "test", WorkspaceID: "workspace", AgentID: "agent", ConversationID: "issue"}, log)
	if err != nil {
		t.Fatal(err)
	}
	defer exec.Command("git", "-C", repo, "worktree", "remove", "--force", wt.Path).Run()
	dirty := filepath.Join(wt.WorkDir, "notes.txt")
	os.WriteFile(dirty, []byte("unfinished\n"), 0600)
	head := git("rev-parse", "HEAD")
	original := Task{ID: "original", WorkspaceID: "workspace", AgentID: "agent", IssueID: "issue"}
	flag := &atomic.Bool{}
	flag.Store(true)
	ctx := context.WithValue(context.Background(), releasePauseKey{}, flag)
	d := &Daemon{}
	env := &execenv.Environment{RootDir: root, WorkDir: wt.WorkDir, LocalWorktree: wt}
	if err = d.preserveReleaseTask(ctx, original, env); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(dirty)
	if err != nil || string(data) != "unfinished\n" {
		t.Fatalf("未提交代码丢失: %v %s", err, data)
	}
	if git("rev-parse", "HEAD") != head {
		t.Fatal("暂停产生了自动提交")
	}
	resumed := original
	resumed.ID = "continuation"
	resumed.ReleaseResumeTaskID = "original"
	resumed.ReleaseResumeRoot = root
	resumed.PriorWorkDir = wt.WorkDir
	saved, err := releaseCheckpointForTask(resumed, filepath.Dir(root))
	if err != nil {
		t.Fatal(err)
	}
	if saved.Worktree == nil || saved.Worktree.Branch != wt.Branch {
		t.Fatal("worktree 收尾状态未保存")
	}
	resumed.AgentID = "another-agent"
	if _, err = releaseCheckpointForTask(resumed, filepath.Dir(root)); err == nil {
		t.Fatal("其他 Agent 取得了暂停目录")
	}
}

func TestReleasePauseCheckpointRequiresActualSavedState(t *testing.T) {
	root := t.TempDir()
	task := Task{ID: "source"}
	result := TaskResult{EnvRoot: root, WorkDir: filepath.Join(root, "workdir")}
	if releaseCheckpointReady(task, result) {
		t.Fatal("缺少检查点仍确认停止")
	}
	raw, _ := json.Marshal(releaseCheckpoint{Task: task, Root: root, WorkDir: result.WorkDir})
	os.WriteFile(filepath.Join(root, ".release-pause.json"), raw, 0600)
	if !releaseCheckpointReady(task, result) {
		t.Fatal("已保存的检查点不可用")
	}
	task.ID = "other"
	if releaseCheckpointReady(task, result) {
		t.Fatal("接受了其他执行的检查点")
	}
	if !shouldInterruptAgent("release_paused", nil) {
		t.Fatal("暂停信号未中断执行")
	}
}
