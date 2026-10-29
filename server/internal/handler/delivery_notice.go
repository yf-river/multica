package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"net/http"
	"strings"
)

// 发布结果通知文案：已发布按「生产验证通过即收尾」，失败按「暂停并修复」，两者都不能让 Agent 把未完成的交付当成完成。
const (
	// 1. 生产验证通过即收尾，不再要求用户另行授权关闭；验证未通过或发布异常时明确禁止标记完成。
	publishedNotice = "交付 %s 生产发布已完成，候选 %s。请核对实际生产版本、执行生产页面验证并上传截图；生产验证通过后直接标记任务完成，无需再次等待用户确认；验证未通过或发布异常时不要标记完成，按原因修复后重新交付。"
	// 2. 发布失败只暂停交付，任务保持未完成，修复后按既有授权接续。
	pausedNotice = "交付 %s 暂停，原因：%s。请核对实际环境和恢复资料，在既有授权内修复后接续；不要盲目重跑或覆盖其他任务占用。"
	// 3. 组合候选就绪：本次发布清单已固定，成员一起验证组合路径，未纳入的交付不会被夹带。
	batchReadyNotice = "交付 %s 已进入组合候选 %s，本批成员在此候选上一起验收登录、聊天、Agent 执行及相互影响。提交证据后按批次发布，未纳入本批的任务保持原状。"
)

// deliveryNoticeText 按阶段选择通知文案，避免两处各写一份而漂移。
func deliveryNoticeText(stage, delivery, candidate, detail string) string {
	if strings.HasPrefix(stage, "verification_failed:") || strings.HasPrefix(stage, "verification_update_failed:") {
		return fmt.Sprintf("交付 %s 的测试执行未提交通过证据：%s。原测试环境仍保留，请在原任务目录检查问题并继续测试；代码有改动时登记新提交后重新验收。", delivery, detail)
	}
	if stage == "needs_changes" {
		return fmt.Sprintf(pausedNotice, delivery, detail)
	}
	if stage == "batch_ready" {
		return fmt.Sprintf(batchReadyNotice, delivery, candidate) + detail
	}
	return fmt.Sprintf(publishedNotice, delivery, candidate)
}

// notifyDeliveryResult 用持久化通知号续接任务，服务重启或响应丢失不重复派发。
// 修改 5. 组合候选通知要指向批次固定的候选：成员单任务阶段保存的 candidate_id 不是组合产物，
// 混用会让 Agent 去验收另一份候选。
func (h *Handler) notifyDeliveryResult(r *http.Request, ws, issue, delivery, stage string) (bool, error) {
	var id, kind, assignee, candidate, detail string
	var done bool
	err := h.DB.QueryRow(r.Context(), `SELECT n.comment_id::text,n.delivered_at IS NOT NULL,COALESCE(i.assignee_type,''),COALESCE(i.assignee_id::text,''),COALESCE(NULLIF((SELECT b.candidate_id FROM delivery_batch b JOIN delivery_batch_member m ON m.batch_id=b.id WHERE m.delivery_id=n.delivery_id AND b.status IN ('validating','publishing') AND b.candidate_id<>'' ORDER BY b.created_at DESC LIMIT 1),''),d.candidate_id),d.error FROM delivery_notice n JOIN issue_delivery d ON d.id=n.delivery_id JOIN issue i ON i.id=d.issue_id WHERE n.delivery_id=$1 AND n.stage=$2 AND d.workspace_id=$3`, delivery, stage, ws).Scan(&id, &done, &kind, &assignee, &candidate, &detail)
	if err != nil || done {
		return false, err
	}
	var ownerTx pgx.Tx
	var externalCloseout bool
	if stage == "batch_ready" || stage == "published" {
		ownerTx, err = h.TxStarter.Begin(r.Context())
		if err != nil {
			return false, err
		}
		defer ownerTx.Rollback(r.Context())
		external, checkErr := externalDeliveryVerificationOwner(r.Context(), ownerTx, ws, issue)
		if checkErr != nil {
			return false, checkErr
		}
		if external {
			if stage == "published" {
				// 外部执行者收尾期间由其验收；完成时间必须晚于该交付发布。
				err = ownerTx.QueryRow(r.Context(), `SELECT i.status='done' AND i.updated_at>=d.updated_at FROM issue_delivery d JOIN issue i ON i.id=d.issue_id WHERE d.id=$1 AND d.workspace_id=$2 AND d.phase='published'`, delivery, ws).Scan(&externalCloseout)
				if err != nil {
					return false, err
				}
			}
			if !externalCloseout {
				return true, nil
			}
		}
	}
	var completedAudit bool
	if stage == "published" && !externalCloseout {
		pending, completed, checkErr := h.resumedPublishedCloseout(r.Context(), ws, issue, delivery, candidate)
		if checkErr != nil {
			return false, checkErr
		}
		if pending {
			// 恢复任务还在执行，通知保持未投递，由既有持久队列重试。
			return true, nil
		}
		completedAudit = completed
	}
	projectBatch := false
	if stage == "batch_ready" {
		var representative string
		err = h.DB.QueryRow(r.Context(), `SELECT m.delivery_id::text,b.project_id IS NOT NULL FROM delivery_batch_member m JOIN delivery_batch b ON b.id=m.batch_id WHERE b.id=(SELECT b.id FROM delivery_batch b JOIN delivery_batch_member m ON m.batch_id=b.id WHERE m.delivery_id=$1 AND b.status='validating' ORDER BY b.created_at DESC LIMIT 1) AND m.result='pending' ORDER BY m.created_at,m.delivery_id LIMIT 1`, delivery).Scan(&representative, &projectBatch)
		if err != nil {
			return false, err
		}
		if projectBatch {
			detail = fmt.Sprintf("请在这个组合候选上验证本任务需要联调的业务，使用 multica issue delivery accept --delivery %s --file <JSON> 提交结果。JSON 必须包含当前 revision、candidate_id=%s 和实际 evidence；发布 Agent 负责协调本批和基本业务检查。", delivery, candidate)
		} else {
			detail = fmt.Sprintf("本批代表交付为 %s；由代表汇总全部成员及集成冒烟证据，使用 multica issue delivery accept --delivery <代表交付> --file <JSON> 提交。JSON 必须包含当前 revision、candidate_id=%s 和实际 evidence。其他成员协助验收，不重复登记候选或单独发布。", representative, candidate)
		}
	}
	var exists bool
	if err = h.DB.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM comment WHERE id=$1 AND issue_id=$2)`, id, issue).Scan(&exists); err != nil {
		return false, err
	}
	if !exists {
		mention := ""
		if assignee != "" && (kind == "agent" || kind == "squad") {
			mention = fmt.Sprintf("[@负责人](mention://%s/%s)\n\n", kind, assignee)
		}
		text := deliveryNoticeText(stage, delivery, candidate, detail)
		if projectBatch {
			text = fmt.Sprintf("交付 %s 已进入组合候选 %s。", delivery, candidate) + detail
		}
		if completedAudit {
			// 便签在创建、完成补派和评论重放路径都持久免触发。
			mention = ""
			text = fmt.Sprintf("/note 交付 %s 的候选 %s 已发布；本次发布恢复执行已完成生产验收，任务已完成。", delivery, candidate)
		}
		if externalCloseout {
			mention = ""
			text = fmt.Sprintf("/note 交付 %s 的候选 %s 已发布；当前外部执行者已在发布后完成任务收尾。", delivery, candidate)
		}
		payload, _ := json.Marshal(map[string]any{"id": id, "content": mention + text})
		rr := r.Clone(context.WithValue(r.Context(), systemCommentContextKey{}, true))
		rr = rr.WithContext(context.WithValue(rr.Context(), commentIDContextKey{}, parseUUID(id)))
		route := chi.NewRouteContext()
		route.URLParams.Add("id", issue)
		rr = rr.WithContext(context.WithValue(rr.Context(), chi.RouteCtxKey, route))
		rr.Body = readCloser{bytes.NewReader(payload)}
		response := &supervisionFeedbackResponse{header: make(http.Header)}
		h.CreateComment(response, rr)
		if response.status >= 400 {
			return false, fmt.Errorf("结果评论失败：%d", response.status)
		}
	}
	_, err = h.DB.Exec(r.Context(), `UPDATE delivery_notice SET delivered_at=now() WHERE delivery_id=$1 AND stage=$2`, delivery, stage)
	if err == nil && ownerTx != nil {
		err = ownerTx.Commit(r.Context())
	}
	return false, err
}
