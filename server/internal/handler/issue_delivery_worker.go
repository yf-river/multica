package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/service"
	"net/http"
	"strings"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

func (h *Handler) deliveryOperator(w http.ResponseWriter, r *http.Request) (string, bool) {
	ws := h.resolveWorkspaceID(r)
	if !h.deliveryHuman(r, ws) {
		writeError(w, 403, "仅外部发布器或工作区管理员可调用")
		return "", false
	}
	member, err := h.getWorkspaceMember(r.Context(), requestUserID(r), ws)
	if err != nil || (member.Role != "owner" && member.Role != "admin") {
		writeError(w, 403, "需要工作区管理员权限")
		return "", false
	}
	return ws, true
}
func (h *Handler) ListDeliveryQueue(w http.ResponseWriter, r *http.Request) {
	ws, ok := h.deliveryOperator(w, r)
	if !ok {
		return
	}
	var data json.RawMessage
	current := r.URL.Query().Get("current")
	if current != "" {
		if _, ok = parseUUIDOrBadRequest(w, current, "delivery_id"); !ok {
			return
		}
	}
	err := h.DB.QueryRow(r.Context(), `SELECT COALESCE(jsonb_agg(x ORDER BY priority DESC,created_at),'[]') FROM (SELECT d.*,(d.phase='awaiting_review' AND d.approved_by IS NULL AND d.acceptance_submitted AND d.plan_snapshot->>'integration_complete' IS DISTINCT FROM 'true' AND EXISTS(SELECT 1 FROM jsonb_array_elements(COALESCE(d.plan_snapshot->'required_checks','[]')) c WHERE c->>'stage'='integration')) AS integration_pending,i.number,i.project_id,i.status AS issue_status,COALESCE((SELECT pn.stage FROM delivery_notice pn WHERE pn.delivery_id=d.id AND pn.stage LIKE 'verification_update_failed:%' AND pn.delivered_at IS NULL ORDER BY pn.created_at LIMIT 1),'') AS pending_update_notice,COALESCE((SELECT src.work_dir FROM agent_task_queue src WHERE src.id::text=d.plan_snapshot->>'source_execution_id' AND src.issue_id=d.issue_id AND src.status='completed'),'') AS source_work_dir,(NOT EXISTS(SELECT 1 FROM agent_task_queue src WHERE src.id::text=d.plan_snapshot->>'source_execution_id') OR EXISTS(SELECT 1 FROM agent_task_queue src WHERE src.id::text=d.plan_snapshot->>'source_execution_id' AND src.issue_id=d.issue_id AND src.status='completed' AND COALESCE(src.work_dir,'')<>'')) AS source_ready,EXISTS(SELECT 1 FROM delivery_evidence be WHERE be.delivery_id=d.id AND be.workspace_id=d.workspace_id AND be.issue_id=d.issue_id AND be.candidate_id=d.candidate_id AND be.source_commit=d.plan_snapshot->>'candidate_commit') AS integration_accepted,(SELECT jsonb_build_object('state',r.state,'head_sha',r.head_sha) FROM issue_review r WHERE r.issue_id=i.id AND r.workspace_id=i.workspace_id) AS final_review,(SELECT jsonb_build_object('id',q.id,'status',q.status,'error',q.error,'settled',q.completed_at<now()-interval '30 seconds','failure_notice',EXISTS(SELECT 1 FROM delivery_notice fn WHERE fn.delivery_id=d.id AND fn.stage='verification_failed:'||d.candidate_id||':'||q.id::text)) FROM agent_task_queue q WHERE q.issue_id=d.issue_id AND (q.trigger_comment_id=CASE WHEN d.phase='approved' THEN (SELECT n.comment_id FROM delivery_notice n WHERE n.delivery_id=d.id AND n.stage='batch_ready') WHEN d.phase='preparing_release' THEN d.release_comment_id ELSE d.verification_comment_id END OR CASE WHEN d.phase='approved' THEN (SELECT n.comment_id FROM delivery_notice n WHERE n.delivery_id=d.id AND n.stage='batch_ready') WHEN d.phase='preparing_release' THEN d.release_comment_id ELSE d.verification_comment_id END=ANY(q.coalesced_comment_ids) OR EXISTS(SELECT 1 FROM delivery_notice vn WHERE vn.delivery_id=d.id AND (vn.stage LIKE 'verification_failed:'||d.candidate_id||':%' OR vn.stage LIKE 'verification_update_failed:%') AND (vn.comment_id=q.trigger_comment_id OR vn.comment_id=ANY(q.coalesced_comment_ids)))) ORDER BY q.created_at DESC LIMIT 1) AS verification_run,(SELECT count(*) FROM delivery_notice vn WHERE vn.delivery_id=d.id AND vn.stage LIKE 'verification_failed:'||d.candidate_id||':%') AS verification_failure_count,(SELECT jsonb_build_object('execution_id',o.execution_id,'generation',o.generation,'expires_at',o.expires_at,'external',NOT EXISTS(SELECT 1 FROM agent_task_queue native WHERE native.id::text=o.execution_id)) FROM delivery_owner o WHERE o.workspace_id=d.workspace_id AND o.issue_id=d.issue_id AND o.expires_at>now()) AS verification_owner,CASE WHEN d.id=NULLIF($2,'')::uuid THEN 3 WHEN d.phase IN('testing','preparing_release','publishing') THEN 2 WHEN d.phase IN('waiting_int','approved') THEN 1 ELSE 0 END AS priority FROM issue_delivery d JOIN issue i ON i.id=d.issue_id AND i.workspace_id=d.workspace_id WHERE d.workspace_id=$1 AND (d.phase IN('waiting_int','testing','approved','preparing_release','publishing') OR (d.phase='awaiting_review' AND d.approved_by IS NULL AND d.acceptance_submitted AND d.plan_snapshot->>'integration_complete' IS DISTINCT FROM 'true' AND EXISTS(SELECT 1 FROM jsonb_array_elements(COALESCE(d.plan_snapshot->'required_checks','[]')) c WHERE c->>'stage'='integration')) OR (d.phase='awaiting_review' AND d.plan_snapshot->>'authorization_pending'='true') OR (d.phase='awaiting_review' AND COALESCE(d.plan_snapshot->>'authorization_stalled','')<>'true' AND NOT EXISTS(SELECT 1 FROM agent_task_queue aq WHERE aq.issue_id=d.issue_id AND aq.status IN('queued','dispatched','running','waiting_local_directory')) AND ((d.plan_snapshot->'authorization_notice' IS NOT NULL AND COALESCE((d.plan_snapshot->'authorization_notice'->>'notified_at')::timestamptz,'-infinity'::timestamptz)<=now()-interval '10 minutes') OR (d.plan_snapshot->'authorization_notice' IS NULL AND d.plan_snapshot->>'authorization_pending' IS DISTINCT FROM 'true' AND EXISTS(SELECT 1 FROM issue_delivery_authorization a JOIN comment c ON c.id=a.comment_id AND c.workspace_id=a.workspace_id AND c.issue_id=a.issue_id AND c.revision=a.comment_revision JOIN member m ON m.workspace_id=c.workspace_id AND m.user_id=c.author_id WHERE a.workspace_id=d.workspace_id AND a.issue_id=d.issue_id AND a.plan_id=d.plan_id AND a.kind='release' AND NOT a.revoked AND c.author_type='member' AND m.role IN ('owner','admin','member'))))) OR d.id=NULLIF($2,'')::uuid OR EXISTS(SELECT 1 FROM delivery_notice n WHERE n.delivery_id=d.id AND d.phase IN ('published','needs_changes') AND n.stage=d.phase AND n.delivered_at IS NULL)) ORDER BY priority DESC,d.created_at LIMIT 100)x`, ws, current).Scan(&data)
	if err != nil {
		writeError(w, 500, "读取交付队列失败")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, data)
}
func (h *Handler) AdvanceDelivery(w http.ResponseWriter, r *http.Request) {
	ws, ok := h.deliveryOperator(w, r)
	if !ok {
		return
	}
	var body struct {
		ID              string          `json:"id"`
		Revision        int64           `json:"revision"`
		Action          string          `json:"action"`
		Token           string          `json:"worker_token"`
		Candidate       string          `json:"candidate_id"`
		URL             string          `json:"candidate_url"`
		Error           string          `json:"error"`
		VerificationRun string          `json:"verification_run_id"`
		Source          string          `json:"source_commit"`
		Identity        json.RawMessage `json:"candidate_identity"`
		Generation      int64           `json:"publication_generation"`
		SlotGeneration  int64           `json:"slot_generation"`
		ValidationPlan  json.RawMessage `json:"validation_plan"`
		CandidateCommit string          `json:"candidate_commit"`
		Stage           string          `json:"stage"`
		Checks          []deliveryCheck `json:"evidence"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&body) != nil {
		writeError(w, 400, "交付更新格式错误")
		return
	}
	if _, ok = parseUUIDOrBadRequest(w, body.ID, "delivery_id"); !ok {
		return
	}
	if body.Token != "" {
		if _, ok = parseUUIDOrBadRequest(w, body.Token, "worker_token"); !ok {
			return
		}
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, 500, "更新交付失败")
		return
	}
	defer tx.Rollback(r.Context())
	var phase, token, issue, commit string
	var rev int64
	var approved, integrationOnly bool
	err = tx.QueryRow(r.Context(), `SELECT phase,COALESCE(worker_token::text,''),issue_id::text,source_commit,revision,(approved_by IS NOT NULL OR auto_release),COALESCE(plan_snapshot->>'integration_only'='true',false) FROM issue_delivery WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, ws, body.ID).Scan(&phase, &token, &issue, &commit, &rev, &approved, &integrationOnly)
	if err != nil {
		writeError(w, 404, "交付不存在")
		return
	}
	// 修改 3. 组合就绪通知不改交付阶段，成员记录可能刚被固定组合改写：按当前版本通知，
	// 由 delivery_notice 唯一键保证只发一次，其余动作仍要求提交时看到的版本。
	if rev != body.Revision && !(body.Action == "notify-result" && body.Stage == "batch_ready") {
		writeError(w, 409, "交付已变化")
		return
	}
	if body.Action == "notify-result" {
		// 修改 1. 组合候选就绪时成员还在授权阶段：只按持久通知号提醒一次组合验收，不改交付阶段。
		batchReady := body.Stage == "batch_ready" && phase == "approved"
		if token == "" || token != body.Token || (phase != "published" && phase != "needs_changes" && !batchReady) {
			writeError(w, 409, "当前交付不允许通知结果")
			return
		}
		if err = tx.Commit(r.Context()); err != nil {
			writeError(w, 500, "读取通知状态失败")
			return
		}
		deferred, noticeErr := h.notifyDeliveryResult(r, ws, issue, body.ID, ternaryStage(batchReady, "batch_ready", phase))
		if noticeErr != nil {
			writeError(w, 500, "结果通知失败，后台将重试")
			return
		}
		writeJSON(w, 200, map[string]any{"id": body.ID, "phase": phase, "revision": rev, "worker_token": token, "notification_deferred": deferred})
		return
	}
	if body.Action == "notify-verification-failure" {
		if phase != "testing" || token == "" || token != body.Token || body.VerificationRun == "" || strings.TrimSpace(body.Error) == "" {
			writeError(w, 409, "当前测试执行不能发送失败通知")
			return
		}
		if _, ok = parseUUIDOrBadRequest(w, body.VerificationRun, "verification_run_id"); !ok {
			return
		}
		ended, checkErr := deliveryVerificationRunEnded(r.Context(), tx, ws, issue, body.ID, body.VerificationRun)
		var managed bool
		if checkErr == nil {
			checkErr = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM issue i JOIN release_agent_config c ON c.workspace_id=i.workspace_id AND c.project_id=i.project_id AND c.enabled WHERE i.workspace_id=$1 AND i.id=$2)`, ws, issue).Scan(&managed)
		}
		if checkErr != nil || !ended || !managed {
			writeError(w, 409, "测试执行或项目状态已变化")
			return
		}
		var candidate string
		if err = tx.QueryRow(r.Context(), `SELECT candidate_id FROM issue_delivery WHERE id=$1`, body.ID).Scan(&candidate); err != nil || candidate == "" {
			writeError(w, 409, "当前测试候选已变化")
			return
		}
		stage := "verification_failed:" + candidate + ":" + body.VerificationRun
		_, err = tx.Exec(r.Context(), `INSERT INTO delivery_notice(delivery_id,stage,comment_id) VALUES($1,$2,$3) ON CONFLICT(delivery_id,stage) DO NOTHING`, body.ID, stage, dbid.NewV7())
		if err == nil {
			_, err = tx.Exec(r.Context(), `UPDATE issue_delivery SET error=$2 WHERE id=$1`, body.ID, body.Error)
		}
		if err == nil {
			err = tx.Commit(r.Context())
		}
		if err != nil {
			writeError(w, 500, "记录测试失败通知失败")
			return
		}
		deferred, noticeErr := h.notifyDeliveryResult(r, ws, issue, body.ID, stage)
		if noticeErr != nil {
			writeError(w, 500, "发送测试失败通知失败")
			return
		}
		writeJSON(w, 200, map[string]any{"id": body.ID, "phase": phase, "revision": rev, "worker_token": token, "notification_deferred": deferred})
		return
	}
	if body.Action == "verification-update-failed" {
		if phase != "testing" || token == "" || token != body.Token || body.Source != commit || strings.TrimSpace(body.Error) == "" {
			writeError(w, 409, "当前测试更新不能标记失败")
			return
		}
		var operation string
		err = tx.QueryRow(r.Context(), `SELECT plan_snapshot->'verification_update'->'request'->>'operation_id' FROM issue_delivery WHERE id=$1 AND plan_snapshot->'verification_update'->>'state'='pending'`, body.ID).Scan(&operation)
		if err != nil {
			writeError(w, 409, "测试更新已变化")
			return
		}
		stage := "verification_update_failed:" + operation
		_, err = tx.Exec(r.Context(), `UPDATE issue_delivery SET plan_snapshot=jsonb_set(plan_snapshot,'{verification_update,state}','"failed"'::jsonb),error=$2,revision=revision+1,updated_at=now() WHERE id=$1`, body.ID, body.Error)
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO delivery_notice(delivery_id,stage,comment_id) VALUES($1,$2,$3) ON CONFLICT(delivery_id,stage) DO NOTHING`, body.ID, stage, dbid.NewV7())
		}
		if err == nil {
			err = tx.Commit(r.Context())
		}
		if err != nil {
			writeError(w, 500, "记录测试更新失败")
			return
		}
		deferred, noticeErr := h.notifyDeliveryResult(r, ws, issue, body.ID, stage)
		if noticeErr != nil {
			deferred = true
		}
		writeJSON(w, 200, map[string]any{"id": body.ID, "phase": phase, "revision": rev + 1, "worker_token": token, "notification_deferred": deferred})
		return
	}
	if body.Action == "notify-pending-verification" {
		if phase != "testing" || token == "" || token != body.Token || !strings.HasPrefix(body.Stage, "verification_update_failed:") {
			writeError(w, 409, "测试通知已变化")
			return
		}
		if err = tx.Commit(r.Context()); err != nil {
			writeError(w, 500, "读取测试通知失败")
			return
		}
		deferred, noticeErr := h.notifyDeliveryResult(r, ws, issue, body.ID, body.Stage)
		if noticeErr != nil {
			writeError(w, 500, "发送测试通知失败")
			return
		}
		writeJSON(w, 200, map[string]any{"id": body.ID, "phase": phase, "revision": rev, "worker_token": token, "notification_deferred": deferred})
		return
	}
	if body.Action == "begin-verification-update" {
		if phase != "testing" || token == "" || token != body.Token || body.Source != commit || !deliveryCommit.MatchString(body.CandidateCommit) {
			writeError(w, 409, "测试更新缺少准确候选或占用身份")
			return
		}
		var slotName, pendingSource, pendingBranch, pendingBase, oldCandidate string
		var generation int64
		err = tx.QueryRow(r.Context(), `SELECT s.name,s.generation,d.plan_snapshot->'verification_update'->'request'->>'source_commit',d.plan_snapshot->'verification_update'->'request'->>'source_branch',d.plan_snapshot->'verification_update'->'request'->>'base_commit',s.candidate_id FROM verification_slot s JOIN issue_delivery d ON d.id=s.delivery_id WHERE d.id=$1 AND d.workspace_id=$2 AND s.workspace_id=$2 AND s.worker_token::text=$3 AND s.source_commit=$4 AND s.state='verifying' AND s.lease_expires_at>now() AND s.generation=$5 AND d.plan_snapshot->'verification_update'->>'state'='pending' FOR UPDATE OF s`, body.ID, ws, token, commit, body.SlotGeneration).Scan(&slotName, &generation, &pendingSource, &pendingBranch, &pendingBase, &oldCandidate)
		if err != nil || pendingSource != body.CandidateCommit || !deliveryBranch.MatchString(pendingBranch) || !deliveryCommit.MatchString(pendingBase) {
			writeError(w, 409, "测试更新或槽位代次已变化")
			return
		}
		_, err = tx.Exec(r.Context(), `UPDATE verification_slot SET source_commit=$2,candidate_id='',state='preparing',generation=generation+1,lease_expires_at=now()+interval '2 minutes',updated_at=now() WHERE name=$1`, slotName, pendingSource)
		if err == nil {
			_, err = tx.Exec(r.Context(), `UPDATE issue_delivery SET source_commit=$2,source_branch=$3,base_commit=$4,candidate_id='',candidate_url='',verification_comment_id=$5,acceptance_submitted=false,evidence='[]'::jsonb,plan_snapshot=jsonb_set((plan_snapshot-'candidate_commit'-'candidate_identity'),'{verification_update,state}','"applying"'::jsonb),error='',revision=revision+1,updated_at=now() WHERE id=$1`, body.ID, pendingSource, pendingBranch, pendingBase, dbid.NewV7())
		}
		if err == nil {
			err = recordDeliveryProgress(r.Context(), tx, ws, body.ID, "verification_update", phase, generation+1)
		}
		if err == nil {
			err = tx.Commit(r.Context())
		}
		if err != nil {
			writeError(w, 500, "保存测试更新失败")
			return
		}
		writeJSON(w, 200, map[string]any{"id": body.ID, "phase": phase, "revision": rev + 1, "worker_token": token, "source_commit": pendingSource, "candidate_id": "", "slot_generation": generation + 1, "previous_candidate": oldCandidate})
		return
	}
	if (body.Action == "validated" || body.Action == "publishing") && (phase != "preparing_release" || token == "" || token != body.Token) {
		writeError(w, 409, "当前阶段或领取凭据不允许发布复核")
		return
	}
	if (body.Action == "claim" && phase == "approved") || body.Action == "publishing" || body.Action == "validated" {
		ready, checkErr := deliveryAuthorizationReady(r.Context(), tx, ws, issue, body.ID)
		var planValid bool
		if checkErr == nil {
			checkErr = tx.QueryRow(r.Context(), deliveryPlanValidSQL, body.ID).Scan(&planValid)
		}
		ready = ready && planValid
		if checkErr != nil {
			writeError(w, 500, "核对发布授权失败")
			return
		}
		if !ready {
			// 1. 复核失效时按授权状态分开处理：有授权但缺当前候选复核才落库待复核并唤醒负责人，
			// 没有授权或已撤销只记录等待原因，不能自动批准。
			state, authorization, stateErr := deliveryReleaseState(r.Context(), tx, ws, issue, body.ID)
			if stateErr != nil {
				writeError(w, 500, "核对发布授权失败")
				return
			}
			reason := "等待用户发布授权：当前候选没有有效发布授权"
			notify := false
			if state == deliveryReleaseUnbound {
				var notice json.RawMessage
				notice, err = deliveryAuthorizationNoticeFor(r.Context(), tx, ws, issue, body.ID, authorization)
				if err == nil {
					_, err = tx.Exec(r.Context(), `UPDATE issue_delivery SET plan_snapshot=jsonb_set(plan_snapshot,'{authorization_notice}',$2::jsonb)||'{"authorization_pending":true}'::jsonb,phase='awaiting_review',error=$3,approved_by=NULL,approved_at=NULL,auto_release=false,release_validated=false,revision=revision+1,updated_at=now() WHERE id=$1`, body.ID, notice, "已有发布授权，等待负责人复核当前候选")
				}
				reason = "已有发布授权，等待负责人复核当前候选"
				notify = true
			} else {
				_, err = tx.Exec(r.Context(), `UPDATE issue_delivery SET plan_snapshot=plan_snapshot-'authorization_pending'-'authorization_notice',phase='awaiting_review',error=$2,approved_by=NULL,approved_at=NULL,auto_release=false,release_validated=false,revision=revision+1,updated_at=now() WHERE id=$1`, body.ID, reason)
			}
			if err == nil {
				err = recordDeliveryProgress(r.Context(), tx, ws, body.ID, "authorization_wait", phase, body.Generation)
			}
			if err == nil {
				err = tx.Commit(r.Context())
			}
			if err != nil {
				writeError(w, 500, "保存授权待核对状态失败")
				return
			}
			// 2. 通知失败不丢工作：待复核意图留在记录里，下一次队列轮询会重试。
			if notify {
				_ = h.deliverDeliveryAuthorizationReview(r, ws, issue, body.ID)
			}
			writeError(w, 409, reason)
			return
		}
	}
	if body.Action == "notify-authorization" {
		if phase != "awaiting_review" {
			writeError(w, 409, "当前无需授权核对")
			return
		}
		var pending bool
		if err = tx.QueryRow(r.Context(), `SELECT COALESCE(plan_snapshot->>'authorization_pending','')='true' FROM issue_delivery WHERE id=$1`, body.ID).Scan(&pending); err != nil {
			writeError(w, 500, "读取待复核状态失败")
			return
		}
		if err = tx.Commit(r.Context()); err != nil {
			writeError(w, 500, "读取待复核状态失败")
			return
		}
		// 3. 自动轮询只补发尚未送达的通知，同一交付和候选不重复唤醒；待复核意图已送达时
		// 再调用被视为显式接续，按次数上限补发。
		if pending {
			err = h.deliverDeliveryAuthorizationReview(r, ws, issue, body.ID)
		} else {
			err = h.renotifyDeliveryAuthorizationReview(r, ws, issue, body.ID)
		}
		if err != nil {
			writeError(w, 409, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"id": body.ID, "phase": phase, "revision": rev, "worker_token": token, "issue_id": issue})
		return
	}
	// 池化交付的回执同时核对候选及有效占用；旧 int 交付没有槽位记录。
	if phase == "preparing_release" || phase == "publishing" {
		if body.Action != "notify-authorization" {
			var owned bool
			err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM delivery_publication WHERE resource='int-prod' AND workspace_id=$1 AND delivery_id=$2 AND worker_token::text=$3 AND generation=$4 AND state='active' AND expires_at>now())`, ws, body.ID, body.Token, body.Generation).Scan(&owned)
			if err != nil || !owned {
				writeError(w, 409, "最终发布占用已失效；禁止旧发布器继续推进")
				return
			}
		}
	}
	if phase == "testing" && (body.Action == "prepared" || body.Action == "accepted" || body.Action == "notify" || body.Action == "tested" || body.Action == "request-final-review") {
		var valid bool
		candidate := body.Candidate
		err = tx.QueryRow(r.Context(), `SELECT NOT EXISTS(SELECT 1 FROM verification_slot WHERE delivery_id=$1) OR EXISTS(SELECT 1 FROM verification_slot s JOIN issue_delivery d ON d.id=s.delivery_id WHERE s.delivery_id=$1 AND s.workspace_id=$2 AND s.worker_token::text=$3 AND s.source_commit=$4 AND s.state='verifying' AND s.lease_expires_at>now() AND s.candidate_id=CASE WHEN $5='' THEN d.candidate_id ELSE $5 END)`, body.ID, ws, token, commit, candidate).Scan(&valid)
		if err != nil {
			writeError(w, 500, "核对验收占用失败")
			return
		}
		if !valid {
			writeError(w, 409, "验收槽位或候选已失效")
			return
		}
	}
	next := phase
	// 5. 待复核意图先随事务落库，提交后再定向通知；通知失败由队列重试，不影响状态真源。
	notifyReview := false
	automaticComplete := false
	projectReleaseReady := false
	requestFinalReview := false
	switch body.Action {
	case "claim-integration":
		// Modification 1. Reuse the serialized int stage without granting production approval.
		if phase != "awaiting_review" || approved {
			writeError(w, 409, "当前交付不能领取仅集成验收")
			return
		}
		var valid bool
		if tx.QueryRow(r.Context(), deliveryPlanValidSQL, body.ID).Scan(&valid) != nil || !valid {
			writeError(w, 409, "方案已变化")
			return
		}
		if err = requireStoredDeliveryChecks(r.Context(), tx, body.ID, "verification"); err != nil {
			writeError(w, 409, err.Error())
			return
		}
		var eligible bool
		if err = tx.QueryRow(r.Context(), `SELECT acceptance_submitted AND plan_snapshot->>'integration_complete' IS DISTINCT FROM 'true' AND EXISTS(SELECT 1 FROM jsonb_array_elements(plan_snapshot->'required_checks') c WHERE c->>'stage'='integration') FROM issue_delivery WHERE id=$1`, body.ID).Scan(&eligible); err != nil || !eligible {
			writeError(w, 409, "缺少独立验收或已完成集成")
			return
		}
		// Retain the verification candidate in delivery_evidence; the new int identity must supply its own checks.
		_, err = tx.Exec(r.Context(), `UPDATE issue_delivery SET plan_snapshot=plan_snapshot||'{"integration_only":true}'::jsonb,release_comment_id=gen_random_uuid(),acceptance_submitted=false,candidate_id='',candidate_url='' WHERE id=$1`, body.ID)
		if err != nil {
			writeError(w, 500, "登记集成验收失败")
			return
		}
		token = dbid.NewV7().String()
		next = "preparing_release"
	case "claim":
		if phase == "waiting_int" {
			var project, reviewed bool
			err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM issue i JOIN release_agent_config c ON c.workspace_id=i.workspace_id AND c.project_id=i.project_id AND c.enabled WHERE i.workspace_id=$1 AND i.id=$2),EXISTS(SELECT 1 FROM issue i JOIN issue_review r ON r.issue_id=i.id AND r.workspace_id=i.workspace_id AND r.state='passed' AND r.head_sha=$3 WHERE i.workspace_id=$1 AND i.id=$2 AND i.status='in_progress')`, ws, issue, commit).Scan(&project, &reviewed)
			if err != nil {
				writeError(w, 500, "读取审查状态失败")
				return
			}
			if project && !reviewed {
				writeError(w, 409, "当前提交需要先完成代码审查")
				return
			}
		}

		var valid bool
		if tx.QueryRow(r.Context(), deliveryPlanValidSQL, body.ID).Scan(&valid) != nil || !valid {
			_, err = tx.Exec(r.Context(), `UPDATE issue_delivery SET phase='needs_changes',error='方案已变化，需要重新确认',revision=revision+1 WHERE id=$1`, body.ID)
			if err == nil {
				err = recordDeliveryProgress(r.Context(), tx, ws, body.ID, "plan_invalid", phase, body.Generation)
			}
			if err == nil {
				err = tx.Commit(r.Context())
			}
			if err != nil {
				writeError(w, 500, "记录方案失效失败")
				return
			}
			writeError(w, 409, "方案已变化，需要重新确认")
			return
		}
		if phase != "waiting_int" && phase != "approved" {
			writeError(w, 409, "该交付不能领取")
			return
		}
		if phase == "approved" && !approved {
			writeError(w, 403, "缺少生产批准")
			return
		}
		token = dbid.NewV7().String()
		next = "testing"
		if phase == "approved" {
			next = "preparing_release"
		}
	default:
		if body.Action == "fail" && token == "" && (phase == "waiting_int" || phase == "approved") {
			token = dbid.NewV7().String()
			body.Token = token
		}
		if token == "" || token != body.Token {
			writeError(w, 403, "发布器领取凭据已失效")
			return
		}
		switch body.Action {
		case "prepared":
			if phase != "testing" && phase != "preparing_release" {
				writeError(w, 409, "当前阶段不能登记联调")
				return
			}
			if strings.TrimSpace(body.Candidate) == "" || !strings.HasPrefix(body.URL, "http") {
				writeError(w, 400, "缺少联调版本或地址")
				return
			}
		case "tested":
			batchCandidate := ""
			if phase == "approved" {
				batchCandidate, err = batchAcceptanceCandidate(r, tx, ws, body.ID)
				if err != nil {
					writeError(w, 500, "读取组合验收状态失败")
					return
				}
				if batchCandidate == "" || body.Candidate != batchCandidate {
					writeError(w, 409, "需要代表交付及准确组合候选")
					return
				}
			}
			if (phase != "testing" && phase != "preparing_release" && batchCandidate == "") || !validDeliveryChecks(body.Checks) {
				writeError(w, 409, "当前候选缺少实际自动验收证据")
				return
			}
			stage := "verification"
			if phase != "testing" {
				stage = "integration"
			}
			var actualCandidate string
			if err = tx.QueryRow(r.Context(), `SELECT candidate_id FROM issue_delivery WHERE id=$1`, body.ID).Scan(&actualCandidate); err != nil {
				writeError(w, 500, "读取当前候选失败")
				return
			}
			checkErr := requireDeliveryChecks(r.Context(), tx, body.ID, actualCandidate, stage, body.Checks)
			var gap *deliveryChecksGap
			if checkErr != nil && !errors.As(checkErr, &gap) {
				writeError(w, 500, checkErr.Error())
				return
			}
			automaticComplete = checkErr == nil
			checks, _ := json.Marshal(body.Checks)
			if automaticComplete {
				_, err = tx.Exec(r.Context(), `INSERT INTO delivery_evidence(id,workspace_id,issue_id,delivery_id,candidate_id,source_commit,identity,evidence) SELECT $1,workspace_id,issue_id,id,candidate_id,COALESCE(plan_snapshot->>'candidate_commit',source_commit),COALESCE(plan_snapshot->'candidate_identity','{}'),$3 FROM issue_delivery WHERE id=$2 AND candidate_id<>'' ON CONFLICT(delivery_id,candidate_id) DO UPDATE SET evidence=EXCLUDED.evidence`, dbid.NewV7(), body.ID, checks)
			}
			if err == nil {
				_, err = tx.Exec(r.Context(), `UPDATE issue_delivery SET acceptance_submitted=$3,evidence=$2 WHERE id=$1 AND candidate_id<>''`, body.ID, checks, automaticComplete)
			}
			if err != nil {
				writeError(w, 500, "记录自动验收证据失败")
				return
			}

		case "request-final-review":
			var allowed bool
			err = tx.QueryRow(r.Context(), `SELECT d.phase='testing' AND d.acceptance_submitted AND EXISTS(SELECT 1 FROM issue i JOIN release_agent_config c ON c.workspace_id=i.workspace_id AND c.project_id=i.project_id AND c.enabled WHERE i.id=d.issue_id AND i.workspace_id=d.workspace_id) FROM issue_delivery d WHERE d.id=$1`, body.ID).Scan(&allowed)
			if err != nil || !allowed {
				writeError(w, 409, "当前交付不能请求最终审查")
				return
			}
			if err = requireStoredDeliveryChecks(r.Context(), tx, body.ID, "verification"); err != nil {
				writeError(w, 409, err.Error())
				return
			}
			requestFinalReview = true
		case "accepted":
			if err = requireStoredDeliveryChecks(r.Context(), tx, body.ID, "verification"); err != nil {
				writeError(w, 409, err.Error())
				return
			}
			if phase != "testing" {
				writeError(w, 409, "当前阶段不能完成联调")
				return
			}
			var submitted bool
			if tx.QueryRow(r.Context(), `SELECT acceptance_submitted FROM issue_delivery WHERE id=$1`, body.ID).Scan(&submitted) != nil || !submitted {
				writeError(w, 409, "缺少功能验收证据")
				return
			}
			var managed, held, reviewed bool
			err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM issue i JOIN release_agent_config c ON c.workspace_id=i.workspace_id AND c.project_id=i.project_id AND c.enabled WHERE i.id=$1 AND i.workspace_id=$2),COALESCE((SELECT metadata->>'release_hold'='true' FROM issue WHERE id=$1 AND workspace_id=$2),false),EXISTS(SELECT 1 FROM issue_review r JOIN issue_delivery d ON d.issue_id=r.issue_id AND d.workspace_id=r.workspace_id WHERE d.id=$3 AND r.state='passed' AND r.head_sha=d.source_commit AND EXISTS(SELECT 1 FROM delivery_evidence e WHERE e.delivery_id=d.id AND e.issue_id=d.issue_id AND e.workspace_id=d.workspace_id AND e.source_commit=d.source_commit AND e.candidate_id=d.candidate_id))`, issue, ws, body.ID).Scan(&managed, &held, &reviewed)
			if err != nil {
				writeError(w, 500, "核对任务审查与测试失败")
				return
			}
			if managed && !held {
				if !reviewed {
					writeError(w, 409, "当前提交需要完成审查后再进入待发布")
					return
				}
				commentsVersion, versionErr := deliveryCommentsVersion(r.Context(), tx, ws, issue)
				if versionErr != nil {
					writeError(w, 500, "核对任务评论失败")
					return
				}
				_, err = tx.Exec(r.Context(), `UPDATE issue_delivery SET auto_release=true,approved_by=NULL,approved_at=now(),plan_snapshot=jsonb_set(plan_snapshot-'authorization_pending'-'authorization_notice','{project_comments_version}',to_jsonb(COALESCE(plan_snapshot->>'project_comments_version',$2::text))),error='' WHERE id=$1`, body.ID, commentsVersion)
				if err != nil {
					writeError(w, 500, "登记待发布失败")
					return
				}
				next = "approved"
				projectReleaseReady = true
				break
			}
			next = "awaiting_review"
			// 4. int 验收完成后按授权状态分流：已复核当前候选直接推进；有授权但缺候选复核
			// 落库待复核并定向唤醒负责人；没有有效授权只记录等待原因。
			state, authorization, stateErr := deliveryReleaseState(r.Context(), tx, ws, issue, body.ID)
			if stateErr != nil {
				writeError(w, 500, "读取授权复核失败")
				return
			}
			switch state {
			case deliveryReleaseReady:
				var author string
				err = tx.QueryRow(r.Context(), `SELECT COALESCE(a.comment_snapshot->>'author_id',d.approved_by::text) FROM issue_delivery d LEFT JOIN issue_delivery_authorization a ON a.id::text=d.plan_snapshot->'authorization_review'->>'authorization_id' AND a.kind='release' WHERE d.id=$1`, body.ID).Scan(&author)
				if err == nil {
					_, err = tx.Exec(r.Context(), `UPDATE issue_delivery SET approved_by=$2,approved_at=now(),plan_snapshot=plan_snapshot-'authorization_pending'-'authorization_notice' WHERE id=$1`, body.ID, author)
					next = "approved"
				}
			case deliveryReleaseUnbound:
				var notice json.RawMessage
				notice, err = deliveryAuthorizationNoticeFor(r.Context(), tx, ws, issue, body.ID, authorization)
				if err == nil {
					body.Error = "已有发布授权，等待负责人复核当前候选"
					notifyReview = true
					_, err = tx.Exec(r.Context(), `UPDATE issue_delivery SET plan_snapshot=jsonb_set(plan_snapshot,'{authorization_notice}',$2::jsonb)||'{"authorization_pending":true}'::jsonb,approved_by=NULL,approved_at=NULL,auto_release=false WHERE id=$1`, body.ID, notice)
				}
			default:
				body.Error = "等待用户发布授权：当前候选没有有效发布授权"
				_, err = tx.Exec(r.Context(), `UPDATE issue_delivery SET plan_snapshot=plan_snapshot-'authorization_pending'-'authorization_notice',approved_by=NULL,approved_at=NULL,auto_release=false WHERE id=$1`, body.ID)
			}
			if err != nil {
				writeError(w, 500, "保存授权复核状态失败")
				return
			}
		case "integrated":
			// Modification 2. Int evidence completes review only; publishing still requires independent authorization.
			if phase != "preparing_release" || !integrationOnly || approved {
				writeError(w, 409, "当前交付不是仅集成验收")
				return
			}
			if err = requireStoredDeliveryChecks(r.Context(), tx, body.ID, "integration"); err != nil {
				writeError(w, 409, err.Error())
				return
			}
			var submitted bool
			if tx.QueryRow(r.Context(), `SELECT acceptance_submitted AND candidate_id=$2 AND candidate_id<>'' FROM issue_delivery WHERE id=$1`, body.ID, body.Candidate).Scan(&submitted) != nil || !submitted {
				writeError(w, 409, "缺少准确 int 候选验收")
				return
			}
			_, err = tx.Exec(r.Context(), `UPDATE issue_delivery SET plan_snapshot=plan_snapshot||'{"integration_complete":true}'::jsonb WHERE id=$1`, body.ID)
			if err != nil {
				writeError(w, 500, "保存集成结果失败")
				return
			}
			next = "awaiting_review"
		case "validated":
			if err = requireStoredDeliveryChecks(r.Context(), tx, body.ID, "integration"); err != nil {
				writeError(w, 409, err.Error())
				return
			}
			var submitted bool
			if tx.QueryRow(r.Context(), `SELECT acceptance_submitted FROM issue_delivery WHERE id=$1`, body.ID).Scan(&submitted) != nil || !submitted {
				writeError(w, 409, "缺少当前候选的功能验收证据")
				return
			}
			if phase != "preparing_release" || !approved {
				writeError(w, 409, "缺少生产复核条件")
				return
			}
		case "notify":
			if phase != "testing" && phase != "preparing_release" {
				writeError(w, 409, "当前阶段不能通知验证")
				return
			}
		case "publishing":
			if err = requireStoredDeliveryChecks(r.Context(), tx, body.ID, "integration"); err != nil {
				writeError(w, 409, err.Error())
				return
			}
			var validated bool
			if tx.QueryRow(r.Context(), `SELECT release_validated AND acceptance_submitted FROM issue_delivery WHERE id=$1`, body.ID).Scan(&validated) != nil || !validated {
				writeError(w, 409, "发布候选尚未完成复核")
				return
			}
			if phase != "preparing_release" || !approved {
				writeError(w, 409, "未完成生产确认")
				return
			}
			next = "publishing"
		case "published":
			if phase != "publishing" || !approved {
				writeError(w, 409, "当前阶段不能标记已发布")
				return
			}
			next = "published"
		case "fail":
			if body.Error == "" {
				writeError(w, 400, "需要具体失败原因")
				return
			}
			next = "needs_changes"
		case "retry":
			if phase != "testing" && phase != "preparing_release" && !(phase == "needs_changes" && approved) {
				writeError(w, 409, "当前阶段不能排队")
				return
			}
			next = "waiting_int"
			if approved {
				next = "approved"
			}
		default:
			writeError(w, 400, "未知发布器操作")
			return
		}
	}
	if body.Source != "" && body.Source != commit {
		writeError(w, 409, "交付源码版本已变化")
		return
	}
	if body.Action == "notify" || (body.Action == "fail" && body.VerificationRun != "") {
		external, checkErr := externalDeliveryVerificationOwner(r.Context(), tx, ws, issue)
		if checkErr != nil {
			writeError(w, 500, "核对验收归属失败")
			return
		}
		if body.Action == "fail" {
			if _, valid := parseUUIDOrBadRequest(w, body.VerificationRun, "verification_run_id"); !valid {
				return
			}
			ended, checkErr := deliveryVerificationRunEnded(r.Context(), tx, ws, issue, body.ID, body.VerificationRun)
			if checkErr != nil {
				writeError(w, 500, "核对原生验收执行失败")
				return
			}
			if !ended || (phase != "testing" && phase != "preparing_release") {
				writeError(w, 409, "原生验收执行或交付阶段已变化")
				return
			}
		}
		if external {
			if err = tx.Commit(r.Context()); err != nil {
				writeError(w, 500, "保存验收归属检查失败")
				return
			}
			writeJSON(w, 200, map[string]any{"id": body.ID, "phase": phase, "revision": rev, "worker_token": token, "verification_deferred": true})
			return
		}
	}
	if body.Action != "notify" {
		_, err = tx.Exec(r.Context(), `UPDATE issue_delivery SET acceptance_submitted=CASE WHEN $8='prepared' THEN EXISTS(SELECT 1 FROM delivery_evidence e WHERE e.delivery_id=issue_delivery.id AND e.identity=$9::jsonb AND e.identity<>'{}'::jsonb) ELSE acceptance_submitted END,release_comment_id=CASE WHEN $8='claim' AND phase='approved' THEN gen_random_uuid() ELSE release_comment_id END,release_validated=CASE WHEN $8='validated' THEN true WHEN $8 IN ('claim','retry') THEN false ELSE release_validated END,phase=$3,worker_token=$4,candidate_id=CASE WHEN $5='' THEN candidate_id ELSE $5 END,candidate_url=CASE WHEN $6='' THEN candidate_url ELSE $6 END,error=$7,revision=revision+1,updated_at=now() WHERE workspace_id=$1 AND id=$2`, ws, body.ID, next, token, body.Candidate, body.URL, body.Error, body.Action, identityOrEmpty(body.Identity))
	}
	if err == nil && body.Action == "prepared" && len(body.Identity) > 0 {
		_, err = tx.Exec(r.Context(), `INSERT INTO delivery_evidence(id,workspace_id,issue_id,delivery_id,candidate_id,source_commit,identity,evidence) SELECT $1,d.workspace_id,d.issue_id,d.id,d.candidate_id,COALESCE(NULLIF($4,''),d.source_commit),e.identity,e.evidence FROM issue_delivery d JOIN LATERAL(SELECT identity,evidence FROM delivery_evidence WHERE delivery_id=d.id AND identity=$3::jsonb AND identity<>'{}'::jsonb ORDER BY created_at DESC LIMIT 1)e ON true WHERE d.id=$2 ON CONFLICT(delivery_id,candidate_id) DO NOTHING`, dbid.NewV7(), body.ID, body.Identity, body.CandidateCommit)
		if err != nil {
			writeError(w, 500, "保存复用依据失败")
			return
		}

		_, err = tx.Exec(r.Context(), `UPDATE issue_delivery SET plan_snapshot=jsonb_set(plan_snapshot,'{candidate_identity}',$2::jsonb) WHERE id=$1`, body.ID, body.Identity)
		if err == nil {
			_, err = tx.Exec(r.Context(), `UPDATE issue_delivery d SET evidence=e.evidence FROM delivery_evidence e WHERE e.delivery_id=d.id AND e.candidate_id=d.candidate_id AND d.id=$1 AND d.acceptance_submitted`, body.ID)
		}
	}
	if err == nil && body.Action == "prepared" {
		_, err = tx.Exec(r.Context(), `UPDATE issue_delivery SET plan_snapshot=jsonb_set(plan_snapshot,'{verification_update,state}','"applied"'::jsonb) WHERE id=$1 AND plan_snapshot->'verification_update'->>'state'='applying'`, body.ID)
	}
	if err == nil && body.Action == "prepared" {
		err = tx.QueryRow(r.Context(), `SELECT acceptance_submitted FROM issue_delivery WHERE id=$1`, body.ID).Scan(&automaticComplete)
		if err == nil && automaticComplete {
			stage := "verification"
			if phase == "preparing_release" {
				stage = "integration"
			}
			checkErr := requireStoredDeliveryChecks(r.Context(), tx, body.ID, stage)
			var gap *deliveryChecksGap
			if checkErr != nil && !errors.As(checkErr, &gap) {
				err = checkErr
			} else if checkErr != nil {
				automaticComplete = false
				_, err = tx.Exec(r.Context(), `UPDATE issue_delivery SET acceptance_submitted=false WHERE id=$1`, body.ID)
			}
		}
	}
	if err == nil && body.Action == "prepared" && len(body.ValidationPlan) > 0 {
		_, err = tx.Exec(r.Context(), `UPDATE issue_delivery SET plan_snapshot=jsonb_set(plan_snapshot,'{validation_plan}',$2::jsonb) WHERE id=$1`, body.ID, body.ValidationPlan)
	}
	if err == nil && body.Action == "prepared" && body.CandidateCommit != "" {
		if !deliveryCommit.MatchString(body.CandidateCommit) {
			writeError(w, 400, "候选需要完整提交")
			return
		}
		_, err = tx.Exec(r.Context(), `UPDATE issue_delivery SET plan_snapshot=jsonb_set(plan_snapshot,'{candidate_commit}',to_jsonb($2::text)) WHERE id=$1`, body.ID, body.CandidateCommit)
	}
	if err == nil && (next == "published" || next == "needs_changes") {
		_, err = tx.Exec(r.Context(), `INSERT INTO delivery_notice(delivery_id,stage,comment_id) VALUES($1,$2,$3) ON CONFLICT(delivery_id,stage) DO NOTHING`, body.ID, next, dbid.NewV7())
	}
	var issueEvent *events.Event
	// 1. 交付验收通过也把任务推进到审核中；把前后两版任务行留到事务外，提交后
	// 与手动改状态共用同一个后处理入口，避免这条写入路径单独漏掉审查派发和清绿勾。
	var previous, updated db.Issue
	if err == nil && (body.Action == "integrated" || body.Action == "validated" || projectReleaseReady || requestFinalReview) {
		q := h.Queries.WithTx(tx)
		previous, err = q.GetIssue(r.Context(), parseUUID(issue))
		if err == nil {
			if projectReleaseReady {
				_, err = tx.Exec(r.Context(), `UPDATE issue SET status='pending_release',metadata=metadata||jsonb_build_object('release_workflow',true,'verification_passed',true,'verification_commit',$3::text,'integration_passed',false,'integration_commit',''),revision=revision+1,updated_at=now() WHERE workspace_id=$1 AND id=$2`, ws, issue, commit)
			} else {
				_, err = tx.Exec(r.Context(), `UPDATE issue SET status='in_review',revision=revision+1,updated_at=now() WHERE workspace_id=$1 AND id=$2`, ws, issue)
			}
		}
		if err == nil {
			updated, err = q.GetIssue(r.Context(), parseUUID(issue))
			if err == nil {
				status := "in_review"
				if projectReleaseReady {
					status = "pending_release"
				}
				event, recordErr := service.RecordDurableEventTx(r.Context(), q, buildIssueUpdatedEvent(r.Context(), h, updated, previous, UpdateIssueRequest{Status: &status}, nil, "member", requestUserID(r)))
				err = recordErr
				issueEvent = &event
			}
		}
	}
	if err == nil && body.Action != "notify" {
		err = recordDeliveryProgress(r.Context(), tx, ws, body.ID, body.Action, phase, body.Generation)
	}
	if body.Action == "notify" {
		var operation, assignee, kind, url string
		err = h.DB.QueryRow(r.Context(), `SELECT CASE WHEN d.phase='preparing_release' THEN d.release_comment_id ELSE d.verification_comment_id END::text,COALESCE(i.assignee_id::text,''),COALESCE(i.assignee_type,''),d.candidate_url FROM issue_delivery d JOIN issue i ON i.id=d.issue_id WHERE d.id=$1`, body.ID).Scan(&operation, &assignee, &kind, &url)
		if err != nil {
			writeError(w, 500, "读取验收通知失败")
			return
		}
		var exists bool
		if err = h.DB.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM comment WHERE id=$1 AND issue_id=$2)`, operation, issue).Scan(&exists); err != nil {
			writeError(w, 500, "读取验收通知失败")
			return
		}
		if !exists {
			mention := ""
			if assignee != "" && (kind == "agent" || kind == "squad") {
				mention = fmt.Sprintf("[@负责人](mention://%s/%s)\n\n", kind, assignee)
			}
			var validation json.RawMessage
			if err = h.DB.QueryRow(r.Context(), `SELECT COALESCE(plan_snapshot->'validation_plan','{}') FROM issue_delivery WHERE id=$1`, body.ID).Scan(&validation); err != nil {
				writeError(w, 500, "读取补测计划失败")
				return
			}
			mention += "验收范围与依据：" + string(validation) + "。提交证据后结束当前运行，平台会在下一阶段通知续接，禁止在本运行中等待生产重启。\n\n"
			payload, _ := json.Marshal(map[string]any{"id": operation, "content": mention + "交付 " + body.ID + " 的联调已准备，源码提交 " + commit + "，入口 " + url + "。请按已确认计划进行实际功能验收，使用 multica issue delivery accept 提交证据；本轮不要合入主分支或发布生产。"})
			rr := r.Clone(context.WithValue(r.Context(), systemCommentContextKey{}, true))
			rr = rr.WithContext(context.WithValue(rr.Context(), commentIDContextKey{}, parseUUID(operation)))
			route := chi.NewRouteContext()
			route.URLParams.Add("id", issue)
			rr = rr.WithContext(context.WithValue(rr.Context(), chi.RouteCtxKey, route))
			rr.Body = readCloser{bytes.NewReader(payload)}
			response := &supervisionFeedbackResponse{header: make(http.Header)}
			h.CreateComment(response, rr)
			if response.status >= 400 {
				w.WriteHeader(response.status)
				_, _ = w.Write(response.Bytes())
				return
			}
		}
	}

	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		writeError(w, 500, "更新交付失败")
		return
	}
	if issueEvent != nil {
		h.publishEvent(*issueEvent)
	}
	// 2. 与 PUT /issues、批量改状态同一条后处理：进入审核中即派发审查并清掉上一轮
	// 绿勾。事务已提交，这里的失败不影响交付结果，由周期补扫兜底重派。
	if updated.ID.Valid {
		h.afterIssueStatusChangeForReview(r.Context(), previous, updated)
	}
	if notifyReview {
		// 6. 通知失败不回滚已保存的待复核状态；待复核意图保留，下一次队列轮询会补发。
		_ = h.deliverDeliveryAuthorizationReview(r, ws, issue, body.ID)
	}

	receipt := map[string]any{"id": body.ID, "phase": next, "revision": func() int64 {
		if body.Action == "notify" {
			return rev
		}
		return rev + 1
	}(), "worker_token": token, "issue_id": issue}
	if body.Action == "claim-integration" {
		receipt["candidate_id"] = ""
		receipt["acceptance_submitted"] = false
	}
	if body.Action == "tested" || body.Action == "prepared" {
		receipt["acceptance_submitted"] = automaticComplete
	}
	writeJSON(w, 200, receipt)
}

func identityOrEmpty(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`{}`)
	}
	return raw
}
