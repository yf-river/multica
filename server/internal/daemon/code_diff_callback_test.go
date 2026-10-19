package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/codediff"
)

func TestCompleteTaskCarriesImmutableCodeDiff(t *testing.T) {
	snapshot := &codediff.Diff{Before: "before", After: "after", Files: []codediff.File{{Path: "app.go", Added: 1, Patch: "+new"}}}
	var received struct {
		CodeDiff *codediff.Diff `json:"code_diff"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	err := NewClient(srv.URL).CompleteTask(context.Background(), "task", "done", "", "", "", false, "", "", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if received.CodeDiff == nil || received.CodeDiff.Before != "before" || received.CodeDiff.Files[0].Patch != "+new" {
		t.Fatalf("snapshot missing: %+v", received)
	}
}

// Drive the production runner with a test-owned executable: no installed agent
// CLI or authenticated account participates in this regression test.
func TestRunTaskCapturesCommittedChangesAfterRuntimeInjection(t *testing.T) {
	d, _, cleanup := newLeaderReuseTestDaemon(t)
	defer cleanup()
	task := leaderReuseTestTask("task-code-diff")
	first, err := d.runTask(context.Background(), task, "claude", 0, d.logger)
	if err != nil {
		t.Fatal(err)
	}
	dir := first.WorkDir
	task.ID = "task-code-diff-followup"
	task.PriorWorkDir = dir
	task.PriorSessionID = first.SessionID
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	runGit("init")
	runGit("config", "user.name", "Test")
	runGit("config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(dir, "existing.txt"), []byte("before run\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runGit("add", ".")
	runGit("commit", "-m", "base")
	script := `#!/bin/sh
IFS= read -r _
printf 'after run\n' > existing.txt
git add existing.txt
git commit -qm 'agent change' >/dev/null
printf '%s\n' '{"type":"system","session_id":"session-leader-reuse"}'
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"session_id":"session-leader-reuse","result":"done"}'
`
	if err := os.WriteFile(d.cfg.Agents["claude"].Path, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	result, err := d.runTask(context.Background(), task, "claude", 0, d.logger)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "completed" || result.CodeDiff == nil {
		t.Fatalf("missing completed snapshot: status=%s diff=%+v", result.Status, result.CodeDiff)
	}
	if len(result.CodeDiff.Files) != 1 || result.CodeDiff.Files[0].Path != "existing.txt" || !strings.Contains(result.CodeDiff.Files[0].Patch, "-before run\n+after run") {
		t.Fatalf("runtime config leaked or agent commit lost: %+v", result.CodeDiff)
	}
}
