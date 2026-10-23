package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

// 授权状态区分三种情况：当前候选已复核、存在有效授权但缺当前候选复核、没有可用授权。
// 缺候选复核才需要唤醒负责人；没有授权时等用户决定，不能自动批准。
const (
	deliveryReleaseReady   = "ready"
	deliveryReleaseUnbound = "unbound"
	deliveryReleaseMissing = "missing"
)

// 已有有效发布授权时的唤醒上限：自动通知只发一次，显式接续允许有限次补发。
const deliveryReviewRewakeLimit = 3

// 有效发布授权：评论版本、任务归属、用户身份与撤销状态都为真。
const deliveryReleaseAuthorizationSQL = `SELECT a.id::text FROM issue_delivery_authorization a
 JOIN comment c ON c.id=a.comment_id AND c.workspace_id=a.workspace_id AND c.issue_id=a.issue_id AND c.revision=a.comment_revision
 JOIN member m ON m.workspace_id=c.workspace_id AND m.user_id=c.author_id
 WHERE a.workspace_id=$1 AND a.issue_id=$2 AND a.plan_id=$3 AND a.kind='release' AND NOT a.revoked AND c.author_type='member' AND m.role IN ('owner','admin','member')
 ORDER BY a.created_at DESC LIMIT 1`

// deliveryAuthorizationNotice 记录待复核意图：通知号预先固定，重复轮询不会重复唤醒。
type deliveryAuthorizationNotice struct {
	CommentID       string `json:"comment_id"`
	CandidateID     string `json:"candidate_id"`
	SourceCommit    string `json:"source_commit"`
	CommentsVersion string `json:"comments_version"`
	AuthorizationID string `json:"authorization_id"`
	Attempts        int    `json:"attempts"`
}

func deliverySnapshotValue(snapshot json.RawMessage, key string) json.RawMessage {
	var values map[string]json.RawMessage
	if json.Unmarshal(snapshot, &values) != nil {
		return nil
	}
	return values[key]
}

func deliveryAuthorizationNoticeOf(snapshot json.RawMessage) deliveryAuthorizationNotice {
	var notice deliveryAuthorizationNotice
	if raw := deliverySnapshotValue(snapshot, "authorization_notice"); len(raw) > 0 {
		_ = json.Unmarshal(raw, &notice)
	}
	return notice
}

// deliveryReleaseState 判断 int 验收完成后该直接推进还是先唤醒复核。
//
// 1. 当前候选已有有效复核：直接推进发布。
// 2. 存在有效发布授权但未复核当前候选：登记待复核意图，交给负责人核对。
// 3. 没有有效授权、授权已撤销或范围不再匹配：等待用户决定。
func deliveryReleaseState(ctx context.Context, tx pgx.Tx, ws, issue, id string) (string, string, error) {
	var review json.RawMessage
	var plan string
	if err := tx.QueryRow(ctx, `SELECT COALESCE(plan_snapshot->'authorization_review','{}'::jsonb),plan_id::text FROM issue_delivery WHERE id=$1`, id).Scan(&review, &plan); err != nil {
		return "", "", err
	}
	ready, err := deliveryAuthorizationReady(ctx, tx, ws, issue, id)
	if err != nil {
		return "", "", err
	}
	var saved map[string]string
	_ = json.Unmarshal(review, &saved)
	if ready {
		return deliveryReleaseReady, saved["authorization_id"], nil
	}
	var authorization string
	err = tx.QueryRow(ctx, deliveryReleaseAuthorizationSQL, ws, issue, plan).Scan(&authorization)
	if errors.Is(err, pgx.ErrNoRows) {
		return deliveryReleaseMissing, "", nil
	}
	if err != nil {
		return "", "", err
	}
	return deliveryReleaseUnbound, authorization, nil
}

// deliveryAuthorizationNoticeFor 生成或复用待复核意图。
//
// 同一交付、候选、源码提交、评论依据和授权记录复用同一通知号；任一项变化说明复核依据
// 已变，换新通知号重新唤醒，旧通知不再代表当前候选。
func deliveryAuthorizationNoticeFor(ctx context.Context, tx pgx.Tx, ws, issue, id, authorization string) (json.RawMessage, error) {
	var candidate, source string
	var snapshot json.RawMessage
	if err := tx.QueryRow(ctx, `SELECT candidate_id,source_commit,plan_snapshot FROM issue_delivery WHERE id=$1`, id).Scan(&candidate, &source, &snapshot); err != nil {
		return nil, err
	}
	version, err := deliveryCommentsVersion(ctx, tx, ws, issue)
	if err != nil {
		return nil, err
	}
	notice := deliveryAuthorizationNoticeOf(snapshot)
	if notice.CommentID == "" || notice.CandidateID != candidate || notice.SourceCommit != source || notice.CommentsVersion != version || notice.AuthorizationID != authorization {
		notice = deliveryAuthorizationNotice{
			CommentID:       dbid.NewV7().String(),
			CandidateID:     candidate,
			SourceCommit:    source,
			CommentsVersion: version,
			AuthorizationID: authorization,
		}
	}
	return json.Marshal(notice)
}

// 通知文案说明为什么暂停和唯一继续路径，避免负责人重新向用户索取已给出的授权。
const deliveryReviewNotice = "交付 %s 已在 int 完成功能验收，候选 %s（源码提交 %s）。用户已授权发布（授权记录 %s，来源评论 %s），但该授权尚未复核当前候选，发布暂停。请阅读最新用户评论，核对改动范围、候选提交和验收证据，再按 `multica issue delivery status` 的 user_comments_version，用 `multica issue delivery approve` 提交 authorization_id、scope_review 和当前 user_comments_version 完成复核；范围未变时不要再让用户重复确认。归属仍被上一轮验收运行持有时先核对再 recover-owner 接续，不要抢占其他任务。候选实质变化、范围扩大或用户撤回授权时不要继续发布。"

// 复核失败或长期未接续时的可见说明，避免记录只剩“待审核”而没有下一步。
const deliveryReviewStalledNotice = "已有发布授权，但负责人复核未接续（候选 %s）。请核对通知是否送达、负责人运行是否结束，再显式接续复核；不要手工改写批准状态。"

// 复核唤醒前先看负责人是否已有活动运行：同一交付和候选不能再叠一轮。
type deliveryRowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func deliveryReviewRunActive(ctx context.Context, q deliveryRowQuerier, issue, assignee string) (bool, error) {
	var active bool
	err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_task_queue q WHERE q.issue_id=$1 AND q.status IN ('queued','dispatched','running','waiting_local_directory') AND ($2='' OR q.agent_id::text=$2))`, issue, assignee).Scan(&active)
	return active, err
}

// deliverDeliveryAuthorizationReview 用固定通知号唤醒当前负责人复核准确候选。
//
// 通知号已存在即视为已送达；负责人已有活动运行时保留待通知意图，等下一次轮询再接续，
// 既不重复派发也不丢工作。返回错误时待复核意图仍然保留，队列会重试。
func (h *Handler) deliverDeliveryAuthorizationReview(r *http.Request, ws, issue, id string) error {
	var phase, kind, assignee string
	var snapshot json.RawMessage
	if err := h.DB.QueryRow(r.Context(), `SELECT d.phase,COALESCE(i.assignee_type,''),COALESCE(i.assignee_id::text,''),d.plan_snapshot FROM issue_delivery d JOIN issue i ON i.id=d.issue_id WHERE d.workspace_id=$1 AND d.id=$2`, ws, id).Scan(&phase, &kind, &assignee, &snapshot); err != nil {
		return err
	}
	notice := deliveryAuthorizationNoticeOf(snapshot)
	if notice.CommentID == "" {
		return nil
	}
	if phase != "awaiting_review" {
		_, err := h.DB.Exec(r.Context(), `UPDATE issue_delivery SET plan_snapshot=plan_snapshot-'authorization_pending'-'authorization_notice' WHERE id=$1`, id)
		return err
	}
	var delivered bool
	if err := h.DB.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM comment WHERE id=$1 AND issue_id=$2)`, notice.CommentID, issue).Scan(&delivered); err != nil {
		return err
	}
	if delivered {
		_, err := h.DB.Exec(r.Context(), `UPDATE issue_delivery SET plan_snapshot=plan_snapshot-'authorization_pending' WHERE id=$1`, id)
		return err
	}
	var busy bool
	busy, err := deliveryReviewRunActive(r.Context(), h.DB, issue, assignee)
	if err != nil {
		return err
	}
	if busy {
		return nil
	}
	mention := ""
	if assignee != "" && (kind == "agent" || kind == "squad") {
		mention = fmt.Sprintf("[@负责人](mention://%s/%s)\n\n", kind, assignee)
	}
	content := fmt.Sprintf(deliveryReviewNotice, id, notice.CandidateID, notice.SourceCommit, notice.AuthorizationID, notice.CommentID)
	payload, _ := json.Marshal(map[string]any{"id": notice.CommentID, "content": mention + content})
	rr := r.Clone(r.Context())
	rr = rr.WithContext(context.WithValue(rr.Context(), commentIDContextKey{}, parseUUID(notice.CommentID)))
	route := chi.NewRouteContext()
	route.URLParams.Add("id", issue)
	rr = rr.WithContext(context.WithValue(rr.Context(), chi.RouteCtxKey, route))
	rr.Body = readCloser{bytes.NewReader(payload)}
	response := &supervisionFeedbackResponse{header: make(http.Header)}
	h.CreateComment(response, rr)
	if response.status >= 400 {
		return fmt.Errorf("通知负责人失败：%d", response.status)
	}
	_, err = h.DB.Exec(r.Context(), `UPDATE issue_delivery SET plan_snapshot=plan_snapshot-'authorization_pending' WHERE id=$1`, id)
	return err
}

// renotifyDeliveryAuthorizationReview 显式接续已送达但未完成复核的交付。
//
// 自动路径不重复唤醒同一交付和候选；只有通知已发出、负责人运行已结束且复核仍未绑定时，
// 才允许换新通知号补发一次，并受 deliveryReviewRewakeLimit 约束。达到上限后保留原意图并
// 标记 authorization_stalled，重复调用只返回同一失败原因，不再补发也不再从零计数。
func (h *Handler) renotifyDeliveryAuthorizationReview(r *http.Request, ws, issue, id string) error {
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		return err
	}
	defer tx.Rollback(r.Context())
	var snapshot json.RawMessage
	var phase string
	if err = tx.QueryRow(r.Context(), `SELECT phase,plan_snapshot FROM issue_delivery WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, ws, id).Scan(&phase, &snapshot); err != nil {
		return err
	}
	if phase != "awaiting_review" {
		return fmt.Errorf("当前无需授权核对")
	}
	state, _, err := deliveryReleaseState(r.Context(), tx, ws, issue, id)
	if err != nil {
		return err
	}
	if state != deliveryReleaseUnbound {
		if _, err = tx.Exec(r.Context(), `UPDATE issue_delivery SET plan_snapshot=plan_snapshot-'authorization_pending'-'authorization_notice' WHERE id=$1`, id); err != nil {
			return err
		}
		if err = tx.Commit(r.Context()); err != nil {
			return err
		}
		return fmt.Errorf("当前没有等待负责人复核的有效授权")
	}
	notice := deliveryAuthorizationNoticeOf(snapshot)
	var delivered bool
	if err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM comment WHERE id=$1 AND issue_id=$2)`, notice.CommentID, issue).Scan(&delivered); err != nil {
		return err
	}
	if !delivered && notice.CommentID != "" {
		return fmt.Errorf("通知尚未送达，等待自动重试")
	}
	// 上一次复核运行仍未结束，再来一轮只会并行改写同一交付。
	var assignee string
	if err = tx.QueryRow(r.Context(), `SELECT COALESCE(i.assignee_id::text,'') FROM issue_delivery d JOIN issue i ON i.id=d.issue_id WHERE d.id=$1`, id).Scan(&assignee); err != nil {
		return err
	}
	busy, err := deliveryReviewRunActive(r.Context(), tx, issue, assignee)
	if err != nil {
		return err
	}
	if busy {
		return fmt.Errorf("上一次复核运行尚未结束，等待其完成再显式接续")
	}
	// 1. 达到补发上限后停止自动重试并保留原意图：清掉通知号会让下一次调用重新从 0 计数，
	// 上限就失去意义。超限后只说明可见原因，等人工按原任务排查后显式接续。
	if notice.Attempts >= deliveryReviewRewakeLimit {
		var stalled bool
		if err = tx.QueryRow(r.Context(), `SELECT COALESCE(plan_snapshot->>'authorization_stalled','')='true' FROM issue_delivery WHERE id=$1`, id).Scan(&stalled); err != nil {
			return err
		}
		if !stalled {
			_, err = tx.Exec(r.Context(), `UPDATE issue_delivery SET error=$2,plan_snapshot=(plan_snapshot-'authorization_pending')||'{"authorization_stalled":true}'::jsonb WHERE id=$1`, id, fmt.Sprintf(deliveryReviewStalledNotice, notice.CandidateID))
			if err != nil {
				return err
			}
		}
		if err = tx.Commit(r.Context()); err != nil {
			return err
		}
		return fmt.Errorf("复核已多次未接续，需要人工处理")
	}
	notice.CommentID = dbid.NewV7().String()
	notice.Attempts++
	raw, err := json.Marshal(notice)
	if err != nil {
		return err
	}
	_, err = tx.Exec(r.Context(), `UPDATE issue_delivery SET plan_snapshot=jsonb_set(plan_snapshot,'{authorization_notice}',$2::jsonb)||'{"authorization_pending":true}'::jsonb-'authorization_stalled',error=$3 WHERE id=$1`, id, raw, fmt.Sprintf("已有发布授权，等待负责人复核当前候选（第 %d 次接续）", notice.Attempts))
	if err != nil {
		return err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	return h.deliverDeliveryAuthorizationReview(r, ws, issue, id)
}
