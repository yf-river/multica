package service

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/attribution"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

// SupervisionContext 将无 issue 的监督运行绑定到服务端审查批次。
type SupervisionContext struct {
	Type          string `json:"type"`
	WorkspaceID   string `json:"workspace_id"`
	BatchID       string `json:"batch_id"`
	Prompt        string `json:"prompt"`
	Investigation bool   `json:"investigation"`
}

// EnqueueSupervisionTaskTx 与调用方的批次认领共用事务，避免重复创建运行。
func (s *TaskService) EnqueueSupervisionTaskTx(ctx context.Context, q *db.Queries, ws, owner, agentID, batch pgtype.UUID, prompt string, investigation bool) (db.AgentTaskQueue, error) {
	agent, err := q.GetAgent(ctx, agentID)
	if err != nil {
		return db.AgentTaskQueue{}, err
	}
	if agent.WorkspaceID != ws || agent.ArchivedAt.Valid {
		return db.AgentTaskQueue{}, fmt.Errorf("监督 Agent 不可用")
	}
	runtime, err := q.GetAgentRuntime(ctx, agent.RuntimeID)
	if err != nil {
		return db.AgentTaskQueue{}, err
	}
	if runtime.Provider != "codex" && runtime.Provider != "codebuddy" {
		return db.AgentTaskQueue{}, fmt.Errorf("首版监督支持 Codex 或 CodeBuddy 运行时")
	}
	ready, err := AgentReadiness(ctx, s.runtimeLookup(), agent)
	if err != nil {
		return db.AgentTaskQueue{}, err
	}
	if !ready.Ready() {
		return db.AgentTaskQueue{}, fmt.Errorf("监督 Agent 暂不可用：%s", ready.Detail)
	}
	raw, err := json.Marshal(SupervisionContext{Type: "quality_supervision", WorkspaceID: util.UUIDToString(ws), BatchID: util.UUIDToString(batch), Prompt: prompt, Investigation: investigation})
	if err != nil {
		return db.AgentTaskQueue{}, err
	}
	attr := attribution.TriggerOwner(owner, attribution.EvidenceKind("supervision_batch"), batch)
	src, _, kind, ref := attributionCreateParams(attr)
	task, err := q.CreateQuickCreateTask(ctx, db.CreateQuickCreateTaskParams{
		ID: dbid.NewV7(), AgentID: agentID, RuntimeID: agent.RuntimeID, Priority: 0, Context: raw,
		OriginatorUserID: attr.UserID, AccountableUserID: attr.AccountableUserID,
		OriginatorSource: src, TriggerEvidenceKind: kind, TriggerEvidenceRefID: ref,
	})
	if err != nil {
		return task, err
	}
	_, err = RecordTaskQueuedEventTx(ctx, q, util.UUIDToString(ws), task)
	return task, err
}
