package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/daemon/execenv"
	"github.com/multica-ai/multica/server/pkg/agent"
)

func (d *Daemon) initCodebuddyProcessPool() error {
	if runtime.GOOS != "linux" {
		return nil
	}
	owner := sha256.Sum256([]byte(d.cfg.DaemonID + "\x00" + d.cfg.Profile))
	root := filepath.Join(d.cfg.WorkspacesRoot, ".runtime-processes", hex.EncodeToString(owner[:]), "codebuddy")
	capacity := d.cfg.MaxConcurrentTasks
	if capacity < 1 {
		capacity = DefaultMaxConcurrentTasks
	}
	pool, err := agent.NewCodebuddyProcessPool(root, capacity, 2*time.Minute, d.logger)
	if err != nil {
		return err
	}
	d.codebuddyPool = pool
	return nil
}

func (d *Daemon) configureCodebuddyResident(task Task, env *execenv.Environment, cfg *agent.Config) {
	if d.codebuddyPool == nil || !cfg.BuiltinRuntime || !agent.CodebuddyResidentSupported(cfg.CLIVersion) || task.LifeJobID != "" || task.SupervisionPrompt != "" {
		return
	}
	conversation := "issue:" + task.IssueID
	if task.ChatSessionID != "" {
		conversation = "chat:" + task.ChatSessionID
	} else if task.IssueID == "" {
		return
	}
	cfg.CodebuddyPool = d.codebuddyPool
	cfg.ResidentSessionKey = strings.Join([]string{task.WorkspaceID, task.RuntimeID, task.AgentID, conversation}, "\x00")
	cfg.PinResidentHome = func() func() { d.markActiveEnvRoot(env.RootDir); return func() { d.unmarkActiveEnvRoot(env.RootDir) } }
}
