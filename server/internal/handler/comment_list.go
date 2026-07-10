package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func (h *Handler) ListComments(w http.ResponseWriter, r *http.Request) {
	issueID := chi.URLParam(r, "id")
	issue, ok := h.loadIssueForUser(w, r, issueID)
	if !ok {
		return
	}

	q := r.URL.Query()

	var sinceTime pgtype.Timestamptz
	if v := q.Get("since"); v != "" {
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			// Fall back to RFC3339 for backwards-compat with the original CLI.
			t, err = time.Parse(time.RFC3339, v)
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid since parameter; expected RFC3339 format")
				return
			}
		}
		sinceTime = pgtype.Timestamptz{Time: t, Valid: true}
	}

	threadStr := q.Get("thread")
	recentStr := q.Get("recent")
	tailStr := q.Get("tail")
	beforeTimeStr := q.Get("before")
	beforeIDStr := q.Get("before_id")
	if beforeIDStr == "" {
		// Accept hyphenated alias to match CLI flag convention.
		beforeIDStr = q.Get("before-id")
	}

	rootsOnlyStr := q.Get("roots_only")
	if rootsOnlyStr == "" {
		// Accept hyphenated alias to match CLI flag convention.
		rootsOnlyStr = q.Get("roots-only")
	}

	rootsOnly := false
	if rootsOnlyStr != "" {
		switch rootsOnlyStr {
		case "true":
			rootsOnly = true
		case "false":
		default:
			writeError(w, http.StatusBadRequest, "invalid roots_only parameter; expected boolean")
			return
		}
	}

	// summary=true is an orthogonal content projection: it clips each comment's
	// content to a fixed budget so an agent can scan a list without pulling full
	// bodies into context. It is intentionally NOT mutually exclusive with any
	// mode — it composes with the default list, since, thread, recent, and
	// roots_only alike.
	summary := false
	if summaryStr := q.Get("summary"); summaryStr != "" {
		switch summaryStr {
		case "true":
			summary = true
		case "false":
		default:
			writeError(w, http.StatusBadRequest, "invalid summary parameter; expected boolean")
			return
		}
	}

	// --- combination validation ----------------------------------------
	if rootsOnly && threadStr != "" {
		writeError(w, http.StatusBadRequest, "roots_only and thread are mutually exclusive")
		return
	}
	if rootsOnly && recentStr != "" {
		writeError(w, http.StatusBadRequest, "roots_only and recent are mutually exclusive")
		return
	}
	if rootsOnly && tailStr != "" {
		writeError(w, http.StatusBadRequest, "roots_only and tail are mutually exclusive")
		return
	}
	if rootsOnly && (beforeTimeStr != "" || beforeIDStr != "") {
		writeError(w, http.StatusBadRequest, "roots_only does not support before / before_id")
		return
	}
	if threadStr != "" && recentStr != "" {
		writeError(w, http.StatusBadRequest, "thread and recent are mutually exclusive")
		return
	}
	if tailStr != "" && threadStr == "" {
		writeError(w, http.StatusBadRequest, "tail requires thread (it is a thread-scoped limit)")
		return
	}
	if (beforeTimeStr == "") != (beforeIDStr == "") {
		writeError(w, http.StatusBadRequest, "before and before_id must be set together (composite cursor)")
		return
	}
	// Cursor needs either a recent window (thread cursor) or a tailed thread
	// (reply cursor). A bare cursor would otherwise fall through to the
	// default / since path — returning a full timeline that the caller did
	// not ask for. Reject loudly so the API surface matches the documented
	// semantics.
	if beforeTimeStr != "" && recentStr == "" && (threadStr == "" || tailStr == "") {
		writeError(w, http.StatusBadRequest, "before / before_id require recent (thread cursor) or thread + tail (reply cursor)")
		return
	}

	// --- parse cursor / recent ----------------------------------------
	var beforeCursor pgtype.Timestamptz
	var beforeUUID pgtype.UUID
	hasCursor := false
	if beforeTimeStr != "" {
		t, err := time.Parse(time.RFC3339Nano, beforeTimeStr)
		if err != nil {
			t, err = time.Parse(time.RFC3339, beforeTimeStr)
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid before parameter; expected RFC3339 format")
				return
			}
		}
		beforeCursor = pgtype.Timestamptz{Time: t, Valid: true}
		uuid, perr := util.ParseUUID(beforeIDStr)
		if perr != nil {
			writeError(w, http.StatusBadRequest, "invalid before_id parameter; expected UUID")
			return
		}
		beforeUUID = uuid
		hasCursor = true
	}

	recentN := 0
	if recentStr != "" {
		n, err := strconv.Atoi(recentStr)
		if err != nil || n <= 0 {
			writeError(w, http.StatusBadRequest, "invalid recent parameter; expected positive integer")
			return
		}
		if n > commentHardCap {
			n = commentHardCap
		}
		recentN = n
	}

	// tail=0 is allowed (returns root only — useful for "what is this thread
	// about" lookups without dragging any replies into context). Negative
	// values are rejected because they'd round-trip to LIMIT -N which
	// PostgreSQL flags as a syntax error.
	threadTail := -1
	threadTailSet := false
	if tailStr != "" {
		n, err := strconv.Atoi(tailStr)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "invalid tail parameter; expected non-negative integer")
			return
		}
		if n > commentHardCap {
			n = commentHardCap
		}
		threadTail = n
		threadTailSet = true
	}

	result, err := h.fetchCommentsForList(r.Context(), fetchCommentsArgs{
		Issue:         issue,
		Since:         sinceTime,
		ThreadAnchor:  threadStr,
		ThreadTail:    threadTail,
		ThreadTailSet: threadTailSet,
		RecentN:       recentN,
		HasCursor:     hasCursor,
		BeforeAt:      beforeCursor,
		BeforeID:      beforeUUID,
		RootsOnly:     rootsOnly,
	})
	if err != nil {
		switch err {
		case errCommentThreadNotFound:
			writeError(w, http.StatusNotFound, "thread anchor not found in this issue")
			return
		case errCommentThreadBadID:
			writeError(w, http.StatusBadRequest, "invalid thread parameter; expected UUID")
			return
		default:
			writeError(w, http.StatusInternalServerError, "failed to list comments")
			return
		}
	}

	commentIDs := make([]pgtype.UUID, len(result.Comments))
	for i, c := range result.Comments {
		commentIDs[i] = c.ID
	}
	grouped := h.groupReactions(r, commentIDs)
	groupedAtt := h.groupAttachments(r, commentIDs)

	resp := make([]CommentResponse, len(result.Comments))
	for i, c := range result.Comments {
		cid := uuidToString(c.ID)
		resp[i] = commentToResponse(c, grouped[cid], groupedAtt[cid])
		// Attach roots_only orientation stats when present (nil map elsewhere).
		if st, ok := result.RootStats[cid]; ok {
			rc := st.ReplyCount
			resp[i].ReplyCount = &rc
			if st.LastActivityAt.Valid {
				la := timestampToString(st.LastActivityAt)
				resp[i].LastActivityAt = &la
			}
		}
		// Apply the summary projection last so it clips whatever content the
		// chosen read mode produced, uniformly across every mode.
		if summary {
			clipped, truncated := summarizeContent(resp[i].Content)
			resp[i].Content = clipped
			resp[i].ContentTruncated = &truncated
		}
	}

	// Emit the next cursor as response headers when the page is likely not
	// the last one. The cursor's meaning is context-dependent: under recent
	// it points at the oldest thread in the page (next page = older threads);
	// under thread + tail it points at the oldest reply in the page (next
	// page = older replies in the same thread). Headers stay out of the JSON
	// body so the default flat-array response shape — which the desktop UI
	// and existing callers depend on — is unchanged.
	if result.NextBefore != "" && result.NextBeforeID != "" {
		w.Header().Set("X-Multica-Next-Before", result.NextBefore)
		w.Header().Set("X-Multica-Next-Before-Id", result.NextBeforeID)
	}

	writeJSON(w, http.StatusOK, resp)
}

// fetchCommentsArgs bundles the parsed query params so fetchCommentsForList
// stays readable. Sentinel errors below let the caller turn DB-layer outcomes
// into the right HTTP status without leaking SQL details.
//
// ThreadTail is split into a value + a "set" flag because tail=0 is a
// meaningful caller intent (return just the root). A bare int would collapse
// "user did not pass --tail" and "user passed --tail 0" into the same state,
// which would silently downgrade the latter to the full-thread path.
type fetchCommentsArgs struct {
	Issue         db.Issue
	Since         pgtype.Timestamptz
	RootsOnly     bool
	ThreadAnchor  string
	ThreadTail    int
	ThreadTailSet bool
	RecentN       int
	HasCursor     bool
	BeforeAt      pgtype.Timestamptz
	BeforeID      pgtype.UUID
}

// fetchCommentsResult carries both the materialised comments and (for the
// recent/thread-grouped path) the cursor to use for the next page. Cursor
// fields are empty strings when there is no next page or the path does not
// support cursors.
type fetchCommentsResult struct {
	Comments     []db.Comment
	NextBefore   string
	NextBeforeID string
	// RootStats carries per-root orientation stats keyed by comment id string.
	// Populated only on the roots_only path; nil for every other mode.
	RootStats map[string]rootStat
}

// rootStat is the per-thread orientation metadata attached to each root comment
// on the roots_only path. See CommentResponse.ReplyCount / LastActivityAt.
type rootStat struct {
	ReplyCount     int
	LastActivityAt pgtype.Timestamptz
}

var (
	errCommentThreadNotFound = &commentFetchError{"thread anchor not found"}
	errCommentThreadBadID    = &commentFetchError{"invalid thread anchor id"}
)

type commentFetchError struct{ msg string }

func (e *commentFetchError) Error() string { return e.msg }

func (h *Handler) fetchCommentsForList(ctx context.Context, args fetchCommentsArgs) (fetchCommentsResult, error) {
	issue := args.Issue

	// Thread-scoped read. Server resolves the anchor → root via recursive
	// CTE, so we don't have to assume two-layer flat threads here.
	if args.ThreadAnchor != "" {
		anchor, err := util.ParseUUID(args.ThreadAnchor)
		if err != nil {
			return fetchCommentsResult{}, errCommentThreadBadID
		}
		// Tailed path: paged query that returns root + the @reply_limit
		// most recent replies (per (created_at, id)). The thread root is
		// always returned, so a reader can land on a long thread without
		// dragging hundreds of replies into context. The reply-internal
		// cursor (--before / --before-id under --thread + --tail) scrolls
		// to older replies inside the same thread.
		if args.ThreadTailSet {
			// Probe for has-more by asking the SQL for one extra reply
			// beyond what the caller wants. If we get back >tail replies
			// there is at least one older reply still on disk; if we get
			// back ≤tail the page is the tail of the thread and there is
			// nothing older to scroll to (so we must NOT emit a cursor —
			// otherwise the next page is wasted round-trip that returns
			// just the root). This is the exact-boundary fix called out
			// in the MUL-2421 review.
			rows, err := h.Queries.ListThreadCommentsForIssuePaged(ctx, db.ListThreadCommentsForIssuePagedParams{
				AnchorID:    anchor,
				IssueID:     issue.ID,
				WorkspaceID: issue.WorkspaceID,
				HasCursor:   args.HasCursor,
				BeforeAt:    args.BeforeAt,
				BeforeID:    args.BeforeID,
				ReplyLimit:  int32(args.ThreadTail) + 1,
			})
			if err != nil {
				return fetchCommentsResult{}, err
			}
			if len(rows) == 0 {
				return fetchCommentsResult{}, errCommentThreadNotFound
			}
			// Split the result into root + replies (ASC order preserved).
			// Root is identified by parent_id IS NULL and is always
			// present in the SQL output; we keep it out of the cursor /
			// tail-trim logic so the user always sees thread context.
			var rootComment *db.Comment
			replies := make([]db.Comment, 0, len(rows))
			for _, r := range rows {
				c := db.Comment{
					ID:             r.ID,
					IssueID:        r.IssueID,
					AuthorType:     r.AuthorType,
					AuthorID:       r.AuthorID,
					Content:        r.Content,
					Type:           r.Type,
					CreatedAt:      r.CreatedAt,
					UpdatedAt:      r.UpdatedAt,
					ParentID:       r.ParentID,
					WorkspaceID:    r.WorkspaceID,
					ResolvedAt:     r.ResolvedAt,
					ResolvedByType: r.ResolvedByType,
					ResolvedByID:   r.ResolvedByID,
				}
				if !r.ParentID.Valid {
					root := c
					rootComment = &root
					continue
				}
				replies = append(replies, c)
			}
			// Trim the probe overflow back to the caller's tail. The SQL
			// emits ASC, so the extra row is the oldest reply — dropping
			// it from the head is what aligns "newest N" with the user's
			// request.
			hasMore := len(replies) > args.ThreadTail
			if hasMore {
				replies = replies[1:]
			}
			out := make([]db.Comment, 0, len(replies)+1)
			if rootComment != nil {
				out = append(out, *rootComment)
			}
			for _, r := range replies {
				// since drops stale rows AFTER the tail / cursor cut.
				// The root is exempt (already appended above): a reader
				// who set --since to skip already-seen replies still
				// needs the root context if the page only contained
				// the root.
				if args.Since.Valid && !r.CreatedAt.Time.After(args.Since.Time) {
					continue
				}
				out = append(out, r)
			}
			// Emit a reply cursor only when we proved an older reply
			// exists (hasMore). On an exact-boundary page (replyCount
			// == tail with no overflow) hasMore is false and the cursor
			// stays empty.
			//
			// Additionally suppress the cursor when `since` is set and
			// the oldest retained reply on this page is already <= since.
			// The next page walks replies strictly older than that one,
			// so every older reply has created_at strictly less — if the
			// cursor target itself can't satisfy `> since`, no older
			// reply can either, and continuing to paginate would only
			// return root-only pages until the agent walks the entire
			// pre-`since` history. This mirrors the head-thread guard on
			// the recent + since path. Flagged by Elon's second review on
			// MUL-2421.
			res := fetchCommentsResult{Comments: out}
			emitCursor := hasMore && len(replies) > 0
			if emitCursor && args.Since.Valid && !replies[0].CreatedAt.Time.After(args.Since.Time) {
				emitCursor = false
			}
			if emitCursor {
				oldest := replies[0]
				res.NextBefore = oldest.CreatedAt.Time.UTC().Format(time.RFC3339Nano)
				res.NextBeforeID = uuidToString(oldest.ID)
			}
			return res, nil
		}
		rows, err := h.Queries.ListThreadCommentsForIssue(ctx, db.ListThreadCommentsForIssueParams{
			AnchorID:    anchor,
			IssueID:     issue.ID,
			WorkspaceID: issue.WorkspaceID,
			RowLimit:    commentHardCap,
		})
		if err != nil {
			return fetchCommentsResult{}, err
		}
		if len(rows) == 0 {
			return fetchCommentsResult{}, errCommentThreadNotFound
		}
		out := make([]db.Comment, 0, len(rows))
		for _, r := range rows {
			if args.Since.Valid && !r.CreatedAt.Time.After(args.Since.Time) {
				continue
			}
			out = append(out, db.Comment{
				ID:             r.ID,
				IssueID:        r.IssueID,
				AuthorType:     r.AuthorType,
				AuthorID:       r.AuthorID,
				Content:        r.Content,
				Type:           r.Type,
				CreatedAt:      r.CreatedAt,
				UpdatedAt:      r.UpdatedAt,
				ParentID:       r.ParentID,
				WorkspaceID:    r.WorkspaceID,
				ResolvedAt:     r.ResolvedAt,
				ResolvedByType: r.ResolvedByType,
				ResolvedByID:   r.ResolvedByID,
			})
		}
		return fetchCommentsResult{Comments: out}, nil
	}

	// Thread-grouped recent read: N most recently active threads.
	if args.RecentN > 0 {
		rows, err := h.Queries.ListRecentThreadCommentsForIssue(ctx, db.ListRecentThreadCommentsForIssueParams{
			IssueID:     issue.ID,
			WorkspaceID: issue.WorkspaceID,
			HasCursor:   args.HasCursor,
			BeforeAt:    args.BeforeAt,
			BeforeID:    args.BeforeID,
			ThreadLimit: int32(args.RecentN),
		})
		if err != nil {
			return fetchCommentsResult{}, err
		}

		// The SQL already orders rows by (last_activity_at ASC, root_id ASC,
		// created_at ASC, id ASC), so the OLDEST-active thread sits at the
		// head and the FRESHEST thread at the tail. Walk the rows once to:
		//   1. Strip the thread-metadata columns down to db.Comment for the
		//      caller (uniform shape across paths).
		//   2. Count distinct threads in the page so we know whether a "next
		//      older page" is likely to exist.
		//   3. Capture the head thread's (last_activity_at, root_id) — that
		//      is the cursor for the next page (next page = threads strictly
		//      less recent than this one).
		comments := make([]db.Comment, 0, len(rows))
		var headRoot pgtype.UUID
		var headLast pgtype.Timestamptz
		seenRoot := map[string]struct{}{}
		for _, r := range rows {
			if !headRoot.Valid {
				headRoot = r.ThreadRootID
				headLast = r.ThreadLastActivityAt
			}
			seenRoot[uuidToString(r.ThreadRootID)] = struct{}{}
			// Since filter on the recent path: drop comments older than
			// `since`. Done in-memory so we keep the thread-grouped
			// semantics from the query (don't pre-filter rows before the
			// MAX(created_at) ranking — that would silently downgrade a
			// thread whose most recent activity falls inside the window).
			if args.Since.Valid && !r.CreatedAt.Time.After(args.Since.Time) {
				continue
			}
			comments = append(comments, db.Comment{
				ID:             r.ID,
				IssueID:        r.IssueID,
				AuthorType:     r.AuthorType,
				AuthorID:       r.AuthorID,
				Content:        r.Content,
				Type:           r.Type,
				CreatedAt:      r.CreatedAt,
				UpdatedAt:      r.UpdatedAt,
				ParentID:       r.ParentID,
				WorkspaceID:    r.WorkspaceID,
				ResolvedAt:     r.ResolvedAt,
				ResolvedByType: r.ResolvedByType,
				ResolvedByID:   r.ResolvedByID,
			})
		}

		// Only emit a cursor when the page is full. Fewer threads than
		// requested ⇒ the SELECT exhausted matching threads, so there is
		// no older page to scroll to.
		//
		// Additionally suppress the cursor when `since` is set and the head
		// thread's last_activity_at is already <= since. The pagination
		// walks threads in strictly decreasing last_activity_at, so every
		// older page has last_activity_at strictly less than the head's —
		// if the head itself can't satisfy `> since`, no older thread can
		// either. Predicating on the head (not on whether `comments` is
		// empty) also catches the mixed case where this page keeps rows
		// from fresher threads but the head thread is already past `since`.
		// Flagged by Elon in #2787's second review (MUL-2340 nit).
		out := fetchCommentsResult{Comments: comments}
		emitCursor := len(seenRoot) >= args.RecentN && headRoot.Valid && headLast.Valid
		if emitCursor && args.Since.Valid && !headLast.Time.After(args.Since.Time) {
			emitCursor = false
		}
		if emitCursor {
			out.NextBefore = headLast.Time.UTC().Format(time.RFC3339Nano)
			out.NextBeforeID = uuidToString(headRoot)
		}
		return out, nil
	}

	if args.RootsOnly {
		// Root-only read for issue-level orientation. This intentionally
		// stays separate from thread/recent modes: callers get the global
		// top-level discussion first, then fetch a specific thread only when
		// they need reply context. Each root carries reply_count +
		// last_activity_at so the reader can triage which thread to drill into.
		stats := map[string]rootStat{}
		if args.Since.Valid {
			rows, err := h.Queries.ListRootCommentsSinceForIssue(ctx, db.ListRootCommentsSinceForIssueParams{
				IssueID:     issue.ID,
				WorkspaceID: issue.WorkspaceID,
				Since:       args.Since,
				RowLimit:    commentHardCap,
			})
			if err != nil {
				return fetchCommentsResult{}, err
			}
			comments := make([]db.Comment, len(rows))
			for i, r := range rows {
				comments[i] = db.Comment{
					ID: r.ID, IssueID: r.IssueID, AuthorType: r.AuthorType, AuthorID: r.AuthorID,
					Content: r.Content, Type: r.Type, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
					ParentID: r.ParentID, WorkspaceID: r.WorkspaceID, ResolvedAt: r.ResolvedAt,
					ResolvedByType: r.ResolvedByType, ResolvedByID: r.ResolvedByID,
				}
				stats[uuidToString(r.ID)] = rootStat{ReplyCount: int(r.ReplyCount), LastActivityAt: r.LastActivityAt}
			}
			return fetchCommentsResult{Comments: comments, RootStats: stats}, nil
		}

		rows, err := h.Queries.ListRootCommentsForIssue(ctx, db.ListRootCommentsForIssueParams{
			IssueID:     issue.ID,
			WorkspaceID: issue.WorkspaceID,
			RowLimit:    commentHardCap,
		})
		if err != nil {
			return fetchCommentsResult{}, err
		}
		comments := make([]db.Comment, len(rows))
		for i, r := range rows {
			comments[i] = db.Comment{
				ID: r.ID, IssueID: r.IssueID, AuthorType: r.AuthorType, AuthorID: r.AuthorID,
				Content: r.Content, Type: r.Type, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
				ParentID: r.ParentID, WorkspaceID: r.WorkspaceID, ResolvedAt: r.ResolvedAt,
				ResolvedByType: r.ResolvedByType, ResolvedByID: r.ResolvedByID,
			}
			stats[uuidToString(r.ID)] = rootStat{ReplyCount: int(r.ReplyCount), LastActivityAt: r.LastActivityAt}
		}
		return fetchCommentsResult{Comments: comments, RootStats: stats}, nil
	}

	// Default + since paths preserved verbatim (no behavioural change for
	// existing callers).
	if args.Since.Valid {
		comments, err := h.Queries.ListCommentsSinceForIssue(ctx, db.ListCommentsSinceForIssueParams{
			IssueID:     issue.ID,
			WorkspaceID: issue.WorkspaceID,
			CreatedAt:   args.Since,
			Limit:       commentHardCap,
		})
		return fetchCommentsResult{Comments: comments}, err
	}
	comments, err := h.Queries.ListCommentsForIssue(ctx, db.ListCommentsForIssueParams{
		IssueID:     issue.ID,
		WorkspaceID: issue.WorkspaceID,
		Limit:       commentHardCap,
	})
	return fetchCommentsResult{Comments: comments}, err
}

type CreateCommentRequest struct {
	Content          string   `json:"content"`
	Type             string   `json:"type"`
	ParentID         *string  `json:"parent_id"`
	AttachmentIDs    []string `json:"attachment_ids"`
	SuppressAgentIDs []string `json:"suppress_agent_ids"`
}

type CommentTriggerPreviewRequest struct {
	Content          string  `json:"content"`
	ParentID         *string `json:"parent_id"`
	EditingCommentID *string `json:"editing_comment_id"`
}

type CommentTriggerPreviewResponse struct {
	Agents []CommentTriggerAgentResponse `json:"agents"`
}

type CommentTriggerAgentResponse struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	AvatarURL *string `json:"avatar_url,omitempty"`
	Source    string  `json:"source"`
	Reason    string  `json:"reason"`
}

type commentAgentTriggerSource string

const (
	commentTriggerSourceIssueAssignee      commentAgentTriggerSource = "issue_assignee"
	commentTriggerSourceMentionAgent       commentAgentTriggerSource = "mention_agent"
	commentTriggerSourceMentionSquadLeader commentAgentTriggerSource = "mention_squad_leader"
)

var sopRoleKeyMentionRe = regexp.MustCompile(`(?:^|[^A-Za-z0-9_-])@([A-Za-z0-9][A-Za-z0-9-]{0,48})(?:\b|$)`)

type commentAgentTrigger struct {
	Agent  db.Agent
	Source commentAgentTriggerSource
	Squad  *db.Squad
}

type commentSOPProfile struct {
	Mode  string `json:"mode"`
	Steps []struct {
		RoleKey string `json:"role_key"`
	} `json:"steps"`
}

type commentTriggerComputeOptions struct {
	ExcludeTriggerCommentID     pgtype.UUID
	SuppressAssignedSquadLeader bool
}

func commentAgentTriggerReason(trigger commentAgentTrigger) string {
	switch trigger.Source {
	case commentTriggerSourceIssueAssignee:
		return "Current issue assignment will trigger this agent."
	case commentTriggerSourceMentionAgent:
		return "This agent was mentioned in the comment."
	case commentTriggerSourceMentionSquadLeader:
		return "A mentioned squad will trigger its leader."
	default:
		return "This comment will trigger this agent."
	}
}

func commentAgentTriggerToResponse(trigger commentAgentTrigger) CommentTriggerAgentResponse {
	return CommentTriggerAgentResponse{
		ID:        uuidToString(trigger.Agent.ID),
		Name:      trigger.Agent.Name,
		AvatarURL: textToPtr(trigger.Agent.AvatarUrl),
		Source:    string(trigger.Source),
		Reason:    commentAgentTriggerReason(trigger),
	}
}

func (h *Handler) PreviewCommentTriggers(w http.ResponseWriter, r *http.Request) {
	issueID := chi.URLParam(r, "id")
	issue, ok := h.loadIssueForUser(w, r, issueID)
	if !ok {
		return
	}

	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	var req CommentTriggerPreviewRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	var editingComment *db.Comment
	var opts commentTriggerComputeOptions
	if req.EditingCommentID != nil {
		editingID, ok := parseUUIDOrBadRequest(w, *req.EditingCommentID, "editing_comment_id")
		if !ok {
			return
		}
		comment, err := h.Queries.GetCommentInWorkspace(r.Context(), db.GetCommentInWorkspaceParams{
			ID:          editingID,
			WorkspaceID: issue.WorkspaceID,
		})
		if err != nil || uuidToString(comment.IssueID) != uuidToString(issue.ID) {
			writeError(w, http.StatusBadRequest, "invalid editing comment")
			return
		}
		editingComment = &comment
		opts.ExcludeTriggerCommentID = editingID
	}

	var parentID pgtype.UUID
	if req.ParentID != nil {
		parentID, ok = parseUUIDOrBadRequest(w, *req.ParentID, "parent_id")
		if !ok {
			return
		}

		if editingComment != nil && uuidToString(parentID) != uuidToString(editingComment.ParentID) {
			writeError(w, http.StatusBadRequest, "parent_id does not match editing comment")
			return
		}
	} else if editingComment != nil && editingComment.ParentID.Valid {
		parentID = editingComment.ParentID
	}

	var parentComment *db.Comment
	if parentID.Valid {
		parent, err := h.Queries.GetComment(r.Context(), parentID)
		if err != nil || uuidToString(parent.IssueID) != uuidToString(issue.ID) {
			writeError(w, http.StatusBadRequest, "invalid parent comment")
			return
		}
		parentComment = &parent
	}

	content := req.Content
	if content == "" {
		writeJSON(w, http.StatusOK, CommentTriggerPreviewResponse{Agents: []CommentTriggerAgentResponse{}})
		return
	}

	actorType, actorID := h.resolveActor(r, userID, uuidToString(issue.WorkspaceID))
	triggers := h.computeCommentAgentTriggers(r.Context(), issue, content, parentComment, actorType, actorID, opts)
	resp := CommentTriggerPreviewResponse{Agents: make([]CommentTriggerAgentResponse, 0, len(triggers))}
	for _, trigger := range triggers {
		resp.Agents = append(resp.Agents, commentAgentTriggerToResponse(trigger))
	}
	writeJSON(w, http.StatusOK, resp)
}

