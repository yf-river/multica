// 本文件实现评论「表情回应」（reaction）的增删端点。回应按 (comment, actor,
// emoji) 唯一，由 sqlc 的 AddReaction / RemoveReaction（返回是否真的发生变化）驱动，
// 并在同一事务里把 reaction:added / reaction:removed 事件入 outbox（携带评论 revision，
// 供实时客户端做乐观并发校正）。列表/详情场景经 groupReactions 按评论聚合。
package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/eventoutbox"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/logger"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// ReactionResponse 是一条表情回应的对外结构。CommentRevision 为写操作回显的评论
// 版本号（供客户端并发处理）；列表/详情路径不带它。
type ReactionResponse struct {
	ID              string `json:"id"`
	CommentID       string `json:"comment_id"`
	ActorType       string `json:"actor_type"`
	ActorID         string `json:"actor_id"`
	Emoji           string `json:"emoji"`
	CreatedAt       string `json:"created_at"`
	CommentRevision *int64 `json:"comment_revision,omitempty"`
}

// reactionToResponse 把回应行转成对外结构。
func reactionToResponse(r db.CommentReaction) ReactionResponse {
	return ReactionResponse{
		ID:        uuidToString(r.ID),
		CommentID: uuidToString(r.CommentID),
		ActorType: r.ActorType,
		ActorID:   uuidToString(r.ActorID),
		Emoji:     r.Emoji,
		CreatedAt: timestampToString(r.CreatedAt),
	}
}

// addedReactionToResponse 把「添加成功」的返回行转成对外结构，额外回显评论修订号
// （>0 时才有值）。
func addedReactionToResponse(r db.AddReactionRow) ReactionResponse {
	response := ReactionResponse{
		ID:        uuidToString(r.ID),
		CommentID: uuidToString(r.CommentID),
		ActorType: r.ActorType,
		ActorID:   uuidToString(r.ActorID),
		Emoji:     r.Emoji,
		CreatedAt: timestampToString(r.CreatedAt),
	}
	if r.CommentRevision > 0 {
		response.CommentRevision = &r.CommentRevision
	}
	return response
}

// AddReaction 给某条评论添加一个 emoji 回应（POST /api/comments/{commentId}/reactions）。
// 评论修订号变化时才会广播事件；重复添加由数据库唯一约束兜底。
func (h *Handler) AddReaction(w http.ResponseWriter, r *http.Request) {
	commentId := chi.URLParam(r, "commentId")

	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	workspaceID := h.resolveWorkspaceID(r)
	commentUUID, ok := parseUUIDOrBadRequest(w, commentId, "comment id")
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	comment, err := h.Queries.GetCommentInWorkspace(r.Context(), db.GetCommentInWorkspaceParams{
		ID:          commentUUID,
		WorkspaceID: wsUUID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "comment not found")
		return
	}

	var req struct {
		Emoji string `json:"emoji"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Emoji == "" {
		writeError(w, http.StatusBadRequest, "emoji is required")
		return
	}

	actorType, actorID := h.resolveActor(r, userID, workspaceID)

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		slog.Warn("begin comment reaction transaction failed", append(logger.RequestAttrs(r), "error", err, "comment_id", commentId)...)
		writeError(w, http.StatusInternalServerError, "failed to add reaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	reaction, err := qtx.AddReaction(r.Context(), db.AddReactionParams{
		CommentID:   comment.ID,
		WorkspaceID: wsUUID,
		ActorType:   actorType,
		ActorID:     parseUUID(actorID),
		Emoji:       req.Emoji,
	})
	if err != nil {
		slog.Warn("add reaction failed", append(logger.RequestAttrs(r), "error", err, "comment_id", commentId)...)
		writeError(w, http.StatusInternalServerError, "failed to add reaction")
		return
	}

	resp := addedReactionToResponse(reaction)

	// 在与变更同一事务里装载通知上下文（issue 标题/状态、评论作者等），供事件负载用。
	issueID := uuidToString(comment.IssueID)
	issue, err := qtx.GetIssueInWorkspace(r.Context(), db.GetIssueInWorkspaceParams{
		ID: comment.IssueID, WorkspaceID: wsUUID,
	})
	if err != nil {
		slog.Warn("load issue for comment reaction failed", append(logger.RequestAttrs(r), "error", err, "comment_id", commentId)...)
		writeError(w, http.StatusInternalServerError, "failed to add reaction")
		return
	}

	var persisted events.Event
	if reaction.CommentRevision > 0 {
		event := events.Event{
			Type:           protocol.EventReactionAdded,
			IdempotencyKey: "reaction:added:" + uuidToString(reaction.ID),
			StreamKey:      "issue:" + issueID,
			WorkspaceID:    workspaceID,
			ActorType:      actorType,
			ActorID:        actorID,
			Payload: map[string]any{
				"reaction":            resp,
				"issue_id":            issueID,
				"issue_title":         issue.Title,
				"issue_status":        issue.Status,
				"comment_id":          uuidToString(comment.ID),
				"comment_author_type": comment.AuthorType,
				"comment_author_id":   uuidToString(comment.AuthorID),
				"comment_revision":    reaction.CommentRevision,
			},
		}
		persisted, err = eventoutbox.Enqueue(r.Context(), qtx, event)
		if err != nil {
			slog.Warn("enqueue comment reaction event failed", append(logger.RequestAttrs(r), "error", err, "comment_id", commentId)...)
			writeError(w, http.StatusInternalServerError, "failed to add reaction")
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		slog.Warn("commit comment reaction failed", append(logger.RequestAttrs(r), "error", err, "comment_id", commentId)...)
		writeError(w, http.StatusInternalServerError, "failed to add reaction")
		return
	}
	if persisted.ID != "" {
		h.publishEvent(persisted)
	}
	writeJSON(w, http.StatusCreated, resp)
}

// RemoveReaction 移除本人对某评论的一个 emoji 回应。removed.Changed 为 true 表示确实
// 删掉了（幂等重试不会重复发事件）。
func (h *Handler) RemoveReaction(w http.ResponseWriter, r *http.Request) {
	commentId := chi.URLParam(r, "commentId")

	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	workspaceID := h.resolveWorkspaceID(r)
	commentUUID, ok := parseUUIDOrBadRequest(w, commentId, "comment id")
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	comment, err := h.Queries.GetCommentInWorkspace(r.Context(), db.GetCommentInWorkspaceParams{
		ID:          commentUUID,
		WorkspaceID: wsUUID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "comment not found")
		return
	}

	var req struct {
		Emoji string `json:"emoji"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Emoji == "" {
		writeError(w, http.StatusBadRequest, "emoji is required")
		return
	}

	actorType, actorID := h.resolveActor(r, userID, workspaceID)

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		slog.Warn("begin comment reaction removal transaction failed", append(logger.RequestAttrs(r), "error", err, "comment_id", commentId)...)
		writeError(w, http.StatusInternalServerError, "failed to remove reaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	removed, err := qtx.RemoveReaction(r.Context(), db.RemoveReactionParams{
		CommentID: comment.ID,
		ActorType: actorType,
		ActorID:   parseUUID(actorID),
		Emoji:     req.Emoji,
	})
	if err != nil {
		slog.Warn("remove reaction failed", append(logger.RequestAttrs(r), "error", err, "comment_id", commentId)...)
		writeError(w, http.StatusInternalServerError, "failed to remove reaction")
		return
	}

	var persisted events.Event
	if removed.Changed {
		event := events.Event{
			Type:           protocol.EventReactionRemoved,
			IdempotencyKey: "reaction:removed:" + uuidToString(comment.ID) + ":" + actorType + ":" + actorID + ":" + req.Emoji + ":" + strconv.FormatInt(removed.CommentRevision, 10),
			StreamKey:      "issue:" + uuidToString(comment.IssueID),
			WorkspaceID:    workspaceID,
			ActorType:      actorType,
			ActorID:        actorID,
			Payload: map[string]any{
				"comment_id":       uuidToString(comment.ID),
				"issue_id":         uuidToString(comment.IssueID),
				"emoji":            req.Emoji,
				"actor_type":       actorType,
				"actor_id":         actorID,
				"comment_revision": removed.CommentRevision,
			},
		}
		persisted, err = eventoutbox.Enqueue(r.Context(), qtx, event)
		if err != nil {
			slog.Warn("enqueue comment reaction removal event failed", append(logger.RequestAttrs(r), "error", err, "comment_id", commentId)...)
			writeError(w, http.StatusInternalServerError, "failed to remove reaction")
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		slog.Warn("commit comment reaction removal failed", append(logger.RequestAttrs(r), "error", err, "comment_id", commentId)...)
		writeError(w, http.StatusInternalServerError, "failed to remove reaction")
		return
	}
	if persisted.ID != "" {
		h.publishEvent(persisted)
	}
	w.WriteHeader(http.StatusNoContent)
}

// groupReactions 批量拉取给定评论的回应并按 comment_id 分组。用于列表/详情渲染；
// 查询失败返回 nil（回应当前评论为空）而不让整页失败。
func (h *Handler) groupReactions(r *http.Request, commentIDs []pgtype.UUID) map[string][]ReactionResponse {
	if len(commentIDs) == 0 {
		return nil
	}
	reactions, err := h.Queries.ListReactionsByCommentIDs(r.Context(), commentIDs)
	if err != nil {
		return nil
	}
	grouped := make(map[string][]ReactionResponse, len(commentIDs))
	for _, rx := range reactions {
		cid := uuidToString(rx.CommentID)
		grouped[cid] = append(grouped[cid], reactionToResponse(rx))
	}
	return grouped
}
