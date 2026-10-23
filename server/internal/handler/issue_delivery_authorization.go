package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

// 用户评论摘要用于核对 Agent 审查之后是否又有新决定，不解释评论语义。
const deliveryCommentsVersionSQL = `SELECT md5(COALESCE(string_agg(id::text||':'||revision::text,',' ORDER BY id),'')) FROM comment c WHERE workspace_id=$1 AND issue_id=$2 AND author_type='member' AND NOT EXISTS(SELECT 1 FROM delivery_notice n WHERE n.comment_id=c.id AND (n.stage='batch_ready' OR n.stage='batch_ready_history:'||c.id::text))`

type deliveryAuthorizationInput struct {
	CommentID              string `json:"comment_id"`
	CommentRevision        int    `json:"comment_revision"`
	Scope                  string `json:"scope"`
	ReleaseAfterAcceptance bool   `json:"release_after_acceptance"`
}

func (h *Handler) recordDeliveryAuthorization(r *http.Request, tx pgx.Tx, ws, issue, plan, kind string, input deliveryAuthorizationInput) (string, string, error) {
	if _, err := util.ParseUUID(input.CommentID); err != nil || input.CommentID == "" {
		return "", "", fmt.Errorf("需要真实用户评论")
	}
	if input.CommentRevision < 1 || strings.TrimSpace(input.Scope) == "" {
		return "", "", fmt.Errorf("需要评论版本与授权范围解释")
	}
	var author string
	var snapshot json.RawMessage
	err := tx.QueryRow(r.Context(), `SELECT c.author_id::text,jsonb_build_object('id',c.id,'revision',c.revision,'content',c.content,'author_id',c.author_id,'created_at',c.created_at)
 FROM comment c JOIN member m ON m.workspace_id=c.workspace_id AND m.user_id=c.author_id
 WHERE c.workspace_id=$1 AND c.issue_id=$2 AND c.id=$3 AND c.revision=$4 AND c.author_type='member' AND m.role IN ('owner','admin','member') FOR SHARE OF c,m`, ws, issue, input.CommentID, input.CommentRevision).Scan(&author, &snapshot)
	if err != nil {
		return "", "", fmt.Errorf("授权评论不存在、已修改或用户已无任务访问权限")
	}
	actorType, actor := h.resolveActor(r, requestUserID(r), ws)
	id := dbid.NewV7().String()
	_, err = tx.Exec(r.Context(), `INSERT INTO issue_delivery_authorization(id,workspace_id,issue_id,plan_id,kind,comment_id,comment_revision,comment_snapshot,scope,registered_by_type,registered_by)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT(workspace_id,plan_id,kind,comment_id,comment_revision) DO NOTHING`, id, ws, issue, plan, kind, input.CommentID, input.CommentRevision, snapshot, input.Scope, actorType, actor)
	if err != nil {
		return "", "", err
	}
	var savedScope string
	var revoked bool
	err = tx.QueryRow(r.Context(), `SELECT id::text,scope,revoked FROM issue_delivery_authorization WHERE workspace_id=$1 AND plan_id=$2 AND kind=$3 AND comment_id=$4 AND comment_revision=$5`, ws, plan, kind, input.CommentID, input.CommentRevision).Scan(&id, &savedScope, &revoked)
	if err != nil {
		return "", "", err
	}
	if revoked || savedScope != input.Scope {
		return "", "", fmt.Errorf("此评论的授权已撤销或解释发生变化，请引用新的用户决定")
	}
	return id, author, nil
}

// 有效性依赖真实评论版本及成员身份；语义由登记授权的 Agent 负责。
const deliveryAuthorizationValidSQL = `SELECT EXISTS(SELECT 1 FROM issue_delivery_authorization a JOIN comment c ON c.id=a.comment_id AND c.workspace_id=a.workspace_id AND c.issue_id=a.issue_id AND c.revision=a.comment_revision JOIN member m ON m.workspace_id=c.workspace_id AND m.user_id=c.author_id WHERE a.workspace_id=$1 AND a.issue_id=$2 AND a.plan_id=$3 AND a.id=$4 AND a.kind='release' AND NOT a.revoked AND c.author_type='member' AND m.role IN ('owner','admin','member'))`

func deliveryCommentsVersion(ctx context.Context, tx pgx.Tx, ws, issue string) (string, error) {
	var version string
	err := tx.QueryRow(ctx, deliveryCommentsVersionSQL, ws, issue).Scan(&version)
	return version, err
}

// 发布审核同时固定源提交、候选、授权记录和已经核对过的用户评论。
func (h *Handler) bindDeliveryAuthorization(r *http.Request, tx pgx.Tx, ws, issue, id, authorization, scope, commentsVersion string) (string, error) {
	var plan, source, candidate string
	err := tx.QueryRow(r.Context(), `SELECT plan_id::text,source_commit,candidate_id FROM issue_delivery WHERE id=$1`, id).Scan(&plan, &source, &candidate)
	if err != nil {
		return "", err
	}
	var valid bool
	if authorization != "button" {
		if _, parseErr := util.ParseUUID(authorization); parseErr != nil {
			return "", fmt.Errorf("授权记录编号无效")
		}
	}
	if authorization == "button" {
		err = tx.QueryRow(r.Context(), `SELECT approved_by IS NOT NULL AND plan_snapshot->'authorization_review'->>'authorization_id'='button' FROM issue_delivery WHERE id=$1`, id).Scan(&valid)
	} else {
		err = tx.QueryRow(r.Context(), deliveryAuthorizationValidSQL, ws, issue, plan, authorization).Scan(&valid)
	}
	if err != nil || !valid {
		return "", fmt.Errorf("发布授权来源已失效，请核对用户最新决定")
	}
	current, err := deliveryCommentsVersion(r.Context(), tx, ws, issue)
	if err != nil {
		return "", err
	}
	if commentsVersion != current || strings.TrimSpace(scope) == "" {
		return "", fmt.Errorf("请核对最新用户评论及本次改动范围，再提交授权复核")
	}
	var author string
	if authorization == "button" {
		err = tx.QueryRow(r.Context(), `SELECT approved_by::text FROM issue_delivery WHERE id=$1`, id).Scan(&author)
	} else {
		err = tx.QueryRow(r.Context(), `SELECT comment_snapshot->>'author_id' FROM issue_delivery_authorization WHERE id=$1`, authorization).Scan(&author)
	}
	if err != nil {
		return "", err
	}
	actorType, actor := h.resolveActor(r, requestUserID(r), ws)
	review, _ := json.Marshal(map[string]any{"authorization_id": authorization, "scope": scope, "comments_version": current, "source_commit": source, "candidate_id": candidate, "reviewed_by_type": actorType, "reviewed_by": actor})
	// 复核绑定成功后待复核意图即失效，避免队列继续补发同一次复核通知。
	_, err = tx.Exec(r.Context(), `UPDATE issue_delivery SET plan_snapshot=jsonb_set(plan_snapshot,'{authorization_review}',$2::jsonb)-'authorization_pending'-'authorization_notice' WHERE id=$1`, id, review)
	return author, err
}

// 旧的已批准交付继续使用原批准；新登记的授权必须与本次复核一致。
func deliveryAuthorizationReady(ctx context.Context, tx pgx.Tx, ws, issue, id string) (bool, error) {
	var review json.RawMessage
	var plan, source, candidate string
	var approved bool
	err := tx.QueryRow(ctx, `SELECT COALESCE(plan_snapshot->'authorization_review','{}'::jsonb),plan_id::text,source_commit,candidate_id,approved_by IS NOT NULL FROM issue_delivery WHERE id=$1`, id).Scan(&review, &plan, &source, &candidate, &approved)
	if err != nil {
		return false, err
	}
	if approved {
		var memberValid bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM issue_delivery d JOIN member m ON m.workspace_id=d.workspace_id AND m.user_id=d.approved_by WHERE d.id=$1 AND m.role IN ('owner','admin','member'))`, id).Scan(&memberValid); err != nil {
			return false, err
		}
		if !memberValid {
			return false, nil
		}
	}
	var saved map[string]string
	if err = json.Unmarshal(review, &saved); err != nil {
		return false, err
	}
	if len(saved) == 0 {
		var changed bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM comment c JOIN issue_delivery d ON d.issue_id=c.issue_id AND d.workspace_id=c.workspace_id WHERE d.id=$1 AND c.author_type='member' AND GREATEST(c.created_at,c.updated_at)>d.approved_at AND NOT EXISTS(SELECT 1 FROM delivery_notice n WHERE n.comment_id=c.id AND (n.stage='batch_ready' OR n.stage='batch_ready_history:'||c.id::text)))`, id).Scan(&changed)
		return approved && !changed, err
	}
	version, err := deliveryCommentsVersion(ctx, tx, ws, issue)
	if err != nil {
		return false, err
	}
	if saved["comments_version"] != version || saved["source_commit"] != source || saved["candidate_id"] != candidate {
		return false, nil
	}
	if saved["authorization_id"] == "button" {
		return approved, nil
	}
	var valid bool
	err = tx.QueryRow(ctx, deliveryAuthorizationValidSQL, ws, issue, plan, saved["authorization_id"]).Scan(&valid)
	return valid, err
}

// RevokeIssueReleaseAuthorization 记录撤销，不删除历史授权依据。
func (h *Handler) RevokeIssueReleaseAuthorization(w http.ResponseWriter, r *http.Request) {
	ws, issue, ok := h.deliveryIssue(w, r)
	if !ok {
		return
	}
	var body struct {
		PlanID   string `json:"plan_id"`
		Revision int    `json:"revision"`
		deliveryAuthorizationInput
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384)).Decode(&body) != nil {
		writeError(w, 400, "撤销格式错误")
		return
	}
	if _, ok = parseUUIDOrBadRequest(w, body.PlanID, "plan_id"); !ok {
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, 500, "撤销失败")
		return
	}
	defer tx.Rollback(r.Context())
	var valid bool
	err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM issue_delivery_plan p JOIN comment c ON c.id=p.comment_id AND c.revision=p.comment_revision WHERE p.workspace_id=$1 AND p.issue_id=$2 AND p.id=$3 AND p.comment_revision=$4)`, ws, issue, body.PlanID, body.Revision).Scan(&valid)
	if err != nil || !valid {
		writeError(w, 409, "当前方案已变化")
		return
	}
	if body.CommentID != "" {
		_, _, err = h.recordDeliveryAuthorization(r, tx, ws, issue, body.PlanID, "revoke", body.deliveryAuthorizationInput)
	} else if !h.deliveryHuman(r, ws) {
		writeError(w, 403, "需要真实用户撤销依据")
		return
	}
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	_, err = tx.Exec(r.Context(), `UPDATE issue_delivery_authorization SET revoked=true WHERE workspace_id=$1 AND issue_id=$2 AND plan_id=$3 AND kind='release'`, ws, issue, body.PlanID)
	if err == nil {
		_, err = tx.Exec(r.Context(), `UPDATE issue_delivery SET approved_by=NULL,approved_at=NULL,release_validated=false,error='用户已撤销发布授权',plan_snapshot=plan_snapshot-'authorization_pending'-'authorization_notice',phase=CASE WHEN phase IN ('approved','preparing_release') THEN 'awaiting_review' ELSE phase END,revision=revision+1,updated_at=now() WHERE workspace_id=$1 AND issue_id=$2 AND phase NOT IN ('published','cancelled','publishing')`, ws, issue)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		writeError(w, 500, "撤销失败")
		return
	}
	h.deliveryJSON(w, r, ws, issue)
}

const deliveryPlanValidSQL = `SELECT EXISTS(SELECT 1 FROM issue_delivery d JOIN issue_delivery_plan p ON p.id=d.plan_id AND p.workspace_id=d.workspace_id JOIN comment c ON c.id=p.comment_id AND c.revision=p.comment_revision WHERE d.id=$1 AND p.confirmed_by IS NOT NULL AND (
 NOT EXISTS(SELECT 1 FROM issue_delivery_authorization a WHERE a.plan_id=p.id AND a.kind='execute') OR
 EXISTS(SELECT 1 FROM issue_delivery_authorization a JOIN comment ac ON ac.id=a.comment_id AND ac.revision=a.comment_revision AND ac.author_type='member' JOIN member m ON m.workspace_id=a.workspace_id AND m.user_id=ac.author_id WHERE a.plan_id=p.id AND a.kind='execute' AND NOT a.revoked AND m.role IN ('owner','admin','member'))))`

// 组合成员共享真正代表的发布占用，逐成员仍绑定同批源码、候选和当前批准状态。
func deliveryPublicationAuthorization(ctx context.Context, tx pgx.Tx, ws, id string) (int64, bool, error) {
	var generation int64
	var leased bool
	var batch string
	err := tx.QueryRow(ctx, `SELECT b.id::text FROM delivery_batch b JOIN delivery_batch_member m ON m.batch_id=b.id WHERE b.workspace_id=$1 AND m.delivery_id=$2 AND m.result='pending' AND b.status IN ('collecting','frozen','preparing','validating','publishing')`, ws, id).Scan(&batch)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, `SELECT generation,state='active' AND expires_at>now() FROM delivery_publication WHERE resource='int-prod' AND workspace_id=$1 AND delivery_id=$2`, ws, id).Scan(&generation, &leased)
	} else if err == nil {
		err = tx.QueryRow(ctx, `SELECT p.generation,p.state='active' AND p.expires_at>now()
   FROM delivery_publication p
   JOIN delivery_batch b ON b.id=$3 AND b.workspace_id=p.workspace_id
   JOIN delivery_batch_member m ON m.batch_id=b.id AND m.delivery_id=$2 AND m.result='pending'
   JOIN issue_delivery d ON d.id=m.delivery_id AND d.workspace_id=b.workspace_id AND d.source_commit=m.source_commit AND d.approved_by IS NOT NULL
   JOIN delivery_batch_member first ON first.batch_id=b.id AND first.result='pending' AND first.delivery_id=(SELECT delivery_id FROM delivery_batch_member WHERE batch_id=b.id AND result='pending' ORDER BY created_at,delivery_id LIMIT 1)
   JOIN issue_delivery representative ON representative.id=first.delivery_id AND representative.workspace_id=b.workspace_id AND representative.source_commit=first.source_commit AND representative.approved_by IS NOT NULL
   WHERE p.resource='int-prod' AND p.workspace_id=$1 AND p.delivery_id=representative.id AND p.worker_token=representative.worker_token
   AND ((b.status IN ('frozen','preparing','validating') AND d.phase='approved' AND representative.phase='approved') OR (b.status='publishing' AND d.phase='publishing' AND representative.phase='publishing' AND d.release_validated AND representative.release_validated))
   AND (b.status IN ('frozen','preparing') OR (b.candidate_id<>'' AND b.candidate_commit<>'' AND d.candidate_id=b.candidate_id AND representative.candidate_id=b.candidate_id AND d.plan_snapshot->>'candidate_commit'=b.candidate_commit AND representative.plan_snapshot->>'candidate_commit'=b.candidate_commit))`, ws, id, batch).Scan(&generation, &leased)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	return generation, leased, err
}

// CheckDeliveryAuthorization 供外部发布器在实际发布前核对授权，不触发 Agent。
func (h *Handler) CheckDeliveryAuthorization(w http.ResponseWriter, r *http.Request) {
	ws, ok := h.deliveryOperator(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "deliveryId")
	if _, ok = parseUUIDOrBadRequest(w, id, "delivery_id"); !ok {
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, 500, "核对失败")
		return
	}
	defer tx.Rollback(r.Context())
	var issue string
	if err = tx.QueryRow(r.Context(), `SELECT issue_id::text FROM issue_delivery WHERE workspace_id=$1 AND id=$2`, ws, id).Scan(&issue); err != nil {
		writeError(w, 404, "交付不存在")
		return
	}
	ready, err := deliveryAuthorizationReady(r.Context(), tx, ws, issue, id)
	var planValid bool
	if err == nil {
		err = tx.QueryRow(r.Context(), deliveryPlanValidSQL, id).Scan(&planValid)
	}
	if err != nil {
		writeError(w, 500, "核对失败")
		return
	}
	generation, leased, leaseErr := deliveryPublicationAuthorization(r.Context(), tx, ws, id)
	if leaseErr != nil {
		writeError(w, 500, "核对发布占用失败")
		return
	}
	businessError := ""
	if ready && planValid && leased {
		var final bool
		var representative string
		err = tx.QueryRow(r.Context(), `SELECT (d.phase='publishing' OR d.release_validated),COALESCE((
 SELECT m.delivery_id::text FROM delivery_batch b JOIN delivery_batch_member own ON own.batch_id=b.id
 JOIN delivery_batch_member m ON m.batch_id=b.id AND m.result='pending'
 WHERE own.delivery_id=d.id AND own.result='pending' AND b.status IN ('validating','publishing')
 AND b.candidate_id=d.candidate_id ORDER BY m.created_at,m.delivery_id LIMIT 1),d.id::text)
 FROM issue_delivery d WHERE d.id=$1`, id).Scan(&final, &representative)
		if err == nil && final {
			err = requireStoredDeliveryChecks(r.Context(), tx, representative, "integration")
		}
		if err != nil {
			var gap *deliveryChecksGap
			if !errors.As(err, &gap) {
				writeError(w, 500, "核对业务验收失败")
				return
			}
			businessError = err.Error()
		}
	}
	writeJSON(w, 200, map[string]any{"ready": ready && planValid && leased && businessError == "", "publication_generation": generation, "business_error": businessError})
}
