package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/go-chi/chi/v5"
	"net/http"
)

// 发布结果通知文案：已发布按「生产验证通过即收尾」，失败按「暂停并修复」，两者都不能让 Agent 把未完成的交付当成完成。
const (
	// 1. 生产验证通过即收尾，不再要求用户另行授权关闭；验证未通过或发布异常时明确禁止标记完成。
	publishedNotice = "交付 %s 生产发布已完成，候选 %s。请核对实际生产版本、执行生产页面验证并上传截图；生产验证通过后直接标记任务完成，无需再次等待用户确认；验证未通过或发布异常时不要标记完成，按原因修复后重新交付。"
	// 2. 发布失败只暂停交付，任务保持未完成，修复后按既有授权接续。
	pausedNotice = "交付 %s 暂停，原因：%s。请核对实际环境和恢复资料，在既有授权内修复后接续；不要盲目重跑或覆盖其他任务占用。"
)

// deliveryNoticeText 按阶段选择通知文案，避免两处各写一份而漂移。
func deliveryNoticeText(stage, delivery, candidate, detail string) string {
	if stage == "needs_changes" {
		return fmt.Sprintf(pausedNotice, delivery, detail)
	}
	return fmt.Sprintf(publishedNotice, delivery, candidate)
}

// notifyDeliveryResult 用持久化通知号续接任务，服务重启或响应丢失不重复派发。
func (h *Handler) notifyDeliveryResult(r *http.Request, ws, issue, delivery, stage string) error {
	var id, kind, assignee, candidate, detail string
	var done bool
	err := h.DB.QueryRow(r.Context(), `SELECT n.comment_id::text,n.delivered_at IS NOT NULL,COALESCE(i.assignee_type,''),COALESCE(i.assignee_id::text,''),d.candidate_id,d.error FROM delivery_notice n JOIN issue_delivery d ON d.id=n.delivery_id JOIN issue i ON i.id=d.issue_id WHERE n.delivery_id=$1 AND n.stage=$2 AND d.workspace_id=$3`, delivery, stage, ws).Scan(&id, &done, &kind, &assignee, &candidate, &detail)
	if err != nil || done {
		return err
	}
	var exists bool
	if err = h.DB.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM comment WHERE id=$1 AND issue_id=$2)`, id, issue).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		mention := ""
		if assignee != "" && (kind == "agent" || kind == "squad") {
			mention = fmt.Sprintf("[@负责人](mention://%s/%s)\n\n", kind, assignee)
		}
		text := deliveryNoticeText(stage, delivery, candidate, detail)
		payload, _ := json.Marshal(map[string]any{"id": id, "content": mention + text})
		rr := r.Clone(r.Context())
		rr = rr.WithContext(context.WithValue(rr.Context(), commentIDContextKey{}, parseUUID(id)))
		route := chi.NewRouteContext()
		route.URLParams.Add("id", issue)
		rr = rr.WithContext(context.WithValue(rr.Context(), chi.RouteCtxKey, route))
		rr.Body = readCloser{bytes.NewReader(payload)}
		response := &supervisionFeedbackResponse{header: make(http.Header)}
		h.CreateComment(response, rr)
		if response.status >= 400 {
			return fmt.Errorf("结果评论失败：%d", response.status)
		}
	}
	_, err = h.DB.Exec(r.Context(), `UPDATE delivery_notice SET delivered_at=now() WHERE delivery_id=$1 AND stage=$2`, delivery, stage)
	return err
}
