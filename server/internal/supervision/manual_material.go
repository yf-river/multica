package supervision

import (
	"context"
	"os"

	"github.com/jackc/pgx/v5"
)

// PrepareManualMaterials freezes a separate one-shot scope without opening an automatic completion round.
// 1. Existing manual scope is reused until its batch dispatches; unrelated runs cannot join later.
func PrepareManualMaterials(ctx context.Context, tx pgx.Tx, workspace, issue string) error {
	if os.Getenv("MULTICA_SUPERVISION_TRACE_PREPARATION_SINCE") == "" {
		return nil
	}
	var scope string
	err := tx.QueryRow(ctx, `INSERT INTO supervision_manual_preparation(id,workspace_id,issue_id)
 SELECT gen_random_uuid(),i.workspace_id,i.id FROM issue i WHERE i.workspace_id=$1 AND i.id=$2
 ON CONFLICT(workspace_id,issue_id) WHERE status='open' DO UPDATE SET status='open'
 RETURNING id::text`, workspace, issue).Scan(&scope)
	if err != nil {
		return err
	}
	// Modification 1. A run finishing after the frozen manual cutoff belongs to a later scope.
	_, err = tx.Exec(ctx, `INSERT INTO supervision_trace_material(id,workspace_id,issue_id,round_id,run_id,runtime,source,status,manual)
 SELECT gen_random_uuid(),m.workspace_id,m.issue_id,m.id,t.id,
 COALESCE((SELECT NULLIF(e.provider,'') FROM task_trace_event e WHERE e.workspace_id=m.workspace_id AND e.task_id=t.id AND e.event_type='task.started' ORDER BY e.created_at,e.id LIMIT 1),''),
 'execution','pending',true FROM supervision_manual_preparation m JOIN agent_task_queue t ON t.issue_id=m.issue_id
 WHERE m.workspace_id=$1 AND m.id=$2 AND t.created_at<=m.material_cutoff
 AND t.completed_at IS NOT NULL AND t.completed_at<=m.material_cutoff
 AND t.status IN('completed','failed','cancelled') AND COALESCE(t.context->>'type','')<>'quality_supervision' ON CONFLICT DO NOTHING`, workspace, scope)
	return err
}
