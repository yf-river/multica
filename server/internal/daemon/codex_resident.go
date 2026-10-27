package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/daemon/execenv"
	"github.com/multica-ai/multica/server/pkg/agent"
)

func (d *Daemon) prepareCodexNativeState(ctx context.Context, task Task, env *execenv.Environment, store string) (execenv.CodexNativeState, error) {
	var empty execenv.CodexNativeState
	if store == "" || os.Getenv("CODEX_SQLITE_HOME") != "" || (task.Agent != nil && task.Agent.CustomEnv["CODEX_SQLITE_HOME"] != "") {
		return empty, nil
	}
	source := ""
	state := execenv.CodexNativeStateDir(store, task.RuntimeID)
	if _, err := os.Stat(state); errors.Is(err, os.ErrNotExist) && task.PriorTaskID != "" {
		root, err := execenv.ExistingRootDir(execenv.RootDirParams{WorkspacesRoot: d.cfg.WorkspacesRoot, WorkspaceID: task.WorkspaceID, TaskID: task.PriorTaskID})
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return empty, fmt.Errorf("resolve Codex native state source: %w", err)
		}
		if root != "" && root != env.RootDir {
			d.markActiveEnvRoot(root)
			defer d.unmarkActiveEnvRoot(root)
			wsRoot, err := os.OpenRoot(d.cfg.WorkspacesRoot)
			if err != nil {
				return empty, err
			}
			defer wsRoot.Close()
			relative, err := filepath.Rel(d.cfg.WorkspacesRoot, root)
			if err != nil {
				return empty, err
			}
			claim, _, err := execenv.LockEnvRootForReuse(wsRoot, relative, root)
			if err != nil {
				return empty, err
			}
			if claim != nil {
				defer claim.Release()
				owner, err := execenv.ReadEnvRootOwner(root)
				if err != nil || owner.WorkspaceID != task.WorkspaceID || owner.TaskID != task.PriorTaskID {
					return empty, errors.New("Codex native state source ownership changed")
				}
				candidate := filepath.Join(root, "codex-home")
				if _, err := os.Stat(candidate); err == nil {
					source = candidate
				} else if !errors.Is(err, os.ErrNotExist) {
					return empty, err
				}
			}
		} else if root == env.RootDir {
			source = env.CodexHome
		}
	}
	return execenv.PrepareCodexNativeState(ctx, env.CodexHome, source, store, task.RuntimeID, task.PriorSessionID, d.codexPool.RetireHome)
}

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
	if d.codexPool == nil || !cfg.BuiltinRuntime || !agent.CodexResidentSupported(cfg.CodexVersion) || env.CodexHome == "" || task.SupervisionPrompt != "" {
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
