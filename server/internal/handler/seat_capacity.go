// 本文件把「席位容量」（seat capacity）的编排接进各类加人/入会 handler。容量指
// workspace 已购买的付费成员席位数——邀请被接受、分享链接加入、成员落库等操作都
// 可能让 workspace 超出已购席位，因此必须先经 seatcapacity 服务裁决。
//
// 统一的编排模式：每个容量动作先落一条 seat_capacity_intent（操作意图）行，再调用
// seatcapacity 服务做真实裁决；通过则把意图标记为 delivered，失败则删除意图或把它
// 补偿成 release，并把重试/释放排进 outbox 供后台执行。同一 workspace 的意图读写
// 用 SeatCapacityLocker 加锁串行（拿不到锁时降级为失败关闭），避免两个并发加入
// 互相超卖。
//
// 关键约束：ActionConfirm 时对应席位已在业务事务里真正消费，补偿绝不能释放它——
// 一个失败的重复请求不能把赢家成员的席位放掉（见 compensateCapacityIntentLocked）。
package handler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/seatcapacity"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// 三个本文件可辨识的容量错误（其中 Full / Overcommitted 是需要返回给客户端的状态，
// Unavailable 表示容量服务暂时不可用）。
var (
	errSeatCapacityFull          = errors.New("no purchased member seats are available")
	errSeatCapacityOvercommitted = errors.New("workspace members exceed purchased seat capacity")
	errSeatCapacityUnavailable   = errors.New("seat capacity service is unavailable")
)

// isPersistentSeatCapacityAdmissionRejection 判断错误是否为「持久性的准入拒绝」
// （已满 / 已超卖）——这类错误重试不会成功，应直接反馈用户；其余错误可能是瞬时的。
func isPersistentSeatCapacityAdmissionRejection(err error) bool {
	return errors.Is(err, errSeatCapacityFull) || errors.Is(err, errSeatCapacityOvercommitted)
}

// seatCapacityWorkspaceLockWait 是等待 workspace 容量锁的最长时间，超时即视为
// 容量服务不可用、失败关闭。
const seatCapacityWorkspaceLockWait = 2 * time.Second

// seatCapacityEnabled 判断本部署是否接入了席位容量（Handler 上挂了 SeatCapacity
// 服务才启用；没接入时所有容量编排直接放行）。
func (h *Handler) seatCapacityEnabled() bool {
	return h != nil && h.SeatCapacity != nil
}

// lockSeatCapacityWorkspace 取得当前 workspace 的容量锁，返回「锁内专用」的
// *db.Queries（若配置了 SeatCapacityLocker，则指向锁定的连接池）与解锁函数。
// 没配锁时退化为直接使用默认 Queries、空解锁函数。
func (h *Handler) lockSeatCapacityWorkspace(ctx context.Context, workspaceID uuid.UUID) (*db.Queries, func(), error) {
	if h == nil {
		return nil, nil, errors.New("seat capacity handler is unavailable")
	}
	if h.SeatCapacityLocker == nil {
		return h.Queries, func() {}, nil
	}
	lockCtx, cancel := context.WithTimeout(ctx, seatCapacityWorkspaceLockWait)
	defer cancel()
	lockedDB, unlock, err := h.SeatCapacityLocker.Lock(lockCtx, workspaceID)
	if err != nil {
		return nil, nil, err
	}
	return db.New(lockedDB), unlock, nil
}

// capacityIntentParams 组装一条 seat_capacity_intent 的公共字段（以 operation token
// 唯一标识一次容量操作；NextAttemptAt 是后台重试/释放到期时间）。
func capacityIntentParams(workspaceID, token uuid.UUID, action string, due time.Time) db.UpsertSeatCapacityIntentParams {
	return db.UpsertSeatCapacityIntentParams{
		WorkspaceID:    uuidToPG(workspaceID),
		OperationToken: uuidToPG(token),
		Action:         action,
		NextAttemptAt:  pgtype.Timestamptz{Time: due, Valid: true},
	}
}

// reserveInvitationCapacity 在处理「邀请接受」前预占一个席位：先落意图行，再让
// seatcapacity 裁决；被限流/超卖/拒绝时清理意图并映射成本文件的哨兵错误。返回 nil
// 表示可以继续走成员落库流程。
func (h *Handler) reserveInvitationCapacity(ctx context.Context, workspaceID, invitationID uuid.UUID, expiresAt time.Time) error {
	if !h.seatCapacityEnabled() {
		return nil
	}
	q, unlock, err := h.lockSeatCapacityWorkspace(ctx, workspaceID)
	if err != nil {
		return fmt.Errorf("%w: acquire workspace capacity lock: %v", errSeatCapacityUnavailable, err)
	}
	defer unlock()
	params := capacityIntentParams(workspaceID, invitationID, seatcapacity.ActionReserveInvitation, seatcapacity.RecoveryDue(time.Now()).Time)
	params.SubjectID = uuidToPG(invitationID)
	params.InvitationID = uuidToPG(invitationID)
	params.ExpiresAt = pgtype.Timestamptz{Time: expiresAt, Valid: true}
	if _, err := q.UpsertSeatCapacityIntent(ctx, params); err != nil {
		return fmt.Errorf("record invitation capacity intent: %w", err)
	}
	decision, err := h.SeatCapacity.ReserveInvitation(ctx, workspaceID, invitationID, expiresAt)
	if err != nil {
		if seatcapacity.IsRateLimited(err) {
			if deleteErr := deleteCapacityIntentForAction(ctx, q, invitationID, seatcapacity.ActionReserveInvitation); deleteErr != nil {
				return fmt.Errorf("discard rate-limited invitation capacity intent: %w", deleteErr)
			}
			return fmt.Errorf("reserve invitation capacity: %w", err)
		}
		if seatcapacity.IsCapacityOvercommitted(err) {
			if deleteErr := deleteCapacityIntentForAction(ctx, q, invitationID, seatcapacity.ActionReserveInvitation); deleteErr != nil {
				return fmt.Errorf("discard overcommitted invitation capacity intent: %w", deleteErr)
			}
			return errSeatCapacityOvercommitted
		}
		h.compensateCapacityIntentLocked(ctx, q, invitationID)
		return fmt.Errorf("%w: %v", errSeatCapacityUnavailable, err)
	}
	if !decision.Managed {
		return deleteCapacityIntentForAction(ctx, q, invitationID, seatcapacity.ActionReserveInvitation)
	}
	if !decision.Allowed {
		if deleteErr := deleteCapacityIntentForAction(ctx, q, invitationID, seatcapacity.ActionReserveInvitation); deleteErr != nil {
			return fmt.Errorf("discard rejected invitation capacity intent: %w", deleteErr)
		}
		if decision.Reason == "capacity_full" {
			return errSeatCapacityFull
		}
		return fmt.Errorf("%w: reservation rejected in state %s", errSeatCapacityUnavailable, decision.Reason)
	}
	if err := q.MarkSeatCapacityIntentDelivered(ctx, db.MarkSeatCapacityIntentDeliveredParams{
		OperationToken: uuidToPG(invitationID), Action: seatcapacity.ActionReserveInvitation,
	}); err != nil {
		h.compensateCapacityIntentLocked(ctx, q, invitationID)
		return fmt.Errorf("record invitation capacity reservation: %w", err)
	}
	return nil
}

// beginCapacityConsume 在成员正式落库时「消费」预留的席位（邀请路径）：
// 裁决通过则把意图置为 delivered 并返回 consumed=true；拒绝/限流则清理或报错。
func (h *Handler) beginCapacityConsume(ctx context.Context, workspaceID, token, invitationID, userID uuid.UUID) (bool, error) {
	if !h.seatCapacityEnabled() {
		return false, nil
	}
	q, unlock, err := h.lockSeatCapacityWorkspace(ctx, workspaceID)
	if err != nil {
		return false, fmt.Errorf("%w: acquire workspace capacity lock: %v", errSeatCapacityUnavailable, err)
	}
	defer unlock()
	params := capacityIntentParams(workspaceID, token, seatcapacity.ActionConsumeInvitation, seatcapacity.RecoveryDue(time.Now()).Time)
	params.SubjectID = uuidToPG(token)
	params.InvitationID = uuidToPG(invitationID)
	params.UserID = uuidToPG(userID)
	if _, err := q.UpsertSeatCapacityIntent(ctx, params); err != nil {
		return false, fmt.Errorf("record invitation consume intent: %w", err)
	}
	decision, err := h.SeatCapacity.Consume(ctx, workspaceID, token)
	if err != nil {
		if seatcapacity.IsRateLimited(err) {
			return false, fmt.Errorf("consume invitation capacity: %w", err)
		}
		return false, fmt.Errorf("%w: %v", errSeatCapacityUnavailable, err)
	}
	if !decision.Managed {
		return false, deleteCapacityIntentForAction(ctx, q, token, seatcapacity.ActionConsumeInvitation)
	}
	if !decision.Allowed {
		if expireErr := q.ExpireInvitationForCapacityRecovery(ctx, uuidToPG(invitationID)); expireErr != nil {
			return false, fmt.Errorf("expire invitation after rejected capacity consume: %w", expireErr)
		}
		if deleteErr := deleteCapacityIntentForAction(ctx, q, token, seatcapacity.ActionConsumeInvitation); deleteErr != nil {
			return false, fmt.Errorf("discard rejected invitation consume intent: %w", deleteErr)
		}
		return false, errSeatCapacityFull
	}
	err = q.MarkSeatCapacityIntentDelivered(ctx, db.MarkSeatCapacityIntentDeliveredParams{
		OperationToken: uuidToPG(token), Action: seatcapacity.ActionConsumeInvitation,
	})
	return err == nil, err
}

// beginShareJoinCapacity 在「分享链接加入」路径认领一个席位：建一条随机的 operation
// token 意图并让 seatcapacity 裁决，返回该 token 供后续 confirm 使用（token 是本次
// 加入事务在容量侧的凭据）。
func (h *Handler) beginShareJoinCapacity(ctx context.Context, workspaceID, shareLinkID, userID uuid.UUID) (uuid.UUID, error) {
	if !h.seatCapacityEnabled() {
		return uuid.Nil, nil
	}
	q, unlock, err := h.lockSeatCapacityWorkspace(ctx, workspaceID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("%w: acquire workspace capacity lock: %v", errSeatCapacityUnavailable, err)
	}
	defer unlock()
	intent, err := q.CreateOrReactivateShareJoinCapacityIntent(ctx, db.CreateOrReactivateShareJoinCapacityIntentParams{
		WorkspaceID: uuidToPG(workspaceID), OperationToken: uuidToPG(uuid.New()),
		ShareLinkID: uuidToPG(shareLinkID), UserID: uuidToPG(userID),
		NextAttemptAt: seatcapacity.RecoveryDue(time.Now()),
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("record share-join capacity intent: %w", err)
	}
	token := uuid.UUID(intent.OperationToken.Bytes)
	decision, err := h.SeatCapacity.ClaimShareJoin(ctx, workspaceID, token)
	if err != nil {
		if seatcapacity.IsRateLimited(err) {
			if deleteErr := deleteCapacityIntentForAction(ctx, q, token, seatcapacity.ActionClaimShareJoin); deleteErr != nil {
				return uuid.Nil, fmt.Errorf("discard rate-limited share-join capacity intent: %w", deleteErr)
			}
			return uuid.Nil, fmt.Errorf("claim share-join capacity: %w", err)
		}
		if seatcapacity.IsCapacityOvercommitted(err) {
			if deleteErr := deleteCapacityIntentForAction(ctx, q, token, seatcapacity.ActionClaimShareJoin); deleteErr != nil {
				return uuid.Nil, fmt.Errorf("discard overcommitted share-join capacity intent: %w", deleteErr)
			}
			return uuid.Nil, errSeatCapacityOvercommitted
		}
		return uuid.Nil, fmt.Errorf("%w: %v", errSeatCapacityUnavailable, err)
	}
	if !decision.Managed {
		return uuid.Nil, deleteCapacityIntentForAction(ctx, q, token, seatcapacity.ActionClaimShareJoin)
	}
	if !decision.Allowed {
		if deleteErr := deleteCapacityIntentForAction(ctx, q, token, seatcapacity.ActionClaimShareJoin); deleteErr != nil {
			return uuid.Nil, fmt.Errorf("discard rejected share-join capacity intent: %w", deleteErr)
		}
		return uuid.Nil, errSeatCapacityFull
	}
	if err := q.MarkSeatCapacityIntentDelivered(ctx, db.MarkSeatCapacityIntentDeliveredParams{
		OperationToken: uuidToPG(token), Action: seatcapacity.ActionClaimShareJoin,
	}); err != nil {
		return uuid.Nil, err
	}
	return token, nil
}

// transitionCapacityIntentToConfirm 把意图从当前阶段推进到 confirm 阶段（事务提交
// 后由 confirmCapacityIntent 真正完成消费）。rows!=1 说明意图已被并发改动，返回错误
// 让调用方放弃而不是误确认。
func transitionCapacityIntentToConfirm(ctx context.Context, q *db.Queries, token, memberID uuid.UUID, currentAction string) error {
	rows, err := q.TransitionSeatCapacityIntent(ctx, db.TransitionSeatCapacityIntentParams{
		NextAction: seatcapacity.ActionConfirm, CurrentAction: currentAction,
		MemberID: uuidToPG(memberID), OperationToken: uuidToPG(token),
		NextAttemptAt: seatcapacity.RetryDue(time.Now()),
	})
	if err != nil {
		return err
	}
	if rows != 1 {
		return errors.New("seat capacity intent changed concurrently")
	}
	return nil
}

// enqueueCapacityRelease 给后台排一条「释放容量」的意图（产品事务失败需要回补席位时
// 使用）。
func enqueueCapacityRelease(ctx context.Context, q *db.Queries, workspaceID, token uuid.UUID) error {
	_, err := q.EnqueueSeatCapacityRelease(ctx, db.EnqueueSeatCapacityReleaseParams{
		WorkspaceID: uuidToPG(workspaceID), OperationToken: uuidToPG(token),
		SubjectID: uuidToPG(token), NextAttemptAt: seatcapacity.RetryDue(time.Now()),
	})
	return err
}

// enqueueMemberCapacityRelease 排一条「按成员释放席位」的意图（成员被移除时使用，
// 用新的随机 token 标识这次释放操作）。
func enqueueMemberCapacityRelease(ctx context.Context, q *db.Queries, workspaceID, memberID uuid.UUID) error {
	operationToken := uuid.New()
	params := capacityIntentParams(workspaceID, operationToken, seatcapacity.ActionReleaseMember, time.Now())
	params.MemberID = uuidToPG(memberID)
	_, err := q.UpsertSeatCapacityIntent(ctx, params)
	return err
}

// confirmCapacityIntent 确认一次已随业务事务提交的席位消费：只有意图正处于
// confirm 阶段时才真正让 seatcapacity 入账；服务不可用时留给后台重试，绝不回滚
// 已提交的加入事务。
func (h *Handler) confirmCapacityIntent(ctx context.Context, workspaceID, token, memberID uuid.UUID) {
	if !h.seatCapacityEnabled() {
		return
	}
	q, unlock, err := h.lockSeatCapacityWorkspace(ctx, workspaceID)
	if err != nil {
		slog.WarnContext(ctx, "seat capacity confirm deferred while workspace lock is unavailable",
			"workspace_id", workspaceID.String(), "operation_token", token.String(), "error", err)
		return
	}
	defer unlock()
	intent, err := q.GetSeatCapacityIntent(ctx, uuidToPG(token))
	if err != nil || intent.Action != seatcapacity.ActionConfirm {
		return
	}
	decision, err := h.SeatCapacity.Confirm(ctx, workspaceID, token, memberID)
	if err == nil && (!decision.Managed || decision.Allowed) {
		if deleteErr := deleteCapacityIntentForAction(ctx, q, token, seatcapacity.ActionConfirm); deleteErr == nil {
			return
		} else {
			err = deleteErr
		}
	}
	if err == nil {
		err = fmt.Errorf("confirm rejected in state %s", decision.Reason)
	}
	recordCapacityFailure(ctx, q, token, seatcapacity.ActionConfirm, err)
}

// compensateCapacityIntent 按 token 补偿一次失败的容量操作（无锁版本：先读出意图
// 确定 workspace，再上锁执行补偿）。
func (h *Handler) compensateCapacityIntent(ctx context.Context, token uuid.UUID) {
	if !h.seatCapacityEnabled() {
		return
	}
	intent, err := h.Queries.GetSeatCapacityIntent(ctx, uuidToPG(token))
	if err != nil {
		return
	}
	q, unlock, err := h.lockSeatCapacityWorkspace(ctx, uuid.UUID(intent.WorkspaceID.Bytes))
	if err != nil {
		return
	}
	defer unlock()
	h.compensateCapacityIntentLocked(ctx, q, token)
}

// compensateCapacityIntentLocked 在持锁前提下补偿一次失败动作：把意图迁到 release
// 阶段并调用 seatcapacity 释放对应席位；释放成功或本就找不到时删除意图。
func (h *Handler) compensateCapacityIntentLocked(ctx context.Context, q *db.Queries, token uuid.UUID) {
	intent, err := q.GetSeatCapacityIntent(ctx, uuidToPG(token))
	if err != nil {
		return
	}
	// confirm 行对应的是一个已经提交的产品事务。输掉竞争的重复请求绝不能把赢家
	// 成员的席位释放掉。
	if intent.Action == seatcapacity.ActionConfirm || intent.Action == seatcapacity.ActionReleaseMember {
		return
	}
	rows, err := q.TransitionSeatCapacityIntent(ctx, db.TransitionSeatCapacityIntentParams{
		NextAction: seatcapacity.ActionRelease, CurrentAction: intent.Action,
		OperationToken: uuidToPG(token), NextAttemptAt: seatcapacity.RetryDue(time.Now()),
	})
	if err != nil || rows != 1 {
		return
	}
	decision, releaseErr := h.SeatCapacity.Release(ctx, uuid.UUID(intent.WorkspaceID.Bytes), token)
	if (releaseErr == nil && (!decision.Managed || decision.Allowed || decision.Reason == "released")) || seatcapacity.IsNotFound(releaseErr) {
		_ = deleteCapacityIntentForAction(ctx, q, token, seatcapacity.ActionRelease)
		return
	}
	if releaseErr == nil {
		releaseErr = fmt.Errorf("release rejected in state %s", decision.Reason)
	}
	recordCapacityFailure(ctx, q, token, seatcapacity.ActionRelease, releaseErr)
}

// settleMemberCapacityRelease 结算一条按成员释放席位的意图（成员移除已提交后调用）：
// 从容量侧真正释放该成员所占席位，成功后清理意图。
func (h *Handler) settleMemberCapacityRelease(ctx context.Context, workspaceID, memberID uuid.UUID) {
	if !h.seatCapacityEnabled() {
		return
	}
	q, unlock, err := h.lockSeatCapacityWorkspace(ctx, workspaceID)
	if err != nil {
		return
	}
	defer unlock()
	intent, err := q.GetMemberReleaseCapacityIntent(ctx, db.GetMemberReleaseCapacityIntentParams{
		WorkspaceID: uuidToPG(workspaceID), MemberID: uuidToPG(memberID),
	})
	if err != nil {
		return
	}
	decision, releaseErr := h.SeatCapacity.ReleaseMember(ctx, workspaceID, memberID)
	if (releaseErr == nil && (!decision.Managed || decision.Allowed || decision.Reason == "released")) || seatcapacity.IsNotFound(releaseErr) {
		_ = deleteCapacityIntentForAction(ctx, q, uuid.UUID(intent.OperationToken.Bytes), seatcapacity.ActionReleaseMember)
		return
	}
	if releaseErr == nil {
		releaseErr = fmt.Errorf("member release rejected in state %s", decision.Reason)
	}
	recordCapacityFailure(ctx, q, uuid.UUID(intent.OperationToken.Bytes), intent.Action, releaseErr)
}

// deleteCapacityIntentForAction 按 (token, action) 删除意图行，用于「裁决已结束、
// 无需再追踪」的收尾。
func deleteCapacityIntentForAction(ctx context.Context, q *db.Queries, token uuid.UUID, action string) error {
	return q.DeleteSeatCapacityIntentForAction(ctx, db.DeleteSeatCapacityIntentForActionParams{
		OperationToken: uuidToPG(token), Action: action,
	})
}

// recordCapacityFailure 把失败的容量操作标记为 failed 并排进 outbox 后台重试，
// 同时记录告警日志。
func recordCapacityFailure(ctx context.Context, q *db.Queries, token uuid.UUID, action string, capacityErr error) {
	if capacityErr == nil {
		return
	}
	_ = q.MarkSeatCapacityIntentFailed(ctx, db.MarkSeatCapacityIntentFailedParams{
		LastError: capacityErr.Error(), NextAttemptAt: pgtype.Timestamptz{Time: time.Now().Add(5 * time.Second), Valid: true},
		OperationToken: uuidToPG(token), Action: action,
	})
	slog.WarnContext(ctx, "seat capacity intent deferred to outbox",
		"operation_token", token.String(), "action", action, "error", capacityErr)
}

// writeSeatCapacityError 把容量错误统一映射成带 code 的 HTTP 响应：限流带
// Retry-After（429）；已满/超卖是冲突（409）；服务不可用为 503。
func writeSeatCapacityError(w http.ResponseWriter, err error) {
	switch {
	case seatcapacity.IsRateLimited(err):
		retryAfter := seatcapacity.RateLimitRetryAfter(err)
		if retryAfter < time.Second {
			retryAfter = time.Second
		}
		retryAfterSeconds := int64((retryAfter + time.Second - 1) / time.Second)
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfterSeconds))
		writeErrorCode(w, http.StatusTooManyRequests, "seat_capacity_rate_limited", "Member capacity is busy. Please retry shortly.")
	case errors.Is(err, errSeatCapacityOvercommitted):
		writeErrorCode(w, http.StatusConflict, "seat_capacity_overcommitted", "Workspace members exceed purchased seats. Add enough seats or remove members before adding another member.")
	case errors.Is(err, errSeatCapacityFull):
		writeErrorCode(w, http.StatusConflict, "seat_capacity_full", "No purchased member seats are available. Add seats in Billing before adding another member.")
	default:
		writeErrorCode(w, http.StatusServiceUnavailable, "seat_capacity_unavailable", "Member capacity could not be verified. Please try again.")
	}
}

// uuidToPG 把 google/uuid 转成 pgtype.UUID（Nil 表示无效/NULL）。
func uuidToPG(value uuid.UUID) pgtype.UUID {
	return pgtype.UUID{Bytes: value, Valid: value != uuid.Nil}
}
