package daemon

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/daemon/execenv"
	"github.com/multica-ai/multica/server/pkg/agent"
)

func TestCodexResidentTaskOwnershipAndGCPin(t *testing.T) {
	d := newCodexStoreGuardDaemon()
	d.codexPool = &agent.CodexProcessPool{}
	root, store := t.TempDir(), t.TempDir()
	env := &execenv.Environment{RootDir: root, CodexHome: filepath.Join(root, "codex-home")}
	task := Task{WorkspaceID: "workspace", RuntimeID: "runtime", AgentID: "agent", IssueID: "issue", ChatSessionID: "chat"}
	cfg := agent.Config{BuiltinRuntime: true, CodexVersion: "0.164.0"}
	d.configureCodexResident(task, env, store, &cfg)
	if cfg.CodexPool != d.codexPool || !strings.Contains(cfg.ResidentSessionKey, "chat:chat") || strings.Contains(cfg.ResidentSessionKey, "issue:issue") || cfg.PinResidentHome == nil {
		t.Fatal("resident execution did not bind the actual logical chat")
	}
	release := cfg.PinResidentHome()
	if !d.isActiveEnvRoot(root) {
		t.Fatal("resident home is exposed to environment GC")
	}
	release()
	if d.isActiveEnvRoot(root) {
		t.Fatal("resident home GC pin survived release")
	}
	for _, candidate := range []agent.Config{{BuiltinRuntime: true, CodexVersion: "0.163.0"}, {CodexVersion: "0.164.0"}, {BuiltinRuntime: true, CodexVersion: "unknown"}} {
		d.configureCodexResident(task, env, store, &candidate)
		if candidate.CodexPool != nil {
			t.Fatal("an unverified CLI profile acquired a resident process")
		}
	}
}
