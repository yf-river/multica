// 本文件实现 issue 订阅（subscriber）的 HTTP 端点：列出订阅者、显式订阅、取消
// 订阅，以及「整棵子树取消订阅」（含对后续新子议题的静默防打扰墓碑，MUL-5483）。
//
// 订阅行由 issue_subscriber 表承载，带 Reason 区分来源（manual 显式订阅、rule 规则
// 驱动、delegation 委派等）。显式订阅会清除之前任何「取消订阅」墓碑——用户是在覆盖
// 自己先前的退订决定，而规则驱动的订阅刻意做不到这点。
//
// 子树退订是一条独立路由而不是 /unsubscribe 上的旗标：前端随 merge 自动部署、后端
// 手工部署，新客户端常会对话老服务器。老服务器会把未知的 "subtree": true 静默解成
// 无、只退订根并回 200——用户以为整棵树静音了，实际每个现存与未来的子议题还在
// 通知。未知路由则直接 404，客户端大声失败、用户可重试，而不是信任一次静默空操作。
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// SubscriberResponse 是每个 issue 订阅者的对外 JSON 结构。
type SubscriberResponse struct {
	IssueID   string `json:"issue_id"`
	UserType  string `json:"user_type"`
	UserID    string `json:"user_id"`
	Reason    string `json:"reason"`
	CreatedAt string `json:"created_at"`
}

// subscriberToResponse 把订阅行转成对外结构。
func subscriberToResponse(s db.IssueSubscriber) SubscriberResponse {
	return SubscriberResponse{
		IssueID:   uuidToString(s.IssueID),
		UserType:  s.UserType,
		UserID:    uuidToString(s.UserID),
		Reason:    s.Reason,
		CreatedAt: timestampToString(s.CreatedAt),
	}
}

// ListIssueSubscribers 返回某 issue 的全部订阅者。
func (h *Handler) ListIssueSubscribers(w http.ResponseWriter, r *http.Request) {
	issueID := chi.URLParam(r, "id")
	issue, ok := h.loadIssueForUser(w, r, issueID)
	if !ok {
		return
	}

	subscribers, err := h.Queries.ListIssueSubscribers(r.Context(), issue.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list subscribers")
		return
	}

	resp := make([]SubscriberResponse, len(subscribers))
	for i, s := range subscribers {
		resp[i] = subscriberToResponse(s)
	}

	writeJSON(w, http.StatusOK, resp)
}

// SubscribeToIssue 以 reason=manual 把某用户订阅到一个 issue。请求体里若带了 user_id，
// 就订阅该用户；否则订阅调用者本人。
func (h *Handler) SubscribeToIssue(w http.ResponseWriter, r *http.Request) {
	issueID := chi.URLParam(r, "id")
	issue, ok := h.loadIssueForUser(w, r, issueID)
	if !ok {
		return
	}

	workspaceID := uuidToString(issue.WorkspaceID)
	// 默认目标：调用者，经 resolveActor 解析——这样 agent 调用者（设了 X-Agent-ID）
	// 订阅的是它自己，而不是其背后的成员。
	callerActorType, callerActorID := h.resolveActor(r, requestUserID(r), workspaceID)
	targetUserType := callerActorType
	targetUserID := callerActorID
	var req struct {
		UserID   *string `json:"user_id"`
		UserType *string `json:"user_type"`
	}
	if r.Body != nil {
		json.NewDecoder(r.Body).Decode(&req)
	}
	if req.UserID != nil && *req.UserID != "" {
		targetUserID = *req.UserID
	}
	if req.UserType != nil && *req.UserType != "" {
		targetUserType = *req.UserType
	}

	if !h.isWorkspaceEntity(r.Context(), targetUserType, targetUserID, workspaceID) {
		writeError(w, http.StatusForbidden, "target user is not a member of this workspace")
		return
	}

	// 这是显式动作，所以它会清掉早前的任何退订墓碑——用户是在覆盖自己先前的退订
	// 决定。规则驱动的订阅刻意做不到这点（见 AddIssueSubscriber）。
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start subscription transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	err = qtx.SubscribeToIssueExplicitly(r.Context(), db.SubscribeToIssueExplicitlyParams{
		IssueID:  issue.ID,
		UserType: targetUserType,
		UserID:   parseUUID(targetUserID),
		Reason:   "manual",
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to subscribe")
		return
	}

	event, err := service.RecordDurableEventTx(r.Context(), qtx, buildSubscriberEvent(
		protocol.EventSubscriberAdded, issueID, workspaceID, callerActorType, callerActorID,
		targetUserType, targetUserID, "manual",
	))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record subscription event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit subscription")
		return
	}
	h.publishEvent(event)

	writeJSON(w, http.StatusOK, map[string]bool{"subscribed": true})
}

// UnsubscribeFromIssue 把某用户的订阅从 issue 移除。请求体里若带 user_id，退订该用户；
// 否则退订调用者本人。
func (h *Handler) UnsubscribeFromIssue(w http.ResponseWriter, r *http.Request) {
	h.unsubscribeFromIssue(w, r, false)
}

// UnsubscribeFromIssueSubtree 退订本 issue 及其所有后代，并且——经由委托规则运行的
// 祖先退订检查——让本树未来新增的子议题不再重新订阅该用户（MUL-5483）。
//
// 它刻意是独立路由，而不是 /unsubscribe 上的旗标：前端随 merge 自动部署、后端手工
// 部署，新客户端常会对话老服务器。老服务器把未知的 "subtree": true 静默解成无、只
// 退订根并回 200——用户被告知整棵树已静音，可每个现存与未来的子议题还在通知。
// 未知路由则直接 404，客户端大声失败、用户可重试，而不是信任一次静默空操作。
func (h *Handler) UnsubscribeFromIssueSubtree(w http.ResponseWriter, r *http.Request) {
	h.unsubscribeFromIssue(w, r, true)
}

// unsubscribeFromIssue 是退订的公共实现；subtree=true 时走整树退订（带序列化边界）。
func (h *Handler) unsubscribeFromIssue(w http.ResponseWriter, r *http.Request, subtree bool) {
	issueID := chi.URLParam(r, "id")
	issue, ok := h.loadIssueForUser(w, r, issueID)
	if !ok {
		return
	}

	workspaceID := uuidToString(issue.WorkspaceID)
	// 默认目标：调用者，经 resolveActor 解析——agent 调用者（设了 X-Agent-ID）退订
	// 的是它自己，而不是其背后的成员。
	callerActorType, callerActorID := h.resolveActor(r, requestUserID(r), workspaceID)
	targetUserType := callerActorType
	targetUserID := callerActorID
	var req struct {
		UserID   *string `json:"user_id"`
		UserType *string `json:"user_type"`
	}
	if r.Body != nil {
		json.NewDecoder(r.Body).Decode(&req)
	}
	if req.UserID != nil && *req.UserID != "" {
		targetUserID = *req.UserID
	}
	if req.UserType != nil && *req.UserType != "" {
		targetUserType = *req.UserType
	}

	if !h.isWorkspaceEntity(r.Context(), targetUserType, targetUserID, workspaceID) {
		writeError(w, http.StatusForbidden, "target user is not a member of this workspace")
		return
	}

	// 先规范化，再让目标进入告警锁或广播。锁按 UUID 值作键，客户端按 user_id 字符串
	// 去重，所以大写的请求不能把两者的拼写与委托规则的规范形态弄出差异
	// （MUL-5483 第 8 轮评审）。
	targetUserID = uuidToString(parseUUID(targetUserID))

	// 本次调用真正离开的每个 issue。子树变体要上报完整集合，让每个 issue 各自收到
	// 广播：开着 CHILD issue 的客户端永远看不到根的事件，否则它会一直显示一个服务端
	// 早已撤销的订阅。
	var committedEvents []events.Event
	if subtree {
		// 子树退订只有同时覆盖 agent 片刻之后才归档的子议题才有意义，所以它绝不能与
		// 委托规则的资格检查交错执行。两者在各自事务期间持有同一个 (workspace, user)
		// 锁；没有它，墓碑写入与规则读取之间新建的子议题会作为活跃观察者回来
		// （MUL-5483 第 7 轮评审）。
		_, committed, err := h.unsubscribeSubtreeSerialized(r.Context(), workspaceID, issue.ID, targetUserType, targetUserID, callerActorType, callerActorID)
		if errors.Is(err, errTargetNoLongerMember) {
			writeError(w, http.StatusForbidden, "target user is not a member of this workspace")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to unsubscribe")
			return
		}
		committedEvents = committed
	} else {
		tx, err := h.TxStarter.Begin(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to start subscription transaction")
			return
		}
		defer tx.Rollback(r.Context())
		qtx := h.Queries.WithTx(tx)
		if err := qtx.RemoveIssueSubscriber(r.Context(), db.RemoveIssueSubscriberParams{
			IssueID: issue.ID, UserType: targetUserType, UserID: parseUUID(targetUserID),
		}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to unsubscribe")
			return
		}
		event, err := service.RecordDurableEventTx(r.Context(), qtx, buildSubscriberEvent(
			protocol.EventSubscriberRemoved, issueID, workspaceID, callerActorType, callerActorID,
			targetUserType, targetUserID, "manual",
		))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to record subscription event")
			return
		}
		if err := tx.Commit(r.Context()); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to commit subscription")
			return
		}
		committedEvents = []events.Event{event}
	}

	for _, event := range committedEvents {
		h.publishEvent(event)
	}

	writeJSON(w, http.StatusOK, map[string]bool{"subscribed": false})
}

// errTargetNoLongerMember 报告目标在 handler 预检与序列化写入之间离开了 workspace，
// 使调用方能按预检同样的方式回 403。
var errTargetNoLongerMember = errors.New("target user is no longer a workspace member")

// unsubscribeSubtreeSerialized 在委托自动订阅规则使用的同一个 (workspace, user) 序列化
// 边界内给整棵树写退订墓碑，并返回实际撤销的每个 issue。
func (h *Handler) unsubscribeSubtreeSerialized(
	ctx context.Context, workspaceID string, rootID pgtype.UUID, userType, userID, actorType, actorID string,
) ([]pgtype.UUID, []events.Event, error) {
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback(ctx)
	qtx := h.Queries.WithTx(tx)

	if err := qtx.LockSubscriberWrites(ctx, db.LockSubscriberWritesParams{
		WorkspaceID: parseUUID(workspaceID),
		UserID:      parseUUID(userID),
	}); err != nil {
		return nil, nil, err
	}

	// 调用方的成员资格预检跑在它自己的快照上、早于本事务存在——它描述的是过去。
	// 若其间提交了一次撤销，会把该用户的订阅清掉并删掉他的 member 行；在其后写墓碑，
	// 会留下一个比成员资格活得更久、在重新邀请时还会继承的退订标记。现在持锁重查
	// 一次成员资格——锁保证不会有撤销插进这中间（MUL-5483 第 8 轮评审）。
	//
	// 仅成员：agent 目标没有 member 行，而撤销的订阅清理是成员维度的，所以 agent
	// 不存在「墓碑比成员资格活得更久」的问题。
	if userType == "member" {
		if _, err := qtx.LockActiveMember(ctx, db.LockActiveMemberParams{
			UserID:      parseUUID(userID),
			WorkspaceID: parseUUID(workspaceID),
		}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, nil, errTargetNoLongerMember
			}
			return nil, nil, err
		}
	}

	ids, err := qtx.UnsubscribeFromIssueSubtree(ctx, db.UnsubscribeFromIssueSubtreeParams{
		ID:       rootID,
		UserType: userType,
		UserID:   parseUUID(userID),
	})
	if err != nil {
		return nil, nil, err
	}
	committedEvents := make([]events.Event, 0, len(ids))
	for _, id := range ids {
		event, err := service.RecordDurableEventTx(ctx, qtx, buildSubscriberEvent(
			protocol.EventSubscriberRemoved, uuidToString(id), workspaceID, actorType, actorID,
			userType, userID, "subtree",
		))
		if err != nil {
			return nil, nil, err
		}
		committedEvents = append(committedEvents, event)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, nil, err
	}
	return ids, committedEvents, nil
}

// buildSubscriberEvent 组装订阅增删的领域事件（幂等键以 issue+user 唯一）。
func buildSubscriberEvent(eventType, issueID, workspaceID, actorType, actorID, userType, userID, reason string) events.Event {
	return events.Event{
		Type:           eventType,
		IdempotencyKey: "subscriber:" + eventType + ":" + issueID + ":" + userType + ":" + userID,
		StreamKey:      "issue:" + issueID,
		WorkspaceID:    workspaceID,
		ActorType:      actorType,
		ActorID:        actorID,
		Payload: map[string]any{
			"issue_id": issueID, "user_type": userType, "user_id": userID, "reason": reason,
		},
	}
}
