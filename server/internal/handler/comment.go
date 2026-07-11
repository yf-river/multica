package handler

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
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
	// Orientation stats — populated only on the roots_only path and omitted in
	// every other mode, so the default response shape stays byte-identical for
	// existing callers. ReplyCount is the number of descendants in the thread;
	// LastActivityAt is the MAX(created_at) across the whole subtree. Together
	// they let an agent triage which thread to drill into without fetching any
	// replies.
	ReplyCount     *int    `json:"reply_count,omitempty"`
	LastActivityAt *string `json:"last_activity_at,omitempty"`
	// ContentTruncated is set only under summary=true: true when Content was
	// clipped to the summary budget, false when it fit. nil (omitted) means the
	// caller did not request a summary projection, so Content is verbatim.
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

// summaryContentRunes bounds comment content under summary=true. 200 runes is
// enough to tell what a comment is about (its opening) while cutting the bulk
// of a long body out of an agent's context budget. Counted in runes, not bytes,
// so multi-byte (e.g. CJK) content is clipped on a character boundary.
const summaryContentRunes = 200

// summarizeContent clips content to summaryContentRunes for the summary
// projection. Returns the (possibly clipped) content and whether it was
// truncated. An ellipsis marks a clip so the reader knows more text exists.
//
// It scans by rune and stops at the (budget+1)th rune rather than allocating a
// full []rune for the whole body — so a pathologically long comment costs only
// the budget, not its full length, under summary mode.
func summarizeContent(content string) (string, bool) {
	count := 0
	for byteOffset := range content { // range over a string yields rune start offsets
		if count == summaryContentRunes {
			return content[:byteOffset] + "…", true
		}
		count++
	}
	return content, false
}

// commentHardCap bounds the comments returned per issue. Sized as a defensive
// safety net rather than a UX paging window: prod p99 is ~30 comments and
// the all-time max observed is ~1.1k, so 2000 leaves ~2x headroom while still
// preventing a runaway response if some user manages to accumulate a wild
// number of rows on a single issue.
const commentHardCap = 2000

// ListComments returns comments for an issue. The default behaviour is
// unchanged — full chronological dump capped at commentHardCap — so existing
// callers and the desktop UI keep working as-is. Optional query params give
// agent-style readers bounded views that scale to long issues without dragging
// every prior reply into context:
//
//   - roots_only=true — return only top-level comments (parent_id IS NULL),
//     each annotated with reply_count + last_activity_at so the caller can
//     triage which thread to drill into. May combine with since for incremental
//     polling of newly created roots, but is exclusive with thread/recent/tail/
//     cursor modes because those have their own grouping or pagination semantics.
//
//   - summary=true — orthogonal content projection. Clips each returned
//     comment's content to a fixed budget and sets content_truncated, so an
//     agent can scan a list cheaply before pulling a full body. Composes with
//     every mode (default, since, thread, recent, roots_only).
//
//   - thread=<comment-uuid> — return the root of the thread containing this
//     comment plus every descendant. The anchor may be a root or any reply;
//     the server walks up to the root via a recursive CTE, so callers do not
//     need to know whether the id they have is a root.
//
//   - tail=<N> — only valid with thread. Cap the reply count at the N most
//     recent replies (per (created_at, id)). The thread root is always
//     returned, even when N=0, so the reader keeps the "what is this thread
//     about" context. Without tail, thread returns the entire thread (the
//     pre-MUL-2421 behavior).
//
//   - recent=<N> — return the N most recently active threads (root + every
//     descendant per thread). A thread's recency is MAX(created_at) across
//     the whole subtree, so a stale-but-recently-replied thread ranks ahead
//     of an active-but-quiet one. Row-based "newest N comments" is
//     deliberately NOT exposed — it surfaces unrelated thread tails and
//     hides relevant history (#2340).
//
//   - before=<RFC3339> + before-id=<uuid> — cursor. The pair's meaning is
//     context-dependent so the flag surface stays small:
//
//   - with recent: a *thread* cursor — (last_activity_at, root_id) — and
//     the next page returns threads strictly less recent.
//
//   - with thread + tail: a *reply* cursor — (created_at, id) — and the
//     next page returns replies in the same thread strictly older than
//     that reply.
//
// Both values must be set together so the cursor can tie-break entries
// landing in the same microsecond. The cursor for the next page is
// emitted via the X-Multica-Next-Before / X-Multica-Next-Before-Id
// response headers.
//
// Combination rules (kept narrow on purpose — Elon flagged the matrix risk):
//
//   - roots_only is exclusive with thread, recent, tail, and before/before-id.
//     It may combine with since. This keeps "list issue roots" separate from
//     "read a specific thread" and "read recently active threads".
//   - thread is exclusive with recent. Asking for "the most recent N within
//     thread X" mixes two different navigation models and is rejected.
//   - thread + before/before-id requires tail. Without tail, thread returns
//     the entire thread and a cursor would be ignored — reject loudly so
//     the documented "cursor scrolls within a tailed window" rule holds.
//   - tail requires thread (it is a thread-scoped limit; outside of thread
//     it has no defined behavior).
//   - thread may combine with since (incremental polling of one thread),
//     and the since filter is applied after the tail/cursor cut so the
//     thread root is still emitted but stale rows drop out.
//   - recent may combine with before/before-id (scroll older threads) and
//     with since (recent activity in a window).
//
// The response body is always chronological (oldest → newest); under recent
// that means threads are listed oldest-active first and the freshest thread
// sits at the tail, closest to "now" in an agent prompt.
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

	var req CreateCommentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Content == "" {
		writeError(w, http.StatusBadRequest, "content is required")
		return
	}
	if req.Type == "" {
		req.Type = "comment"
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

	// Determine author identity: agent (via X-Agent-ID header) or member.
	authorType, authorID := h.resolveActor(r, userID, uuidToString(issue.WorkspaceID))

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

	// NOTE: Comment content is stored as Markdown source. XSS is handled at the
	// rendering layer (rehype-sanitize) and at the editor layer
	// (@tiptap/markdown with html:false). Running an HTML sanitizer here would
	// entity-encode Markdown syntax characters (>, ", &, <) and corrupt the
	// source. See issue #1303 / discussion in MUL-1119, MUL-1125.

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
	commentId := chi.URLParam(r, "commentId")

	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	commentUUID, ok := parseUUIDOrBadRequest(w, commentId, "comment id")
	if !ok {
		return
	}

	// Load comment scoped to current workspace.
	workspaceID := h.resolveWorkspaceID(r)
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	existing, err := h.Queries.GetCommentInWorkspace(r.Context(), db.GetCommentInWorkspaceParams{
		ID:          commentUUID,
		WorkspaceID: wsUUID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "comment not found")
		return
	}

	member, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return
	}

	actorType, actorID := h.resolveActor(r, userID, workspaceID)
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Content == "" {
		writeError(w, http.StatusBadRequest, "content is required")
		return
	}

	var attachmentIDs []pgtype.UUID
	replaceAttachments := req.AttachmentIDs != nil
	if replaceAttachments {
		var ok bool
		attachmentIDs, ok = parseUUIDSliceOrBadRequest(w, *req.AttachmentIDs, "attachment_ids")
		if !ok {
			return
		}
	}
	suppressAgentIDs, ok := parseUUIDSliceOrBadRequest(w, req.SuppressAgentIDs, "suppress_agent_ids")
	if !ok {
		return
	}

	// NOTE: See CreateComment — Markdown is sanitized at render/edit time, not here.

	contentChanged := existing.Content != req.Content
	groupedReactions := h.groupReactions(r, []pgtype.UUID{existing.ID})

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		slog.Warn("begin update comment transaction failed", append(logger.RequestAttrs(r), "error", err, "comment_id", commentId)...)
		writeError(w, http.StatusInternalServerError, "failed to update comment")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	qtx := h.Queries.WithTx(tx)

	comment, err := qtx.UpdateComment(r.Context(), db.UpdateCommentParams{
		ID:      commentUUID,
		Content: req.Content,
	})
	if err != nil {
		slog.Warn("update comment failed", append(logger.RequestAttrs(r), "error", err, "comment_id", commentId)...)
		writeError(w, http.StatusInternalServerError, "failed to update comment")
		return
	}

	var attachments []db.Attachment
	if replaceAttachments {
		attachments, err = replaceCommentAttachmentSet(r.Context(), qtx, comment, attachmentIDs)
	} else {
		attachments, err = listCommentAttachments(r.Context(), qtx, comment)
	}
	if errors.Is(err, errCommentAttachmentsUnavailable) {
		writeError(w, http.StatusBadRequest, "one or more attachments are unavailable for this comment")
		return
	}
	if err != nil {
		slog.Warn("update comment attachments failed", append(logger.RequestAttrs(r), "error", err, "comment_id", commentId)...)
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
			slog.Warn("cancel tasks for edited comment failed", append(logger.RequestAttrs(r), "comment_id", commentId, "error", err)...)
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
				slog.Warn("load parent comment for edit failed", append(logger.RequestAttrs(r), "comment_id", commentId, "error", err)...)
				writeError(w, http.StatusInternalServerError, "failed to update comment")
				return
			}
			parentComment = &parent
		}
		taskProjection, err = h.createCommentTaskProjectionInTx(r.Context(), qtx, issue, comment, parentComment, actorType, actorID, suppressAgentIDs, existing.ID)
		if err != nil {
			slog.Warn("create edited comment task projection failed", append(logger.RequestAttrs(r), "comment_id", commentId, "error", err)...)
			writeError(w, http.StatusInternalServerError, "failed to update comment")
			return
		}
	}

	cid := uuidToString(comment.ID)
	resp := commentToResponse(comment, groupedReactions[cid], h.attachmentsToResponses(attachments))
	updatedEvent := buildCommentUpdatedEvent(issue, resp, actorType, actorID)
	updatedEvent, err = eventoutbox.Enqueue(r.Context(), qtx, updatedEvent)
	if err != nil {
		slog.Warn("enqueue comment-updated event failed", append(logger.RequestAttrs(r), "comment_id", commentId, "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to update comment")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		slog.Warn("commit update comment transaction failed", append(logger.RequestAttrs(r), "error", err, "comment_id", commentId)...)
		writeError(w, http.StatusInternalServerError, "failed to update comment")
		return
	}

	slog.Info("comment updated", append(logger.RequestAttrs(r), "comment_id", commentId)...)
	h.publishEvent(updatedEvent)
	h.TaskService.PublishCancelledTasks(r.Context(), cancelledTasks, cancelledEvents)
	h.publishCommentTaskProjection(r.Context(), taskProjection)

	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) DeleteComment(w http.ResponseWriter, r *http.Request) {
	commentId := chi.URLParam(r, "commentId")

	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	commentUUID, ok := parseUUIDOrBadRequest(w, commentId, "comment id")
	if !ok {
		return
	}

	// Load comment scoped to current workspace.
	workspaceID := h.resolveWorkspaceID(r)
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

	member, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return
	}

	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	isAuthor := comment.AuthorType == actorType && uuidToString(comment.AuthorID) == actorID
	isAdmin := roleAllowed(member.Role, "owner", "admin")
	if !isAuthor && !isAdmin {
		writeError(w, http.StatusForbidden, "only comment author or admin can delete")
		return
	}

	// Collect attachment URLs before CASCADE delete removes them.
	attachmentURLs, _ := h.Queries.ListAttachmentURLsByCommentID(r.Context(), comment.ID)

	// Cancel any active tasks triggered by this comment so the agent does not
	// run with the now-deleted content already embedded in its prompt. Must
	// run before DeleteComment because the FK ON DELETE SET NULL would
	// otherwise nullify trigger_comment_id and orphan those tasks in queued.
	if err := h.TaskService.CancelTasksByTriggerComment(r.Context(), comment.ID); err != nil {
		slog.Warn("cancel tasks for deleted trigger comment failed", append(logger.RequestAttrs(r), "error", err, "comment_id", commentId)...)
	}

	if err := h.Queries.DeleteComment(r.Context(), db.DeleteCommentParams{
		ID:          comment.ID,
		WorkspaceID: comment.WorkspaceID,
	}); err != nil {
		slog.Warn("delete comment failed", append(logger.RequestAttrs(r), "error", err, "comment_id", commentId)...)
		writeError(w, http.StatusInternalServerError, "failed to delete comment")
		return
	}

	h.deleteStorageObjects(r.Context(), attachmentURLs)
	slog.Info("comment deleted", append(logger.RequestAttrs(r), "comment_id", commentId, "issue_id", uuidToString(comment.IssueID))...)
	h.publish(protocol.EventCommentDeleted, workspaceID, actorType, actorID, map[string]any{
		"comment_id": uuidToString(comment.ID),
		"issue_id":   uuidToString(comment.IssueID),
	})
	w.WriteHeader(http.StatusNoContent)
}

// loadCommentForActor resolves a {commentId} URL param to a comment in the
// caller's workspace. Returns the comment, the workspace UUID, the actor
// identity, and ok. Resolve / unresolve handlers share this scaffolding so the
// workspace membership + tenant guard stay identical. Any comment (root or
// reply) may be resolved: resolving a root collapses the whole thread; resolving
// a reply marks it as the thread's resolution. Which one is the thread's
// resolution is a pure frontend derivation, so the backend stays a plain setter.
func (h *Handler) loadCommentForActor(w http.ResponseWriter, r *http.Request) (db.Comment, string, string, string, bool) {
	commentId := chi.URLParam(r, "commentId")
	userID, ok := requireUserID(w, r)
	if !ok {
		return db.Comment{}, "", "", "", false
	}
	commentUUID, ok := parseUUIDOrBadRequest(w, commentId, "comment id")
	if !ok {
		return db.Comment{}, "", "", "", false
	}
	workspaceID := h.resolveWorkspaceID(r)
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return db.Comment{}, "", "", "", false
	}
	if _, ok := h.workspaceMember(w, r, workspaceID); !ok {
		return db.Comment{}, "", "", "", false
	}
	comment, err := h.Queries.GetCommentInWorkspace(r.Context(), db.GetCommentInWorkspaceParams{
		ID:          commentUUID,
		WorkspaceID: wsUUID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "comment not found")
		return db.Comment{}, "", "", "", false
	}
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
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
		clearedReactions := h.groupReactions(r, []pgtype.UUID{c.ID})
		clearedAtt := h.groupAttachments(r, []pgtype.UUID{c.ID})
		clearedResp := commentToResponse(c, clearedReactions[clearedID], clearedAtt[clearedID])
		slog.Info("comment unresolved (replaced)", append(logger.RequestAttrs(r), "comment_id", clearedID)...)
		h.publish(protocol.EventCommentUnresolved, workspaceID, actorType, actorID, map[string]any{"comment": clearedResp})
	}

	grouped := h.groupReactions(r, []pgtype.UUID{updated.ID})
	groupedAtt := h.groupAttachments(r, []pgtype.UUID{updated.ID})
	cid := uuidToString(updated.ID)
	resp := commentToResponse(updated, grouped[cid], groupedAtt[cid])

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

	updated, err := h.Queries.UnresolveComment(r.Context(), comment.ID)
	if err != nil {
		slog.Warn("unresolve comment failed", append(logger.RequestAttrs(r), "error", err, "comment_id", uuidToString(comment.ID))...)
		writeError(w, http.StatusInternalServerError, "failed to unresolve comment")
		return
	}

	grouped := h.groupReactions(r, []pgtype.UUID{updated.ID})
	groupedAtt := h.groupAttachments(r, []pgtype.UUID{updated.ID})
	cid := uuidToString(updated.ID)
	resp := commentToResponse(updated, grouped[cid], groupedAtt[cid])

	if wasResolved {
		slog.Info("comment unresolved", append(logger.RequestAttrs(r), "comment_id", cid)...)
		h.publish(protocol.EventCommentUnresolved, workspaceID, actorType, actorID, map[string]any{"comment": resp})
	}
	writeJSON(w, http.StatusOK, resp)
}
