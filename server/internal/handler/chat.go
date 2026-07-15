package handler

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/analytics"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// chatSessionTitleMaxLen caps the rename input. Long enough to fit a
// meaningful summary, short enough to keep the dropdown row scannable.
const chatSessionTitleMaxLen = 200

// ---------------------------------------------------------------------------
// Chat Sessions
// ---------------------------------------------------------------------------

type createChatSessionRequest struct {
	AgentID string `json:"agent_id"`
	Title   string `json:"title"`
}

type chatRequestScope struct {
	userID      string
	workspaceID string
}

func requireChatRequestScope(w http.ResponseWriter, r *http.Request) (chatRequestScope, bool) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return chatRequestScope{}, false
	}
	workspaceID := ctxWorkspaceID(r.Context())
	return chatRequestScope{
		userID:      userID,
		workspaceID: workspaceID,
	}, true
}

func (h *Handler) CreateChatSession(w http.ResponseWriter, r *http.Request) {
	requestScope, ok := requireChatRequestScope(w, r)
	if !ok {
		return
	}
	idempotencyKey, ok := requireIdempotencyKey(w, r)
	if !ok {
		return
	}

	var req createChatSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.AgentID == "" {
		writeError(w, http.StatusBadRequest, "agent_id is required")
		return
	}
	if utf8.RuneCountInString(req.Title) > chatSessionTitleMaxLen {
		writeError(w, http.StatusBadRequest, "title must be at most 200 characters")
		return
	}
	agentID, ok := parseUUIDOrBadRequest(w, req.AgentID, "agent_id")
	if !ok {
		return
	}
	workspaceUUID, ok := parseUUIDOrBadRequest(w, requestScope.workspaceID, "workspace id")
	if !ok {
		return
	}
	actorType, actorID := resolveActor(r, requestScope.userID)
	idempotencyScope, err := newChatIdempotencyScope(
		workspaceUUID,
		actorType,
		parseUUID(actorID),
		chatCreateSessionOperation,
		idempotencyKey,
		struct {
			Version int    `json:"version"`
			AgentID string `json:"agent_id"`
			Title   string `json:"title"`
		}{Version: 1, AgentID: req.AgentID, Title: req.Title},
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to prepare chat session request")
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start transaction")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	qtx := h.Queries.WithTx(tx)
	record, created, err := reserveChatIdempotencyRecord(r.Context(), qtx, idempotencyScope)
	if err != nil {
		writeChatIdempotencyFailure(w, err)
		return
	}
	if !created {
		response, status, err := decodeChatIdempotencyResponse[chatSessionResponse](record)
		if err != nil {
			slog.Error("decode create-chat-session replay failed", "error", err)
			writeChatIdempotencyFailure(w, err)
			return
		}
		w.Header().Set("Idempotency-Replayed", "true")
		writeJSON(w, status, response)
		return
	}

	lockedAgent, err := qtx.LockAgentInWorkspaceForChat(r.Context(), db.LockAgentInWorkspaceForChatParams{
		ID:          agentID,
		WorkspaceID: workspaceUUID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "agent not found")
		return
	}
	if lockedAgent.ArchivedAt.Valid {
		writeError(w, http.StatusBadRequest, "agent is archived")
		return
	}
	if !h.requirePersonalAgentAccess(w, r, lockedAgent, actorType, actorID, requestScope.workspaceID, "you do not have access to this agent") {
		return
	}
	session, err := qtx.CreateChatSession(r.Context(), db.CreateChatSessionParams{
		WorkspaceID: workspaceUUID,
		AgentID:     agentID,
		CreatorID:   parseUUID(requestScope.userID),
		Title:       req.Title,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create chat session")
		return
	}
	response := chatSessionToResponse(session)
	if err := completeChatIdempotencyRecord(r.Context(), qtx, idempotencyScope, http.StatusCreated, response); err != nil {
		slog.Error("persist create-chat-session response failed", "error", err)
		writeChatIdempotencyFailure(w, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit chat session")
		return
	}
	writeJSON(w, http.StatusCreated, response)
}

func (h *Handler) ListChatSessions(w http.ResponseWriter, r *http.Request) {
	scope, ok := requireChatRequestScope(w, r)
	if !ok {
		return
	}

	// Compute the accessible-agents set once and use it to drop sessions
	// whose target agent the caller no longer has access to — without this,
	// a member whose role was downgraded would still see the session list
	// (and transcripts via ListChatMessages) for any personal agent they
	// previously had access to. Falls back to the user's role from the
	// workspace member context.
	allowed, ok := h.chatAccessibleAgentIDs(w, r, scope)
	if !ok {
		return
	}

	rows, err := h.Queries.ListChatSessionsByCreator(r.Context(), db.ListChatSessionsByCreatorParams{
		WorkspaceID: parseUUID(scope.workspaceID),
		CreatorID:   parseUUID(scope.userID),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list chat sessions")
		return
	}
	resp := make([]chatSessionResponse, 0, len(rows))
	for _, s := range rows {
		if _, ok := allowed[uuidToString(s.AgentID)]; !ok {
			continue
		}
		resp = append(resp, chatSessionResponse{
			ID:          uuidToString(s.ID),
			WorkspaceID: uuidToString(s.WorkspaceID),
			AgentID:     uuidToString(s.AgentID),
			CreatorID:   uuidToString(s.CreatorID),
			Title:       s.Title,
			HasUnread:   s.HasUnread,
			CreatedAt:   timestampToString(s.CreatedAt),
			UpdatedAt:   timestampToString(s.UpdatedAt),
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) chatAccessibleAgentIDs(w http.ResponseWriter, r *http.Request, scope chatRequestScope) (map[string]struct{}, bool) {
	member, ok := h.workspaceMember(w, r, scope.workspaceID)
	if !ok {
		return nil, false
	}
	return h.requestAccessibleAgentIDs(w, r, scope.workspaceID, scope.userID, member.Role)
}

func (h *Handler) loadChatSessionForUser(w http.ResponseWriter, r *http.Request, scope chatRequestScope, sessionID string) (db.ChatSession, bool) {
	sessionUUID, ok := parseUUIDOrBadRequest(w, sessionID, "chat session id")
	if !ok {
		return db.ChatSession{}, false
	}
	workspaceUUID, ok := parseUUIDOrBadRequest(w, scope.workspaceID, "workspace id")
	if !ok {
		return db.ChatSession{}, false
	}
	session, err := h.Queries.GetChatSessionInWorkspace(r.Context(), db.GetChatSessionInWorkspaceParams{
		ID:          sessionUUID,
		WorkspaceID: workspaceUUID,
	})
	if err != nil {
		writeEntityLoadError(w, r, err, "chat session", "session_id", sessionID)
		return db.ChatSession{}, false
	}
	if uuidToString(session.CreatorID) != scope.userID {
		writeError(w, http.StatusForbidden, "not your chat session")
		return db.ChatSession{}, false
	}
	return session, true
}

// gateChatSessionForUser combines the session ownership check with the
// personal-agent access gate so a member who has lost access to the target
// agent (role downgrade, ownership transfer, agent flipped to private)
// cannot continue reading the chat transcript even though they remain the
// session creator. Returns ok=false after writing the error response.
func (h *Handler) gateChatSessionForUser(w http.ResponseWriter, r *http.Request, scope chatRequestScope, sessionID string) (db.ChatSession, bool) {
	session, ok := h.loadChatSessionForUser(w, r, scope, sessionID)
	if !ok {
		return db.ChatSession{}, false
	}
	agent, err := h.Queries.GetAgent(r.Context(), session.AgentID)
	if err != nil {
		writeEntityLoadError(w, r, err, "agent", "agent_id", uuidToString(session.AgentID))
		return db.ChatSession{}, false
	}
	actorType, actorID := resolveActor(r, scope.userID)
	if !h.requirePersonalAgentAccess(w, r, agent, actorType, actorID, scope.workspaceID, "you do not have access to this agent") {
		return db.ChatSession{}, false
	}
	return session, true
}

func (h *Handler) requireCurrentChatSession(w http.ResponseWriter, r *http.Request) (chatRequestScope, db.ChatSession, bool) {
	scope, ok := requireChatRequestScope(w, r)
	if !ok {
		return chatRequestScope{}, db.ChatSession{}, false
	}
	session, ok := h.gateChatSessionForUser(w, r, scope, chi.URLParam(r, "sessionId"))
	return scope, session, ok
}

func (h *Handler) GetChatSession(w http.ResponseWriter, r *http.Request) {
	_, session, ok := h.requireCurrentChatSession(w, r)
	if !ok {
		return
	}

	writeJSON(w, http.StatusOK, chatSessionToResponse(session))
}

// UpdateChatSession updates user-editable fields on a chat session — today
// just `title`, surfaced by the inline rename affordance in the session
// dropdown. Title is the only field accepted; agent/creator/workspace are
// immutable, and the resume pointers
// (session_id / work_dir / runtime_id) are daemon-owned.
func (h *Handler) UpdateChatSession(w http.ResponseWriter, r *http.Request) {
	scope, ok := requireChatRequestScope(w, r)
	if !ok {
		return
	}
	sessionID := chi.URLParam(r, "sessionId")

	var req struct {
		Title *string `json:"title"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Title == nil {
		writeError(w, http.StatusBadRequest, "title is required")
		return
	}
	title := strings.TrimSpace(*req.Title)
	if title == "" {
		writeError(w, http.StatusBadRequest, "title is required")
		return
	}
	if len([]rune(title)) > chatSessionTitleMaxLen {
		writeError(w, http.StatusBadRequest, "title is too long")
		return
	}

	session, ok := h.gateChatSessionForUser(w, r, scope, sessionID)
	if !ok {
		return
	}

	updated, err := h.Queries.UpdateChatSessionTitle(r.Context(), db.UpdateChatSessionTitleParams{
		ID:    session.ID,
		Title: title,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update chat session")
		return
	}

	resolvedSessionID := uuidToString(updated.ID)
	h.publishChat(protocol.EventChatSessionUpdated, scope.workspaceID, "member", scope.userID, resolvedSessionID, protocol.ChatSessionUpdatedPayload{
		ChatSessionID: resolvedSessionID,
		Title:         updated.Title,
		UpdatedAt:     timestampToString(updated.UpdatedAt),
	})

	writeJSON(w, http.StatusOK, chatSessionToResponse(updated))
}

// DeleteChatSession hard-deletes a chat session owned by the caller. The
// row lock + cancel + delete run inside a single tx so a concurrent
// SendChatMessage cannot enqueue a task that would later be orphaned by
// the FK ON DELETE SET NULL on agent_task_queue.chat_session_id. Cancel
// failure aborts the delete; events fire only after commit.
func (h *Handler) DeleteChatSession(w http.ResponseWriter, r *http.Request) {
	scope, ok := requireChatRequestScope(w, r)
	if !ok {
		return
	}
	sessionID := chi.URLParam(r, "sessionId")

	session, ok := h.loadChatSessionForUser(w, r, scope, sessionID)
	if !ok {
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start transaction")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	qtx := h.Queries.WithTx(tx)

	// FOR UPDATE on the chat_session row blocks any concurrent INSERT into
	// agent_task_queue that references it (the FK validation needs a
	// KEY SHARE lock). After we commit the delete, the blocked INSERT
	// fails its FK check, so it can't land an orphaned task.
	if _, err := qtx.LockChatSessionForDelete(r.Context(), session.ID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Already gone — treat as idempotent success.
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to lock chat session")
		return
	}

	cancelled, err := qtx.CancelAgentTasksByChatSession(r.Context(), session.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to cancel chat session tasks")
		return
	}

	// Write cancel traces while chat_session still exists in this tx so
	// task_trace_event.chat_session_id keeps a valid FK. Post-commit
	// capture used to race the hard delete and drop cancel evidence on
	// task_trace_event_chat_session_id_fkey.
	h.TaskService.CaptureCancelledTaskTracesInTx(r.Context(), qtx, cancelled)
	cancelledEvents, err := h.TaskService.EnqueueCancelledTaskEvents(r.Context(), qtx, cancelled)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record chat session task cancellation")
		return
	}

	if err := qtx.DeleteChatSession(r.Context(), db.DeleteChatSessionParams{
		ID:          session.ID,
		WorkspaceID: session.WorkspaceID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete chat session")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		slog.Warn("commit chat session delete failed", "session_id", sessionID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to commit chat session delete")
		return
	}

	// Post-commit side effects only — traces already landed in the tx above.
	// Subscribers should never observe events for a tx that didn't persist.
	h.TaskService.NotifyCancelledTasks(r.Context(), cancelled, cancelledEvents)

	resolvedSessionID := uuidToString(session.ID)
	h.publishChat(protocol.EventChatSessionDeleted, scope.workspaceID, "member", scope.userID, resolvedSessionID, protocol.ChatSessionDeletedPayload{
		ChatSessionID: resolvedSessionID,
	})

	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Chat Messages
// ---------------------------------------------------------------------------

type sendChatMessageRequest struct {
	Content       string   `json:"content"`
	AttachmentIDs []string `json:"attachment_ids"`
}

type SendChatMessageResponse struct {
	MessageID string `json:"message_id"`
	TaskID    string `json:"task_id"`
	// AttachmentIDs are the attachment rows actually bound to this message by
	// the server. The response always includes an array, including `[]` when
	// none were requested or bound.
	AttachmentIDs []string `json:"attachment_ids"`
	// CreatedAt anchors the chat StatusPill timer the instant the user
	// hits send. Without it the front-end falls back to its local clock
	// and the timer "snaps backwards" later when WS events deliver the
	// real created_at. Returning it here means the pill renders 0s from
	// the start with a stable anchor.
	CreatedAt string `json:"created_at"`
}

func (h *Handler) SendChatMessage(w http.ResponseWriter, r *http.Request) {
	requestScope, ok := requireChatRequestScope(w, r)
	if !ok {
		return
	}
	sessionID := chi.URLParam(r, "sessionId")
	idempotencyKey, ok := requireIdempotencyKey(w, r)
	if !ok {
		return
	}

	var req sendChatMessageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Content == "" {
		writeError(w, http.StatusBadRequest, "content is required")
		return
	}

	// Pre-validate attachment ids early so invalid input returns 400 before
	// any state mutation. The actual link runs after CreateChatMessage so we
	// have a message_id to back-fill into the attachment rows.
	attachmentIDs, ok := parseUUIDSliceOrBadRequest(w, req.AttachmentIDs, "attachment_ids")
	if !ok {
		return
	}

	sessionUUID, ok := parseUUIDOrBadRequest(w, sessionID, "chat session id")
	if !ok {
		return
	}
	workspaceUUID, ok := parseUUIDOrBadRequest(w, requestScope.workspaceID, "workspace id")
	if !ok {
		return
	}
	actorType, actorID := resolveActor(r, requestScope.userID)
	idempotencyScope, err := newChatIdempotencyScope(
		workspaceUUID,
		actorType,
		parseUUID(actorID),
		chatSendMessageOperation,
		idempotencyKey,
		struct {
			Version       int      `json:"version"`
			SessionID     string   `json:"session_id"`
			Content       string   `json:"content"`
			AttachmentIDs []string `json:"attachment_ids"`
		}{
			Version:       1,
			SessionID:     sessionID,
			Content:       req.Content,
			AttachmentIDs: canonicalAttachmentIDs(attachmentIDs),
		},
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to prepare chat message request")
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start transaction")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	qtx := h.Queries.WithTx(tx)
	record, created, err := reserveChatIdempotencyRecord(r.Context(), qtx, idempotencyScope)
	if err != nil {
		writeChatIdempotencyFailure(w, err)
		return
	}
	if !created {
		response, status, err := decodeChatIdempotencyResponse[SendChatMessageResponse](record)
		if err != nil {
			slog.Error("decode send-chat-message replay failed", "error", err)
			writeChatIdempotencyFailure(w, err)
			return
		}
		_ = tx.Rollback(r.Context())
		h.TaskService.WakeChatTaskIfQueued(r.Context(), response.TaskID)
		w.Header().Set("Idempotency-Replayed", "true")
		writeJSON(w, status, response)
		return
	}

	// Mutable business preconditions run only for a new operation. A replay
	// must return the committed response even if the session or agent changed
	// after the original 201 was lost.
	session, err := qtx.GetChatSessionInWorkspace(r.Context(), db.GetChatSessionInWorkspaceParams{
		ID:          sessionUUID,
		WorkspaceID: workspaceUUID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "chat session not found")
		return
	}
	if uuidToString(session.CreatorID) != requestScope.userID {
		writeError(w, http.StatusForbidden, "not your chat session")
		return
	}

	lockedAgent, err := qtx.LockAgentInWorkspaceForChat(r.Context(), db.LockAgentInWorkspaceForChatParams{
		ID:          session.AgentID,
		WorkspaceID: session.WorkspaceID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "agent not found")
		return
	}
	if lockedAgent.ArchivedAt.Valid {
		writeError(w, http.StatusBadRequest, "agent is archived")
		return
	}
	if !lockedAgent.RuntimeID.Valid {
		writeError(w, http.StatusBadRequest, "agent has no runtime")
		return
	}
	if !h.requirePersonalAgentAccess(w, r, lockedAgent, actorType, actorID, requestScope.workspaceID, "you do not have access to this agent") {
		return
	}
	lockedSession, err := qtx.LockChatSessionForSend(r.Context(), db.LockChatSessionForSendParams{
		ID:          session.ID,
		WorkspaceID: session.WorkspaceID,
		CreatorID:   session.CreatorID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "chat session not found")
		return
	}
	if lockedSession.AgentID != lockedAgent.ID {
		writeError(w, http.StatusConflict, "chat session agent changed")
		return
	}
	if len(attachmentIDs) > 0 {
		if _, err := qtx.LockAttachmentsForChatMessage(r.Context(), db.LockAttachmentsForChatMessageParams{
			WorkspaceID:   lockedSession.WorkspaceID,
			ChatSessionID: lockedSession.ID,
			UploaderType:  actorType,
			UploaderID:    parseUUID(actorID),
			AttachmentIds: attachmentIDs,
		}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to lock chat attachments")
			return
		}
	}

	msg, err := qtx.CreateChatMessage(r.Context(), db.CreateChatMessageParams{
		ChatSessionID: lockedSession.ID,
		Role:          "user",
		Content:       req.Content,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create chat message")
		return
	}

	boundAttachmentIDs := make([]string, 0, len(attachmentIDs))
	if len(attachmentIDs) > 0 {
		bound, err := qtx.LinkAttachmentsToChatMessage(r.Context(), db.LinkAttachmentsToChatMessageParams{
			ChatMessageID: msg.ID,
			ChatSessionID: lockedSession.ID,
			WorkspaceID:   lockedSession.WorkspaceID,
			UploaderType:  actorType,
			UploaderID:    parseUUID(actorID),
			AttachmentIds: attachmentIDs,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to link chat attachments")
			return
		}
		for _, id := range bound {
			boundAttachmentIDs = append(boundAttachmentIDs, uuidToString(id))
		}
		sort.Strings(boundAttachmentIDs)
	}

	task, err := h.TaskService.CreateChatTaskInTx(
		r.Context(),
		qtx,
		lockedSession,
		lockedAgent,
		parseUUID(requestScope.userID),
	)
	if err != nil {
		slog.Error("create transactional chat task failed", "session_id", sessionID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to enqueue chat task")
		return
	}
	if err := qtx.LinkChatMessageToTask(r.Context(), db.LinkChatMessageToTaskParams{
		ID:     msg.ID,
		TaskID: task.ID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to link chat message to task")
		return
	}
	if err := qtx.TouchChatSession(r.Context(), lockedSession.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update chat session")
		return
	}
	response := SendChatMessageResponse{
		MessageID:     uuidToString(msg.ID),
		TaskID:        uuidToString(task.ID),
		CreatedAt:     timestampToString(task.CreatedAt),
		AttachmentIDs: boundAttachmentIDs,
	}
	if err := completeChatIdempotencyRecord(r.Context(), qtx, idempotencyScope, http.StatusCreated, response); err != nil {
		slog.Error("persist send-chat-message response failed", "error", err)
		writeChatIdempotencyFailure(w, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit chat message")
		return
	}

	h.TaskService.PublishChatTaskEnqueued(r.Context(), task)
	taskContext := h.TaskService.AnalyticsContextForTask(r.Context(), task)
	platform, _, _ := middleware.ClientMetadataFromContext(r.Context())
	obsmetrics.RecordEvent(h.Analytics, h.Metrics, analytics.ChatMessageSent(
		requestScope.userID,
		requestScope.workspaceID,
		uuidToString(lockedSession.ID),
		uuidToString(task.ID),
		uuidToString(lockedSession.AgentID),
		taskContext.RuntimeMode,
		taskContext.Provider,
		platform,
	))

	// Broadcast the user message.
	resolvedSessionID := uuidToString(lockedSession.ID)
	h.publishChat(protocol.EventChatMessage, requestScope.workspaceID, "member", requestScope.userID, resolvedSessionID, protocol.ChatMessagePayload{
		ChatSessionID: resolvedSessionID,
		MessageID:     uuidToString(msg.ID),
		Role:          "user",
		Content:       req.Content,
		TaskID:        uuidToString(task.ID),
		CreatedAt:     timestampToString(msg.CreatedAt),
	})

	writeJSON(w, http.StatusCreated, response)
}

type ChatMessagesCursorResponse struct {
	CreatedAt string `json:"created_at"`
	ID        string `json:"id"`
}

type chatMessagesPageResponse struct {
	Messages   []ChatMessageResponse       `json:"messages"`
	Limit      int                         `json:"limit"`
	HasMore    bool                        `json:"has_more"`
	NextCursor *ChatMessagesCursorResponse `json:"next_cursor,omitempty"`
}

func parseChatMessagesPageParams(r *http.Request) (int, pgtype.Timestamptz, pgtype.UUID, error) {
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			return 0, pgtype.Timestamptz{}, pgtype.UUID{}, errors.New("invalid limit")
		}
		limit = parsed
	}

	rawBeforeCreatedAt := r.URL.Query().Get("before_created_at")
	rawBeforeID := r.URL.Query().Get("before_id")
	if rawBeforeCreatedAt == "" && rawBeforeID == "" {
		return limit, pgtype.Timestamptz{}, pgtype.UUID{}, nil
	}
	if rawBeforeCreatedAt == "" || rawBeforeID == "" {
		return 0, pgtype.Timestamptz{}, pgtype.UUID{}, errors.New("invalid cursor")
	}
	beforeTime, err := time.Parse(time.RFC3339Nano, rawBeforeCreatedAt)
	if err != nil {
		return 0, pgtype.Timestamptz{}, pgtype.UUID{}, errors.New("invalid cursor")
	}
	beforeID, err := util.ParseUUID(rawBeforeID)
	if err != nil {
		return 0, pgtype.Timestamptz{}, pgtype.UUID{}, errors.New("invalid cursor")
	}
	return limit, pgtype.Timestamptz{Time: beforeTime, Valid: true}, beforeID, nil
}

func (h *Handler) ListChatMessagesPage(w http.ResponseWriter, r *http.Request) {
	scope, session, ok := h.requireCurrentChatSession(w, r)
	if !ok {
		return
	}
	sessionID := uuidToString(session.ID)

	limit, beforeCreatedAt, beforeID, err := parseChatMessagesPageParams(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	messages, err := h.Queries.ListChatMessagesPage(r.Context(), db.ListChatMessagesPageParams{
		ChatSessionID:   session.ID,
		Limit:           int32(limit + 1),
		BeforeCreatedAt: beforeCreatedAt,
		BeforeID:        beforeID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list chat messages")
		return
	}
	hasMore := len(messages) > limit
	if hasMore {
		messages = messages[:limit]
	}
	var nextCursor *ChatMessagesCursorResponse
	if hasMore && len(messages) > 0 {
		oldest := messages[len(messages)-1]
		nextCursor = &ChatMessagesCursorResponse{
			CreatedAt: oldest.CreatedAt.Time.Format(time.RFC3339Nano),
			ID:        uuidToString(oldest.ID),
		}
	}
	// SQL fetches newest windows first so the empty cursor opens at the recent
	// tail. Reverse each cursor page before serializing to keep message order
	// chronological within the viewport.
	for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
		messages[i], messages[j] = messages[j], messages[i]
	}

	messageIDs := make([]pgtype.UUID, len(messages))
	for i, m := range messages {
		messageIDs[i] = m.ID
	}
	groupedAtt, err := h.loadChatMessageAttachments(r.Context(), scope.workspaceID, messageIDs)
	if err != nil {
		slog.Error("load chat message attachments failed", "error", err, "session_id", sessionID, "workspace_id", scope.workspaceID)
		writeError(w, http.StatusInternalServerError, "failed to list chat messages")
		return
	}

	resp := make([]ChatMessageResponse, len(messages))
	for i, m := range messages {
		resp[i] = chatMessageToResponse(m, groupedAtt[uuidToString(m.ID)])
	}
	writeJSON(w, http.StatusOK, chatMessagesPageResponse{
		Messages:   resp,
		Limit:      limit,
		HasMore:    hasMore,
		NextCursor: nextCursor,
	})
}

// PendingChatTaskResponse is returned by GetPendingChatTask — either the
// current in-flight task's id/status, or an empty object when none is active.
// CreatedAt is the anchor the frontend uses to time the chat StatusPill
// (elapsed seconds = now - CreatedAt). It must come from the server because
// optimistic seeds don't have a real task created_at and the timer needs to
// survive refresh / reopen.
type PendingChatTaskResponse struct {
	TaskID    string `json:"task_id,omitempty"`
	Status    string `json:"status,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
}

// MarkChatSessionRead clears the session's unread_since (→ has_unread=false)
// and broadcasts chat:session_read so other devices of the same user drop
// their badges.
func (h *Handler) MarkChatSessionRead(w http.ResponseWriter, r *http.Request) {
	scope, session, ok := h.requireCurrentChatSession(w, r)
	if !ok {
		return
	}

	if err := h.Queries.MarkChatSessionRead(r.Context(), session.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to mark session read")
		return
	}

	resolvedSessionID := uuidToString(session.ID)
	h.publishChat(protocol.EventChatSessionRead, scope.workspaceID, "member", scope.userID, resolvedSessionID, protocol.ChatSessionReadPayload{
		ChatSessionID: resolvedSessionID,
	})

	w.WriteHeader(http.StatusNoContent)
}

// PendingChatTasksResponse is the aggregate view consumed by the FAB.
type PendingChatTasksResponse struct {
	Tasks []PendingChatTaskItem `json:"tasks"`
}

type PendingChatTaskItem struct {
	TaskID        string `json:"task_id"`
	Status        string `json:"status"`
	ChatSessionID string `json:"chat_session_id"`
}

type CancelledChatMessageResponse struct {
	ChatSessionID  string               `json:"chat_session_id"`
	MessageID      string               `json:"message_id"`
	Content        string               `json:"content"`
	RestoreToInput bool                 `json:"restore_to_input"`
	Attachments    []AttachmentResponse `json:"attachments,omitempty"`
}

type cancelTaskByUserResponse struct {
	AgentTaskResponse
	CancelledChatMessage *CancelledChatMessageResponse `json:"cancelled_chat_message,omitempty"`
}

// ListPendingChatTasks returns every in-flight chat task owned by the current
// user in this workspace. Drives the FAB's "running" indicator when the chat
// window is closed (no per-session query is subscribed). Tasks belonging to
// personal agents the caller has lost access to are dropped from the response.
func (h *Handler) ListPendingChatTasks(w http.ResponseWriter, r *http.Request) {
	scope, ok := requireChatRequestScope(w, r)
	if !ok {
		return
	}

	allowed, ok := h.chatAccessibleAgentIDs(w, r, scope)
	if !ok {
		return
	}

	rows, err := h.Queries.ListPendingChatTasksByCreator(r.Context(), db.ListPendingChatTasksByCreatorParams{
		WorkspaceID: parseUUID(scope.workspaceID),
		CreatorID:   parseUUID(scope.userID),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list pending chat tasks")
		return
	}

	// Map session → agent so we can filter without an N+1. The user's own
	// session list is small, so one extra query is cheaper than per-row
	// lookups.
	sessions, err := h.Queries.ListChatSessionsByCreator(r.Context(), db.ListChatSessionsByCreatorParams{
		WorkspaceID: parseUUID(scope.workspaceID),
		CreatorID:   parseUUID(scope.userID),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to resolve chat session agents")
		return
	}
	sessionAgent := make(map[string]string, len(sessions))
	for _, s := range sessions {
		sessionAgent[uuidToString(s.ID)] = uuidToString(s.AgentID)
	}

	items := make([]PendingChatTaskItem, 0, len(rows))
	for _, row := range rows {
		sessionID := uuidToString(row.ChatSessionID)
		agentID, hasAgent := sessionAgent[sessionID]
		if !hasAgent {
			continue
		}
		if _, ok := allowed[agentID]; !ok {
			continue
		}
		items = append(items, PendingChatTaskItem{
			TaskID:        uuidToString(row.TaskID),
			Status:        row.Status,
			ChatSessionID: sessionID,
		})
	}
	writeJSON(w, http.StatusOK, PendingChatTasksResponse{Tasks: items})
}

// GetPendingChatTask returns the most recent in-flight task (queued / dispatched
// / running) for a chat session. The frontend polls this on mount / session
// switch so pending UI state survives refresh and reopen.
func (h *Handler) GetPendingChatTask(w http.ResponseWriter, r *http.Request) {
	_, session, ok := h.requireCurrentChatSession(w, r)
	if !ok {
		return
	}
	sessionID := uuidToString(session.ID)

	task, err := h.Queries.GetPendingChatTask(r.Context(), session.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// No in-flight task — return an empty object, not an error.
			writeJSON(w, http.StatusOK, PendingChatTaskResponse{})
			return
		}
		if writeClientClosedIfCanceled(w, err) {
			return
		}
		slog.Error("load pending chat task failed", "session_id", sessionID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to load pending chat task")
		return
	}

	writeJSON(w, http.StatusOK, PendingChatTaskResponse{
		TaskID:    uuidToString(task.ID),
		Status:    task.Status,
		CreatedAt: timestampToString(task.CreatedAt),
	})
}

// ---------------------------------------------------------------------------
// Task cancellation (user-facing, with ownership check)
// ---------------------------------------------------------------------------

// CancelTaskByUser cancels a task the caller is allowed to act on within the
// current workspace.
//
// Tenancy is enforced uniformly through the task's owning agent: every
// agent_task_queue row carries a NOT NULL agent_id (ON DELETE CASCADE, so the
// agent always exists), and agents are workspace-scoped. GetAgentTaskInWorkspace
// is therefore the single tenant guard that works regardless of which optional
// source FK (issue / chat_session / autopilot_run) is set — which is what makes
// run_only autopilot tasks and quick_create tasks (whose issue does not exist
// yet) cancellable at all. Keying cancellation off issue_id / chat_session_id
// alone is exactly what 404'd these tasks before (MUL-2827).
//
// On top of tenancy, two privacy models layer on:
//   - a chat task is private to the member who started the conversation, so
//     only that creator may cancel it;
//   - every other task surfaces on the agent Activity tab and the workspace
//     task snapshot, both of which hide personal agents from members without
//     access. Cancellation mirrors that gate via canAccessPersonalAgent so the
//     id-only endpoint is never more permissive than the surface that exposes
//     the task.
func (h *Handler) CancelTaskByUser(w http.ResponseWriter, r *http.Request) {
	scope, ok := requireChatRequestScope(w, r)
	if !ok {
		return
	}
	workspaceUUID, ok := parseUUIDOrBadRequest(w, scope.workspaceID, "workspace id")
	if !ok {
		return
	}
	taskID := chi.URLParam(r, "taskId")
	taskUUID, ok := parseUUIDOrBadRequest(w, taskID, "task id")
	if !ok {
		return
	}

	task, err := h.Queries.GetAgentTaskInWorkspace(r.Context(), db.GetAgentTaskInWorkspaceParams{
		ID:          taskUUID,
		WorkspaceID: workspaceUUID,
	})
	if err != nil {
		writeEntityLoadError(w, r, err, "task", "task_id", taskID)
		return
	}

	if task.ChatSessionID.Valid {
		// Chat privacy: only the member who opened the conversation may
		// cancel its task, even though the workspace is shared.
		cs, err := h.Queries.GetChatSessionInWorkspace(r.Context(), db.GetChatSessionInWorkspaceParams{
			ID:          task.ChatSessionID,
			WorkspaceID: workspaceUUID,
		})
		if err != nil {
			writeEntityLoadError(w, r, err, "task", "task_id", taskID, "chat_session_id", uuidToString(task.ChatSessionID))
			return
		}
		if uuidToString(cs.CreatorID) != scope.userID {
			writeError(w, http.StatusForbidden, "not your task")
			return
		}
	} else {
		// Issue / autopilot / quick_create tasks are all visible on the
		// agent Activity tab + workspace snapshot, which gate private
		// agents. Mirror that gate here.
		agent, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{
			ID:          task.AgentID,
			WorkspaceID: workspaceUUID,
		})
		if err != nil {
			writeEntityLoadError(w, r, err, "task", "task_id", taskID, "agent_id", uuidToString(task.AgentID))
			return
		}
		actorType, actorID := resolveActor(r, scope.userID)
		if !h.requirePersonalAgentAccess(w, r, agent, actorType, actorID, scope.workspaceID, "you do not have access to this agent") {
			return
		}
	}

	cancelled, err := h.TaskService.CancelTaskWithResult(r.Context(), taskUUID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	resp := cancelTaskByUserResponse{
		AgentTaskResponse: taskToResponse(cancelled.Task, scope.workspaceID),
	}
	if cancelled.CancelledChatMessage != nil {
		attachments := make([]AttachmentResponse, 0, len(cancelled.CancelledChatMessage.Attachments))
		for _, a := range cancelled.CancelledChatMessage.Attachments {
			attachments = append(attachments, h.attachmentToResponse(a))
		}
		resp.CancelledChatMessage = &CancelledChatMessageResponse{
			ChatSessionID:  cancelled.CancelledChatMessage.ChatSessionID,
			MessageID:      cancelled.CancelledChatMessage.MessageID,
			Content:        cancelled.CancelledChatMessage.Content,
			RestoreToInput: cancelled.CancelledChatMessage.RestoreToInput,
			Attachments:    attachments,
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

// ---------------------------------------------------------------------------
// Response types & helpers
// ---------------------------------------------------------------------------

type chatSessionResponse struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	AgentID     string `json:"agent_id"`
	CreatorID   string `json:"creator_id"`
	Title       string `json:"title"`
	// Only populated by list endpoints — single-session fetches return false.
	HasUnread bool   `json:"has_unread"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

type ChatMessageResponse struct {
	ID            string  `json:"id"`
	ChatSessionID string  `json:"chat_session_id"`
	Role          string  `json:"role"`
	Content       string  `json:"content"`
	TaskID        *string `json:"task_id"`
	CreatedAt     string  `json:"created_at"`
	// FailureReason flags an assistant row synthesized by FailTask's chat
	// fallback. Front-end uses it to switch to the destructive bubble.
	FailureReason *string `json:"failure_reason"`
	// ElapsedMs is the wall-clock duration from task creation to terminal
	// state. Drives "Replied in 38s" / "Failed after 12s" captions.
	ElapsedMs *int64 `json:"elapsed_ms"`
	// Attachments linked to this message via chat_message_id. The chat
	// bubble renders file cards from these, and the daemon claim path
	// (daemon.go) pulls structured metadata from the same source so the
	// agent can `multica attachment download <id>` rather than guessing
	// from a markdown URL that may expire.
	Attachments []AttachmentResponse `json:"attachments,omitempty"`
}

func chatSessionToResponse(s db.ChatSession) chatSessionResponse {
	return chatSessionResponse{
		ID:          uuidToString(s.ID),
		WorkspaceID: uuidToString(s.WorkspaceID),
		AgentID:     uuidToString(s.AgentID),
		CreatorID:   uuidToString(s.CreatorID),
		Title:       s.Title,
		CreatedAt:   timestampToString(s.CreatedAt),
		UpdatedAt:   timestampToString(s.UpdatedAt),
	}
}

func chatMessageToResponse(m db.ChatMessage, attachments []AttachmentResponse) ChatMessageResponse {
	return ChatMessageResponse{
		ID:            uuidToString(m.ID),
		ChatSessionID: uuidToString(m.ChatSessionID),
		Role:          m.Role,
		Content:       m.Content,
		TaskID:        uuidToPtr(m.TaskID),
		CreatedAt:     timestampToString(m.CreatedAt),
		FailureReason: textToPtr(m.FailureReason),
		ElapsedMs:     int8ToPtr(m.ElapsedMs),
		Attachments:   attachments,
	}
}
