package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/service"
	"net/http"
	"strings"

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
	err := h.DB.QueryRow(r.Context(), `SELECT COALESCE(jsonb_agg(x ORDER BY priority DESC,created_at),'[]') FROM (SELECT d.*,i.number,i.project_id,CASE WHEN d.id=NULLIF($2,'')::uuid THEN 3 WHEN d.phase IN('testing','preparing_release','publishing') THEN 2 WHEN d.phase IN('waiting_int','approved') THEN 1 ELSE 0 END AS priority FROM issue_delivery d JOIN issue i ON i.id=d.issue_id AND i.workspace_id=d.workspace_id WHERE d.workspace_id=$1 AND (d.phase IN('waiting_int','testing','approved','preparing_release','publishing') OR (d.phase='awaiting_review' AND d.plan_snapshot->>'authorization_pending'='true') OR d.id=NULLIF($2,'')::uuid) ORDER BY priority DESC,d.created_at LIMIT 100)x`, ws, current).Scan(&data)
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
		ID        string `json:"id"`
		Revision  int64  `json:"revision"`
		Action    string `json:"action"`
		Token     string `json:"worker_token"`
		Candidate string `json:"candidate_id"`
		URL       string `json:"candidate_url"`
		Error     string `json:"error"`
		Source    string `json:"source_commit"`
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
	var approved bool
	err = tx.QueryRow(r.Context(), `SELECT phase,COALESCE(worker_token::text,''),issue_id::text,source_commit,revision,approved_by IS NOT NULL FROM issue_delivery WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, ws, body.ID).Scan(&phase, &token, &issue, &commit, &rev, &approved)
	if err != nil {
		writeError(w, 404, "交付不存在")
		return
	}
	if rev != body.Revision {
		writeError(w, 409, "交付已变化")
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
			_, err = tx.Exec(r.Context(), `UPDATE issue_delivery SET plan_snapshot=jsonb_set(plan_snapshot,'{authorization_pending}','true'::jsonb),phase='awaiting_review',error='存在新用户评论或授权依据变化，等待负责人核对',approved_by=NULL,approved_at=NULL,release_validated=false,release_comment_id=$2,revision=revision+1,updated_at=now() WHERE id=$1`, body.ID, dbid.NewV7())
			if err == nil {
				err = tx.Commit(r.Context())
			}
			if err == nil {
				err = h.notifyDeliveryAuthorization(r, ws, issue, body.ID)
			}
			if err != nil {
				writeError(w, 500, "保存授权待核对状态失败")
				return
			}
			writeError(w, 409, "存在新用户评论或授权依据变化，已通知负责人核对")
			return
		}
	}
	if body.Action == "notify-authorization" {
		if phase != "awaiting_review" {
			writeError(w, 409, "当前无需授权核对")
			return
		}
		if err = tx.Commit(r.Context()); err == nil {
			err = h.notifyDeliveryAuthorization(r, ws, issue, body.ID)
		}
		if err != nil {
			writeError(w, 500, "通知负责人失败")
			return
		}
		writeJSON(w, 200, map[string]any{"id": body.ID, "phase": phase, "revision": rev, "worker_token": token, "issue_id": issue})
		return
	}
	next := phase
	switch body.Action {
	case "claim":

		var valid bool
		if tx.QueryRow(r.Context(), deliveryPlanValidSQL, body.ID).Scan(&valid) != nil || !valid {
			_, err = tx.Exec(r.Context(), `UPDATE issue_delivery SET phase='needs_changes',error='方案已变化，需要重新确认',revision=revision+1 WHERE id=$1`, body.ID)
			if err == nil {
				err = tx.Commit(r.Context())
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
		}
		if body.Action != "fail" && (token == "" || token != body.Token) {
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
		case "accepted":
			if phase != "testing" {
				writeError(w, 409, "当前阶段不能完成联调")
				return
			}
			var submitted bool
			if tx.QueryRow(r.Context(), `SELECT acceptance_submitted FROM issue_delivery WHERE id=$1`, body.ID).Scan(&submitted) != nil || !submitted {
				writeError(w, 409, "缺少功能验收证据")
				return
			}
			next = "awaiting_review"
			ready, readyErr := deliveryAuthorizationReady(r.Context(), tx, ws, issue, body.ID)
			if readyErr != nil {
				writeError(w, 500, "读取授权复核失败")
				return
			}
			if ready {
				var author string
				err = tx.QueryRow(r.Context(), `SELECT COALESCE(a.comment_snapshot->>'author_id',d.approved_by::text) FROM issue_delivery d LEFT JOIN issue_delivery_authorization a ON a.id::text=d.plan_snapshot->'authorization_review'->>'authorization_id' AND a.kind='release' WHERE d.id=$1`, body.ID).Scan(&author)
				if err == nil {
					_, err = tx.Exec(r.Context(), `UPDATE issue_delivery SET approved_by=$2,approved_at=now() WHERE id=$1`, body.ID, author)
					next = "approved"
				}
			}
		case "validated":
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
			if phase != "testing" && phase != "preparing_release" {
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
	if body.Action != "notify" {
		_, err = tx.Exec(r.Context(), `UPDATE issue_delivery SET acceptance_submitted=CASE WHEN $8 IN ('claim','retry') THEN false ELSE acceptance_submitted END,release_comment_id=CASE WHEN $8='claim' AND phase='approved' THEN gen_random_uuid() ELSE release_comment_id END,release_validated=CASE WHEN $8='validated' THEN true WHEN $8 IN ('claim','retry') THEN false ELSE release_validated END,phase=$3,worker_token=$4,candidate_id=CASE WHEN $8='claim' THEN '' WHEN $5='' THEN candidate_id ELSE $5 END,candidate_url=CASE WHEN $6='' THEN candidate_url ELSE $6 END,error=$7,revision=revision+1,updated_at=now() WHERE workspace_id=$1 AND id=$2`, ws, body.ID, next, token, body.Candidate, body.URL, body.Error, body.Action)
	}
	var issueEvent *events.Event
	if err == nil && (next == "awaiting_review" || body.Action == "accepted") {
		q := h.Queries.WithTx(tx)
		previous, readErr := q.GetIssue(r.Context(), parseUUID(issue))
		err = readErr
		if err == nil {
			_, err = tx.Exec(r.Context(), `UPDATE issue SET status='in_review',revision=revision+1,updated_at=now() WHERE workspace_id=$1 AND id=$2`, ws, issue)
		}
		if err == nil {
			updated, readErr := q.GetIssue(r.Context(), parseUUID(issue))
			err = readErr
			if err == nil {
				status := "in_review"
				event, recordErr := service.RecordDurableEventTx(r.Context(), q, buildIssueUpdatedEvent(r.Context(), h, updated, previous, UpdateIssueRequest{Status: &status}, nil, "member", requestUserID(r)))
				err = recordErr
				issueEvent = &event
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
			payload, _ := json.Marshal(map[string]any{"id": operation, "content": mention + "交付 " + body.ID + " 的联调已准备，源码提交 " + commit + "，入口 " + url + "。请按已确认计划进行实际功能验收，使用 multica issue delivery accept 提交证据；本轮不要合入主分支或发布生产。"})
			rr := r.Clone(r.Context())
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

	writeJSON(w, 200, map[string]any{"id": body.ID, "phase": next, "revision": func() int64 {
		if body.Action == "notify" {
			return rev
		}
		return rev + 1
	}(), "worker_token": token, "issue_id": issue})
}
