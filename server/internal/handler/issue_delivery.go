package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

var deliveryCommit = regexp.MustCompile(`^[0-9a-f]{40}$`)
var deliveryBranch = regexp.MustCompile(`^feature/[a-zA-Z0-9_./-]+$`)

type deliveryCheck struct {
	Name     string `json:"name"`
	Status   string `json:"status"`
	Evidence string `json:"evidence"`
}

func validDeliveryChecks(checks []deliveryCheck) bool {
	if len(checks) == 0 || len(checks) > 100 {
		return false
	}
	for _, c := range checks {
		if strings.TrimSpace(c.Name) == "" || c.Status != "passed" || strings.TrimSpace(c.Evidence) == "" {
			return false
		}
	}
	return true
}
func (h *Handler) deliveryIssue(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	i, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return "", "", false
	}
	return uuidToString(i.WorkspaceID), uuidToString(i.ID), true
}
func (h *Handler) deliveryHuman(r *http.Request, ws string) bool {
	actor, _ := h.resolveActor(r, requestUserID(r), ws)
	return actor == "member" && r.Header.Get("X-Actor-Source") != "task_token" && r.Header.Get("X-Agent-ID") == ""
}
func (h *Handler) deliveryJSON(w http.ResponseWriter, r *http.Request, ws, issue string) {
	var data json.RawMessage
	err := h.DB.QueryRow(r.Context(), `SELECT jsonb_build_object('owner',(SELECT jsonb_build_object('execution_id',execution_id,'generation',generation,'expires_at',expires_at) FROM delivery_owner WHERE workspace_id=$1 AND issue_id=$2),'publication',(SELECT jsonb_build_object('delivery_id',delivery_id,'state',state,'generation',generation,'expires_at',expires_at) FROM delivery_publication WHERE resource='int-prod'),'acceptances',(SELECT COALESCE(jsonb_agg(to_jsonb(e) ORDER BY created_at),'[]') FROM delivery_evidence e WHERE workspace_id=$1 AND issue_id=$2),'plan',(SELECT to_jsonb(p)||jsonb_build_object('body',c.content,'stale',c.revision<>p.comment_revision) FROM issue_delivery_plan p JOIN comment c ON c.id=p.comment_id AND c.issue_id=p.issue_id WHERE p.workspace_id=$1 AND p.issue_id=$2),'user_comments_version',(SELECT md5(COALESCE(string_agg(id::text||':'||revision::text,',' ORDER BY id),'')) FROM comment WHERE workspace_id=$1 AND issue_id=$2 AND author_type='member'),'authorizations',(SELECT COALESCE(jsonb_agg(a ORDER BY a.created_at DESC),'[]') FROM issue_delivery_authorization a JOIN issue_delivery_plan p ON p.id=a.plan_id WHERE a.workspace_id=$1 AND a.issue_id=$2),'deliveries',(SELECT COALESCE(jsonb_agg(x ORDER BY created_at DESC),'[]') FROM (SELECT (SELECT jsonb_build_object('stage',e.stage,'environment',e.environment,'generation',e.generation,'candidate_id',e.candidate_id,'at',e.created_at,'detail',e.detail) FROM verification_event e WHERE e.delivery_id=d.id ORDER BY e.created_at DESC,e.id DESC LIMIT 1) AS execution,d.id,d.issue_id,d.plan_snapshot,d.source_commit,d.source_branch,d.base_commit,d.phase,d.evidence,d.candidate_id,d.candidate_url,d.approved_by,d.approved_at,d.error,d.revision,d.created_at,d.updated_at,(SELECT count(*) FROM issue_delivery q WHERE q.workspace_id=d.workspace_id AND q.phase IN ('waiting_int','approved','testing','preparing_release','publishing') AND q.created_at<d.created_at) AS queue_ahead,(CASE WHEN d.phase<>'awaiting_review' THEN '' WHEN d.plan_snapshot->>'authorization_pending'='true' THEN 'owner_review' WHEN EXISTS(SELECT 1 FROM issue_delivery_authorization a JOIN comment c ON c.id=a.comment_id AND c.workspace_id=a.workspace_id AND c.issue_id=a.issue_id AND c.revision=a.comment_revision JOIN member m ON m.workspace_id=c.workspace_id AND m.user_id=c.author_id WHERE a.workspace_id=d.workspace_id AND a.issue_id=d.issue_id AND a.plan_id=d.plan_id AND a.kind='release' AND NOT a.revoked AND c.author_type='member' AND m.role IN ('owner','admin','member')) THEN 'review_incomplete' ELSE 'user_authorization' END) AS authorization_state FROM issue_delivery d WHERE d.workspace_id=$1 AND d.issue_id=$2 ORDER BY created_at DESC LIMIT 30)x),'can_confirm',$3::boolean)`, ws, issue, h.deliveryHuman(r, ws)).Scan(&data)
	if err != nil {
		writeError(w, 500, "读取交付记录失败")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, data)
}
func (h *Handler) GetIssueDeliveries(w http.ResponseWriter, r *http.Request) {
	ws, issue, ok := h.deliveryIssue(w, r)
	if ok {
		h.deliveryJSON(w, r, ws, issue)
	}
}

// RegisterIssuePlan 固定方案评论的版本；修改方案后必须重新确认。
func (h *Handler) RegisterIssuePlan(w http.ResponseWriter, r *http.Request) {
	ws, issue, ok := h.deliveryIssue(w, r)
	if !ok {
		return
	}
	var body struct {
		CommentID string `json:"comment_id"`
		Revision  int    `json:"revision"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body) != nil {
		writeError(w, 400, "方案格式错误")
		return
	}
	comment, ok := parseUUIDOrBadRequest(w, body.CommentID, "comment_id")
	if !ok {
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, 500, "保存方案失败")
		return
	}
	defer tx.Rollback(r.Context())
	if err = deliveryOwner(r, tx, ws, issue); err != nil {
		writeError(w, 409, err.Error())
		return
	}
	var revision int
	err = tx.QueryRow(r.Context(), `SELECT revision FROM comment WHERE workspace_id=$1 AND issue_id=$2 AND id=$3 FOR UPDATE`, ws, issue, comment).Scan(&revision)
	if err != nil || revision != body.Revision {
		writeError(w, 409, "方案评论已变化，请重新读取")
		return
	}
	var unchanged bool
	err = tx.QueryRow(r.Context(), `SELECT comment_id=$3 AND comment_revision=$4 FROM issue_delivery_plan WHERE workspace_id=$1 AND issue_id=$2 FOR UPDATE`, ws, issue, comment, revision).Scan(&unchanged)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 500, "读取方案失败")
		return
	}
	if !unchanged {
		_, err = tx.Exec(r.Context(), `INSERT INTO issue_delivery_plan(id,workspace_id,issue_id,comment_id,comment_revision,confirmation_id) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(workspace_id,issue_id) DO UPDATE SET id=EXCLUDED.id,comment_id=EXCLUDED.comment_id,comment_revision=EXCLUDED.comment_revision,confirmed_by=NULL,confirmed_at=NULL,confirmation_id=EXCLUDED.confirmation_id,updated_at=now()`, dbid.NewV7(), ws, issue, comment, revision, dbid.NewV7())
		if err == nil {
			_, err = tx.Exec(r.Context(), `UPDATE issue_delivery SET phase='needs_changes',error='方案已更新，需要重新确认与验收',approved_by=NULL,approved_at=NULL,revision=revision+1,updated_at=now() WHERE workspace_id=$1 AND issue_id=$2 AND phase NOT IN ('published','cancelled','publishing')`, ws, issue)
		}
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		writeError(w, 500, "保存方案失败")
		return
	}
	h.deliveryJSON(w, r, ws, issue)
}

// ConfirmIssuePlan 复用真实评论派发；固定评论ID使确认重试不会重复启动。
func (h *Handler) ConfirmIssuePlan(w http.ResponseWriter, r *http.Request) {
	ws, issue, ok := h.deliveryIssue(w, r)
	if !ok {
		return
	}

	var body struct {
		PlanID   string `json:"plan_id"`
		Revision int    `json:"revision"`
		deliveryAuthorizationInput
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body) != nil {
		writeError(w, 400, "确认格式错误")
		return
	}
	if _, ok = parseUUIDOrBadRequest(w, body.PlanID, "plan_id"); !ok {
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, 500, "确认失败")
		return
	}
	defer tx.Rollback(r.Context())
	var comment, operation string
	var current int
	var kind, assignee string
	err = tx.QueryRow(r.Context(), `SELECT p.comment_id::text,p.confirmation_id::text,c.revision,COALESCE(i.assignee_type,''),COALESCE(i.assignee_id::text,'') FROM issue_delivery_plan p JOIN comment c ON c.id=p.comment_id JOIN issue i ON i.id=p.issue_id WHERE p.workspace_id=$1 AND p.issue_id=$2 AND p.id=$3 AND p.comment_revision=$4 FOR UPDATE OF p,c`, ws, issue, body.PlanID, body.Revision).Scan(&comment, &operation, &current, &kind, &assignee)
	if err != nil || current != body.Revision {
		writeError(w, 409, "方案已变化，请重新查看后确认")
		return
	}
	confirmedBy := requestUserID(r)
	if body.CommentID != "" {
		_, confirmedBy, err = h.recordDeliveryAuthorization(r, tx, ws, issue, body.PlanID, "execute", body.deliveryAuthorizationInput)
		if err == nil && body.ReleaseAfterAcceptance {
			_, _, err = h.recordDeliveryAuthorization(r, tx, ws, issue, body.PlanID, "release", body.deliveryAuthorizationInput)
		}
		if err != nil {
			writeError(w, 409, err.Error())
			return
		}
	} else if !h.deliveryHuman(r, ws) {
		writeError(w, 403, "需要真实用户授权依据")
		return
	}

	_, err = tx.Exec(r.Context(), `UPDATE issue_delivery_plan SET confirmed_by=$3,confirmed_at=COALESCE(confirmed_at,now()),updated_at=now() WHERE workspace_id=$1 AND issue_id=$2`, ws, issue, confirmedBy)
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		writeError(w, 500, "确认失败")
		return
	}
	if body.CommentID != "" {
		operation = body.CommentID
	}
	var exists bool
	if err = h.DB.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM comment WHERE id=$1 AND issue_id=$2)`, operation, issue).Scan(&exists); err != nil {
		writeError(w, 500, "读取确认回执失败")
		return
	}
	if !exists {
		mention := ""
		if assignee != "" && (kind == "agent" || kind == "squad") {
			mention = fmt.Sprintf("[@负责人](mention://%s/%s)\n\n", kind, assignee)
		}
		payload, _ := json.Marshal(map[string]any{"id": operation, "parent_id": comment, "content": mention + "确认当前计划，开始按计划开发、提交任务分支及联调验证。生产发布需另行确认。"})
		rr := r.Clone(r.Context())
		rr = rr.WithContext(context.WithValue(rr.Context(), commentIDContextKey{}, parseUUID(operation)))
		rr.Body = readCloser{bytes.NewReader(payload)}
		response := &supervisionFeedbackResponse{header: make(http.Header)}
		h.CreateComment(response, rr)
		if response.status >= 400 {
			w.WriteHeader(response.status)
			_, _ = w.Write(response.Bytes())
			return
		}
	}
	h.deliveryJSON(w, r, ws, issue)
}

func (h *Handler) SubmitIssueDelivery(w http.ResponseWriter, r *http.Request) {
	ws, issue, ok := h.deliveryIssue(w, r)
	if !ok {
		return
	}
	var body struct {
		OperationID     string          `json:"operation_id"`
		Commit          string          `json:"source_commit"`
		Branch          string          `json:"source_branch"`
		Base            string          `json:"base_commit"`
		Checks          []deliveryCheck `json:"evidence"`
		ReplaceID       string          `json:"replace_delivery_id"`
		ReplaceRevision int64           `json:"replace_revision"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 65536)).Decode(&body) != nil || !deliveryCommit.MatchString(body.Commit) || !deliveryCommit.MatchString(body.Base) || !deliveryBranch.MatchString(body.Branch) || strings.Contains(body.Branch, "..") || !validDeliveryChecks(body.Checks) {
		writeError(w, 400, "需要完整提交、任务分支、基线及通过的检查证据")
		return
	}
	op, ok := parseUUIDOrBadRequest(w, body.OperationID, "operation_id")
	if !ok {
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, 500, "提交交付失败")
		return
	}
	defer tx.Rollback(r.Context())
	if err = deliveryOwner(r, tx, ws, issue); err != nil {
		writeError(w, 409, err.Error())
		return
	}
	var plan string
	var planSnapshot json.RawMessage
	err = tx.QueryRow(r.Context(), `SELECT p.id::text,jsonb_build_object('comment_id',p.comment_id,'revision',p.comment_revision,'body',c.content,'confirmed_by',p.confirmed_by,'confirmed_at',p.confirmed_at,'execute_authorizations',(SELECT COALESCE(jsonb_agg(a),'[]') FROM issue_delivery_authorization a WHERE a.plan_id=p.id AND a.kind='execute' AND NOT a.revoked)) FROM issue_delivery_plan p JOIN comment c ON c.id=p.comment_id AND c.revision=p.comment_revision WHERE p.workspace_id=$1 AND p.issue_id=$2 AND p.confirmed_by IS NOT NULL AND (NOT EXISTS(SELECT 1 FROM issue_delivery_authorization a WHERE a.plan_id=p.id AND a.kind='execute') OR EXISTS(SELECT 1 FROM issue_delivery_authorization a JOIN comment ac ON ac.id=a.comment_id AND ac.revision=a.comment_revision AND ac.author_type='member' JOIN member m ON m.workspace_id=a.workspace_id AND m.user_id=ac.author_id WHERE a.plan_id=p.id AND a.kind='execute' AND NOT a.revoked AND m.role IN ('owner','admin','member'))) FOR UPDATE OF p`, ws, issue).Scan(&plan, &planSnapshot)
	if err != nil {
		writeError(w, 409, "请先确认当前方案")
		return
	}
	// 同一方案和提交复用历史交付；不同提交必须显式替换已知版本。
	var duplicate string
	err = tx.QueryRow(r.Context(), `SELECT id::text FROM issue_delivery WHERE workspace_id=$1 AND issue_id=$2 AND plan_id=$3 AND source_commit=$4 AND phase NOT IN ('cancelled','needs_changes') ORDER BY created_at DESC LIMIT 1`, ws, issue, plan, body.Commit).Scan(&duplicate)
	if err == nil {
		if tx.Commit(r.Context()) != nil {
			writeError(w, 500, "提交交付失败")
			return
		}
		h.deliveryJSON(w, r, ws, issue)
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 500, "读取现有交付失败")
		return
	}
	var active string
	var activeRevision int64
	var activePhase string
	err = tx.QueryRow(r.Context(), `SELECT id::text,revision,phase FROM issue_delivery WHERE workspace_id=$1 AND issue_id=$2 AND phase NOT IN ('published','cancelled','needs_changes') ORDER BY created_at DESC LIMIT 1 FOR UPDATE`, ws, issue).Scan(&active, &activeRevision, &activePhase)
	if err == nil {
		if active != body.ReplaceID || activeRevision != body.ReplaceRevision || activePhase == "preparing_release" || activePhase == "publishing" {
			writeError(w, 409, "已有活动交付；请接续或由负责人显式替换，最终发布期间不能替换")
			return
		}
		var leased bool
		if e := tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM verification_slot WHERE delivery_id=$1 AND state<>'idle') OR EXISTS(SELECT 1 FROM delivery_publication WHERE delivery_id=$1 AND state<>'idle')`, active).Scan(&leased); e != nil || leased {
			writeError(w, 409, "交付仍占用环境，安全释放后才能替换")
			return
		}
		_, err = tx.Exec(r.Context(), `UPDATE issue_delivery SET phase='needs_changes',error='负责人显式提交下一版本',revision=revision+1,updated_at=now() WHERE id=$1`, active)
	} else if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	if err != nil {
		writeError(w, 500, "替换交付失败")
		return
	}
	evidence, _ := json.Marshal(body.Checks)
	tag, err := tx.Exec(r.Context(), `INSERT INTO issue_delivery(id,workspace_id,issue_id,operation_id,plan_id,source_commit,source_branch,base_commit,evidence,verification_comment_id,release_comment_id,plan_snapshot) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) ON CONFLICT(workspace_id,operation_id) DO NOTHING`, dbid.NewV7(), ws, issue, op, plan, body.Commit, body.Branch, body.Base, evidence, dbid.NewV7(), dbid.NewV7(), planSnapshot)
	if err == nil && tag.RowsAffected() == 0 {
		var same bool
		err = tx.QueryRow(r.Context(), `SELECT issue_id=$3 AND source_commit=$4 AND source_branch=$5 AND base_commit=$6 AND evidence=$7::jsonb FROM issue_delivery WHERE workspace_id=$1 AND operation_id=$2`, ws, op, issue, body.Commit, body.Branch, body.Base, evidence).Scan(&same)
		if err == nil && !same {
			writeError(w, 409, "操作编号已用于其他交付")
			return
		}
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		writeError(w, 500, "提交交付失败")
		return
	}
	h.deliveryJSON(w, r, ws, issue)
}

func (h *Handler) ActIssueDelivery(w http.ResponseWriter, r *http.Request) {
	ws, issue, ok := h.deliveryIssue(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "deliveryId")
	if _, ok = parseUUIDOrBadRequest(w, id, "delivery_id"); !ok {
		return
	}
	var body struct {
		Action   string          `json:"action"`
		Revision int64           `json:"revision"`
		Checks   []deliveryCheck `json:"evidence"`
		deliveryAuthorizationInput
		AuthorizationID string `json:"authorization_id"`
		ScopeReview     string `json:"scope_review"`
		CommentsVersion string `json:"user_comments_version"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 65536)).Decode(&body) != nil {
		writeError(w, 400, "交付操作格式错误")
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, 500, "交付操作失败")
		return
	}
	defer tx.Rollback(r.Context())
	if err = deliveryOwner(r, tx, ws, issue); err != nil {
		writeError(w, 409, err.Error())
		return
	}
	var phase string
	var revision int64
	var candidate string
	err = tx.QueryRow(r.Context(), `SELECT phase,revision,candidate_id FROM issue_delivery WHERE workspace_id=$1 AND issue_id=$2 AND id=$3 FOR UPDATE`, ws, issue, id).Scan(&phase, &revision, &candidate)
	if err != nil {
		writeError(w, 404, "交付记录不存在")
		return
	}
	if revision != body.Revision {
		writeError(w, 409, "交付版本已变化，请刷新后重试")
		return
	}
	if body.Action == "accept" || body.Action == "approve" || body.Action == "retest" {
		var valid bool
		err = tx.QueryRow(r.Context(), deliveryPlanValidSQL, id).Scan(&valid)
		if err != nil || !valid {
			writeError(w, 409, "方案已变化，需要重新确认")
			return
		}
	}
	switch body.Action {
	case "accept":
		if (phase != "testing" && phase != "preparing_release") || candidate == "" || !validDeliveryChecks(body.Checks) {
			writeError(w, 409, "请先完成该版本联调并提交实际证据")
			return
		}
		if body.AuthorizationID != "" {
			if _, err = h.bindDeliveryAuthorization(r, tx, ws, issue, id, body.AuthorizationID, body.ScopeReview, body.CommentsVersion); err != nil {
				writeError(w, 409, err.Error())
				return
			}
		}
		evidence, _ := json.Marshal(body.Checks)
		_, err = tx.Exec(r.Context(), `INSERT INTO delivery_evidence(id,workspace_id,issue_id,delivery_id,candidate_id,source_commit,identity,evidence) SELECT $1,workspace_id,issue_id,id,candidate_id,COALESCE(plan_snapshot->>'candidate_commit',source_commit),COALESCE(plan_snapshot->'candidate_identity','{}'),$3 FROM issue_delivery WHERE id=$2 ON CONFLICT(delivery_id,candidate_id) DO NOTHING`, dbid.NewV7(), id, evidence)
		if err != nil {
			writeError(w, 500, "保存验收历史失败")
			return
		}
		_, err = tx.Exec(r.Context(), `UPDATE issue_delivery SET acceptance_submitted=true,evidence=$4,error='',revision=revision+1,updated_at=now() WHERE workspace_id=$1 AND issue_id=$2 AND id=$3`, ws, issue, id, evidence)
	case "approve":
		if phase != "awaiting_review" {
			writeError(w, 409, "该交付尚未完成联调审核")
			return
		}
		approver := ""
		if body.CommentID != "" {
			var plan string
			err = tx.QueryRow(r.Context(), `SELECT plan_id::text FROM issue_delivery WHERE id=$1`, id).Scan(&plan)
			if err == nil {
				body.AuthorizationID, _, err = h.recordDeliveryAuthorization(r, tx, ws, issue, plan, "release", body.deliveryAuthorizationInput)
			}
			if err != nil {
				writeError(w, 409, err.Error())
				return
			}
		}
		if body.AuthorizationID != "" {
			approver, err = h.bindDeliveryAuthorization(r, tx, ws, issue, id, body.AuthorizationID, body.ScopeReview, body.CommentsVersion)
			if err != nil {
				writeError(w, 409, err.Error())
				return
			}
		} else if h.deliveryHuman(r, ws) {
			approver = requestUserID(r)
			var source string
			err = tx.QueryRow(r.Context(), `SELECT source_commit FROM issue_delivery WHERE id=$1`, id).Scan(&source)
			version, versionErr := deliveryCommentsVersion(r.Context(), tx, ws, issue)
			if err == nil {
				err = versionErr
			}
			if err == nil {
				review, _ := json.Marshal(map[string]string{"authorization_id": "button", "comments_version": version, "source_commit": source, "candidate_id": candidate, "reviewed_by": approver})
				_, err = tx.Exec(r.Context(), `UPDATE issue_delivery SET plan_snapshot=jsonb_set(plan_snapshot,'{authorization_review}',$2::jsonb) WHERE id=$1`, id, review)
			}
		} else {
			writeError(w, 403, "需要用户批准或可核对的真实用户授权记录")
			return
		}
		if err == nil {
			_, err = tx.Exec(r.Context(), `UPDATE issue_delivery SET phase=CASE WHEN EXISTS(SELECT 1 FROM delivery_publication p WHERE p.delivery_id=issue_delivery.id AND p.state='active') THEN 'preparing_release' ELSE 'approved' END,approved_by=$4,approved_at=now(),approval_comment_id=NULLIF($5,'')::uuid,error='',revision=revision+1,updated_at=now() WHERE workspace_id=$1 AND issue_id=$2 AND id=$3`, ws, issue, id, approver, body.CommentID)
		}

	case "retest":
		if phase != "awaiting_review" {
			writeError(w, 409, "当前阶段不能重新准备联调")
			return
		}
		// 重新联调会换候选，旧的待复核意图随之失效。
		_, err = tx.Exec(r.Context(), `UPDATE issue_delivery SET phase='waiting_int',candidate_id='',candidate_url='',acceptance_submitted=false,verification_comment_id=$2,worker_token=NULL,error='',`+clearAuthorizationPendingSQL+`,revision=revision+1,updated_at=now() WHERE id=$1`, id, dbid.NewV7())
	case "cancel":
		if !h.deliveryHuman(r, ws) || phase == "publishing" || phase == "published" {
			writeError(w, 409, "当前交付不能取消")
			return
		}
		_, err = tx.Exec(r.Context(), `UPDATE issue_delivery SET phase='cancelled',revision=revision+1,updated_at=now() WHERE id=$1`, id)
	default:
		writeError(w, 400, "未知交付操作")
		return
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		writeError(w, 500, "交付操作失败")
		return
	}
	h.deliveryJSON(w, r, ws, issue)
}

func deleteDeliveryData(ctx context.Context, tx pgx.Tx, ws, issue string) error {
	var active bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM delivery_publication WHERE workspace_id=$1 AND ($2='' OR delivery_id IN (SELECT id FROM issue_delivery WHERE issue_id=NULLIF($2,'')::uuid)) AND state<>'idle')`, ws, issue).Scan(&active); err != nil {
		return err
	} else if active {
		return fmt.Errorf("活动发布尚未安全释放")
	}
	if _, err := tx.Exec(ctx, `DELETE FROM delivery_notice WHERE delivery_id IN (SELECT id FROM issue_delivery WHERE workspace_id=$1 AND ($2='' OR issue_id=NULLIF($2,'')::uuid))`, ws, issue); err != nil {
		return err
	}

	for _, table := range []string{"delivery_evidence", "delivery_owner", "issue_delivery_authorization", "issue_delivery", "issue_delivery_plan"} {
		if _, err := tx.Exec(ctx, `DELETE FROM `+table+` WHERE workspace_id=$1 AND ($2='' OR issue_id=NULLIF($2,'')::uuid)`, ws, issue); err != nil {
			return err
		}
	}
	return nil
}
