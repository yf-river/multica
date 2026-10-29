package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/multica-ai/multica/server/internal/releaseagent"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testagents"
	"github.com/multica-ai/multica/server/pkg/agent"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

// GetReleaseAgentSettings 返回工作区的发布协调配置。
func (h *Handler) GetReleaseAgentSettings(w http.ResponseWriter, r *http.Request) {
	ws, _, ok := h.reviewScope(w, r, false)
	if !ok {
		return
	}
	var enabled bool
	var projectID, agentID, runtimeID, model, thinking, instructions string
	err := h.DB.QueryRow(r.Context(), `SELECT c.enabled,c.project_id::text,COALESCE(c.agent_id::text,''),COALESCE(a.runtime_id::text,''),COALESCE(a.model,''),COALESCE(a.thinking_level,''),c.instructions FROM release_agent_config c LEFT JOIN agent a ON a.id=c.agent_id AND a.system_key=$2 WHERE c.workspace_id=$1`, ws, releaseagent.SystemKey).Scan(&enabled, &projectID, &agentID, &runtimeID, &model, &thinking, &instructions)
	configured := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 500, "读取发布 Agent 设置失败")
		return
	}
	member, _ := h.getWorkspaceMember(r.Context(), requestUserID(r), ws)
	writeJSON(w, 200, map[string]any{"configured": configured, "enabled": enabled, "project_id": projectID, "agent_id": agentID, "runtime_id": runtimeID, "model": model, "thinking_level": thinking, "instructions": releaseagent.EffectiveInstructions(instructions), "default_instructions": releaseagent.DefaultInstructions, "can_manage": member.Role == "owner" || member.Role == "admin"})
}

// dispatchReleaseAgent 使用持久通知号恢复派发；已入队的执行不会因发布器重启重复创建。
func (h *Handler) dispatchReleaseAgent(ctx context.Context, ws string, batch deliveryBatch, stage string) error {
	if h.TaskService == nil {
		return errors.New("发布 Agent 调度不可用")
	}
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	current, err := scanDeliveryBatch(tx.QueryRow(ctx, deliveryBatchSelect+` WHERE id=$1 AND workspace_id=$2 FOR UPDATE`, batch.ID, ws))
	if err != nil {
		return err
	}
	if current.WorkerToken != batch.WorkerToken || current.ProjectID != batch.ProjectID || (stage == "batch" && (current.Status != batchFrozen || current.Error != "")) || (stage == "conflict" && (current.Status != batchFrozen || current.Error == "")) || (stage == "integration" && current.Status != batchValidating) {
		return errors.New("发布批次阶段已变化")
	}
	batch = current
	var deliveryID, issueID, noticeID, agentID, ownerID string
	var delivered bool
	key := "release_agent:" + stage + ":" + batch.ID
	err = tx.QueryRow(ctx, `SELECT n.delivery_id::text,m.issue_id::text,n.comment_id::text,n.delivered_at IS NOT NULL,c.agent_id::text,c.owner_id::text FROM delivery_notice n JOIN delivery_batch_member m ON m.delivery_id=n.delivery_id AND m.batch_id=$1 JOIN release_agent_config c ON c.workspace_id=$2 AND c.project_id=$3 AND c.enabled AND c.agent_id IS NOT NULL WHERE n.stage=$4 AND ($5<>'conflict' OR m.error<>'') ORDER BY n.created_at DESC LIMIT 1 FOR UPDATE OF n`, batch.ID, ws, batch.ProjectID, key, stage).Scan(&deliveryID, &issueID, &noticeID, &delivered, &agentID, &ownerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return errors.New("发布 Agent 未配置或批次通知不存在")
	}
	if err != nil {
		return err
	}
	if delivered {
		// 集成协调运行失败时可有限续派；独立任务的代表还必须提供共同冒烟证据。
		if stage != "integration" {
			return tx.Commit(ctx)
		}
		var status string
		var cooled, needsSmoke, hasEvidence bool
		err = tx.QueryRow(ctx, `SELECT q.status,COALESCE(q.completed_at,q.created_at)<now()-interval '30 seconds',
			NOT EXISTS(SELECT 1 FROM jsonb_array_elements(COALESCE(d.plan_snapshot->'required_checks','[]')) x WHERE x->>'stage'='integration'),
			EXISTS(SELECT 1 FROM delivery_evidence e WHERE e.delivery_id=d.id AND e.candidate_id=$4 AND e.source_commit=$5)
			FROM agent_task_queue q JOIN issue_delivery d ON d.id=$1 WHERE q.agent_id=$2 AND q.issue_id=$3 AND q.trigger_evidence_kind='issue_release' AND q.trigger_evidence_ref_id=$6 ORDER BY q.created_at DESC LIMIT 1`, deliveryID, agentID, issueID, batch.CandidateID, batch.CandidateCommit, noticeID).Scan(&status, &cooled, &needsSmoke, &hasEvidence)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err == nil && (!cooled || hasEvidence || (status != "failed" && status != "cancelled" && status != "blocked" && !(status == "completed" && needsSmoke))) {
			return tx.Commit(ctx)
		}
		prefix := "release_agent:retry:" + stage + ":" + batch.ID + ":"
		var attempts int
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM delivery_notice n JOIN delivery_batch_member m ON m.delivery_id=n.delivery_id WHERE m.batch_id=$1 AND n.stage LIKE $2`, batch.ID, prefix+"%").Scan(&attempts); err != nil {
			return err
		}
		if attempts >= 3 {
			return tx.Commit(ctx)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO delivery_notice(delivery_id,stage,comment_id,delivered_at) SELECT delivery_id,$3,comment_id,delivered_at FROM delivery_notice WHERE delivery_id=$1 AND stage=$2 ON CONFLICT(delivery_id,stage) DO NOTHING`, deliveryID, key, prefix+fmt.Sprint(attempts+1)); err != nil {
			return err
		}
		noticeID = dbid.NewV7().String()
		if _, err = tx.Exec(ctx, `UPDATE delivery_notice SET comment_id=$3,delivered_at=NULL WHERE delivery_id=$1 AND stage=$2`, deliveryID, key, noticeID); err != nil {
			return err
		}
	}
	var existing bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_task_queue WHERE agent_id=$1 AND issue_id=$2 AND trigger_evidence_kind='issue_release' AND trigger_evidence_ref_id=$3)`, agentID, issueID, noticeID).Scan(&existing); err != nil {
		return err
	}
	if !existing {
		rows, queryErr := tx.Query(ctx, `SELECT i.id::text,i.number,m.source_commit,d.id::text,d.candidate_url,COALESCE(d.plan_snapshot->'required_checks','[]'),COALESCE(d.plan_snapshot->>'integration_skip_reason','') FROM delivery_batch_member m JOIN issue i ON i.id=m.issue_id AND i.workspace_id=$2 JOIN issue_delivery d ON d.id=m.delivery_id WHERE m.batch_id=$1 AND m.result='pending' ORDER BY m.created_at,m.delivery_id`, batch.ID, ws)
		if queryErr != nil {
			return queryErr
		}
		var members strings.Builder
		for rows.Next() {
			var number int64
			var issueID, commit, deliveryID, url, skip string
			var checks json.RawMessage
			if err = rows.Scan(&issueID, &number, &commit, &deliveryID, &url, &checks, &skip); err != nil {
				rows.Close()
				return err
			}
			fmt.Fprintf(&members, "- 任务 #%d（%s），交付 %s，提交 %s，入口 %s，检查 %s", number, issueID, deliveryID, commit, url, checks)
			if skip != "" {
				fmt.Fprintf(&members, "，无需单独联调：%s", skip)
			}
			members.WriteByte('\n')
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		issue, loadErr := h.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: parseUUID(issueID), WorkspaceID: parseUUID(ws)})
		if loadErr != nil {
			return loadErr
		}
		handoff := fmt.Sprintf("发布批次 %s，阶段 %s。\n%s候选编号：%s\n候选提交：%s\n当前问题：%s\n请按发布职责协调，使用任务评论向相关负责人说明需要处理的事项。", batch.ID, stage, members.String(), batch.CandidateID, batch.CandidateCommit, batch.Error)
		_, err = h.TaskService.EnqueueIssueReleaseTask(ctx, issue, parseUUID(agentID), parseUUID(ownerID), parseUUID(noticeID), handoff, batch.CandidateCommit)
		if errors.Is(err, service.ErrDuplicatePendingTask) {
			return errors.New("发布 Agent 正在处理其他事项，请稍后重试")
		}
		if err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE delivery_notice SET delivered_at=now() WHERE delivery_id=$1 AND stage=$2 AND comment_id=$3`, deliveryID, key, noticeID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// UpdateReleaseAgentSettings 配置项目、运行时和托管发布 Agent。
func (h *Handler) UpdateReleaseAgentSettings(w http.ResponseWriter, r *http.Request) {
	ws, user, ok := h.reviewScope(w, r, true)
	if !ok {
		return
	}
	var body struct {
		Enabled      bool    `json:"enabled"`
		ProjectID    string  `json:"project_id"`
		RuntimeID    string  `json:"runtime_id"`
		Model        string  `json:"model"`
		Thinking     string  `json:"thinking_level"`
		Instructions *string `json:"instructions"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 256*1024)).Decode(&body) != nil || (body.Instructions != nil && len(*body.Instructions) > 200000) {
		writeError(w, 400, "发布 Agent 配置格式错误")
		return
	}
	if !body.Enabled && body.ProjectID == "" && body.RuntimeID == "" {
		if _, err := h.DB.Exec(r.Context(), `UPDATE release_agent_config SET enabled=false,updated_at=now() WHERE workspace_id=$1`, ws); err != nil {
			writeError(w, 500, "暂停发布 Agent 失败")
			return
		}
		h.GetReleaseAgentSettings(w, r)
		return
	}
	projectID, valid := parseUUIDOrBadRequest(w, body.ProjectID, "project_id")
	if !valid {
		return
	}
	runtimeID, valid := parseUUIDOrBadRequest(w, body.RuntimeID, "runtime_id")
	if !valid {
		return
	}
	ctx := r.Context()
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		writeError(w, 500, "保存发布 Agent 设置失败")
		return
	}
	defer tx.Rollback(ctx)
	var lockedProject string
	if err = tx.QueryRow(ctx, `SELECT id::text FROM project WHERE id=$1 AND workspace_id=$2 FOR SHARE`, projectID, ws).Scan(&lockedProject); err != nil {
		writeError(w, 400, "项目不存在或不属于当前工作区")
		return
	}
	q := h.Queries.WithTx(tx)
	runtime, err := q.GetAgentRuntimeForWorkspace(ctx, db.GetAgentRuntimeForWorkspaceParams{ID: runtimeID, WorkspaceID: parseUUID(ws)})
	if err != nil || (runtime.Provider != "codex" && runtime.Provider != "codebuddy") {
		writeError(w, 400, "发布 Agent 需要当前工作区的 Codex 或 CodeBuddy 运行时")
		return
	}
	model, thinking := strings.TrimSpace(body.Model), strings.TrimSpace(body.Thinking)
	if (model != "" && agent.ModelKnownIncompatibleWithProvider(runtime.Provider, model)) || (thinking != "" && !agent.IsKnownThinkingValue(runtime.Provider, thinking)) {
		writeError(w, 400, "模型或思考等级不适用于所选运行时")
		return
	}
	_, err = tx.Exec(ctx, `INSERT INTO release_agent_config(workspace_id,project_id,owner_id) VALUES($1,$2,$3) ON CONFLICT(workspace_id) DO NOTHING`, ws, projectID, user)
	if err != nil {
		writeError(w, 500, "保存发布 Agent 设置失败")
		return
	}
	var agentID, ownerID, oldInstructions, oldRuntime, oldProject string
	if err = tx.QueryRow(ctx, `SELECT COALESCE(c.agent_id::text,''),c.owner_id::text,c.instructions,COALESCE((SELECT runtime_id::text FROM agent WHERE id=c.agent_id),''),c.project_id::text FROM release_agent_config c WHERE workspace_id=$1 FOR UPDATE OF c`, ws).Scan(&agentID, &ownerID, &oldInstructions, &oldRuntime, &oldProject); err != nil {
		writeError(w, 500, "读取发布 Agent 设置失败")
		return
	}
	if oldProject != body.ProjectID {
		var active bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM delivery_batch WHERE workspace_id=$1 AND project_id=$2 AND status IN ('collecting','frozen','preparing','validating','publishing'))`, ws, oldProject).Scan(&active); err != nil {
			writeError(w, 500, "检查发布批次失败")
			return
		}
		if active {
			writeError(w, 409, "原项目仍有发布批次，请先完成或处理该批次")
			return
		}
	}
	if agentID == "" {
		lookup := tx.QueryRow(ctx, `SELECT id::text,COALESCE(runtime_id::text,'') FROM agent WHERE workspace_id=$1 AND system_key=$2 AND archived_at IS NULL`, ws, releaseagent.SystemKey).Scan(&agentID, &oldRuntime)
		if lookup != nil && !errors.Is(lookup, pgx.ErrNoRows) {
			writeError(w, 500, "读取发布 Agent 失败")
			return
		}
	}
	instructions := oldInstructions
	if body.Instructions != nil {
		instructions = *body.Instructions
		if strings.TrimSpace(instructions) == "" || instructions == releaseagent.DefaultInstructions {
			instructions = ""
		}
	}
	effective := releaseagent.EffectiveInstructions(instructions)
	if agentID == "" || oldRuntime != body.RuntimeID {
		ownerID = user
	}
	if agentID == "" {
		err = tx.QueryRow(ctx, `INSERT INTO agent(workspace_id,name,description,runtime_mode,runtime_config,runtime_id,visibility,permission_mode,max_concurrent_tasks,owner_id,instructions,custom_env,custom_args,model,thinking_level,system_key,avatar_url) VALUES($1,$2,'由待发布设置维护',$3,'{}',$4,'private','private',1,$5,$6,'{}','[]',NULLIF($7,''),NULLIF($8,''),$9,$10) RETURNING id::text`, ws, releaseagent.DefaultName, runtime.RuntimeMode, runtime.ID, user, effective, model, thinking, releaseagent.SystemKey, releaseagent.DefaultAvatar).Scan(&agentID)
	} else {
		var changed pgconn.CommandTag
		changed, err = tx.Exec(ctx, `UPDATE agent SET runtime_id=$2,runtime_mode=$3,model=NULLIF($4,''),thinking_level=NULLIF($5,''),instructions=$6,owner_id=$9,updated_at=now() WHERE id=$1 AND workspace_id=$7 AND system_key=$8 AND archived_at IS NULL`, agentID, runtime.ID, runtime.RuntimeMode, model, thinking, effective, ws, releaseagent.SystemKey, ownerID)
		if err == nil && changed.RowsAffected() != 1 {
			err = errors.New("发布 Agent 不可用")
		}
	}
	if err == nil {
		_, err = tx.Exec(ctx, `UPDATE release_agent_config SET project_id=$2,agent_id=$3,owner_id=$4,enabled=$5,instructions=$6,updated_at=now() WHERE workspace_id=$1`, ws, projectID, agentID, ownerID, body.Enabled, instructions)
	}
	if err == nil {
		err = testagents.Apply(ctx, tx, ws, agentID)
	}
	if err != nil || tx.Commit(ctx) != nil {
		writeError(w, 500, "保存发布 Agent 设置失败")
		return
	}
	h.GetReleaseAgentSettings(w, r)
}
