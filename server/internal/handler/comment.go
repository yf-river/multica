package handler

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/eventoutbox"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/logger"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type CommentResponse struct {
	ID             string               `json:"id"`
	IssueID        string               `json:"issue_id"`
	AuthorType     string               `json:"author_type"`
	AuthorID       string               `json:"author_id"`
	Content        string               `json:"content"`
	Type           string               `json:"type"`
	ParentID       *string              `json:"parent_id"`
	CreatedAt      string               `json:"created_at"`
	UpdatedAt      string               `json:"updated_at"`
	ResolvedAt     *string              `json:"resolved_at"`
	ResolvedByType *string              `json:"resolved_by_type"`
	ResolvedByID   *string              `json:"resolved_by_id"`
	SourceTaskID   *string              `json:"source_task_id,omitempty"`
	Reactions      []ReactionResponse   `json:"reactions"`
	Attachments    []AttachmentResponse `json:"attachments"`
	// Orientation stats exist only for roots_only responses.
	ReplyCount     *int    `json:"reply_count,omitempty"`
	LastActivityAt *string `json:"last_activity_at,omitempty"`
	// ContentTruncated exists only for summary responses.
	ContentTruncated *bool `json:"content_truncated,omitempty"`
}

func commentToResponse(c db.Comment, reactions []ReactionResponse, attachments []AttachmentResponse) CommentResponse {
	if reactions == nil {
		reactions = []ReactionResponse{}
	}
	if attachments == nil {
		attachments = []AttachmentResponse{}
	}
	return CommentResponse{
		ID:             uuidToString(c.ID),
		IssueID:        uuidToString(c.IssueID),
		AuthorType:     c.AuthorType,
		AuthorID:       uuidToString(c.AuthorID),
		Content:        c.Content,
		Type:           c.Type,
		ParentID:       uuidToPtr(c.ParentID),
		CreatedAt:      timestampToString(c.CreatedAt),
		UpdatedAt:      timestampToString(c.UpdatedAt),
		ResolvedAt:     timestampToPtr(c.ResolvedAt),
		ResolvedByType: textToPtr(c.ResolvedByType),
		ResolvedByID:   uuidToPtr(c.ResolvedByID),
		SourceTaskID:   uuidToPtr(c.SourceTaskID),
		Reactions:      reactions,
		Attachments:    attachments,
	}
}

// Summary limits are measured in runes so clipping cannot split UTF-8 text.
const summaryContentRunes = 200

func summarizeContent(content string) (string, bool) {
	count := 0
	for byteOffset := range content {
		if count == summaryContentRunes {
			return content[:byteOffset] + "…", true
		}
		count++
	}
	return content, false
}

// commentHardCap bounds unpaginated issue reads without acting as a UI page.
const commentHardCap = 2000

func (h *Handler) CreateComment(w http.ResponseWriter, r *http.Request) {
	issueID := chi.URLParam(r, "id")
	issue, ok := h.loadIssueForUser(w, r, issueID)
	if !ok {
		return
	}

	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	var req createCommentRequest
	if !decodeRequiredJSON(w, r, &req) {
		return
	}

	if req.Content == "" {
		writeError(w, http.StatusBadRequest, "content is required")
		return
	}
	if req.Type == "" {
		req.Type = "comment"
	}

	// Resolve the operation owner and completed replay before mutable parent or
	// task validation. Access to the Issue is still checked above on every call.
	authorType, authorID := resolveActor(r, userID)
	requestHash, err := hashRequestFingerprint(struct {
		IssueID string               `json:"issue_id"`
		Request createCommentRequest `json:"request"`
	}{IssueID: issueID, Request: req})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to fingerprint comment request")
		return
	}
	idempotencyKey, ok := requireIdempotencyKey(w, r)
	if !ok {
		return
	}
	writeReplayError := resourceCreateReplayErrorWriter(
		"Idempotency-Key was already used with a different comment request",
		"failed to recover comment request",
	)
	loadReplay := func() (CommentResponse, bool, error) {
		return loadResourceCreateReplay(
			r.Context(), h.Queries, issue.WorkspaceID, parseUUID(authorID), resourceTypeComment,
			idempotencyKey, requestHash,
			func(response CommentResponse) bool { return response.ID != "" },
		)
	}
	if handleResourceCreateReplay(w, http.StatusCreated, loadReplay, writeReplayError) {
		return
	}

	var parentID pgtype.UUID
	var parentComment *db.Comment
	if req.ParentID != nil {
		var parsed pgtype.UUID
		parsed, ok = parseUUIDOrBadRequest(w, *req.ParentID, "parent_id")
		if !ok {
			return
		}
		parentID = parsed
		parent, err := h.Queries.GetComment(r.Context(), parentID)
		if err != nil || uuidToString(parent.IssueID) != uuidToString(issue.ID) {
			writeError(w, http.StatusBadRequest, "invalid parent comment")
			return
		}
		parentComment = &parent
	}

	attachmentIDs, ok := parseUUIDSliceOrBadRequest(w, req.AttachmentIDs, "attachment_ids")
	if !ok {
		return
	}
	suppressAgentIDs, ok := parseUUIDSliceOrBadRequest(w, req.SuppressAgentIDs, "suppress_agent_ids")
	if !ok {
		return
	}

	// Defense against resumed-session drift: when an agent posts from inside a
	// comment-triggered task AND the comment is being posted on that same
	// issue, the parent_id must exactly match the task's trigger comment.
	// Resumed Claude sessions otherwise carry forward a previous turn's
	// --parent UUID and silently misplace the reply.
	//
	// The task.IssueID scope is important: the CLI stamps X-Task-ID on every
	// request, so an agent legitimately commenting on a different issue must
	// not be blocked by its current task's trigger. Assignment-triggered
	// tasks (no TriggerCommentID) are also unaffected.
	var sourceTaskID pgtype.UUID
	if authorType == "agent" {
		if taskIDHeader := r.Header.Get("X-Task-ID"); taskIDHeader != "" {
			taskUUID, parseErr := util.ParseUUID(taskIDHeader)
			if parseErr == nil {
				sourceTaskID = taskUUID
				task, err := h.Queries.GetAgentTask(r.Context(), taskUUID)
				if err == nil && task.IssueID.Valid && uuidToString(task.IssueID) == uuidToString(issue.ID) {
					if task.TriggerCommentID.Valid {
						if parentID.Valid && uuidToString(parentID) != uuidToString(task.TriggerCommentID) {
							writeError(w, http.StatusConflict,
								"parent_id must equal this task's trigger comment id ("+uuidToString(task.TriggerCommentID)+")")
							return
						}
					}
					noAction, checkErr := service.HasSquadLeaderNoActionEvaluationForTask(r.Context(), h.Queries, task)
					if checkErr != nil {
						slog.Warn("checking squad leader no_action evaluation failed", append(logger.RequestAttrs(r),
							"error", checkErr,
							"task_id", taskIDHeader,
							"issue_id", issueID,
						)...)
					} else if noAction && isNoActionOnlyComment(req.Content) {
						writeError(w, http.StatusConflict, "squad leader recorded no_action; comments are not allowed for this task")
						return
					}
				}
			}
		}
	}

	// Store Markdown source unchanged; rendering and editor boundaries sanitize
	// HTML so request-time sanitization cannot corrupt Markdown syntax.

	// parent_id stores the exact comment being replied to. Thread-level behavior
	// (for example auto-unresolving a resolved thread) resolves the root
	// separately so storing a reply-to-reply does not destroy the direct-parent
	// signal used by trigger decisions.
	var rootComment *db.Comment
	if parentID.Valid {
		if root, err := h.Queries.GetThreadRoot(r.Context(), db.GetThreadRootParams{
			CommentID:   parentID,
			WorkspaceID: issue.WorkspaceID,
		}); err == nil {
			rootComment = &root
		}
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		slog.Warn("begin create comment transaction failed", append(logger.RequestAttrs(r), "error", err, "issue_id", issueID)...)
		writeError(w, http.StatusInternalServerError, "failed to create comment")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	qtx := h.Queries.WithTx(tx)
	reservationErr := reserveResourceCreateRequest(r.Context(), qtx, issue.WorkspaceID, parseUUID(authorID), resourceTypeComment, idempotencyKey, requestHash)
	if reservationErr != nil && !errors.Is(reservationErr, pgx.ErrNoRows) {
		slog.Warn("reserve comment request failed", append(logger.RequestAttrs(r), "error", reservationErr, "issue_id", issueID)...)
	}
	if !handleResourceCreateReservation(
		w, r.Context(), tx, reservationErr, loadReplay, writeReplayError,
		"failed to create comment", http.StatusCreated,
	) {
		return
	}

	comment, err := qtx.CreateComment(r.Context(), db.CreateCommentParams{
		IssueID:      issue.ID,
		WorkspaceID:  issue.WorkspaceID,
		AuthorType:   authorType,
		AuthorID:     parseUUID(authorID),
		Content:      req.Content,
		Type:         req.Type,
		ParentID:     parentID,
		SourceTaskID: sourceTaskID,
	})
	if err != nil {
		slog.Warn("create comment failed", append(logger.RequestAttrs(r), "error", err, "issue_id", issueID)...)
		writeError(w, http.StatusInternalServerError, "failed to create comment: "+err.Error())
		return
	}

	attachments, err := linkAttachmentsToNewComment(r.Context(), qtx, comment, attachmentIDs)
	if errors.Is(err, errCommentAttachmentsUnavailable) {
		writeError(w, http.StatusBadRequest, "one or more attachments are unavailable for this comment")
		return
	}
	if err != nil {
		slog.Warn("link comment attachments failed", append(logger.RequestAttrs(r), "error", err, "issue_id", issueID)...)
		writeError(w, http.StatusInternalServerError, "failed to create comment")
		return
	}
	resp := commentToResponse(comment, nil, h.attachmentsToResponses(attachments))
	var unresolvedEvent events.Event
	var threadReopened bool
	unresolvedEvent, threadReopened, err = service.UnresolveThreadOnReply(
		r.Context(),
		qtx,
		rootComment,
		uuidToString(issue.WorkspaceID),
		authorType,
		authorID,
	)
	if err != nil {
		slog.Warn("reopen resolved comment thread failed", append(logger.RequestAttrs(r), "error", err, "issue_id", issueID)...)
		writeError(w, http.StatusInternalServerError, "failed to create comment")
		return
	}
	createdEvent := buildCommentCreatedEvent(issue, resp, authorType, authorID)
	createdEvent, err = eventoutbox.Enqueue(r.Context(), qtx, createdEvent)
	if err != nil {
		slog.Warn("enqueue comment-created event failed", append(logger.RequestAttrs(r), "error", err, "issue_id", issueID)...)
		writeError(w, http.StatusInternalServerError, "failed to create comment")
		return
	}
	taskProjection, err := h.createCommentTaskProjectionInTx(
		r.Context(),
		qtx,
		issue,
		comment,
		parentComment,
		authorType,
		authorID,
		suppressAgentIDs,
		pgtype.UUID{},
	)
	if err != nil {
		slog.Warn("create comment task projection failed", append(logger.RequestAttrs(r), "error", err, "issue_id", issueID)...)
		writeError(w, http.StatusInternalServerError, "failed to create comment")
		return
	}
	if err := completeResourceCreateRequest(
		r.Context(), qtx, issue.WorkspaceID, parseUUID(authorID), resourceTypeComment,
		idempotencyKey, requestHash, parseUUID(resp.ID), resp,
	); err != nil {
		slog.Warn("complete comment request failed", append(logger.RequestAttrs(r), "error", err, "issue_id", issueID)...)
		writeError(w, http.StatusInternalServerError, "failed to create comment")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		slog.Warn("commit create comment transaction failed", append(logger.RequestAttrs(r), "error", err, "issue_id", issueID)...)
		writeError(w, http.StatusInternalServerError, "failed to create comment")
		return
	}

	slog.Info("comment created", append(logger.RequestAttrs(r), "comment_id", uuidToString(comment.ID), "issue_id", issueID)...)
	h.publishEvent(createdEvent)
	if threadReopened {
		h.publishEvent(unresolvedEvent)
	}

	h.publishCommentTaskProjection(r.Context(), taskProjection)

	writeJSON(w, http.StatusCreated, resp)
}

// noteCommentPrefix marks a comment as a human-only note. A comment whose first
// whitespace-delimited token is this prefix (case-insensitive) is stored like
// any other comment but never triggers an agent.
const noteCommentPrefix = "/note"

// isNoteComment reports whether content opts out of agent triggering via the
// reserved /note prefix. The prefix must be the comment's first token, so
// "/note check expiry", "  /NOTE", and "/note" all match, while "/notes",
// "/ note", and "see foo/note" do not.
func (h *Handler) UpdateComment(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	existing, _, wsUUID, ok := h.loadCommentForRequest(w, r)
	if !ok {
		return
	}
	commentID := uuidToString(existing.ID)

	member, ok := requireWorkspaceMemberContext(w, r)
	if !ok {
		return
	}

	actorType, actorID := resolveActor(r, userID)
	isAuthor := existing.AuthorType == actorType && uuidToString(existing.AuthorID) == actorID
	isAdmin := roleAllowed(member.Role, "owner", "admin")
	if !isAuthor && !isAdmin {
		writeError(w, http.StatusForbidden, "only comment author or admin can edit")
		return
	}

	var req struct {
		Content          string    `json:"content"`
		AttachmentIDs    *[]string `json:"attachment_ids"`
		SuppressAgentIDs []string  `json:"suppress_agent_ids"`
	}
	if !decodeRequiredJSON(w, r, &req) {
		return
	}
	if req.Content == "" {
		writeError(w, http.StatusBadRequest, "content is required")
		return
	}
	if req.AttachmentIDs == nil {
		writeError(w, http.StatusBadRequest, "attachment_ids is required")
		return
	}

	attachmentIDs, ok := parseUUIDSliceOrBadRequest(w, *req.AttachmentIDs, "attachment_ids")
	if !ok {
		return
	}
	suppressAgentIDs, ok := parseUUIDSliceOrBadRequest(w, req.SuppressAgentIDs, "suppress_agent_ids")
	if !ok {
		return
	}

	// NOTE: See CreateComment — Markdown is sanitized at render/edit time, not here.

	contentChanged := existing.Content != req.Content
	groupedReactions, err := loadCommentReactions(r.Context(), h.Queries, []pgtype.UUID{existing.ID})
	if err != nil {
		slog.Warn("load reactions for comment update failed", append(logger.RequestAttrs(r), "error", err, "comment_id", commentID)...)
		writeError(w, http.StatusInternalServerError, "failed to update comment")
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		slog.Warn("begin update comment transaction failed", append(logger.RequestAttrs(r), "error", err, "comment_id", commentID)...)
		writeError(w, http.StatusInternalServerError, "failed to update comment")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	qtx := h.Queries.WithTx(tx)

	comment, err := qtx.UpdateComment(r.Context(), db.UpdateCommentParams{
		ID:      existing.ID,
		Content: req.Content,
	})
	if err != nil {
		slog.Warn("update comment failed", append(logger.RequestAttrs(r), "error", err, "comment_id", commentID)...)
		writeError(w, http.StatusInternalServerError, "failed to update comment")
		return
	}

	attachments, err := replaceCommentAttachmentSet(r.Context(), qtx, comment, attachmentIDs)
	if errors.Is(err, errCommentAttachmentsUnavailable) {
		writeError(w, http.StatusBadRequest, "one or more attachments are unavailable for this comment")
		return
	}
	if err != nil {
		slog.Warn("update comment attachments failed", append(logger.RequestAttrs(r), "error", err, "comment_id", commentID)...)
		writeError(w, http.StatusInternalServerError, "failed to update comment")
		return
	}

	issue, err := qtx.GetIssueInWorkspace(r.Context(), db.GetIssueInWorkspaceParams{
		ID:          existing.IssueID,
		WorkspaceID: wsUUID,
	})
	if err != nil {
		slog.Warn("load issue for comment update failed", append(logger.RequestAttrs(r), "error", err, "issue_id", uuidToString(existing.IssueID))...)
		writeError(w, http.StatusInternalServerError, "failed to update comment")
		return
	}

	var cancelledTasks []db.AgentTaskQueue
	var cancelledEvents []events.Event
	taskProjection := commentTaskProjection{}
	if contentChanged {
		cancelledTasks, cancelledEvents, err = h.TaskService.CancelTasksByTriggerCommentInTx(r.Context(), qtx, existing.ID)
		if err != nil {
			slog.Warn("cancel tasks for edited comment failed", append(logger.RequestAttrs(r), "comment_id", commentID, "error", err)...)
			writeError(w, http.StatusInternalServerError, "failed to update comment")
			return
		}

		var parentComment *db.Comment
		if existing.ParentID.Valid {
			parent, err := qtx.GetCommentInWorkspace(r.Context(), db.GetCommentInWorkspaceParams{
				ID:          existing.ParentID,
				WorkspaceID: wsUUID,
			})
			if err != nil {
				slog.Warn("load parent comment for edit failed", append(logger.RequestAttrs(r), "comment_id", commentID, "error", err)...)
				writeError(w, http.StatusInternalServerError, "failed to update comment")
				return
			}
			parentComment = &parent
		}
		taskProjection, err = h.createCommentTaskProjectionInTx(r.Context(), qtx, issue, comment, parentComment, actorType, actorID, suppressAgentIDs, existing.ID)
		if err != nil {
			slog.Warn("create edited comment task projection failed", append(logger.RequestAttrs(r), "comment_id", commentID, "error", err)...)
			writeError(w, http.StatusInternalServerError, "failed to update comment")
			return
		}
	}

	cid := uuidToString(comment.ID)
	resp := commentToResponse(comment, groupedReactions[cid], h.attachmentsToResponses(attachments))
	updatedEvent := buildCommentUpdatedEvent(issue, resp, actorType, actorID)
	updatedEvent, err = eventoutbox.Enqueue(r.Context(), qtx, updatedEvent)
	if err != nil {
		slog.Warn("enqueue comment-updated event failed", append(logger.RequestAttrs(r), "comment_id", commentID, "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to update comment")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		slog.Warn("commit update comment transaction failed", append(logger.RequestAttrs(r), "error", err, "comment_id", commentID)...)
		writeError(w, http.StatusInternalServerError, "failed to update comment")
		return
	}

	slog.Info("comment updated", append(logger.RequestAttrs(r), "comment_id", commentID)...)
	h.publishEvent(updatedEvent)
	h.TaskService.PublishCancelledTasks(r.Context(), cancelledTasks, cancelledEvents)
	h.publishCommentTaskProjection(r.Context(), taskProjection)

	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) DeleteComment(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	comment, _, _, ok := h.loadCommentForRequest(w, r)
	if !ok {
		return
	}
	commentID := uuidToString(comment.ID)

	member, ok := requireWorkspaceMemberContext(w, r)
	if !ok {
		return
	}

	actorType, actorID := resolveActor(r, userID)
	isAuthor := comment.AuthorType == actorType && uuidToString(comment.AuthorID) == actorID
	isAdmin := roleAllowed(member.Role, "owner", "admin")
	if !isAuthor && !isAdmin {
		writeError(w, http.StatusForbidden, "only comment author or admin can delete")
		return
	}

	// Collect attachment URLs before CASCADE delete removes them.
	attachmentURLs, err := h.Queries.ListAttachmentURLsByCommentID(r.Context(), comment.ID)
	if err != nil {
		slog.Warn("list deleted comment attachments failed", append(logger.RequestAttrs(r), "error", err, "comment_id", commentID)...)
		writeError(w, http.StatusInternalServerError, "failed to delete comment")
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		slog.Warn("begin delete comment transaction failed", append(logger.RequestAttrs(r), "error", err, "comment_id", commentID)...)
		writeError(w, http.StatusInternalServerError, "failed to delete comment")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	qtx := h.Queries.WithTx(tx)

	cancelledTasks, cancelledEvents, err := h.TaskService.CancelTasksByTriggerCommentInTx(r.Context(), qtx, comment.ID)
	if err != nil {
		slog.Warn("cancel tasks for deleted trigger comment failed", append(logger.RequestAttrs(r), "error", err, "comment_id", commentID)...)
		writeError(w, http.StatusInternalServerError, "failed to delete comment")
		return
	}

	if err := qtx.DeleteComment(r.Context(), db.DeleteCommentParams{
		ID:          comment.ID,
		WorkspaceID: comment.WorkspaceID,
	}); err != nil {
		slog.Warn("delete comment failed", append(logger.RequestAttrs(r), "error", err, "comment_id", commentID)...)
		writeError(w, http.StatusInternalServerError, "failed to delete comment")
		return
	}
	deletedEvent := buildCommentDeletedEvent(comment, actorType, actorID)
	deletedEvent, err = eventoutbox.Enqueue(r.Context(), qtx, deletedEvent)
	if err != nil {
		slog.Warn("enqueue comment-deleted event failed", append(logger.RequestAttrs(r), "error", err, "comment_id", commentID)...)
		writeError(w, http.StatusInternalServerError, "failed to delete comment")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		slog.Warn("commit delete comment transaction failed", append(logger.RequestAttrs(r), "error", err, "comment_id", commentID)...)
		writeError(w, http.StatusInternalServerError, "failed to delete comment")
		return
	}

	h.deleteStorageObjects(r.Context(), attachmentURLs)
	slog.Info("comment deleted", append(logger.RequestAttrs(r), "comment_id", commentID, "issue_id", uuidToString(comment.IssueID))...)
	h.TaskService.PublishCancelledTasks(r.Context(), cancelledTasks, cancelledEvents)
	h.publishEvent(deletedEvent)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) loadCommentForRequest(w http.ResponseWriter, r *http.Request) (db.Comment, string, pgtype.UUID, bool) {
	commentID := chi.URLParam(r, "commentId")
	commentUUID, ok := parseUUIDOrBadRequest(w, commentID, "comment id")
	if !ok {
		return db.Comment{}, "", pgtype.UUID{}, false
	}
	workspaceID := h.resolveWorkspaceID(r)
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return db.Comment{}, "", pgtype.UUID{}, false
	}
	comment, err := h.Queries.GetCommentInWorkspace(r.Context(), db.GetCommentInWorkspaceParams{
		ID:          commentUUID,
		WorkspaceID: workspaceUUID,
	})
	if err != nil {
		writeEntityLoadError(w, err, "comment", "comment_id", commentID)
		return db.Comment{}, "", pgtype.UUID{}, false
	}
	return comment, workspaceID, workspaceUUID, true
}

// loadCommentForActor resolves a {commentId} URL param to a comment in the
// caller's workspace. Returns the comment, the workspace UUID, the actor
// identity, and ok. Resolve / unresolve handlers share this scaffolding so the
// workspace membership + tenant guard stay identical. Any comment (root or
// reply) may be resolved: resolving a root collapses the whole thread; resolving
// a reply marks it as the thread's resolution. Which one is the thread's
// resolution is a pure frontend derivation, so the backend stays a plain setter.
func (h *Handler) loadCommentForActor(w http.ResponseWriter, r *http.Request) (db.Comment, string, string, string, bool) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return db.Comment{}, "", "", "", false
	}
	comment, workspaceID, _, ok := h.loadCommentForRequest(w, r)
	if !ok {
		return db.Comment{}, "", "", "", false
	}
	if _, ok := requireWorkspaceMemberContext(w, r); !ok {
		return db.Comment{}, "", "", "", false
	}
	actorType, actorID := resolveActor(r, userID)
	return comment, workspaceID, actorType, actorID, true
}

func (h *Handler) ResolveComment(w http.ResponseWriter, r *http.Request) {
	comment, workspaceID, actorType, actorID, ok := h.loadCommentForActor(w, r)
	if !ok {
		return
	}
	wasResolved := comment.ResolvedAt.Valid

	actorUUID, err := util.ParseUUID(actorID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid actor id")
		return
	}

	// Single-resolution invariant: a thread has at most one resolved comment, so
	// resolving this one must clear any other resolution in the same thread. Both
	// writes run in one tx — clearing the old resolution and setting the new one
	// is atomic, so a crash can never leave two resolutions (or none) visible.
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to resolve comment")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	qtx := h.Queries.WithTx(tx)

	cleared, err := qtx.ClearOtherThreadResolutions(r.Context(), db.ClearOtherThreadResolutionsParams{
		TargetID:    comment.ID,
		IssueID:     comment.IssueID,
		WorkspaceID: comment.WorkspaceID,
	})
	if err != nil {
		slog.Warn("clear other thread resolutions failed", append(logger.RequestAttrs(r), "error", err, "comment_id", uuidToString(comment.ID))...)
		writeError(w, http.StatusInternalServerError, "failed to resolve comment")
		return
	}

	updated, err := qtx.ResolveComment(r.Context(), db.ResolveCommentParams{
		ID:             comment.ID,
		ResolvedByType: pgtype.Text{String: actorType, Valid: true},
		ResolvedByID:   actorUUID,
	})
	if err != nil {
		slog.Warn("resolve comment failed", append(logger.RequestAttrs(r), "error", err, "comment_id", uuidToString(comment.ID))...)
		writeError(w, http.StatusInternalServerError, "failed to resolve comment")
		return
	}

	enrichmentIDs := make([]pgtype.UUID, 0, len(cleared)+1)
	enrichmentIDs = append(enrichmentIDs, updated.ID)
	for _, clearedComment := range cleared {
		enrichmentIDs = append(enrichmentIDs, clearedComment.ID)
	}
	enrichment, err := h.loadCommentEnrichment(r.Context(), qtx, comment.WorkspaceID, enrichmentIDs)
	if err != nil {
		slog.Warn("load enrichment for resolved comments failed", append(logger.RequestAttrs(r), "error", err, "comment_id", uuidToString(comment.ID))...)
		writeError(w, http.StatusInternalServerError, "failed to resolve comment")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		slog.Warn("resolve comment commit failed", append(logger.RequestAttrs(r), "error", err, "comment_id", uuidToString(comment.ID))...)
		writeError(w, http.StatusInternalServerError, "failed to resolve comment")
		return
	}

	// Emit a comment:unresolved per cleared sibling so granular realtime
	// consumers (which patch a single comment in place) drop the stale
	// resolution instead of showing two. Published after commit so no event ever
	// describes an uncommitted state.
	for _, c := range cleared {
		clearedID := uuidToString(c.ID)
		clearedResp := enrichment.response(c)
		slog.Info("comment unresolved (replaced)", append(logger.RequestAttrs(r), "comment_id", clearedID)...)
		h.publish(protocol.EventCommentUnresolved, workspaceID, actorType, actorID, map[string]any{"comment": clearedResp})
	}

	cid := uuidToString(updated.ID)
	resp := enrichment.response(updated)

	// Suppress the target event on a re-resolve no-op so consumers do not
	// re-process an unchanged thread (notifications, log spam). Cleared siblings
	// still get their own events above — those rows did change.
	if !wasResolved {
		slog.Info("comment resolved", append(logger.RequestAttrs(r), "comment_id", cid)...)
		h.publish(protocol.EventCommentResolved, workspaceID, actorType, actorID, map[string]any{"comment": resp})
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) UnresolveComment(w http.ResponseWriter, r *http.Request) {
	comment, workspaceID, actorType, actorID, ok := h.loadCommentForActor(w, r)
	if !ok {
		return
	}
	wasResolved := comment.ResolvedAt.Valid

	enrichment, err := h.loadCommentEnrichment(r.Context(), h.Queries, comment.WorkspaceID, []pgtype.UUID{comment.ID})
	if err != nil {
		slog.Warn("load enrichment for comment unresolve failed", append(logger.RequestAttrs(r), "error", err, "comment_id", uuidToString(comment.ID))...)
		writeError(w, http.StatusInternalServerError, "failed to unresolve comment")
		return
	}

	updated, err := h.Queries.UnresolveComment(r.Context(), comment.ID)
	if err != nil {
		slog.Warn("unresolve comment failed", append(logger.RequestAttrs(r), "error", err, "comment_id", uuidToString(comment.ID))...)
		writeError(w, http.StatusInternalServerError, "failed to unresolve comment")
		return
	}

	cid := uuidToString(updated.ID)
	resp := enrichment.response(updated)

	if wasResolved {
		slog.Info("comment unresolved", append(logger.RequestAttrs(r), "comment_id", cid)...)
		h.publish(protocol.EventCommentUnresolved, workspaceID, actorType, actorID, map[string]any{"comment": resp})
	}
	writeJSON(w, http.StatusOK, resp)
}
