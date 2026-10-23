package handler

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/multica-ai/multica/server/pkg/redact"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// recordDeliveryProgress 使用既有验收事件表保存交付动作，与阶段更新同事务提交。
func recordDeliveryProgress(ctx context.Context, tx pgx.Tx, ws, delivery, action, previous string, generation int64) error {
	_, err := tx.Exec(ctx, `INSERT INTO verification_event(workspace_id,delivery_id,generation,candidate_id,stage,detail)
 SELECT workspace_id,id,$3,candidate_id,'delivery.'||$4,
 jsonb_build_object('action',$4::text,'previous_phase',$5::text,'phase',phase,'revision',revision)::text
 FROM issue_delivery WHERE workspace_id=$1 AND id=$2`, ws, delivery, generation, action, previous)
	return err
}

type deliveryDiagnosticEvent struct {
	ID          string    `json:"id"`
	At          time.Time `json:"at"`
	Stage       string    `json:"stage"`
	Environment string    `json:"environment"`
	Generation  int64     `json:"generation"`
	Candidate   string    `json:"candidate_id"`
	Detail      string    `json:"detail,omitempty"`
	UntilNextMS *int64    `json:"until_next_recorded_event_ms,omitempty"`
}

type deliveryDiagnosticCursor struct {
	Since time.Time `json:"since"`
	After string    `json:"after"`
}

func deliveryDiagnosticWindow(r *http.Request) (int, time.Time, string, error) {
	limit := 50
	if text := r.URL.Query().Get("limit"); text != "" {
		n, err := strconv.Atoi(text)
		if err != nil || n < 1 || n > 200 {
			return 0, time.Time{}, "", errors.New("limit 必须为1–200")
		}
		limit = n
	}
	var since time.Time
	if text := r.URL.Query().Get("since"); text != "" {
		var err error
		since, err = time.Parse(time.RFC3339Nano, text)
		if err != nil {
			return 0, time.Time{}, "", errors.New("since 需要RFC3339时间")
		}
	}
	after := r.URL.Query().Get("after")
	if after != "" && since.IsZero() {
		return 0, time.Time{}, "", errors.New("after 必须同时提供since")
	}
	return limit, since, after, nil
}

func diagnosticIntervals(events []deliveryDiagnosticEvent) {
	for i := 0; i+1 < len(events); i++ {
		n := events[i+1].At.Sub(events[i].At).Milliseconds()
		events[i].UntilNextMS = &n
	}
}

// GetDeliveryDiagnostics 只读单项交付；历史按时间与ID增量读取，不读取轨迹正文或写入租约。
func (h *Handler) GetDeliveryDiagnostics(w http.ResponseWriter, r *http.Request) {
	ws, issue, ok := h.deliveryIssue(w, r)
	if !ok {
		return
	}
	limit, since, after, err := deliveryDiagnosticWindow(r)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	if after != "" {
		if _, ok = parseUUIDOrBadRequest(w, after, "after"); !ok {
			return
		}
	} else {
		after = "00000000-0000-0000-0000-000000000000"
	}
	delivery := r.URL.Query().Get("delivery_id")
	if delivery != "" {
		if _, ok = parseUUIDOrBadRequest(w, delivery, "delivery_id"); !ok {
			return
		}
	}
	var state json.RawMessage
	err = h.DB.QueryRow(r.Context(), `SELECT jsonb_build_object('id',id,'phase',phase,'source_commit',source_commit,'candidate_id',candidate_id,
 'created_at',created_at,'updated_at',updated_at,'error',left(error,512),'revision',revision,
 'authorization_pending',COALESCE(plan_snapshot->>'authorization_pending','false')='true','execution_stage',(SELECT stage FROM verification_event e WHERE e.delivery_id=issue_delivery.id AND e.stage NOT LIKE 'delivery.%' ORDER BY created_at DESC,id DESC LIMIT 1))
 FROM issue_delivery WHERE workspace_id=$1 AND issue_id=$2 AND ($3='' OR id=NULLIF($3,'')::uuid) ORDER BY created_at DESC,id DESC LIMIT 1`, ws, issue, delivery).Scan(&state)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 404, "交付不存在")
		return
	}
	if err != nil {
		writeError(w, 500, "读取交付诊断失败")
		return
	}
	var selected struct {
		ID                   string    `json:"id"`
		Phase                string    `json:"phase"`
		ExecutionStage       string    `json:"execution_stage"`
		UpdatedAt            time.Time `json:"updated_at"`
		Error                string    `json:"error"`
		AuthorizationPending bool      `json:"authorization_pending"`
	}
	if json.Unmarshal(state, &selected) != nil {
		writeError(w, 500, "交付诊断格式错误")
		return
	}
	query := `SELECT id::text,created_at,stage,environment,generation,candidate_id,left(detail,512)
 FROM verification_event WHERE workspace_id=$1 AND delivery_id=$2 AND stage<>'heartbeat'
 AND ($3::timestamptz IS NULL OR (created_at,id)>($3,$4::uuid)) ORDER BY created_at DESC,id DESC LIMIT $5`
	if !since.IsZero() {
		query = `SELECT id::text,created_at,stage,environment,generation,candidate_id,left(detail,512)
 FROM verification_event WHERE workspace_id=$1 AND delivery_id=$2 AND stage<>'heartbeat'
 AND ($3::timestamptz IS NULL OR (created_at,id)>($3,$4::uuid)) ORDER BY created_at ASC,id ASC LIMIT $5`
	}
	rows, err := h.DB.Query(r.Context(), query, ws, selected.ID, diagnosticSince(since), after, limit+1)
	if err != nil {
		writeError(w, 500, "读取交付阶段失败")
		return
	}
	events := []deliveryDiagnosticEvent{}
	for rows.Next() {
		var e deliveryDiagnosticEvent
		if err = rows.Scan(&e.ID, &e.At, &e.Stage, &e.Environment, &e.Generation, &e.Candidate, &e.Detail); err != nil {
			break
		}
		events = append(events, e)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		writeError(w, 500, "读取交付阶段失败")
		return
	}
	truncated := len(events) > limit
	if truncated {
		events = events[:limit]
	}
	if since.IsZero() {
		for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
			events[i], events[j] = events[j], events[i]
		}
	}
	for i := range events {
		events[i].Detail = redact.Text(events[i].Detail)
	}
	diagnosticIntervals(events)
	var cursor *deliveryDiagnosticCursor
	if len(events) > 0 {
		last := events[len(events)-1]
		cursor = &deliveryDiagnosticCursor{Since: last.At, After: last.ID}
	}
	var resources json.RawMessage
	err = h.DB.QueryRow(r.Context(), `SELECT jsonb_build_object(
 'publication',(SELECT jsonb_build_object('resource','int-prod','state',state,'expired',COALESCE(expires_at<=now(),false),'delivery_id',CASE WHEN workspace_id=$1 THEN delivery_id ELSE NULL END,'same_batch',EXISTS(SELECT 1 FROM delivery_batch_member me JOIN delivery_batch b ON b.id=me.batch_id JOIN delivery_batch_member holder ON holder.batch_id=b.id WHERE me.delivery_id=$2 AND holder.delivery_id=delivery_publication.delivery_id AND b.workspace_id=$1 AND b.status IN('frozen','preparing','validating','publishing') AND me.result='pending' AND holder.result='pending')) FROM delivery_publication WHERE resource='int-prod'),
 'slots',(SELECT COALESCE(jsonb_agg(jsonb_build_object('name',s.name,'enabled',s.enabled,'state',s.state,'seed_verified',COALESCE(v.verified,false),'delivery_id',CASE WHEN s.workspace_id=$1 THEN s.delivery_id ELSE NULL END) ORDER BY s.name),'[]') FROM verification_slot s LEFT JOIN verification_slot_seed v ON v.name=s.name))`, ws, selected.ID).Scan(&resources)
	if err != nil {
		writeError(w, 500, "读取阻塞资源失败")
		return
	}
	// 只展示本任务最近10次执行；usage原字段分开保留，不能把缺上报当零成本。
	var runs json.RawMessage
	err = h.DB.QueryRow(r.Context(), `SELECT COALESCE(jsonb_agg(jsonb_build_object('task_id',q.id,'status',q.status,'attempt',q.attempt,'max_attempts',q.max_attempts,'retry_of_task_id',q.retry_of_task_id,'rerun_of_task_id',q.rerun_of_task_id,
 'created_at',q.created_at,'started_at',q.started_at,'completed_at',q.completed_at,'failure_reason',q.failure_reason,'wait_reason',q.wait_reason,
 'queue_wait_ms',CASE WHEN q.started_at IS NOT NULL THEN EXTRACT(EPOCH FROM(q.started_at-q.created_at))*1000 END,
 'run_ms',CASE WHEN q.completed_at IS NOT NULL AND q.started_at IS NOT NULL THEN EXTRACT(EPOCH FROM(q.completed_at-q.started_at))*1000 END,
 'latest_trace',(SELECT jsonb_build_object('event_type',t.event_type,'at',t.created_at,'duration_ms',t.duration_ms,'attempt',t.attempt) FROM task_trace_event t WHERE t.task_id=q.id ORDER BY t.created_at DESC,t.id DESC LIMIT 1),
 'usage',(SELECT jsonb_build_object('available',count(*)>0,'rows',count(*),'input_tokens',sum(u.input_tokens),'output_tokens',sum(u.output_tokens),'cache_read_tokens',sum(u.cache_read_tokens),'cache_write_tokens',sum(u.cache_write_tokens)) FROM task_usage u WHERE u.task_id=q.id)) ORDER BY q.created_at DESC),'[]')
 FROM(SELECT id,status,attempt,max_attempts,retry_of_task_id,rerun_of_task_id,created_at,started_at,completed_at,failure_reason,wait_reason FROM agent_task_queue WHERE issue_id=$1 ORDER BY created_at DESC,id DESC LIMIT 10)q`, issue).Scan(&runs)
	if err != nil {
		writeError(w, 500, "读取最近执行摘要失败")
		return
	}
	retries := 0
	for _, e := range events {
		if strings.HasSuffix(e.Stage, ".retry") || e.Stage == "resumed" {
			retries++
		}
	}
	var phaseAt *time.Time
	// 仅 phase 实际变化的事件标记阶段起点；心跳和同阶段动作不能重置耗时。
	err = h.DB.QueryRow(r.Context(), `SELECT created_at FROM verification_event WHERE workspace_id=$1 AND delivery_id=$2 AND stage LIKE 'delivery.%' AND detail::jsonb->>'phase'=$3 AND (detail::jsonb->>'previous_phase' IS DISTINCT FROM detail::jsonb->>'phase') ORDER BY created_at DESC,id DESC LIMIT 1`, ws, selected.ID, selected.Phase).Scan(&phaseAt)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 500, "读取阶段起点失败")
		return
	}
	observed := time.Now().UTC()
	summary := deliveryDiagnosticSummary(selected.Phase, selected.ExecutionStage, selected.ID, selected.AuthorizationPending, selected.Error, resources, phaseAt, observed)
	var safeState map[string]any
	if json.Unmarshal(state, &safeState) == nil {
		safeState["error"] = redact.Text(selected.Error)
		state, _ = json.Marshal(safeState)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"delivery": state, "events": events, "resources": resources, "recent_issue_runs": runs, "run_association": "issue", "observed_retry_or_resume_events": retries, "summary": summary, "observed_at": observed, "truncated_older_events": truncated && since.IsZero(), "has_more_newer_events": truncated && !since.IsZero(), "next_cursor": cursor, "history_boundary": "只计算已记录事件之间的间隔；旧交付或截断窗口不能还原完整阶段耗时，issue执行不等同本候选执行"})
}

func diagnosticSince(since time.Time) any {
	if since.IsZero() {
		return nil
	}
	return since
}

// deliveryDiagnosticSummary 只解释当前已记录的等待条件；无确定证据时不猜测故障。
func deliveryDiagnosticSummary(phase, stage, id string, authorizationPending bool, failure string, resources json.RawMessage, started *time.Time, now time.Time) map[string]any {
	var r struct {
		Publication *struct {
			State     string `json:"state"`
			Delivery  string `json:"delivery_id"`
			Expired   bool   `json:"expired"`
			SameBatch bool   `json:"same_batch"`
		} `json:"publication"`
		Slots []struct {
			Name     string `json:"name"`
			Enabled  bool   `json:"enabled"`
			State    string `json:"state"`
			Verified bool   `json:"seed_verified"`
			Delivery string `json:"delivery_id"`
		} `json:"slots"`
	}
	_ = json.Unmarshal(resources, &r)
	result := map[string]any{"phase": phase, "phase_started_at": started, "phase_elapsed_ms": nil, "blocked_by": []string{}, "message": "当前阶段没有可确认的外部阻塞；查看最近事件与执行"}
	if started != nil {
		result["phase_elapsed_ms"] = max(int64(0), now.Sub(*started).Milliseconds())
	}
	switch phase {
	case "waiting_int", "approved", "preparing_release":
		if r.Publication != nil && r.Publication.State != "idle" && r.Publication.Delivery != id && !r.Publication.SameBatch {
			result["blocked_by"] = []string{"int-prod"}
			result["message"] = "共享集成/发布资源由另一项交付持有"
			result["holder_delivery_id"] = r.Publication.Delivery
			result["holder_expired"] = r.Publication.Expired
		}
	case "testing":
		held, available := false, false
		for _, slot := range r.Slots {
			held = held || slot.Delivery == id
			available = available || (slot.Enabled && slot.State == "idle" && slot.Verified)
		}
		if stage == "building" {
			result["message"] = "正在构建固定源码；构建日志包含依赖、Web、Go阶段计时"
		}
		if stage == "waiting_slot" && !held && !available {
			result["blocked_by"] = []string{"verification_pool"}
			result["message"] = "当前没有已启用且种子校验通过的空闲验收槽"
		}
	case "awaiting_review":
		if authorizationPending {
			result["blocked_by"] = []string{"owner_review"}
			result["message"] = "等待负责人完成候选与发布授权复核"
		} else {
			result["message"] = "等待审查或发布授权；以交付状态中的 authorization_state 为准"
		}
	case "needs_changes":
		result["blocked_by"] = []string{"changes_required"}
		result["message"] = redact.Text(failure)
	case "published", "cancelled":
		result["message"] = "交付已结束"
	}
	return result
}
