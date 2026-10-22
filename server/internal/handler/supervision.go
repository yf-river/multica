package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/supervision"
	agentpkg "github.com/multica-ai/multica/server/pkg/agent"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type supervisionFeedbackResponse struct {
	header http.Header
	status int
	bytes.Buffer
}

func (r *supervisionFeedbackResponse) Header() http.Header    { return r.header }
func (r *supervisionFeedbackResponse) WriteHeader(status int) { r.status = status }
func (r *supervisionFeedbackResponse) Write(body []byte) (int, error) {
	if r.status == 0 {
		r.status = 200
	}
	return r.Buffer.Write(body)
}

// FeedbackSupervisionFinding 复用评论的权限与派发链，操作号及评论标记用于响应丢失后的对账。
func (h *Handler) FeedbackSupervisionFinding(w http.ResponseWriter, r *http.Request) {
	ws, user, ok := h.supervisionScope(w, r, true)
	if !ok {
		return
	}
	finding, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "findingId"), "finding_id")
	if !ok {
		return
	}
	var body struct {
		Content     string `json:"content"`
		OperationID string `json:"operation_id"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 65536)).Decode(&body) != nil || strings.TrimSpace(body.Content) == "" {
		writeError(w, 400, "反馈内容不能为空")
		return
	}
	operation, ok := parseUUIDOrBadRequest(w, body.OperationID, "operation_id")
	if !ok {
		return
	}
	var issue string
	if h.DB.QueryRow(r.Context(), `SELECT issue_id::text FROM supervision_finding WHERE workspace_id=$1 AND id=$2`, ws, finding).Scan(&issue) != nil {
		writeError(w, 404, "问题不存在")
		return
	}
	marker := "<!-- supervision-operation:" + body.OperationID + " -->"
	tag, err := h.DB.Exec(r.Context(), `INSERT INTO supervision_action(workspace_id,finding_id,actor_id,operation_id,action,reason) VALUES($1,$2,$3,$4,'feedback',$5) ON CONFLICT(workspace_id,operation_id) DO NOTHING`, ws, finding, user, operation, body.Content)
	if err != nil {
		writeError(w, 500, "登记反馈失败")
		return
	}
	if tag.RowsAffected() == 0 {
		var receipt json.RawMessage
		var priorFinding, priorContent, priorAction string
		if h.DB.QueryRow(r.Context(), `SELECT finding_id::text,reason,action,receipt FROM supervision_action WHERE workspace_id=$1 AND operation_id=$2`, ws, operation).Scan(&priorFinding, &priorContent, &priorAction, &receipt) != nil {
			writeError(w, 500, "读取反馈回执失败")
			return
		}
		if priorFinding != uuidToString(finding) || priorContent != body.Content || priorAction != "feedback" {
			writeError(w, 409, "同一操作编号不能更换反馈内容")
			return
		}
		if len(receipt) > 0 {
			writeJSON(w, 200, receipt)
			return
		}
		var commentID string
		if h.DB.QueryRow(r.Context(), `SELECT id::text FROM comment WHERE issue_id=$1 AND position($2 in content)>0 ORDER BY created_at LIMIT 1`, issue, marker).Scan(&commentID) == nil {
			writeJSON(w, 200, map[string]any{"comment_id": commentID, "reconciled": true, "trigger_status": "unknown"})
			return
		}
	}
	raw, _ := json.Marshal(CreateCommentRequest{Content: body.Content + "\n\n" + marker, Type: "comment"})
	forwarded := r.Clone(r.Context())
	forwarded = forwarded.WithContext(context.WithValue(forwarded.Context(), commentIDContextKey{}, operation))
	forwarded.Body = http.NoBody
	forwarded.Body = readCloser{Reader: bytes.NewReader(raw)}
	route := chi.NewRouteContext()
	route.URLParams.Add("id", issue)
	forwarded = forwarded.WithContext(context.WithValue(forwarded.Context(), chi.RouteCtxKey, route))
	response := &supervisionFeedbackResponse{header: make(http.Header)}
	h.CreateComment(response, forwarded)
	if response.status < 200 || response.status >= 300 {
		var commentID string
		if h.DB.QueryRow(r.Context(), `SELECT id::text FROM comment WHERE issue_id=$1 AND id=$2 AND position($3 in content)>0`, issue, operation, marker).Scan(&commentID) == nil {
			writeJSON(w, 200, map[string]any{"comment_id": commentID, "reconciled": true, "trigger_status": "unknown"})
			return
		}
		// 评论编号由操作号固定，重试时主键约束阻止重复评论与重复派发。
		w.WriteHeader(response.status)
		_, _ = w.Write(response.Bytes())
		return
	}
	receipt := json.RawMessage(response.Bytes())
	_, err = h.DB.Exec(r.Context(), `UPDATE supervision_action SET receipt=$3 WHERE workspace_id=$1 AND operation_id=$2`, ws, operation, receipt)
	if err != nil {
		writeError(w, 503, "评论可能已发送，回执保存失败；请沿用操作编号查询")
		return
	}
	writeJSON(w, 200, receipt)
}

type readCloser struct{ *bytes.Reader }

func (readCloser) Close() error { return nil }

// supervisionScope 只接受真实成员访问；后台分析通过固定批次材料运行。
func (h *Handler) supervisionScope(w http.ResponseWriter, r *http.Request, write bool) (string, string, bool) {
	if r.Header.Get("X-Actor-Source") == "task_token" {
		writeError(w, 403, "监督配置和处理操作需要成员身份")
		return "", "", false
	}
	ws := middleware.WorkspaceIDFromContext(r.Context())
	user := requestUserID(r)
	member, ok := h.requireWorkspaceMember(w, r, ws, "工作区不存在")
	if !ok {
		return "", "", false
	}
	if write && member.Role != "owner" && member.Role != "admin" {
		writeError(w, 403, "需要工作区管理员权限")
		return "", "", false
	}
	return ws, user, true
}

func (h *Handler) GetSupervision(w http.ResponseWriter, r *http.Request) {
	ws, _, ok := h.supervisionScope(w, r, false)
	if !ok {
		return
	}
	var config json.RawMessage
	err := h.DB.QueryRow(r.Context(), `SELECT jsonb_build_object(
      'enabled',c.enabled,'agent_id',c.agent_id,'concurrency',a.max_concurrent_tasks,
      'interval_seconds',c.interval_seconds,'investigation_enabled',c.investigation_enabled,
      'inventory_complete',c.inventory_complete,'last_scan_at',c.last_scan_at,'last_success_at',c.last_success_at,'error',c.error,
      'instructions',c.instructions,'instructions_customized',c.instructions<>'',
      'instructions_version',CASE WHEN c.instructions='' THEN $2::text ELSE 'custom'::text END,
      'runtime_id',a.runtime_id,'model',a.model,'thinking_level',a.thinking_level)
      FROM supervision_config c LEFT JOIN agent a ON a.id=c.agent_id WHERE c.workspace_id=$1`, ws, supervision.InstructionsVersion).Scan(&config)
	if errors.Is(err, pgx.ErrNoRows) {
		config = json.RawMessage(`{"enabled":false,"agent_id":null,"concurrency":null,"interval_seconds":300,"investigation_enabled":false,"inventory_complete":false,"error":"","instructions":"","instructions_customized":false,"instructions_version":"` + supervision.InstructionsVersion + `","runtime_id":null,"model":null,"thinking_level":null}`)
	} else if err != nil {
		writeError(w, 500, "读取监督配置失败")
		return
	}
	var summary json.RawMessage
	err = h.DB.QueryRow(r.Context(), `SELECT jsonb_build_object(
      'total',count(*),'covered',count(*) FILTER(WHERE s.reviewed_version>=s.desired_version AND s.last_checked_at IS NOT NULL),
      'no_runs',count(*) FILTER(WHERE s.last_checked_at IS NULL AND NOT EXISTS(SELECT 1 FROM agent_task_queue t WHERE t.issue_id=s.issue_id)),
      'pending',count(*) FILTER(WHERE s.desired_version>s.reviewed_version AND s.failures<3 AND isfinite(s.next_attempt_at) AND EXISTS(SELECT 1 FROM agent_task_queue t WHERE t.issue_id=s.issue_id) AND NOT EXISTS(SELECT 1 FROM supervision_batch b WHERE b.issue_id=s.issue_id AND b.status IN('preparing','running'))),
      'running',count(*) FILTER(WHERE EXISTS(SELECT 1 FROM supervision_batch b WHERE b.issue_id=s.issue_id AND b.status IN('preparing','running'))),
      'incomplete',count(*) FILTER(WHERE s.gaps<>'[]'::jsonb),'failed',count(*) FILTER(WHERE s.failures>=3),
 'retrying',count(*) FILTER(WHERE s.failures>0 AND s.failures<3 AND s.desired_version>s.reviewed_version),
      'oldest_pending_at',min(s.changed_at) FILTER(WHERE s.desired_version>s.reviewed_version AND s.failures<3 AND isfinite(s.next_attempt_at) AND EXISTS(SELECT 1 FROM agent_task_queue t WHERE t.issue_id=s.issue_id)),
      'open_findings',(SELECT count(*) FROM supervision_finding f WHERE f.workspace_id=$1 AND f.status IN('open','in_progress')),
      'issues_with_findings',(SELECT count(DISTINCT issue_id) FROM supervision_finding f WHERE f.workspace_id=$1 AND f.status IN('open','in_progress')))
      FROM supervision_subject s JOIN issue i ON i.id=s.issue_id WHERE s.workspace_id=$1 AND COALESCE(i.origin_type,'')<>'health_report'`, ws).Scan(&summary)
	if err != nil {
		writeError(w, 500, "读取监督统计失败")
		return
	}
	member, _ := h.getWorkspaceMember(r.Context(), requestUserID(r), ws)
	writeJSON(w, 200, map[string]any{"config": config, "summary": summary, "can_manage": member.Role == "owner" || member.Role == "admin"})
}

// GetSupervisionInstructions 返回内置默认提示词，供设置页展示和「恢复默认」。
// 单独成接口，避免每次轮询监督快照都带上近八千字。
func (h *Handler) GetSupervisionInstructions(w http.ResponseWriter, r *http.Request) {
	// 与设置页的其他写操作同权限：只有 owner/admin 需要看到完整默认提示词。
	if _, _, ok := h.supervisionScope(w, r, true); !ok {
		return
	}
	writeJSON(w, 200, map[string]any{"instructions": supervision.DefaultInstructions, "version": supervision.InstructionsVersion})
}

// UpdateSupervision 保存监督运行时、型号、提示词与周期。监督员由系统托管：
// 首次保存自动创建，已有配置指向的手工 Agent 会被接管并隐藏。
func (h *Handler) UpdateSupervision(w http.ResponseWriter, r *http.Request) {
	ws, user, ok := h.supervisionScope(w, r, true)
	if !ok {
		return
	}
	var body struct {
		RuntimeID     *string `json:"runtime_id"`
		Model         *string `json:"model"`
		ThinkingLevel *string `json:"thinking_level"`
		Instructions  *string `json:"instructions"`
		Enabled       bool    `json:"enabled"`
		Interval      int     `json:"interval_seconds"`
		Investigation *bool   `json:"investigation_enabled"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 512*1024)).Decode(&body) != nil {
		writeError(w, 400, "配置格式错误")
		return
	}
	if body.Interval == 0 {
		body.Interval = 300
	}
	if body.Interval < 60 || body.Interval > 3600 {
		writeError(w, 400, "检查间隔应为60至3600秒")
		return
	}
	if body.Instructions != nil && len(*body.Instructions) > 200_000 {
		writeError(w, 400, "提示词过长，请控制在20万字符以内")
		return
	}
	member, err := h.getWorkspaceMember(r.Context(), user, ws)
	if err != nil {
		writeError(w, 403, "需要工作区成员权限")
		return
	}

	ctx := r.Context()
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		writeError(w, 500, "保存监督配置失败")
		return
	}
	defer tx.Rollback(ctx)
	qtx := h.Queries.WithTx(tx)

	var agentID, ownerID, currentInstructions string
	var currentInvestigation bool
	err = tx.QueryRow(ctx, `SELECT COALESCE(agent_id::text,''),COALESCE(owner_id::text,''),instructions,investigation_enabled FROM supervision_config WHERE workspace_id=$1 FOR UPDATE`, ws).Scan(&agentID, &ownerID, &currentInstructions, &currentInvestigation)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 500, "读取监督配置失败")
		return
	}

	var current db.Agent
	hasAgent := agentID != ""
	if hasAgent {
		current, err = qtx.GetAgent(ctx, parseUUID(agentID))
		if err != nil || uuidToString(current.WorkspaceID) != ws {
			writeError(w, 409, "原监督 Agent 已不可用，请重新选择运行时后再保存")
			return
		}
		if current.SystemKey.Valid && current.SystemKey.String != service.SupervisionSystemKey {
			writeError(w, 409, "监督 Agent 配置异常，请联系工作区管理员")
			return
		}
	}

	// 目标运行时：显式传入优先，否则沿用已保存的绑定。
	var targetRuntime db.AgentRuntime
	hasRuntime := false
	runtimeChanged := false
	if body.RuntimeID != nil && strings.TrimSpace(*body.RuntimeID) != "" {
		runtimeUUID, valid := parseUUIDOrBadRequest(w, *body.RuntimeID, "runtime_id")
		if !valid {
			return
		}
		targetRuntime, err = qtx.GetAgentRuntimeForWorkspace(ctx, db.GetAgentRuntimeForWorkspaceParams{ID: runtimeUUID, WorkspaceID: parseUUID(ws)})
		if err != nil {
			writeError(w, 400, "运行时不存在或不属于当前工作区")
			return
		}
		if targetRuntime.Provider != "codex" && targetRuntime.Provider != "codebuddy" {
			writeError(w, 400, "质量监督目前只支持 Codex 或 CodeBuddy 运行时")
			return
		}
		hasRuntime = true
		runtimeChanged = !hasAgent || !current.RuntimeID.Valid || uuidToString(current.RuntimeID) != uuidToString(targetRuntime.ID)
		if runtimeChanged && !canUseRuntimeForAgent(member, targetRuntime) {
			writeError(w, 403, "该运行时仅其所有者可用，请选择自己的运行时")
			return
		}
	} else if hasAgent && current.RuntimeID.Valid {
		if targetRuntime, err = qtx.GetAgentRuntime(ctx, current.RuntimeID); err == nil {
			hasRuntime = true
		}
	}
	if body.Enabled && !hasRuntime {
		writeError(w, 400, "请选择监督运行时")
		return
	}
	if body.ThinkingLevel != nil && hasRuntime {
		level := strings.TrimSpace(*body.ThinkingLevel)
		if level != "" && !agentpkg.IsKnownThinkingValue(targetRuntime.Provider, level) {
			writeError(w, 400, "思考等级不适用于所选运行时")
			return
		}
	}

	instructions := currentInstructions
	if body.Instructions != nil {
		text := strings.TrimSpace(*body.Instructions)
		if text == "" || text == strings.TrimSpace(supervision.DefaultInstructions) {
			// 与内置默认一致时不落库，继续跟随平台升级。
			instructions = ""
		} else {
			instructions = *body.Instructions
		}
	}

	// 监督员归属于当前运行身份：换运行时或首次创建时归保存者；沿用原绑定时以
	// Agent 的所有者为调用身份，避免第二个管理员只改间隔却让私有运行时失去调用资格。
	effectiveOwner := ownerID
	if hasAgent && current.OwnerID.Valid {
		effectiveOwner = uuidToString(current.OwnerID)
	}
	if !hasAgent || runtimeChanged || effectiveOwner == "" {
		effectiveOwner = user
	}

	if !hasAgent {
		// 尚未选运行时时只保存周期/开关，等真正启用时再创建托管 Agent。
		if hasRuntime {
			err = tx.QueryRow(ctx, `INSERT INTO agent (
          workspace_id,name,description,runtime_mode,runtime_config,runtime_id,visibility,permission_mode,
          max_concurrent_tasks,owner_id,instructions,custom_env,custom_args,mcp_config,model,thinking_level,service_tier,system_key)
        VALUES ($1,'质量监督','由质量监督设置自动维护，请到质量监督页面修改',$2,'{}'::jsonb,$3,'private','private',
          3,$4,'','{}'::jsonb,'[]'::jsonb,'{}'::jsonb,NULLIF($5,''),NULLIF($6,''),'',$7)
        RETURNING id::text`, ws, targetRuntime.RuntimeMode, targetRuntime.ID, effectiveOwner,
				derefString(body.Model), derefString(body.ThinkingLevel), service.SupervisionSystemKey).Scan(&agentID)
			if err != nil {
				writeError(w, 500, "创建监督 Agent 失败")
				return
			}
		}
	} else {
		model := current.Model.String
		if body.Model != nil {
			model = strings.TrimSpace(*body.Model)
		}
		thinking := current.ThinkingLevel.String
		if body.ThinkingLevel != nil {
			thinking = strings.TrimSpace(*body.ThinkingLevel)
		}
		if hasRuntime {
			if thinking != "" && !agentpkg.IsKnownThinkingValue(targetRuntime.Provider, thinking) {
				writeError(w, 400, "思考等级不适用于所选运行时")
				return
			}
			if body.Model == nil && runtimeChanged && model != "" && agentpkg.ModelKnownIncompatibleWithProvider(targetRuntime.Provider, model) {
				model = ""
			}
			if _, err = tx.Exec(ctx, `UPDATE agent SET runtime_id=$2,runtime_mode=$3,model=NULLIF($4,''),thinking_level=NULLIF($5,''),
              owner_id=$6,system_key=$7,instructions='',archived_at=NULL,archived_by=NULL,updated_at=now() WHERE id=$1`,
				agentID, targetRuntime.ID, targetRuntime.RuntimeMode, model, thinking, effectiveOwner, service.SupervisionSystemKey); err != nil {
				writeError(w, 500, "更新监督 Agent 失败")
				return
			}
		} else if _, err = tx.Exec(ctx, `UPDATE agent SET system_key=$2,instructions='',updated_at=now() WHERE id=$1`, agentID, service.SupervisionSystemKey); err != nil {
			writeError(w, 500, "更新监督 Agent 失败")
			return
		}
	}

	if body.Investigation != nil {
		currentInvestigation = *body.Investigation
	}
	_, err = tx.Exec(ctx, `INSERT INTO supervision_config(workspace_id,agent_id,owner_id,enabled,interval_seconds,investigation_enabled,instructions)
      VALUES($1,NULLIF($2,'')::uuid,$3,$4,$5,$6,$7) ON CONFLICT(workspace_id) DO UPDATE SET
      agent_id=EXCLUDED.agent_id,owner_id=EXCLUDED.owner_id,enabled=EXCLUDED.enabled,interval_seconds=EXCLUDED.interval_seconds,
      investigation_enabled=EXCLUDED.investigation_enabled,instructions=EXCLUDED.instructions,error='',updated_at=now()`,
		ws, agentID, effectiveOwner, body.Enabled, body.Interval, currentInvestigation, instructions)
	if err != nil {
		writeError(w, 500, "保存监督配置失败")
		return
	}
	if err = tx.Commit(ctx); err != nil {
		writeError(w, 500, "保存监督配置失败")
		return
	}
	h.GetSupervision(w, r)
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func (h *Handler) ListSupervisionSubjects(w http.ResponseWriter, r *http.Request) {
	ws, _, ok := h.supervisionScope(w, r, false)
	if !ok {
		return
	}
	limit := 50
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if offset < 0 {
		offset = 0
	}
	search := r.URL.Query().Get("search")
	filter := r.URL.Query().Get("filter")
	project := r.URL.Query().Get("project")
	if project != "" {
		if _, ok := parseUUIDOrBadRequest(w, project, "project_id"); !ok {
			return
		}
	}
	rows, err := h.DB.Query(r.Context(), `SELECT jsonb_build_object('issue_id',i.id,'title',i.title,'identifier',w.issue_prefix||'-'||i.number,'business_status',i.status,'project_id',i.project_id,
      'desired_version',s.desired_version,'reviewed_version',s.reviewed_version,'summary',s.summary,'efficiency',s.efficiency,'correctness',s.correctness,'gaps',s.gaps,'last_checked_at',s.last_checked_at,'error',s.error,'failures',s.failures,
      'run_count',(SELECT count(*) FROM agent_task_queue t WHERE t.issue_id=i.id),
      'stopped',s.next_attempt_at='infinity'::timestamptz,'next_attempt_at',CASE WHEN isfinite(s.next_attempt_at) THEN s.next_attempt_at ELSE NULL END,'running',EXISTS(SELECT 1 FROM supervision_batch b WHERE b.workspace_id=$1 AND b.issue_id=i.id AND b.status IN('preparing','running')),
      'open_findings',(SELECT count(*) FROM supervision_finding f WHERE f.workspace_id=$1 AND f.issue_id=i.id AND f.status IN('open','in_progress')))
      FROM supervision_subject s JOIN issue i ON i.id=s.issue_id JOIN workspace w ON w.id=i.workspace_id
      WHERE s.workspace_id=$1 AND COALESCE(i.origin_type,'')<>'health_report'
      AND ($2='' OR i.title ILIKE '%'||$2||'%' OR (w.issue_prefix||'-'||i.number) ILIKE '%'||$2||'%')
      AND ($3='' OR i.project_id=NULLIF($3,'')::uuid)
      AND ($4='' OR $4='all' OR ($4='pending' AND s.desired_version>s.reviewed_version) OR ($4='incomplete' AND s.gaps<>'[]'::jsonb)
        OR ($4='problems' AND EXISTS(SELECT 1 FROM supervision_finding f WHERE f.workspace_id=$1 AND f.issue_id=i.id AND f.status IN('open','in_progress') AND (f.snoozed_until IS NULL OR f.snoozed_until<=now())))
        OR ($4='failed' AND s.failures>=3))
      ORDER BY EXISTS(SELECT 1 FROM supervision_finding f WHERE f.workspace_id=$1 AND f.issue_id=i.id AND f.status IN('open','in_progress') AND (f.snoozed_until IS NULL OR f.snoozed_until<=now())) DESC,s.changed_at DESC,i.id
      LIMIT $5 OFFSET $6`, ws, search, project, filter, limit+1, offset)
	if err != nil {
		writeError(w, 500, "读取审查任务失败")
		return
	}
	defer rows.Close()
	items := []json.RawMessage{}
	for rows.Next() {
		var raw json.RawMessage
		if rows.Scan(&raw) != nil {
			writeError(w, 500, "读取任务失败")
			return
		}
		items = append(items, raw)
	}
	if rows.Err() != nil {
		writeError(w, 500, "读取任务失败")
		return
	}
	more := len(items) > limit
	if more {
		items = items[:limit]
	}
	writeJSON(w, 200, map[string]any{"items": items, "has_more": more, "offset": offset})
}

func (h *Handler) GetSupervisionSubject(w http.ResponseWriter, r *http.Request) {
	ws, _, ok := h.supervisionScope(w, r, false)
	if !ok {
		return
	}
	id := chi.URLParam(r, "issueId")
	issueUUID, ok := parseUUIDOrBadRequest(w, id, "issue_id")
	if !ok {
		return
	}
	issue, err := h.Queries.GetIssue(r.Context(), issueUUID)
	if err != nil || uuidToString(issue.WorkspaceID) != ws || !healthIssueVisible(issue, requestUserID(r)) {
		writeError(w, 404, "任务不存在")
		return
	}
	var subject json.RawMessage
	err = h.DB.QueryRow(r.Context(), `SELECT (to_jsonb(s)-'next_attempt_at')||jsonb_build_object('stopped',s.next_attempt_at='infinity'::timestamptz,'next_attempt_at',CASE WHEN isfinite(s.next_attempt_at) THEN s.next_attempt_at ELSE NULL END) FROM supervision_subject s WHERE workspace_id=$1 AND issue_id=$2`, ws, id).Scan(&subject)
	if err != nil {
		writeError(w, 404, "任务尚未登记")
		return
	}
	member, allowed := h.requireWorkspaceMember(w, r, ws, "工作区不存在")
	if !allowed {
		return
	}
	canManage := member.Role == "owner" || member.Role == "admin"
	var findings, batches, notes, actions, investigations json.RawMessage
	for _, query := range []struct {
		sql  string
		dest *json.RawMessage
	}{
		{`SELECT COALESCE(jsonb_agg(x),'[]') FROM (SELECT id,batch_id,created_at,request,result-'text' result FROM supervision_investigation WHERE workspace_id=$1 AND issue_id=$2 ORDER BY created_at DESC LIMIT 100)x`, &investigations},
		{`SELECT COALESCE(jsonb_agg(to_jsonb(f) ORDER BY f.last_seen_at DESC),'[]') FROM supervision_finding f WHERE workspace_id=$1 AND issue_id=$2`, &findings},
		{`SELECT COALESCE(jsonb_agg(x),'[]') FROM (SELECT b.id,b.task_id,b.status,b.error,b.rules_version,b.created_at,b.completed_at,EXTRACT(EPOCH FROM (COALESCE(t.started_at,t.completed_at,now())-b.created_at)) queue_seconds,
 EXTRACT(EPOCH FROM (COALESCE(t.completed_at,now())-t.started_at)) execution_seconds,
 EXTRACT(EPOCH FROM (COALESCE(b.completed_at,now())-t.completed_at)) persistence_seconds,jsonb_array_length(b.materials) material_count FROM supervision_batch b LEFT JOIN agent_task_queue t ON t.id=b.task_id WHERE b.workspace_id=$1 AND b.issue_id=$2 ORDER BY b.created_at DESC LIMIT 30)x`, &batches},
		{`SELECT COALESCE(jsonb_agg(x ORDER BY x.created_at),'[]') FROM (SELECT id,content,created_at FROM comment WHERE issue_id=$2 AND author_type='member' AND lower(ltrim(content))~'^/note([[:space:]]|$)' AND EXISTS(SELECT 1 FROM issue WHERE id=$2 AND workspace_id=$1))x`, &notes},
		{`SELECT COALESCE(jsonb_agg(x),'[]') FROM (SELECT a.id,a.finding_id,a.actor_id,a.action,a.reason,a.receipt,a.created_at FROM supervision_action a JOIN supervision_finding f ON f.id=a.finding_id AND f.workspace_id=a.workspace_id WHERE a.workspace_id=$1 AND f.issue_id=$2 ORDER BY a.created_at DESC LIMIT 50)x`, &actions},
	} {
		if err = h.DB.QueryRow(r.Context(), query.sql, ws, id).Scan(query.dest); err != nil {
			writeError(w, 500, "读取审查详情失败")
			return
		}
	}
	if canManage {
		err = h.DB.QueryRow(r.Context(), `SELECT COALESCE(jsonb_agg(x),'[]') FROM (
            SELECT b.id,b.task_id,b.status,b.error,b.rules_version,b.created_at,b.completed_at,EXTRACT(EPOCH FROM (COALESCE(t.started_at,t.completed_at,now())-b.created_at)) queue_seconds,
 EXTRACT(EPOCH FROM (COALESCE(t.completed_at,now())-t.started_at)) execution_seconds,
 EXTRACT(EPOCH FROM (COALESCE(b.completed_at,now())-t.completed_at)) persistence_seconds,jsonb_array_length(b.materials) material_count,
              NULLIF(t.context->>'instructions','') instructions, NULLIF(t.context->>'instructions_sha256','') instructions_sha256
            FROM supervision_batch b LEFT JOIN agent_task_queue t ON t.id=b.task_id
            WHERE b.workspace_id=$1 AND b.issue_id=$2 ORDER BY b.created_at DESC LIMIT 30)x`, ws, id).Scan(&batches)
		if err != nil {
			writeError(w, 500, "读取审查指令失败")
			return
		}
	}
	var coverage json.RawMessage
	err = h.DB.QueryRow(r.Context(), `SELECT jsonb_build_object('materials',count(*),'covered',count(*) FILTER(WHERE reviewed_generation>=generation),'runs',count(DISTINCT run_id),
 'partial',count(*) FILTER(WHERE reviewed_generation<generation AND reviewed_offset>0),
 'pending',count(*) FILTER(WHERE reviewed_generation<generation AND reviewed_offset=0),
 'current',(SELECT jsonb_build_object('kind',m.kind,'id',m.source_id,'offset',m.reviewed_offset,'length',(
 SELECT (part->>'length')::integer FROM supervision_batch b CROSS JOIN LATERAL jsonb_array_elements(b.materials) part
 WHERE b.workspace_id=m.workspace_id AND b.issue_id=m.issue_id AND part->>'id'=m.source_id::text AND (part->>'generation')::bigint=m.generation
 ORDER BY b.created_at DESC LIMIT 1)) FROM supervision_material m WHERE m.workspace_id=$1 AND m.issue_id=$2 AND m.reviewed_generation<m.generation AND m.reviewed_offset>0 ORDER BY m.changed_at,m.source_id LIMIT 1)) FROM supervision_material WHERE workspace_id=$1 AND issue_id=$2`, ws, id).Scan(&coverage)
	if err != nil {
		writeError(w, 500, "读取覆盖范围失败")
		return
	}
	writeJSON(w, 200, map[string]any{"subject": subject, "title": issue.Title, "description": issue.Description.String, "assignee_type": textToPtr(issue.AssigneeType), "assignee_id": uuidToPtr(issue.AssigneeID), "findings": findings, "batches": batches, "notes": notes, "actions": actions, "investigations": investigations, "coverage": coverage})
}

func (h *Handler) CheckSupervision(w http.ResponseWriter, r *http.Request) {
	ws, _, ok := h.supervisionScope(w, r, true)
	if !ok {
		return
	}
	var body struct {
		IssueID     string `json:"issue_id"`
		Full        bool   `json:"full"`
		Stop        bool   `json:"stop"`
		RetryFailed bool   `json:"retry_failed"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		writeError(w, 400, "检查请求格式错误")
		return
	}
	if body.IssueID != "" {
		if _, ok := parseUUIDOrBadRequest(w, body.IssueID, "issue_id"); !ok {
			return
		}
	}
	if body.Stop {
		tx, err := h.TxStarter.Begin(r.Context())
		if err != nil {
			writeError(w, 500, "停止检查失败")
			return
		}
		defer tx.Rollback(r.Context())
		// 与派发使用相同的配置锁，先阻止补位，再取消全部相关运行。
		var enabled bool
		if err = tx.QueryRow(r.Context(), `SELECT enabled FROM supervision_config WHERE workspace_id=$1 FOR UPDATE`, ws).Scan(&enabled); err != nil {
			writeError(w, 500, "读取监督配置失败")
			return
		}
		if body.IssueID == "" {
			if _, err = tx.Exec(r.Context(), `UPDATE supervision_config SET enabled=false WHERE workspace_id=$1`, ws); err != nil {
				writeError(w, 500, "暂停监督失败")
				return
			}
		}
		if _, err = tx.Exec(r.Context(), `UPDATE supervision_subject SET next_attempt_at='infinity' WHERE workspace_id=$1 AND $2<>'' AND issue_id=NULLIF($2,'')::uuid`, ws, body.IssueID); err != nil {
			writeError(w, 500, "保存停止状态失败")
			return
		}
		rows, err := tx.Query(r.Context(), `SELECT task_id::text FROM supervision_batch WHERE workspace_id=$1 AND ($2='' OR issue_id=NULLIF($2,'')::uuid) AND status IN('preparing','running') AND task_id IS NOT NULL`, ws, body.IssueID)
		if err != nil {
			writeError(w, 500, "读取当前检查失败")
			return
		}
		var tasks []string
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				rows.Close()
				writeError(w, 500, "读取当前检查失败")
				return
			}
			tasks = append(tasks, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			writeError(w, 500, "读取当前检查失败")
			return
		}
		if err = tx.Commit(r.Context()); err != nil {
			writeError(w, 500, "保存停止状态失败")
			return
		}
		for _, task := range tasks {
			if _, err = h.TaskService.CancelTaskByUser(r.Context(), parseUUID(task)); err != nil {
				writeError(w, 500, "已阻止新检查，但部分运行取消失败，请重试停止")
				return
			}
			if _, err = h.DB.Exec(r.Context(), `UPDATE supervision_batch SET status='cancelled',completed_at=now() WHERE workspace_id=$1 AND task_id=$2 AND status IN('preparing','running')`, ws, task); err != nil {
				writeError(w, 500, "保存停止状态失败")
				return
			}
		}
		writeJSON(w, 200, map[string]any{"stopped": true, "count": len(tasks)})
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, 500, "检查请求失败")
		return
	}
	defer tx.Rollback(r.Context())
	var enabled bool
	err = tx.QueryRow(r.Context(), `SELECT enabled FROM supervision_config WHERE workspace_id=$1`, ws).Scan(&enabled)
	if err != nil {
		writeError(w, 409, "请先配置并启用监督")
		return
	}
	if !enabled {
		writeError(w, 409, "监督已暂停，请先恢复监督再重新审查")
		return
	}
	if body.IssueID != "" {
		var subject string
		if err = tx.QueryRow(r.Context(), `SELECT issue_id::text FROM supervision_subject WHERE workspace_id=$1 AND issue_id=$2 FOR UPDATE`, ws, body.IssueID).Scan(&subject); err != nil {
			writeError(w, 404, "任务尚未登记")
			return
		}
	}
	if body.Full {
		if body.IssueID == "" {
			writeError(w, 400, "重新审查需要指定任务")
			return
		}
		_, err = tx.Exec(r.Context(), `UPDATE supervision_material SET generation=generation+1,reviewed_offset=0 WHERE workspace_id=$1 AND issue_id=$2`, ws, body.IssueID)
		if err == nil {
			_, err = tx.Exec(r.Context(), `UPDATE supervision_subject SET desired_version=desired_version+1 WHERE workspace_id=$1 AND issue_id=$2`, ws, body.IssueID)
		}
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `UPDATE supervision_subject SET force_review=true,next_attempt_at=now(),failures=0,error='' WHERE workspace_id=$1 AND ($2='' OR issue_id=NULLIF($2,'')::uuid) AND (NOT $3::boolean OR failures>=3)`, ws, body.IssueID, body.RetryFailed)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		writeError(w, 500, "安排检查失败")
		return
	}
	writeJSON(w, 202, map[string]bool{"scheduled": true})
}

func (h *Handler) UpdateSupervisionFinding(w http.ResponseWriter, r *http.Request) {
	ws, user, ok := h.supervisionScope(w, r, true)
	if !ok {
		return
	}
	id := chi.URLParam(r, "findingId")
	if _, ok := parseUUIDOrBadRequest(w, id, "finding_id"); !ok {
		return
	}
	var body struct {
		Status       string     `json:"status"`
		Reason       string     `json:"reason"`
		OperationID  string     `json:"operation_id"`
		SnoozedUntil *time.Time `json:"snoozed_until"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&body) != nil {
		writeError(w, 400, "处理请求格式错误")
		return
	}
	if body.Status != "open" && body.Status != "in_progress" && body.Status != "resolved" && body.Status != "ignored" {
		writeError(w, 400, "处理状态无效")
		return
	}
	if body.Status == "ignored" && strings.TrimSpace(body.Reason) == "" {
		writeError(w, 400, "忽略问题需要填写理由")
		return
	}
	if _, ok := parseUUIDOrBadRequest(w, body.OperationID, "operation_id"); !ok {
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, 500, "保存处理失败")
		return
	}
	defer tx.Rollback(r.Context())
	var issue string
	if err = tx.QueryRow(r.Context(), `SELECT issue_id::text FROM supervision_finding WHERE workspace_id=$1 AND id=$2`, ws, id).Scan(&issue); err != nil {
		writeError(w, 404, "问题不存在")
		return
	}
	var subject string
	if err = tx.QueryRow(r.Context(), `SELECT issue_id::text FROM supervision_subject WHERE workspace_id=$1 AND issue_id=$2 FOR UPDATE`, ws, issue).Scan(&subject); err != nil {
		writeError(w, 404, "任务尚未登记")
		return
	}
	tag, err := tx.Exec(r.Context(), `INSERT INTO supervision_action(workspace_id,finding_id,actor_id,operation_id,action,reason,receipt,created_at) VALUES($1,$2,$3,$4,$5,$6,jsonb_build_object('snoozed_until',$7::timestamptz),clock_timestamp()) ON CONFLICT(workspace_id,operation_id) DO NOTHING`, ws, id, user, body.OperationID, body.Status, body.Reason, body.SnoozedUntil)
	if err == nil && tag.RowsAffected() == 0 {
		var same bool
		err = tx.QueryRow(r.Context(), `SELECT finding_id=$3::uuid AND action=$4 AND reason=$5 AND (receipt->>'snoozed_until')::timestamptz IS NOT DISTINCT FROM $6::timestamptz FROM supervision_action WHERE workspace_id=$1 AND operation_id=$2`, ws, body.OperationID, id, body.Status, body.Reason, body.SnoozedUntil).Scan(&same)
		if err == nil && !same {
			writeError(w, 409, "同一操作编号不能更换问题或处理内容")
			return
		}
	}
	if err == nil && tag.RowsAffected() > 0 {
		_, err = tx.Exec(r.Context(), `UPDATE supervision_finding SET status=$3,human_resolution=($3='resolved'),snoozed_until=$4 WHERE workspace_id=$1 AND id=$2`, ws, id, body.Status, body.SnoozedUntil)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		writeError(w, 500, fmt.Sprintf("保存处理失败：%v", err))
		return
	}
	writeJSON(w, 200, map[string]bool{"saved": true})
}

// deleteSupervisionData 与既有删除事务共同清理任务或工作区的监督记录。
func deleteSupervisionData(ctx context.Context, tx pgx.Tx, ws string, issue string) error {
	for _, query := range []string{
		`DELETE FROM supervision_investigation WHERE workspace_id=$1 AND ($2='' OR issue_id::text=$2)`,
		`DELETE FROM supervision_action WHERE workspace_id=$1 AND ($2='' OR finding_id IN(SELECT id FROM supervision_finding WHERE workspace_id=$1 AND issue_id=NULLIF($2,'')::uuid))`,
		`DELETE FROM supervision_finding WHERE workspace_id=$1 AND ($2='' OR issue_id=NULLIF($2,'')::uuid)`,
		`DELETE FROM supervision_material WHERE workspace_id=$1 AND ($2='' OR issue_id=NULLIF($2,'')::uuid)`,
		`DELETE FROM supervision_batch WHERE workspace_id=$1 AND ($2='' OR issue_id=NULLIF($2,'')::uuid)`,
		`DELETE FROM supervision_subject WHERE workspace_id=$1 AND ($2='' OR issue_id=NULLIF($2,'')::uuid)`,
		`DELETE FROM supervision_config WHERE workspace_id=$1 AND $2=''`,
	} {
		if _, err := tx.Exec(ctx, query, ws, issue); err != nil {
			return err
		}
	}
	return nil
}

// CanUseSupervisionAgent 在每次派发时重新核对配置负责人的现有权限。
func (h *Handler) CanUseSupervisionAgent(ctx context.Context, ws, owner, agentID string) bool {
	member, err := h.getWorkspaceMember(ctx, owner, ws)
	if err != nil || (member.Role != "owner" && member.Role != "admin") {
		return false
	}
	agent, err := h.Queries.GetAgent(ctx, parseUUID(agentID))
	return err == nil && uuidToString(agent.WorkspaceID) == ws && !agent.ArchivedAt.Valid && h.canInvokeAgent(ctx, agent, "member", owner, owner, ws)
}

func (h *Handler) GetSupervisionEvidence(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	ws, _, ok := h.supervisionScope(w, r, false)
	if !ok {
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "sourceId"), "source_id")
	if !ok {
		return
	}
	offset, err := strconv.Atoi(r.URL.Query().Get("offset"))
	if r.URL.Query().Get("offset") == "" {
		offset = 0
		err = nil
	}
	if err != nil || offset < 0 {
		writeError(w, 400, "读取位置无效")
		return
	}
	if h.Supervision == nil {
		writeError(w, 503, "监督读取服务尚未就绪")
		return
	}
	var page supervision.EvidencePage
	if batch := r.URL.Query().Get("batch"); batch != "" {
		batchID, valid := parseUUIDOrBadRequest(w, batch, "batch_id")
		if !valid {
			return
		}
		page, err = h.Supervision.ReadBatchEvidence(r.Context(), ws, uuidToString(batchID), chi.URLParam(r, "kind"), uuidToString(id))
	} else {
		page, err = h.Supervision.ReadEvidence(r.Context(), ws, chi.URLParam(r, "kind"), uuidToString(id), offset)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 404, "证据不存在或已不可访问")
		return
	}
	if err != nil {
		writeError(w, 500, "读取原始证据失败")
		return
	}
	writeJSON(w, 200, page)
}

// GetSupervisionBatchTask 为已有实录组件提供监督运行的真实任务元数据。
func (h *Handler) GetSupervisionBatchTask(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	ws, _, ok := h.supervisionScope(w, r, false)
	if !ok {
		return
	}
	batch, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "batchId"), "batch_id")
	if !ok {
		return
	}
	var taskID string
	if h.DB.QueryRow(r.Context(), `SELECT task_id::text FROM supervision_batch WHERE workspace_id=$1 AND id=$2 AND task_id IS NOT NULL`, ws, batch).Scan(&taskID) != nil {
		writeError(w, 404, "审查运行不存在")
		return
	}
	task, err := h.Queries.GetAgentTask(r.Context(), parseUUID(taskID))
	if err != nil || h.TaskService.ResolveTaskWorkspaceID(r.Context(), task) != ws {
		writeError(w, 404, "审查运行已不可访问")
		return
	}
	writeJSON(w, 200, taskToResponse(task, ws))
}
