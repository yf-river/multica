package daemon

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/multica-ai/multica/server/internal/daemon/execenv"
	"github.com/multica-ai/multica/server/pkg/agent"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// 1. Reuse the authenticated task message store; evidence stays on its original run and workspace.
func (d *Daemon) reportExecutionEvidence(task Task, provider string, env map[string]string, cwd string, result agent.Result, started time.Time, resumed bool, seq *atomic.Int32) {
	if !task.CaptureExecutionEvidence || task.SupervisionPrompt != "" || (provider != "codebuddy" && provider != "codex") {
		return
	}
	evidence := agent.CollectExecutionEvidence(provider, env, result.SessionID, cwd, result.ProviderTraceID, started, resumed)
	d.uploadExecutionEvidence(task.ID, evidence, int(seq.Add(1)))
}

func (d *Daemon) uploadExecutionEvidence(taskID string, evidence agent.ExecutionEvidence, seq int) {
	body, err := json.Marshal(evidence)
	if err != nil {
		return
	}
	if len(body) > 2<<20 {
		evidence.Events = nil
		evidence.Gaps = append(evidence.Gaps, "upload_size_limit_events_omitted")
		body, err = json.Marshal(evidence)
		if err != nil || len(body) > 2<<20 {
			d.logger.Warn("execution evidence exceeds upload limit", "task_id", taskID)
			return
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err = d.client.ReportTaskMessages(ctx, taskID, []TaskMessageData{{Seq: seq, Type: "execution_evidence", Tool: "execution_evidence", Content: string(body)}}); err != nil {
		d.logger.Warn("execution evidence upload unavailable", "task_id", taskID)
	}
}

// 2. Retrospective requests use the stable task-root index, not paths supplied by a model or a user-directory scan.
func (d *Daemon) handleExecutionEvidenceRequest(rt Runtime, request protocol.DaemonExecutionEvidenceRequest) {
	if rt.Provider != request.Provider || request.NextSeq < 1 {
		return
	}
	d.mu.Lock()
	owned := false
	if ws := d.workspaces[request.WorkspaceID]; ws != nil {
		for _, id := range ws.runtimeIDs {
			if id == rt.ID {
				owned = true
				break
			}
		}
	}
	d.mu.Unlock()
	if !owned {
		return
	}
	env := map[string]string{}
	if request.Provider == "codex" {
		root, err := execenv.ExistingRootDir(execenv.RootDirParams{WorkspacesRoot: d.cfg.WorkspacesRoot, WorkspaceID: request.WorkspaceID, TaskID: request.TaskID})
		if err != nil {
			d.uploadExecutionEvidence(request.TaskID, agent.ExecutionEvidence{Runtime: request.Provider, Session: request.SessionID, Started: request.Started, Ended: request.Ended, Gaps: []string{"owned_task_root_unavailable"}}, int(request.NextSeq))
			return
		}
		env["CODEX_HOME"] = filepath.Join(root, "codex-home")
	}
	evidence := agent.CollectExecutionEvidence(request.Provider, env, request.SessionID, request.Cwd, request.CID, request.Started, true, request.Ended)
	d.uploadExecutionEvidence(request.TaskID, evidence, int(request.NextSeq))
}
