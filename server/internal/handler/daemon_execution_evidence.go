package handler

import (
	"context"
	"os"
	"time"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// 1. The authenticated runtime receives only its own persisted task/session and at most two extraction attempts.
func (h *Handler) pendingExecutionEvidence(ctx context.Context, rt db.AgentRuntime) *protocol.DaemonExecutionEvidenceRequest {
	if os.Getenv("MULTICA_SUPERVISION_TRACE_PREPARATION_SINCE") == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	var request protocol.DaemonExecutionEvidenceRequest
	err := h.DB.QueryRow(ctx, `WITH candidate AS (
 SELECT p.id FROM supervision_trace_material p JOIN agent_task_queue t ON t.id=p.run_id AND t.issue_id=p.issue_id
 JOIN supervision_trace_scope s ON s.id=p.round_id AND s.workspace_id=p.workspace_id
 JOIN supervision_config c ON c.workspace_id=p.workspace_id
 WHERE p.workspace_id=$1 AND t.runtime_id=$2 AND COALESCE(t.context->>'type','')<>'quality_supervision' AND p.runtime IN('codex','codebuddy') AND p.status='pending'
 AND s.status='open' AND c.enabled AND c.investigation_enabled
 AND NOT EXISTS(SELECT 1 FROM supervision_subject sub WHERE sub.workspace_id=p.workspace_id AND sub.issue_id=p.issue_id AND sub.next_attempt_at='infinity')
 AND COALESCE((p.cursor->>'extraction_attempts')::integer,0)<2
 AND (p.cursor->>'extraction_requested_at' IS NULL OR (p.cursor->>'extraction_requested_at')::timestamptz<now()-interval '30 seconds')
 AND NOT EXISTS(SELECT 1 FROM task_message m WHERE m.task_id=t.id AND m.type='execution_evidence')
 ORDER BY p.created_at,p.id LIMIT 1 FOR UPDATE OF p SKIP LOCKED
 ), claimed AS (UPDATE supervision_trace_material p
 SET cursor=cursor || jsonb_build_object('extraction_attempts',COALESCE((cursor->>'extraction_attempts')::integer,0)+1,'extraction_requested_at',now())
 FROM candidate WHERE p.id=candidate.id RETURNING p.*)
 SELECT p.id::text,t.id::text,p.workspace_id::text,p.runtime,COALESCE(t.session_id,''),
 COALESCE((SELECT provider_trace_id FROM task_provider_trace WHERE task_id=t.id AND workspace_id=p.workspace_id AND provider=p.runtime AND correlation='exact' ORDER BY created_at LIMIT 1),''),
 COALESCE(t.work_dir,''),COALESCE(t.started_at,t.created_at),COALESCE(t.completed_at,now()),COALESCE((SELECT max(seq) FROM task_message WHERE task_id=t.id),0)+1
 FROM claimed p JOIN agent_task_queue t ON t.id=p.run_id`, uuidToString(rt.WorkspaceID), uuidToString(rt.ID)).Scan(&request.MaterialID, &request.TaskID, &request.WorkspaceID, &request.Provider, &request.SessionID, &request.CID, &request.Cwd, &request.Started, &request.Ended, &request.NextSeq)
	if err != nil {
		return nil
	}
	return &request
}
