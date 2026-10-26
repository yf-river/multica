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

func (d *Daemon) initCodexProcessPool() error {
	if runtime.GOOS != "linux" {
		return nil
	}
	owner := sha256.Sum256([]byte(d.cfg.DaemonID + "\x00" + d.cfg.Profile))
	root := filepath.Join(d.cfg.WorkspacesRoot, ".runtime-processes", hex.EncodeToString(owner[:]), "codex")
	capacity := d.cfg.MaxConcurrentTasks
	if capacity < 1 {
		capacity = DefaultMaxConcurrentTasks
	}
	pool, err := agent.NewCodexProcessPool(root, capacity, 2*time.Minute, d.logger)
	if err != nil {
		return err
	}
	d.codexPool = pool
	return nil
}

func (d *Daemon) configureCodexResident(task Task, env *execenv.Environment, store string, cfg *agent.Config) {
	if d.codexPool == nil || !cfg.BuiltinRuntime || !agent.CodexResidentSupported(cfg.CodexVersion) || env.CodexHome == "" {
		return
	}
	conversation := "issue:" + task.IssueID
	if task.ChatSessionID != "" {
		conversation = "chat:" + task.ChatSessionID
	} else if task.IssueID == "" {
		return
	}
	cfg.CodexPool = d.codexPool
	cfg.ResidentSessionKey = strings.Join([]string{task.WorkspaceID, task.RuntimeID, task.AgentID, conversation}, "\x00")
	cfg.PinResidentHome = func() func() {
		d.markActiveEnvRoot(env.RootDir)
		d.markActiveStore(store)
		return func() {
			d.unmarkActiveStore(store)
			d.unmarkActiveEnvRoot(env.RootDir)
		}
	}
}
