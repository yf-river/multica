package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/daemon/execenv"
)

func TestDSHInteractionCheckpointResumesAcrossPlanningTurns(t *testing.T) {
	base := t.TempDir()
	source := Task{ID: "source-task", WorkspaceID: "workspace", AgentID: "agent", ChatSessionID: "chat", AuthToken: "mat_private_token"}
	root, err := execenv.ResolveRootDir(execenv.RootDirParams{WorkspacesRoot: base, WorkspaceID: source.WorkspaceID, TaskID: source.ID})
	if err != nil {
		t.Fatal(err)
	}
	workdir := filepath.Join(root, "workdir")
	if err := os.MkdirAll(workdir, 0700); err != nil {
		t.Fatal(err)
	}
	env := &execenv.Environment{RootDir: root, WorkDir: workdir}
	if err := saveDSHInteractionCheckpoint(source, TaskResult{SessionID: "native-session"}, env, root); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, ".dsh-interaction.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), source.AuthToken) || strings.Contains(string(raw), "auth_token") {
		t.Fatal("checkpoint contains a task credential")
	}
	next := source
	next.ID = "next-task"
	next.InteractionContinuation = true
	next.InteractionSourceTaskID = source.ID
	next.PriorSessionID = "native-session"
	next.PriorWorkDir = workdir
	saved, err := loadDSHInteractionCheckpoint(next, base)
	if err != nil || saved.Root != root || saved.WorkDir != workdir {
		t.Fatalf("native checkpoint lost: %+v %v", saved, err)
	}
	nextRoot, err := execenv.ResolveRootDir(execenv.RootDirParams{WorkspacesRoot: base, WorkspaceID: source.WorkspaceID, TaskID: next.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(nextRoot, 0700); err != nil {
		t.Fatal(err)
	}
	if err := saveDSHInteractionCheckpoint(next, TaskResult{SessionID: "native-session"}, env, nextRoot); err != nil {
		t.Fatal(err)
	}
	third := next
	third.ID = "third-task"
	third.InteractionSourceTaskID = next.ID
	saved, err = loadDSHInteractionCheckpoint(third, base)
	if err != nil || saved.Root != root {
		t.Fatalf("second planning round lost original environment: %+v %v", saved, err)
	}
	for _, change := range []func(*Task){func(t *Task) { t.AgentID = "other" }, func(t *Task) { t.PriorSessionID = "other" }, func(t *Task) { t.PriorWorkDir = base }, func(t *Task) { t.InteractionSourceTaskID = "other" }} {
		wrong := next
		change(&wrong)
		if _, err := loadDSHInteractionCheckpoint(wrong, base); err == nil {
			t.Fatal("foreign or changed native checkpoint accepted")
		}
	}
}
