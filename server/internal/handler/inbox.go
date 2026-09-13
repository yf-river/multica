// 职责定位：inbox（通知收件箱）的 HTTP 处理器：列出未归档项、列出归档视图、单条已读/未读、
// 单条/批量归档与恢复、未读数计数、跨 workspace 未读汇总。收件人维度目前只服务「member」
// （某 workspace 的某个用户成员），按 workspace + user 过滤。
//
// 语义约定：归档是 issue 级的——归档一条会把同 issue 的同组兄弟通知一起归档，恢复同理；
// 「已读」不影响归档/未归档。所有变更走「先锁行（LockInboxItemForUpdate）→ 改状态 → 在
// 同一事务里落事件 → 提交后发布」的模式，事件承载供实时同步。
package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/logger"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// InboxItemResponse 是单条 inbox 项的接口形态。IssueStatus / IssuePriority 等 issue 投影
// 字段在列表与详情（富化）路径填充；Actor* 是谁触发了这条通知。
type InboxItemResponse struct {
	ID            string          `json:"id"`
	WorkspaceID   string          `json:"workspace_id"`
	RecipientType string          `json:"recipient_type"`
	RecipientID   string          `json:"recipient_id"`
	Type          string          `json:"type"`
	Severity      string          `json:"severity"`
	IssueID       *string         `json:"issue_id"`
	Title         string          `json:"title"`
	Body          *string         `json:"body"`
	Read          bool            `json:"read"`
	Archived      bool            `json:"archived"`
	CreatedAt     string          `json:"created_at"`
	IssueStatus   *string         `json:"issue_status"`
	IssuePriority *string         `json:"issue_priority"`
	ActorType     *string         `json:"actor_type"`
	ActorID       *string         `json:"actor_id"`
	Details       json.RawMessage `json:"details"`
}

// inboxToResponse 把数据库里的单条 inbox 记录转成接口形态（不含 issue 投影字段）。
func inboxToResponse(i db.InboxItem) InboxItemResponse {
	return InboxItemResponse{
		ID:            uuidToString(i.ID),
		WorkspaceID:   uuidToString(i.WorkspaceID),
		RecipientType: i.RecipientType,
		RecipientID:   uuidToString(i.RecipientID),
		Type:          i.Type,
		Severity:      i.Severity,
		IssueID:       uuidToPtr(i.IssueID),
		Title:         i.Title,
		Body:          textToPtr(i.Body),
		Read:          i.Read,
		Archived:      i.Archived,
		CreatedAt:     timestampToString(i.CreatedAt),
		ActorType:     textToPtr(i.ActorType),
		ActorID:       uuidToPtr(i.ActorID),
		Details:       json.RawMessage(i.Details),
	}
}

// inboxRowToResponse 把「列表查询行」转成接口形态（行里多带了 join 出的 issue 投影字段）。
func inboxRowToResponse(r db.ListInboxItemsRow) InboxItemResponse {
	return InboxItemResponse{
		ID:            uuidToString(r.ID),
		WorkspaceID:   uuidToString(r.WorkspaceID),
		RecipientType: r.RecipientType,
		RecipientID:   uuidToString(r.RecipientID),
		Type:          r.Type,
		Severity:      r.Severity,
		IssueID:       uuidToPtr(r.IssueID),
		Title:         r.Title,
		Body:          textToPtr(r.Body),
		Read:          r.Read,
		Archived:      r.Archived,
		CreatedAt:     timestampToString(r.CreatedAt),
		IssueStatus:   textToPtr(r.IssueStatus),
		IssuePriority: textToPtr(r.IssuePriority),
		ActorType:     textToPtr(r.ActorType),
		ActorID:       uuidToPtr(r.ActorID),
		Details:       json.RawMessage(r.Details),
	}
}

// archivedInboxRowToResponse 说明见上（与 ListInboxItemsRow 列一致），所以归档行直接
// 转成活跃行、复用同一个 mapper——两条查询的列清单一旦漂移，这段转换就编不过，这正是
// 想达到的效果。
func archivedInboxRowToResponse(r db.ListArchivedInboxItemsRow) InboxItemResponse {
	return inboxRowToResponse(db.ListInboxItemsRow(r))
}

// enrichInboxResponse 若该条通知关联了 issue，就补上 issue 当前的状态与优先级。读失败静默
// 跳过——这些只是展示性富化字段，issue 不存在（已删除）时保持为空即可。
func (h *Handler) enrichInboxResponse(ctx context.Context, resp InboxItemResponse, issueID pgtype.UUID) InboxItemResponse {
	if !issueID.Valid {
		return resp
	}
	issue, err := h.Queries.GetIssue(ctx, issueID)
	if err == nil {
		s := issue.Status
		resp.IssueStatus = &s
		p := issue.Priority
		resp.IssuePriority = &p
	}
	return resp
}

// ListInbox 列出当前用户（member）在活跃 workspace 下未归档的通知，即收件箱主视图。
func (h *Handler) ListInbox(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}

	items, err := h.Queries.ListInboxItems(r.Context(), db.ListInboxItemsParams{
		WorkspaceID:   wsUUID,
		RecipientType: "member",
		RecipientID:   parseUUID(userID),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list inbox")
		return
	}

	resp := make([]InboxItemResponse, len(items))
	for i, item := range items {
		resp[i] = inboxRowToResponse(item)
	}

	writeJSON(w, http.StatusOK, resp)
}

// ListArchivedInbox 返回收件人的已归档通知，支撑收件箱的「Archived」子视图。单独做成端点
// 而不是 ListInbox 上加个 flag：既保持已装客户端契约不变，也不让无上限的归档列表搭主列表
// 的车。
//
// 查询会剔除「还有活跃行」的 issue，保证这份列表与主收件箱按 issue 组互斥；最多取 200 组，
// 每组只返回最新一行与其可选的评论锚点——上限与分组契约见查询里的注释。
func (h *Handler) ListArchivedInbox(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}

	items, err := h.Queries.ListArchivedInboxItems(r.Context(), db.ListArchivedInboxItemsParams{
		WorkspaceID:   wsUUID,
		RecipientType: "member",
		RecipientID:   parseUUID(userID),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list archived inbox")
		return
	}

	resp := make([]InboxItemResponse, len(items))
	for i, item := range items {
		resp[i] = archivedInboxRowToResponse(item)
	}

	writeJSON(w, http.StatusOK, resp)
}

// MarkInboxRead 把单条通知标记为已读：事务里先锁行再改状态，记 EventInboxRead 事件并发布，
// 返回富化后的最新条目。
func (h *Handler) MarkInboxRead(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	prev, ok := h.loadInboxItemForUser(w, r, id)
	if !ok {
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	if _, err := qtx.LockInboxItemForUpdate(r.Context(), prev.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to lock inbox item")
		return
	}
	item, err := qtx.MarkInboxRead(r.Context(), prev.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to mark read")
		return
	}

	userID := requestUserID(r)
	event, err := recordInboxEventTx(r.Context(), qtx, buildInboxMutationEvent(item, protocol.EventInboxRead, "member", userID, "read", nil))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record inbox event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit inbox update")
		return
	}
	h.publishEvent(event)

	resp := h.enrichInboxResponse(r.Context(), inboxToResponse(item), item.IssueID)
	writeJSON(w, http.StatusOK, resp)
}

// MarkInboxUnread 把通知翻回未读，是 MarkInboxRead 的逆操作。它存在的意义：用户能把「打开过
// 但还没处理」的事先放一边——收件箱在条目被选中的瞬间就会自动标已读，否则「打开过」和
// 「处理完」会变成同一个信号。
//
// 作用范围是单条，与 MarkInboxRead 一致——见查询里的注释。
func (h *Handler) MarkInboxUnread(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	prev, ok := h.loadInboxItemForUser(w, r, id)
	if !ok {
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	if _, err := qtx.LockInboxItemForUpdate(r.Context(), prev.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to lock inbox item")
		return
	}
	item, err := qtx.MarkInboxUnread(r.Context(), prev.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to mark unread")
		return
	}

	userID := requestUserID(r)
	event, err := recordInboxEventTx(r.Context(), qtx, buildInboxMutationEvent(item, protocol.EventInboxUnread, "member", userID, "unread", nil))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record inbox event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit inbox update")
		return
	}
	h.publishEvent(event)

	resp := h.enrichInboxResponse(r.Context(), inboxToResponse(item), item.IssueID)
	writeJSON(w, http.StatusOK, resp)
}

// ArchiveInboxItem 归档一条通知，同时归档同 issue 的所有兄弟通知（issue 级归档），
// 记 EventInboxArchived 事件并发布。
func (h *Handler) ArchiveInboxItem(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	prev, ok := h.loadInboxItemForUser(w, r, id)
	if !ok {
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	if _, err := qtx.LockInboxItemForUpdate(r.Context(), prev.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to lock inbox item")
		return
	}
	item, err := qtx.ArchiveInboxItem(r.Context(), prev.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to archive")
		return
	}

	// 顺带归档同 issue 的兄弟通知（issue 级归档：同一件事的通知整组收走）。
	if item.IssueID.Valid {
		if _, err := qtx.ArchiveInboxByIssue(r.Context(), db.ArchiveInboxByIssueParams{
			WorkspaceID:   item.WorkspaceID,
			RecipientType: item.RecipientType,
			RecipientID:   item.RecipientID,
			IssueID:       item.IssueID,
		}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to archive related inbox items")
			return
		}
	}

	userID := requestUserID(r)
	event, err := recordInboxEventTx(r.Context(), qtx, buildInboxMutationEvent(item, protocol.EventInboxArchived, "member", userID, "archive", map[string]any{
		"issue_id": uuidToPtr(item.IssueID),
	}))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record inbox event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit inbox update")
		return
	}
	h.publishEvent(event)

	resp := h.enrichInboxResponse(r.Context(), inboxToResponse(item), item.IssueID)
	writeJSON(w, http.StatusOK, resp)
}

// UnarchiveInboxItem 把归档通知恢复到主收件箱，是 ArchiveInboxItem 的逆操作，且镜像它的
// issue 级范围：归档一条会把同 issue 的整组兄弟一起归档，所以恢复也把整组带回来。
//
// `read` 刻意不去动。一条「未读时归档」的通知恢复回来仍是未读，未读角标会重新亮起——角标
// 本来就只数未归档项，所以恢复一条是真的增加一条未读，不是 bug。
func (h *Handler) UnarchiveInboxItem(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	prev, ok := h.loadInboxItemForUser(w, r, id)
	if !ok {
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	if _, err := qtx.LockInboxItemForUpdate(r.Context(), prev.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to lock inbox item")
		return
	}
	item, err := qtx.UnarchiveInboxItem(r.Context(), prev.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to unarchive")
		return
	}

	// 顺带恢复同 issue 的兄弟通知（issue 级恢复）。
	if item.IssueID.Valid {
		if _, err := qtx.UnarchiveInboxByIssue(r.Context(), db.UnarchiveInboxByIssueParams{
			WorkspaceID:   item.WorkspaceID,
			RecipientType: item.RecipientType,
			RecipientID:   item.RecipientID,
			IssueID:       item.IssueID,
		}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to restore related inbox items")
			return
		}
	}

	userID := requestUserID(r)
	event, err := recordInboxEventTx(r.Context(), qtx, buildInboxMutationEvent(item, protocol.EventInboxUnarchived, "member", userID, "unarchive", map[string]any{
		"issue_id": uuidToPtr(item.IssueID),
	}))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record inbox event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit inbox update")
		return
	}
	h.publishEvent(event)

	resp := h.enrichInboxResponse(r.Context(), inboxToResponse(item), item.IssueID)
	writeJSON(w, http.StatusOK, resp)
}

// CountUnreadInbox 返回当前用户在活跃 workspace 下的未读数（给未读角标用）。
func (h *Handler) CountUnreadInbox(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}

	count, err := h.Queries.CountUnreadInbox(r.Context(), db.CountUnreadInboxParams{
		WorkspaceID:   wsUUID,
		RecipientType: "member",
		RecipientID:   parseUUID(userID),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to count unread inbox")
		return
	}

	writeJSON(w, http.StatusOK, map[string]int64{"count": count})
}

// InboxWorkspaceUnreadResponse 是跨 workspace 汇总里「某一个 workspace」的未读数。
type InboxWorkspaceUnreadResponse struct {
	WorkspaceID string `json:"workspace_id"`
	Count       int64  `json:"count"`
}

// UnreadInboxSummary 返回当前用户所属「每个 workspace」的未读数。侧边栏用它给 workspace
// 切换器打点：当「非当前激活」的 workspace 有未读时不拉它的完整收件箱也能亮一个点。它天生
// 是账号级的：忽略当前激活的 workspace，只按已登录用户取数。
func (h *Handler) UnreadInboxSummary(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	rows, err := h.Queries.CountUnreadInboxByWorkspace(r.Context(), parseUUID(userID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to summarize unread inbox")
		return
	}

	resp := make([]InboxWorkspaceUnreadResponse, len(rows))
	for i, row := range rows {
		resp[i] = InboxWorkspaceUnreadResponse{
			WorkspaceID: uuidToString(row.WorkspaceID),
			Count:       row.Count,
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

// MarkAllInboxRead 把当前 workspace 下该用户的全部通知标已读。事务内更新，落一个带 count
// 的批量事件（EventInboxBatchRead）并发布，返回被标记的条数。
func (h *Handler) MarkAllInboxRead(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	count, err := qtx.MarkAllInboxRead(r.Context(), db.MarkAllInboxReadParams{
		WorkspaceID: wsUUID,
		RecipientID: parseUUID(userID),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to mark all inbox read")
		return
	}

	slog.Info("inbox: mark all read", append(logger.RequestAttrs(r), "user_id", userID, "count", count)...)
	event := buildInboxBatchEvent(workspaceID, userID, "member", userID, protocol.EventInboxBatchRead, "read_all", count)
	persistedEvent, err := recordInboxEventTx(r.Context(), qtx, event)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record inbox event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit inbox update")
		return
	}
	h.publishEvent(persistedEvent)

	writeJSON(w, http.StatusOK, map[string]any{"count": count})
}

// ArchiveAllInbox 把当前 workspace 下该用户的全部通知归档，返回归档条数并广播批量事件。
func (h *Handler) ArchiveAllInbox(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	count, err := qtx.ArchiveAllInbox(r.Context(), db.ArchiveAllInboxParams{
		WorkspaceID: wsUUID,
		RecipientID: parseUUID(userID),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to archive all inbox")
		return
	}

	slog.Info("inbox: archive all", append(logger.RequestAttrs(r), "user_id", userID, "count", count)...)
	event := buildInboxBatchEvent(workspaceID, userID, "member", userID, protocol.EventInboxBatchArchived, "archive_all", count)
	persistedEvent, err := recordInboxEventTx(r.Context(), qtx, event)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record inbox event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit inbox update")
		return
	}
	h.publishEvent(persistedEvent)

	writeJSON(w, http.StatusOK, map[string]any{"count": count})
}

// ArchiveAllReadInbox 归档该用户「已读」的全部通知（保留未读的继续提醒），返回条数并广播。
func (h *Handler) ArchiveAllReadInbox(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	count, err := qtx.ArchiveAllReadInbox(r.Context(), db.ArchiveAllReadInboxParams{
		WorkspaceID: wsUUID,
		RecipientID: parseUUID(userID),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to archive all read inbox")
		return
	}

	slog.Info("inbox: archive all read", append(logger.RequestAttrs(r), "user_id", userID, "count", count)...)
	event := buildInboxBatchEvent(workspaceID, userID, "member", userID, protocol.EventInboxBatchArchived, "archive_read", count)
	persistedEvent, err := recordInboxEventTx(r.Context(), qtx, event)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record inbox event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit inbox update")
		return
	}
	h.publishEvent(persistedEvent)

	writeJSON(w, http.StatusOK, map[string]any{"count": count})
}

// ArchiveCompletedInbox 归档「关联 issue 已进入终态（完成/取消等）」的通知。先解析出本
// workspace 的终态状态键集合，再在事务里按它批量归档，返回条数并广播。
func (h *Handler) ArchiveCompletedInbox(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}

	terminalStatusKeys, err := h.terminalIssueStatusKeys(r.Context(), wsUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to resolve status categories")
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	count, err := qtx.ArchiveCompletedInbox(r.Context(), db.ArchiveCompletedInboxParams{
		WorkspaceID:        wsUUID,
		RecipientID:        parseUUID(userID),
		TerminalStatusKeys: terminalStatusKeys,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to archive completed inbox")
		return
	}

	slog.Info("inbox: archive completed", append(logger.RequestAttrs(r), "user_id", userID, "count", count)...)
	event := buildInboxBatchEvent(workspaceID, userID, "member", userID, protocol.EventInboxBatchArchived, "archive_completed", count)
	persistedEvent, err := recordInboxEventTx(r.Context(), qtx, event)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record inbox event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit inbox update")
		return
	}
	h.publishEvent(persistedEvent)

	writeJSON(w, http.StatusOK, map[string]any{"count": count})
}
