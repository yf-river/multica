package daemon

import (
	"testing"

	"github.com/multica-ai/multica/server/internal/daemon/execenv"
	"github.com/multica-ai/multica/server/pkg/agent"
)

func TestCodebuddyResidentOwnershipAndGCPin(t *testing.T) {
	d := newCodexStoreGuardDaemon()
	d.codebuddyPool = &agent.CodebuddyProcessPool{}
	env := &execenv.Environment{RootDir: t.TempDir()}
	task := Task{WorkspaceID: "workspace", RuntimeID: "runtime", AgentID: "agent", ChatSessionID: "chat"}
	configure := func(task Task) agent.Config {
		cfg := agent.Config{BuiltinRuntime: true, CLIVersion: "2.176.0"}
		d.configureCodebuddyResident(task, env, &cfg)
		return cfg
	}
	cfg := configure(task)
	if cfg.CodebuddyPool != d.codebuddyPool || cfg.PinResidentHome == nil {
		t.Fatal("supported chat did not acquire its resident ownership")
	}
	release := cfg.PinResidentHome()
	if !d.isActiveEnvRoot(env.RootDir) {
		t.Fatal("resident home is exposed to GC")
	}
	release()
	if d.isActiveEnvRoot(env.RootDir) {
		t.Fatal("released resident home remains pinned")
	}
	for _, field := range []string{"workspace", "runtime", "agent", "chat"} {
		other := task
		switch field {
		case "workspace":
			other.WorkspaceID = "other"
		case "runtime":
			other.RuntimeID = "other"
		case "agent":
			other.AgentID = "other"
		case "chat":
			other.ChatSessionID = "other"
		}
		if configure(other).ResidentSessionKey == cfg.ResidentSessionKey {
			t.Fatalf("%s change reused another owner's logical session", field)
		}
	}
	for _, other := range []Task{{WorkspaceID: "workspace", RuntimeID: "runtime", AgentID: "agent"}, {ChatSessionID: "chat", LifeJobID: "life"}, {ChatSessionID: "chat", SupervisionPrompt: "supervision input"}} {
		if configure(other).CodebuddyPool != nil {
			t.Fatal("task without a supported conversation entered the resident pool")
		}
	}
	for _, candidate := range []agent.Config{{BuiltinRuntime: true, CLIVersion: "2.174.0"}, {CLIVersion: "2.176.0"}, {BuiltinRuntime: true, CLIVersion: "unknown"}} {
		d.configureCodebuddyResident(task, env, &candidate)
		if candidate.CodebuddyPool != nil {
			t.Fatal("unverified profile entered the resident pool")
		}
	}
}
