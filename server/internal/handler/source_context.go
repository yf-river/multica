// 本文件实现「来源上下文」（source context）的 HTTP 层：把某条评论所在环境的
// 「源议题 + 评论串」整体抓成一张快照，供用户基于它手动创建子事项（manual），或
// 快速派发给 agent（agent / quick-create）。
//
// 本文件承担两类脏活：
//   - 计算「捕获时的快照 vs 现在的真实状态」的差异（标题/描述/描述附件/评论线程），
//     供详情接口回答「相比你看到的那一刻，源内容已经变了什么」；
//   - 把来源附件克隆到 workspace 自己的 source-context 存储目录，并登记对象意图
//     （object intent），保证后续源议题被编辑或删除都不影响已抓取的上下文。
//
// 顶层流程：PreviewCommentSubIssue 先让用户看到「将要捕获什么」（不落库）；真正
// 创建时 CreateCommentSubIssue 校验 capture token 未被过期内容顶掉，再按 manual /
// agent 两种模式分别建子事项或派发任务。所有失败经 writeSourceContextError 统一
// 映射成带 code 的 JSON 错误。
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/logger"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	agentpkg "github.com/multica-ai/multica/server/pkg/agent"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// sourceContextStreamUploader 是存储适配器可选实现的流式上传能力：
// cloneSourceContext 需要「读旧对象 → 写新对象」的整体克隆，若存储连流式上传都
// 不支持，就说明克隆功能不可用。
type sourceContextStreamUploader interface {
	UploadStream(ctx context.Context, key string, reader io.Reader, sizeBytes int64, contentType string, filename string) (string, error)
}

// errSourceContextResponseWritten 是内部哨兵：表示“错误/成功响应已经写回客户端”，
// 上层拿到它时不要再重复回写。
var errSourceContextResponseWritten = errors.New("source context response already written")

// errSourceContextBadRequest 是内部哨兵：表示请求参数错误，最终由
// writeSourceContextError 映射成 400 + code=invalid_request。
var errSourceContextBadRequest = errors.New("source context bad request")

// sourceContextBadRequest 包装一个请求参数错误，使其可被 errors.Is 识别为
// errSourceContextBadRequest。
func sourceContextBadRequest(message string) error {
	return fmt.Errorf("%w: %s", errSourceContextBadRequest, message)
}

// sourceContextPreviewResponse 是预览接口的响应：SourceIssue / CommentThread 是准备
// 捕获的源快照，AnchorCommentID 是锚点评论，CaptureToken 是真正创建时必须原样回传
// 的防过期令牌，Limits 是本次构建用到的用量（供配额相关展示）。
type sourceContextPreviewResponse struct {
	SourceIssue     service.SourceContextIssueSnapshot     `json:"source_issue"`
	CommentThread   []service.SourceContextCommentSnapshot `json:"comment_thread"`
	AnchorCommentID string                                 `json:"anchor_comment_id"`
	CaptureToken    string                                 `json:"capture_token"`
	Limits          service.SourceContextLimitUsage        `json:"limits"`
}

// sourceContextAuthorState 描述快照里一位评论作者（member 或 agent）现在的状态：
// CapturedName 是捕获时的名字，CurrentName 是现在库里查到的名字（改名时才有值），
// State 告诉 UI 该展示「未变 / 已改名 / 已归档 / 已删除 / 已离开 workspace」中哪种。
type sourceContextAuthorState struct {
	Type         string  `json:"type"`
	ID           string  `json:"id"`
	CapturedName string  `json:"captured_name"`
	CurrentName  *string `json:"current_name,omitempty"`
	State        string  `json:"state"`
}

// sourceContextCurrentSource 描述「源议题现在的位置」：IssueID 是锚点评论当前所在
// 的议题（若被搬运过则指向新议题），AnchorCommentID 是锚点评论，Identifier 是当前
// 议题的编号。
type sourceContextCurrentSource struct {
	IssueID         string `json:"issue_id"`
	AnchorCommentID string `json:"anchor_comment_id"`
	Identifier      string `json:"identifier"`
}

// sourceContextDetailResponse 是详情接口的响应结构，回答三个问题：捕获的内容是什么
// （Snapshot）、相对捕获那一刻源现在变了什么（ChangeReasons / ChangeDetails）、还能
// 不能打开当前源（CurrentSource / CanOpenCurrentSource）。DisplayState 是把各子状态
// 聚合后的展示结论（unchanged / changed / deleted / unavailable）。
type sourceContextDetailResponse struct {
	ID                   string                        `json:"id"`
	Version              int16                         `json:"version"`
	Usage                string                        `json:"usage"`
	CapturedAt           string                        `json:"captured_at"`
	DisplayState         string                        `json:"display_state"`
	SourceIssueState     string                        `json:"source_issue_state"`
	CommentThreadState   string                        `json:"comment_thread_state"`
	AnchorCommentState   string                        `json:"anchor_comment_state"`
	CanOpenCurrentSource bool                          `json:"can_open_current_source"`
	ChangeReasons        []string                      `json:"change_reasons"`
	ChangeDetails        sourceContextChangeDetails    `json:"change_details"`
	Snapshot             service.SourceContextSnapshot `json:"snapshot"`
	CurrentSource        *sourceContextCurrentSource   `json:"current_source,omitempty"`
	SourceAuthorState    []sourceContextAuthorState    `json:"source_author_state"`
}

// sourceContextChangeDetails 具体列出变化明细：ChangedCommentIDs 是内容/结构变过的
// 评论；AddedComments / RemovedCommentIDs 是增删的评论；DescriptionAttachmentChanges
// 是描述附件的变化（增/删/改名）。
type sourceContextChangeDetails struct {
	ChangedCommentIDs            []string                                   `json:"changed_comment_ids"`
	AddedComments                []service.SourceContextCommentSnapshot     `json:"added_comments,omitempty"`
	RemovedCommentIDs            []string                                   `json:"removed_comment_ids,omitempty"`
	DescriptionAttachmentChanges []sourceContextDescriptionAttachmentChange `json:"description_attachment_changes"`
}

// sourceContextDescriptionAttachmentChange 描述一条描述附件的变更：Kind 区分
// added / removed / renamed，AttachmentID 是附件 ID，Filename 是当前文件名，
// PreviousFilename 仅改名时有值。
type sourceContextDescriptionAttachmentChange struct {
	Kind             string `json:"kind"`
	AttachmentID     string `json:"attachment_id"`
	Filename         string `json:"filename"`
	PreviousFilename string `json:"previous_filename,omitempty"`
}

// 详情接口暴露给前端的「变化原因」常量，分别表示：议题标题变了、议题描述变了、
// 描述附件变了、评论线程变了。前端据此决定展示哪类提示文案。
const (
	sourceContextChangeIssueTitle                  = "issue_title"
	sourceContextChangeIssueDescription            = "issue_description"
	sourceContextChangeIssueDescriptionAttachments = "issue_description_attachments"
	sourceContextChangeCommentThread               = "comment_thread"
)

// sourceContextComparableAttachment 是附件参与 diff 时的可比投影（去掉与内容无关的
// 字段）。ID 优先取 SourceAttachmentID——它是跨来源稳定的附件标识。
type sourceContextComparableAttachment struct {
	ID          string
	Filename    string
	ContentType string
	SizeBytes   int64
}

// sourceContextComparableThreadNode 是评论线程节点的可比投影：只看 ID、父评论与
// 类型，用于判断线程「结构」是否变了。
type sourceContextComparableThreadNode struct {
	ID       string
	ParentID *string
	Type     string
}

// comparableSourceContextAttachments 把附件列表投影成可比结构，只保留判断内容是否
// 变化真正需要的字段。
func comparableSourceContextAttachments(items []service.SourceContextAttachment) []sourceContextComparableAttachment {
	result := make([]sourceContextComparableAttachment, 0, len(items))
	for _, item := range items {
		id := item.ID
		if item.SourceAttachmentID != "" {
			id = item.SourceAttachmentID
		}
		result = append(result, sourceContextComparableAttachment{
			ID: id, Filename: item.Filename, ContentType: item.ContentType, SizeBytes: item.SizeBytes,
		})
	}
	return result
}

// jsonEqual 通过比较两个值序列化后的 JSON 字节串来判断是否相等，用于附件等
// 结构体的等价性比较。
func jsonEqual(a, b any) bool {
	left, leftErr := json.Marshal(a)
	right, rightErr := json.Marshal(b)
	return leftErr == nil && rightErr == nil && string(left) == string(right)
}

// sourceContextIssueChanges 汇总「源议题侧」的变化：Reasons 是变化原因列表，
// DescriptionAttachmentChanges 是描述附件的变化明细。
type sourceContextIssueChanges struct {
	Reasons                      []string
	DescriptionAttachmentChanges []sourceContextDescriptionAttachmentChange
}

// sourceContextIssueChangeDetails 比较捕获时的议题与现在的议题，得出变化原因。
// 描述正文用 sourceContextDescriptionProjection 把附件引用渲染成占位文本后再比较，
// 避免附件内容变化这类无关差异造成误报。
func sourceContextIssueChangeDetails(captured, current service.SourceContextIssueSnapshot) sourceContextIssueChanges {
	reasons := make([]string, 0, 3)
	if captured.Title != current.Title {
		reasons = append(reasons, sourceContextChangeIssueTitle)
	}
	capturedBody, capturedReferences := sourceContextDescriptionProjection(captured.Description, captured.Attachments, current.Attachments)
	currentBody, currentReferences := sourceContextDescriptionProjection(current.Description, captured.Attachments, current.Attachments)
	if capturedBody != currentBody {
		reasons = append(reasons, sourceContextChangeIssueDescription)
	}
	attachmentChanges := sourceContextDescriptionAttachmentChanges(capturedReferences, currentReferences)
	if len(attachmentChanges) > 0 {
		reasons = append(reasons, sourceContextChangeIssueDescriptionAttachments)
	}
	return sourceContextIssueChanges{Reasons: reasons, DescriptionAttachmentChanges: attachmentChanges}
}

// sourceContextIssueChangeReasons 是 sourceContextIssueChangeDetails 的精简封装：
// 只返回变化原因列表。
func sourceContextIssueChangeReasons(captured, current service.SourceContextIssueSnapshot) []string {
	return sourceContextIssueChangeDetails(captured, current).Reasons
}

// sourceContextThreadChanges 汇总「评论线程侧」的变化：Reasons 是变化原因，
// ChangedCommentIDs / AddedComments / RemovedCommentIDs 是被改/增/删的评论明细。
type sourceContextThreadChanges struct {
	Reasons           []string
	ChangedCommentIDs []string
	AddedComments     []service.SourceContextCommentSnapshot
	RemovedCommentIDs []string
}

// sourceContextThreadChangeDetails 比较捕获时的评论线程与现在的评论线程：线程结构
// 或锚点是否变化、每条评论的内容（同样经投影比较）、附件、类型、父子关系是否变化，
// 都会计入结果，并给出被改/增/删的具体评论。
func sourceContextThreadChangeDetails(captured, current service.SourceContextSnapshot) sourceContextThreadChanges {
	capturedThread := make([]sourceContextComparableThreadNode, 0, len(captured.CommentThread))
	currentThread := make([]sourceContextComparableThreadNode, 0, len(current.CommentThread))
	capturedComments := make(map[string]service.SourceContextCommentSnapshot, len(captured.CommentThread))
	currentComments := make(map[string]service.SourceContextCommentSnapshot, len(current.CommentThread))
	for _, comment := range captured.CommentThread {
		capturedThread = append(capturedThread, sourceContextComparableThreadNode{ID: comment.ID, ParentID: comment.ParentID, Type: comment.Type})
		capturedComments[comment.ID] = comment
	}
	for _, comment := range current.CommentThread {
		currentThread = append(currentThread, sourceContextComparableThreadNode{ID: comment.ID, ParentID: comment.ParentID, Type: comment.Type})
		currentComments[comment.ID] = comment
	}
	threadChanged := captured.AnchorCommentID != current.AnchorCommentID || !jsonEqual(capturedThread, currentThread)
	changedCommentIDs := make([]string, 0)
	for _, capturedComment := range captured.CommentThread {
		currentComment, exists := currentComments[capturedComment.ID]
		if !exists {
			continue
		}
		contentChanged := sourceContextMarkdownContentProjection(
			capturedComment.Content,
			capturedComment.Attachments,
			currentComment.Attachments,
		) != sourceContextMarkdownContentProjection(
			currentComment.Content,
			capturedComment.Attachments,
			currentComment.Attachments,
		)
		attachmentsChanged := !jsonEqual(
			comparableSourceContextAttachments(capturedComment.Attachments),
			comparableSourceContextAttachments(currentComment.Attachments),
		)
		structureChanged := capturedComment.Type != currentComment.Type || !jsonEqual(capturedComment.ParentID, currentComment.ParentID)
		if contentChanged || attachmentsChanged || structureChanged {
			changedCommentIDs = append(changedCommentIDs, capturedComment.ID)
			threadChanged = true
		}
	}
	reasons := make([]string, 0, 1)
	if threadChanged {
		reasons = append(reasons, sourceContextChangeCommentThread)
	}
	addedComments := make([]service.SourceContextCommentSnapshot, 0)
	for _, currentComment := range current.CommentThread {
		if _, exists := capturedComments[currentComment.ID]; !exists {
			addedComments = append(addedComments, currentComment)
		}
	}
	removedCommentIDs := make([]string, 0)
	for _, capturedComment := range captured.CommentThread {
		if _, exists := currentComments[capturedComment.ID]; !exists {
			removedCommentIDs = append(removedCommentIDs, capturedComment.ID)
		}
	}
	return sourceContextThreadChanges{
		Reasons: reasons, ChangedCommentIDs: changedCommentIDs,
		AddedComments: addedComments, RemovedCommentIDs: removedCommentIDs,
	}
}

// sourceContextThreadChangeReasons 是 sourceContextThreadChangeDetails 的精简封装：
// 只返回变化原因列表。
func sourceContextThreadChangeReasons(captured, current service.SourceContextSnapshot) []string {
	return sourceContextThreadChangeDetails(captured, current).Reasons
}

// issueSourceContextDetail 组装某议题来源上下文的详情响应：读出捕获行与快照，尝试
// 定位锚点评论（决定能否打开当前源），再重建「现在的」议题与评论线程，与快照对比
// 得出各项子状态与变化原因。任何一步重建失败都会降级成对应的 deleted / unavailable
// 状态，而不是让整个详情接口报错。
func (h *Handler) issueSourceContextDetail(ctx context.Context, issue db.Issue) (*sourceContextDetailResponse, error) {
	row, err := h.Queries.GetIssueSourceContextByIssue(ctx, db.GetIssueSourceContextByIssueParams{WorkspaceID: issue.WorkspaceID, IssueID: issue.ID})
	if err != nil {
		return nil, err
	}
	var captured service.SourceContextSnapshot
	if err := json.Unmarshal(row.Snapshot, &captured); err != nil {
		return nil, fmt.Errorf("decode source context snapshot: %w", err)
	}
	response := &sourceContextDetailResponse{
		ID: uuidToString(row.ID), Version: row.SnapshotVersion, Usage: service.SourceContextUsage,
		CapturedAt: timestampToString(row.CapturedAt), DisplayState: "unchanged",
		SourceIssueState: "unchanged", CommentThreadState: "unchanged", AnchorCommentState: "unavailable", ChangeReasons: []string{},
		ChangeDetails: sourceContextChangeDetails{ChangedCommentIDs: []string{}, DescriptionAttachmentChanges: []sourceContextDescriptionAttachmentChange{}},
		Snapshot:      captured, SourceAuthorState: h.resolveSourceContextAuthors(ctx, issue.WorkspaceID, captured.CommentThread),
	}

	anchor, anchorErr := h.Queries.GetCommentInWorkspace(ctx, db.GetCommentInWorkspaceParams{
		ID: row.AnchorCommentID, WorkspaceID: issue.WorkspaceID,
	})
	anchorExists := anchorErr == nil && anchor.Type == "comment"
	if anchorExists {
		response.AnchorCommentState = "available"
		response.CurrentSource = &sourceContextCurrentSource{
			IssueID: uuidToString(anchor.IssueID), AnchorCommentID: uuidToString(row.AnchorCommentID), Identifier: captured.SourceIssue.Identifier,
		}
		response.CanOpenCurrentSource = true
	} else if errors.Is(anchorErr, pgx.ErrNoRows) || anchorErr == nil {
		response.AnchorCommentState = "deleted"
	}

	var currentIssue service.SourceContextIssueSnapshot
	var currentIssueErr error
	if anchorExists {
		currentIssue, _, currentIssueErr = service.BuildSourceIssueSnapshot(ctx, h.Queries, issue.WorkspaceID, anchor.IssueID)
	} else {
		currentIssue, _, currentIssueErr = service.BuildSourceIssueSnapshot(ctx, h.Queries, issue.WorkspaceID, row.SourceIssueID)
	}
	switch {
	case currentIssueErr == nil:
		issueChanges := sourceContextIssueChangeDetails(captured.SourceIssue, currentIssue)
		response.ChangeDetails.DescriptionAttachmentChanges = issueChanges.DescriptionAttachmentChanges
		response.ChangeReasons = append(response.ChangeReasons, issueChanges.Reasons...)
		if len(response.ChangeReasons) > 0 {
			response.SourceIssueState = "changed"
		}
		if anchorExists {
			response.CurrentSource.Identifier = currentIssue.Identifier
		}
	case errors.Is(currentIssueErr, service.ErrSourceIssueDeleted):
		response.SourceIssueState = "deleted"
		response.CanOpenCurrentSource = false
	default:
		response.SourceIssueState = "unavailable"
	}

	current, buildErr := service.BuildSourceContext(ctx, h.Queries, issue.WorkspaceID, row.AnchorCommentID)
	switch {
	case buildErr == nil || (errors.Is(buildErr, service.ErrSourceContextTooLarge) && current.Snapshot.AnchorCommentID != ""):
		threadChanges := sourceContextThreadChangeDetails(captured, current.Snapshot)
		response.ChangeDetails.ChangedCommentIDs = threadChanges.ChangedCommentIDs
		response.ChangeDetails.AddedComments = threadChanges.AddedComments
		response.ChangeDetails.RemovedCommentIDs = threadChanges.RemovedCommentIDs
		response.ChangeReasons = append(response.ChangeReasons, threadChanges.Reasons...)
		if len(threadChanges.Reasons) > 0 {
			response.CommentThreadState = "changed"
		}
	case errors.Is(buildErr, service.ErrAnchorCommentDeleted):
		response.AnchorCommentState = "deleted"
		response.CommentThreadState = "changed"
		response.CanOpenCurrentSource = false
		response.CurrentSource = nil
		currentThread, threadErr := service.BuildSourceContextThreadAtCaptureBoundary(ctx, h.Queries, issue.WorkspaceID, captured)
		if threadErr == nil {
			currentSnapshot := captured
			currentSnapshot.CommentThread = currentThread
			threadChanges := sourceContextThreadChangeDetails(captured, currentSnapshot)
			response.ChangeDetails.ChangedCommentIDs = threadChanges.ChangedCommentIDs
			response.ChangeDetails.AddedComments = threadChanges.AddedComments
			response.ChangeDetails.RemovedCommentIDs = threadChanges.RemovedCommentIDs
			response.ChangeReasons = append(response.ChangeReasons, threadChanges.Reasons...)
		} else {
			response.ChangeReasons = append(response.ChangeReasons, sourceContextChangeCommentThread)
			response.ChangeDetails.RemovedCommentIDs = []string{captured.AnchorCommentID}
		}
	case errors.Is(buildErr, service.ErrSourceIssueDeleted):
		response.SourceIssueState = "deleted"
		response.CommentThreadState = "unavailable"
		response.CanOpenCurrentSource = false
		response.CurrentSource = nil
	case errors.Is(buildErr, service.ErrSourceContextInvalid) && anchorExists:
		response.CommentThreadState = "changed"
		response.ChangeReasons = append(response.ChangeReasons, sourceContextChangeCommentThread)
	default:
		response.CommentThreadState = "unavailable"
	}
	if response.SourceIssueState == "deleted" {
		response.DisplayState = "deleted"
	} else if response.SourceIssueState == "unavailable" || response.CommentThreadState == "unavailable" {
		response.DisplayState = "unavailable"
	} else if response.SourceIssueState != "unchanged" || response.CommentThreadState != "unchanged" {
		response.DisplayState = "changed"
	}
	return response, nil
}

// resolveSourceContextAuthors 逐个解析快照里评论作者（member / agent）的现状，供 UI
// 提示「此人改名了 / 被删了 / 已离开 workspace / 已归档」等。按 (type, id) 去重。
func (h *Handler) resolveSourceContextAuthors(ctx context.Context, workspaceID pgtype.UUID, comments []service.SourceContextCommentSnapshot) []sourceContextAuthorState {
	seen := make(map[string]struct{})
	states := make([]sourceContextAuthorState, 0)
	for _, comment := range comments {
		key := comment.Author.Type + ":" + comment.Author.ID
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		state := sourceContextAuthorState{Type: comment.Author.Type, ID: comment.Author.ID, CapturedName: comment.Author.Name, State: "unavailable"}
		authorID, parseErr := util.ParseUUID(comment.Author.ID)
		if parseErr != nil {
			states = append(states, state)
			continue
		}
		switch comment.Author.Type {
		case "member":
			if _, err := h.Queries.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{UserID: authorID, WorkspaceID: workspaceID}); errors.Is(err, pgx.ErrNoRows) {
				state.State = "no_longer_in_workspace"
			} else if err == nil {
				user, userErr := h.Queries.GetUser(ctx, authorID)
				if userErr != nil {
					states = append(states, state)
					continue
				}
				state.CurrentName = &user.Name
				if user.Name == comment.Author.Name {
					state.State = "unchanged"
				} else {
					state.State = "renamed"
				}
			}
		case "agent":
			agent, err := h.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{ID: authorID, WorkspaceID: workspaceID})
			if errors.Is(err, pgx.ErrNoRows) {
				state.State = "deleted_agent"
			} else if err == nil {
				state.CurrentName = &agent.Name
				switch {
				case agent.ArchivedAt.Valid:
					state.State = "archived"
				case agent.Name != comment.Author.Name:
					state.State = "renamed"
				default:
					state.State = "unchanged"
				}
			}
		}
		states = append(states, state)
	}
	return states
}

// PreviewCommentSubIssue 处理「预览」：把锚点评论所在的源上下文（源议题 + 评论串 +
// 用量限制）构建出来返回给前端，让用户先确认「从这条评论创建会带走什么」。只读不落库。
func (h *Handler) PreviewCommentSubIssue(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	if _, ok := h.workspaceMember(w, r, workspaceID); !ok {
		return
	}
	anchorCommentID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "commentId"), "comment_id")
	if !ok {
		return
	}
	build, err := service.BuildSourceContext(r.Context(), h.Queries, wsUUID, anchorCommentID)
	if err != nil {
		h.writeSourceContextError(w, err, build.Limits)
		return
	}
	writeJSON(w, http.StatusOK, sourceContextPreviewResponse{
		SourceIssue: build.Snapshot.SourceIssue, CommentThread: build.Snapshot.CommentThread,
		AnchorCommentID: build.Snapshot.AnchorCommentID, CaptureToken: build.Token, Limits: build.Limits,
	})
}

// createCommentSubIssueRequest 是「从评论创建」请求体：Mode 取 manual（用户填表单
// 建子事项）或 agent（快速派发给 agent）；CaptureToken 是预览时下发的防过期令牌；
// Issue / QuickCreate 二选一携带具体内容。
type createCommentSubIssueRequest struct {
	Mode         string                   `json:"mode"`
	CaptureToken string                   `json:"capture_token"`
	Issue        *CreateIssueRequest      `json:"issue,omitempty"`
	QuickCreate  *QuickCreateIssueRequest `json:"quick_create,omitempty"`
}

// CreateCommentSubIssue 处理「从评论创建」：重新构建源上下文并核对 capture token
// 与当前内容一致（防预览过期或被改），再按 mode 分别走 manual（建子议题）或 agent
// （派发带上下文的 quick-create 任务）。创建前先做一次不占配额的容量预检。
func (h *Handler) CreateCommentSubIssue(w http.ResponseWriter, r *http.Request) {
	var req createCommentSubIssueRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	workspaceID := h.resolveWorkspaceID(r)
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	if _, ok := h.workspaceMember(w, r, workspaceID); !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	userUUID, ok := parseUUIDOrBadRequest(w, userID, "user_id")
	if !ok {
		return
	}
	anchorCommentID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "commentId"), "comment_id")
	if !ok {
		return
	}
	wantDigest, tokenIssueID, validToken := service.ParseSourceContextToken(strings.TrimSpace(req.CaptureToken))
	if !validToken {
		writeJSON(w, http.StatusConflict, map[string]any{"code": "source_context_changed", "error": "source context preview is invalid; refresh and try again"})
		return
	}
	build, err := service.BuildSourceContext(r.Context(), h.Queries, wsUUID, anchorCommentID)
	if errors.Is(err, service.ErrAnchorCommentDeleted) {
		if _, issueErr := h.Queries.GetIssueInWorkspace(r.Context(), db.GetIssueInWorkspaceParams{ID: tokenIssueID, WorkspaceID: wsUUID}); errors.Is(issueErr, pgx.ErrNoRows) {
			err = service.ErrSourceIssueDeleted
		}
	}
	if err != nil {
		h.writeSourceContextError(w, err, build.Limits)
		return
	}
	if build.Snapshot.SourceIssue.ID != util.UUIDToString(tokenIssueID) || build.Digest != wantDigest {
		h.writeSourceContextError(w, service.ErrSourceContextChanged, build.Limits)
		return
	}
	var preparedAgent *preparedAgentCommentSubIssue
	switch req.Mode {
	case "manual":
		if req.Issue == nil {
			writeError(w, http.StatusBadRequest, "issue is required for manual mode")
			return
		}
		if strings.TrimSpace(req.Issue.Title) == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"code": "invalid_request", "error": "title is required"})
			return
		}
	case "agent":
		if req.QuickCreate == nil {
			writeError(w, http.StatusBadRequest, "quick_create is required for agent mode")
			return
		}
		if err := service.ValidateSourceContextAgentInput(build, userUUID, strings.TrimSpace(req.QuickCreate.Prompt)); err != nil {
			h.writeSourceContextError(w, err, build.Limits)
			return
		}
		preparedAgent, err = h.prepareAgentCommentSubIssue(w, r, wsUUID, *req.QuickCreate)
		if err != nil {
			if !errors.Is(err, errSourceContextResponseWritten) {
				h.writeSourceContextError(w, err, service.SourceContextLimitUsage{})
			}
			return
		}
	default:
		writeError(w, http.StatusBadRequest, "mode must be manual or agent")
		return
	}

	contextID := dbid.NewV7()
	capture, objectKeys, err := h.cloneSourceContext(r.Context(), wsUUID, userUUID, contextID, build)
	if err != nil {
		slog.Warn("source context capture failed", append(sourceContextAuditAttrs(r, contextID, build.Snapshot.SourceIssue.ID, build.Snapshot.AnchorCommentID, userUUID, req.Mode, build.Limits, "failed"), "error", err)...)
		writeJSON(w, http.StatusBadGateway, map[string]any{"code": "source_context_attachment_copy_failed", "error": "failed to copy source attachments"})
		return
	}
	cleanup := func() {
		for _, key := range objectKeys {
			h.Storage.Delete(context.Background(), key)
		}
	}
	cleanupIfUnpersisted := func() {
		_, lookupErr := h.Queries.GetIssueSourceContextByID(r.Context(), db.GetIssueSourceContextByIDParams{
			WorkspaceID: wsUUID, ID: capture.ID,
		})
		switch {
		case errors.Is(lookupErr, pgx.ErrNoRows):
			cleanup()
		case lookupErr != nil:
			// 提交失败的原因可能含糊不清：归属也拿不准时，绝不删除任何对象——持久
			// 意图协调器删除对象前也会做同样的引用核对，让补偿留到那边处理更安全。
			slog.Warn("source context compensation deferred after ownership lookup failed", append(sourceContextAuditAttrs(r, capture.ID, capture.Snapshot.SourceIssue.ID, capture.Snapshot.AnchorCommentID, userUUID, req.Mode, build.Limits, "deferred_cleanup"), "error", lookupErr)...)
		}
	}

	switch req.Mode {
	case "manual":
		if err := h.createManualCommentSubIssue(w, r, wsUUID, userUUID, *req.Issue, capture, build.Limits); err != nil {
			cleanupIfUnpersisted()
			slog.Warn("source context capture failed", append(sourceContextAuditAttrs(r, capture.ID, capture.Snapshot.SourceIssue.ID, capture.Snapshot.AnchorCommentID, userUUID, req.Mode, build.Limits, "failed"), "error", err)...)
			if !errors.Is(err, errSourceContextResponseWritten) {
				h.writeSourceContextError(w, err, service.SourceContextLimitUsage{})
			}
		}
	case "agent":
		if err := h.createAgentCommentSubIssue(w, r, wsUUID, userUUID, *preparedAgent, capture, build.Limits); err != nil {
			cleanupIfUnpersisted()
			slog.Warn("source context capture failed", append(sourceContextAuditAttrs(r, capture.ID, capture.Snapshot.SourceIssue.ID, capture.Snapshot.AnchorCommentID, userUUID, req.Mode, build.Limits, "failed"), "error", err)...)
			if !errors.Is(err, errSourceContextResponseWritten) {
				h.writeSourceContextError(w, err, service.SourceContextLimitUsage{})
			}
		}
	}
}

// sourceContextAuditAttrs 组装 source context 的审计字段（追加在 http 请求属性
// 之后），供各成功/失败路径统一打日志，含来源上下文 ID、源议题、捕获者、模式、
// 用量与结果。
func sourceContextAuditAttrs(r *http.Request, contextID pgtype.UUID, sourceIssueID, anchorCommentID string, capturedBy pgtype.UUID, mode string, limits service.SourceContextLimitUsage, result string) []any {
	return append(logger.RequestAttrs(r),
		"source_context_id", util.UUIDToString(contextID),
		"source_issue_id", sourceIssueID,
		"anchor_comment_id", anchorCommentID,
		"captured_by_user_id", util.UUIDToString(capturedBy),
		"mode", mode,
		"comment_count", limits.CommentCount,
		"text_bytes", limits.TextBytes,
		"attachment_count", limits.AttachmentCount,
		"attachment_bytes", limits.AttachmentBytes,
		"result", result,
	)
}

// cloneSourceContext 把来源附件克隆到本 workspace 的 source-context 目录，返回可以
// 入库的 capture 与本次克隆出的对象 key 列表。克隆前先为每个新 key 登记对象意图
// （RecordSourceContextObjectIntent），确保随后 DB 归属不确定时，回收任务能核对引用
// 而不误删。没有附件时直接返回不含克隆的 capture。
func (h *Handler) cloneSourceContext(ctx context.Context, workspaceID, userID, contextID pgtype.UUID, build service.SourceContextBuild) (service.SourceContextCapture, []string, error) {
	if len(build.Rows) == 0 {
		capture, err := service.PrepareSourceContextCapture(build, contextID, workspaceID, userID, time.Now(), nil)
		return capture, nil, err
	}
	uploader, ok := h.Storage.(sourceContextStreamUploader)
	if h.Storage == nil || !ok {
		return service.SourceContextCapture{}, nil, errors.New("storage does not support streaming upload")
	}
	clones := make([]service.SourceContextClone, 0, len(build.Rows))
	keys := make([]string, 0, len(build.Rows))
	cleanup := func() {
		for _, key := range keys {
			h.Storage.Delete(context.Background(), key)
		}
	}
	for _, source := range build.Rows {
		sourceKey := h.Storage.KeyFromURL(source.Url)
		reader, err := h.Storage.GetReader(ctx, sourceKey)
		if err != nil {
			cleanup()
			return service.SourceContextCapture{}, nil, err
		}
		cloneID := dbid.NewV7()
		cloneKey := fmt.Sprintf("workspaces/%s/source-context/%s%s", util.UUIDToString(workspaceID), util.UUIDToString(cloneID), path.Ext(source.Filename))
		if _, err := h.Queries.RecordSourceContextObjectIntent(ctx, db.RecordSourceContextObjectIntentParams{
			StorageKey: cloneKey, WorkspaceID: workspaceID, SourceContextID: contextID,
			AttachmentID: cloneID, ObjectUrl: h.Storage.ObjectURL(cloneKey),
		}); err != nil {
			_ = reader.Close()
			cleanup()
			return service.SourceContextCapture{}, nil, fmt.Errorf("record source context object intent: %w", err)
		}
		url, uploadErr := uploader.UploadStream(ctx, cloneKey, reader, source.SizeBytes, source.ContentType, source.Filename)
		closeErr := reader.Close()
		if uploadErr != nil || closeErr != nil {
			cleanup()
			if uploadErr != nil {
				return service.SourceContextCapture{}, nil, uploadErr
			}
			return service.SourceContextCapture{}, nil, closeErr
		}
		keys = append(keys, cloneKey)
		clones = append(clones, service.SourceContextClone{ID: cloneID, Filename: source.Filename, URL: url, ContentType: source.ContentType, SizeBytes: source.SizeBytes})
	}
	capture, err := service.PrepareSourceContextCapture(build, contextID, workspaceID, userID, time.Now(), clones)
	if err != nil {
		cleanup()
		return service.SourceContextCapture{}, nil, err
	}
	return capture, keys, nil
}

// createManualCommentSubIssue 处理 manual 模式：把表单字段清洗、补默认值并逐项校验
// 后，作为源议题的子事项（ParentIssueID 取捕获的源议题）创建，capture 作为
// SourceContext 一并写库。参数错误在此直接回写响应，并返回 errSourceContextResponseWritten
// 让上层不再重复处理。
func (h *Handler) createManualCommentSubIssue(w http.ResponseWriter, r *http.Request, workspaceID, userID pgtype.UUID, input CreateIssueRequest, capture service.SourceContextCapture, limits service.SourceContextLimitUsage) error {
	title := strings.TrimSpace(input.Title)
	if title == "" {
		return sourceContextBadRequest("title is required")
	}
	status := strings.TrimSpace(input.Status)
	if status == "" {
		status = "todo"
	}
	var ok bool
	status, ok = h.resolveIssueStatusKey(w, r, workspaceID, status)
	if !ok {
		return errSourceContextResponseWritten
	}
	priority := strings.TrimSpace(input.Priority)
	if priority == "" {
		priority = "not_important_not_urgent"
	}
	if !validateIssueEnum(w, "priority", priority, validIssuePriorities) {
		return errSourceContextResponseWritten
	}
	var assigneeType pgtype.Text
	var assigneeID pgtype.UUID
	if input.AssigneeType != nil && strings.TrimSpace(*input.AssigneeType) != "" {
		assigneeType = pgtype.Text{String: strings.TrimSpace(*input.AssigneeType), Valid: true}
	}
	if input.AssigneeID != nil && strings.TrimSpace(*input.AssigneeID) != "" {
		parsed, err := util.ParseUUID(strings.TrimSpace(*input.AssigneeID))
		if err != nil {
			return sourceContextBadRequest("invalid assignee_id")
		}
		assigneeID = parsed
	}
	if code, message := h.validateAssigneePair(r.Context(), r, util.UUIDToString(workspaceID), assigneeType, assigneeID, scopeNoDelegation()); code != 0 {
		writeError(w, code, message)
		return errSourceContextResponseWritten
	}
	var projectID pgtype.UUID
	if input.ProjectID != nil && strings.TrimSpace(*input.ProjectID) != "" {
		parsed, err := util.ParseUUID(strings.TrimSpace(*input.ProjectID))
		if err != nil {
			return sourceContextBadRequest("invalid project_id")
		}
		projectID = parsed
	}
	attachmentIDs, ok := parseUUIDSliceOrBadRequest(w, input.AttachmentIDs, "attachment_ids")
	if !ok {
		return errSourceContextResponseWritten
	}
	labelIDs, ok := parseUUIDSliceOrBadRequest(w, input.LabelIDs, "label_ids")
	if !ok {
		return errSourceContextResponseWritten
	}
	var startDate, dueDate pgtype.Date
	if input.StartDate != nil && *input.StartDate != "" {
		parsed, err := util.ParseCalendarDate(*input.StartDate)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid start_date format, expected YYYY-MM-DD")
			return errSourceContextResponseWritten
		}
		startDate = parsed
	}
	if input.DueDate != nil && *input.DueDate != "" {
		parsed, err := util.ParseCalendarDate(*input.DueDate)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid due_date format, expected YYYY-MM-DD")
			return errSourceContextResponseWritten
		}
		dueDate = parsed
	}
	var stage pgtype.Int4
	if input.Stage != nil {
		if *input.Stage < 1 {
			writeError(w, http.StatusBadRequest, "stage must be at least 1")
			return errSourceContextResponseWritten
		}
		stage = pgtype.Int4{Int32: *input.Stage, Valid: true}
	}
	prefix := h.getIssuePrefix(r.Context(), workspaceID)
	result, err := h.IssueService.Create(r.Context(), service.IssueCreateParams{
		WorkspaceID: workspaceID, Title: title, Description: ptrToText(input.Description), Status: status, Priority: priority,
		AssigneeType: assigneeType, AssigneeID: assigneeID, CreatorType: "member", CreatorID: userID,
		ParentIssueID: capture.SourceIssueID, ProjectID: projectID, StartDate: startDate, DueDate: dueDate,
		AttachmentIDs: attachmentIDs, LabelIDs: labelIDs, Stage: stage,
		AllowDuplicate: input.AllowDuplicate, SourceContext: &capture,
	}, service.IssueCreateOpts{
		ActorID: util.UUIDToString(userID),
		BroadcastPayload: func(issue db.Issue, _ []db.Attachment, labels []db.IssueLabel) map[string]any {
			response := issueToResponse(issue, prefix)
			labelResponses := labelsToResponse(labels)
			response.Labels = &labelResponses
			return map[string]any{"issue": response}
		},
	})
	if err != nil {
		return err
	}
	response := issueToResponse(result.Issue, prefix)
	h.fillStatusCategory(r.Context(), workspaceID, &response)
	labelResponses := labelsToResponse(result.Labels)
	response.Labels = &labelResponses
	slog.Info("sub-issue created from captured comment", append(sourceContextAuditAttrs(r, capture.ID, capture.Snapshot.SourceIssue.ID, capture.Snapshot.AnchorCommentID, userID, "manual", limits, "success"), "target_issue_id", uuidToString(result.Issue.ID))...)
	writeJSON(w, http.StatusCreated, response)
	return nil
}

// preparedAgentCommentSubIssue 是一次 agent 模式派发所需的全部参数（已经过所有校验），
// 由 prepareAgentCommentSubIssue 产出，交给 createAgentCommentSubIssue 真正入队。
type preparedAgentCommentSubIssue struct {
	agentID, squadID, runtimeID pgtype.UUID
	prompt, priority, dueDate   string
	projectID                   pgtype.UUID
	attachmentIDs               []pgtype.UUID
}

// prepareAgentCommentSubIssue 校验 agent 模式的请求（prompt 必填；agent_id 与 squad_id
// 必须且只能给一个；squad 需存在且未归档；priority 需在合法集合内），再核对 agent 就绪、
// daemon 版本支持带上下文的 quick-create，最后返回打包好的派发参数。
func (h *Handler) prepareAgentCommentSubIssue(w http.ResponseWriter, r *http.Request, workspaceID pgtype.UUID, input QuickCreateIssueRequest) (*preparedAgentCommentSubIssue, error) {
	prompt := strings.TrimSpace(input.Prompt)
	if prompt == "" {
		return nil, sourceContextBadRequest("prompt is required")
	}
	hasAgent := strings.TrimSpace(input.AgentID) != ""
	hasSquad := strings.TrimSpace(input.SquadID) != ""
	if hasAgent == hasSquad {
		return nil, sourceContextBadRequest("exactly one of agent_id or squad_id is required")
	}
	priority := strings.ToLower(strings.TrimSpace(input.Priority))
	if priority != "" && priority != "important_urgent" && priority != "important_not_urgent" && priority != "not_important_urgent" && priority != "not_important_not_urgent" {
		return nil, sourceContextBadRequest("invalid priority")
	}
	var agentID, squadID pgtype.UUID
	if hasSquad {
		parsed, err := util.ParseUUID(strings.TrimSpace(input.SquadID))
		if err != nil {
			return nil, sourceContextBadRequest("invalid squad_id")
		}
		squadID = parsed
		squad, err := h.Queries.GetSquadInWorkspace(r.Context(), db.GetSquadInWorkspaceParams{ID: squadID, WorkspaceID: workspaceID})
		if err != nil || squad.ArchivedAt.Valid {
			return nil, sourceContextBadRequest("squad not found or archived")
		}
		agentID = squad.LeaderID
	} else {
		parsed, err := util.ParseUUID(strings.TrimSpace(input.AgentID))
		if err != nil {
			return nil, sourceContextBadRequest("invalid agent_id")
		}
		agentID = parsed
	}
	if status, message := h.validateAssigneePair(r.Context(), r, util.UUIDToString(workspaceID), pgtype.Text{String: "agent", Valid: true}, agentID, scopeNoDelegation()); status != 0 {
		writeError(w, status, message)
		return nil, errSourceContextResponseWritten
	}
	agent, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{ID: agentID, WorkspaceID: workspaceID})
	if err != nil {
		return nil, sourceContextBadRequest("agent not found")
	}
	verdict, err := service.AgentReadiness(r.Context(), h.runtimeLookup(obsmetrics.RuntimeLookupSourceSourceContext), agent)
	if err != nil {
		return nil, err
	}
	if !verdict.Ready() {
		writeAgentUnavailable(w, verdict.Detail, verdict.Reason)
		return nil, errSourceContextResponseWritten
	}
	if status, payload := h.checkQuickCreateDaemonVersion(r.Context(), obsmetrics.RuntimeLookupSourceSourceContext, agent.RuntimeID); status != 0 {
		writeJSON(w, status, payload)
		return nil, errSourceContextResponseWritten
	}
	runtime, err := h.getAgentRuntime(r.Context(), obsmetrics.RuntimeLookupSourceSourceContext, agent.RuntimeID)
	if err != nil || !runtimeHasCapability(runtime.Metadata, protocol.DaemonCapabilitySourceContextQuickCreateV1) {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"code": "source_context_quick_create_unsupported", "error": "selected agent runtime must be updated before using captured context"})
		return nil, errSourceContextResponseWritten
	}
	dueDate := strings.TrimSpace(input.DueDate)
	if dueDate != "" {
		parsed, err := util.ParseCalendarDate(dueDate)
		if err != nil {
			return nil, sourceContextBadRequest("invalid due_date")
		}
		dueDate = parsed.Time.Format("2006-01-02")
	}
	if priority != "" || dueDate != "" {
		if status, payload := h.checkQuickCreateDaemonVersionAtLeast(r.Context(), obsmetrics.RuntimeLookupSourceSourceContext, agent.RuntimeID, agentpkg.MinQuickCreateFieldsCLIVersion); status != 0 {
			writeJSON(w, status, payload)
			return nil, errSourceContextResponseWritten
		}
	}
	attachmentIDs, ok := parseUUIDSliceOrBadRequest(w, input.AttachmentIDs, "attachment_ids")
	if !ok {
		return nil, errSourceContextResponseWritten
	}
	var projectID pgtype.UUID
	if strings.TrimSpace(input.ProjectID) != "" {
		parsed, err := util.ParseUUID(strings.TrimSpace(input.ProjectID))
		if err != nil {
			return nil, sourceContextBadRequest("invalid project_id")
		}
		if _, err := h.Queries.GetProjectInWorkspace(r.Context(), db.GetProjectInWorkspaceParams{ID: parsed, WorkspaceID: workspaceID}); err != nil {
			return nil, sourceContextBadRequest("project not found")
		}
		projectID = parsed
	}
	return &preparedAgentCommentSubIssue{
		agentID: agentID, squadID: squadID, runtimeID: agent.RuntimeID,
		prompt: prompt, priority: priority, dueDate: dueDate,
		projectID: projectID, attachmentIDs: attachmentIDs,
	}, nil
}

// createAgentCommentSubIssue 处理 agent 模式：入队前再次核对 daemon 能力（此时可能
// 已经过漫长的流式拷贝），随后把带捕获上下文的 quick-create 任务交给 TaskService 入队，
// 成功返回 202 + task_id。
func (h *Handler) createAgentCommentSubIssue(w http.ResponseWriter, r *http.Request, workspaceID, userID pgtype.UUID, prepared preparedAgentCommentSubIssue, capture service.SourceContextCapture, limits service.SourceContextLimitUsage) error {
	// 拷贝可能耗时较长，之后再复核一次能力：runtime 可能在拷贝期间重新注册，最终
	// 入队前仍须失败关闭（fail closed）。
	runtime, err := h.getAgentRuntime(r.Context(), obsmetrics.RuntimeLookupSourceSourceContext, prepared.runtimeID)
	if err != nil || !runtimeHasCapability(runtime.Metadata, protocol.DaemonCapabilitySourceContextQuickCreateV1) {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"code": "source_context_quick_create_unsupported", "error": "selected agent runtime must be updated before using captured context"})
		return errSourceContextResponseWritten
	}
	task, err := h.TaskService.EnqueueQuickCreateTaskWithSourceContext(r.Context(), workspaceID, userID, prepared.agentID, prepared.squadID, prepared.prompt, prepared.priority, prepared.dueDate, prepared.projectID, capture.SourceIssueID, prepared.attachmentIDs, capture)
	if err != nil {
		return err
	}
	slog.Info("sub-issue quick-create enqueued from captured comment", append(sourceContextAuditAttrs(r, capture.ID, capture.Snapshot.SourceIssue.ID, capture.Snapshot.AnchorCommentID, userID, "agent", limits, "success"), "task_id", uuidToString(task.ID))...)
	writeJSON(w, http.StatusAccepted, QuickCreateIssueResponse{TaskID: uuidToString(task.ID)})
	return nil
}

// writeSourceContextError 把 service 层返回的错误统一映射成带 code 的 JSON 错误响应。
// 按来源上下文哨兵错误类型选择响应
// 类型选择 400/409/422/500 及对应 code。
func (h *Handler) writeSourceContextError(w http.ResponseWriter, err error, limits service.SourceContextLimitUsage) {
	status := http.StatusInternalServerError
	code := "source_context_capture_failed"
	message := "failed to capture source context"
	switch {
	case errors.Is(err, service.ErrSourceContextChanged):
		status, code = http.StatusConflict, "source_context_changed"
		message = err.Error()
	case errors.Is(err, service.ErrAnchorCommentDeleted):
		status, code = http.StatusConflict, "anchor_comment_deleted"
		message = err.Error()
	case errors.Is(err, service.ErrSourceIssueDeleted):
		status, code = http.StatusConflict, "source_issue_deleted"
		message = err.Error()
	case errors.Is(err, service.ErrSourceContextInvalid):
		status, code = http.StatusConflict, "source_context_invalid_path"
		message = err.Error()
	case errors.Is(err, service.ErrSourceContextTooLarge):
		status, code = http.StatusUnprocessableEntity, "source_context_too_large"
		message = err.Error()
	case errors.Is(err, service.ErrSourceContextAlreadyAttached):
		status, code = http.StatusConflict, "source_context_already_attached"
		message = err.Error()
	case errors.Is(err, service.ErrActiveDuplicate):
		status, code = http.StatusConflict, "active_duplicate_issue"
		message = err.Error()
	case errors.Is(err, service.ErrParentIssueNotFound), errors.Is(err, service.ErrProjectNotFound):
		status = http.StatusBadRequest
		message = err.Error()
	case errors.Is(err, errSourceContextBadRequest):
		status, code = http.StatusBadRequest, "invalid_request"
		message = err.Error()
	case err == nil:
		return
	}
	payload := map[string]any{"code": code, "error": message}
	if errors.Is(err, service.ErrSourceContextTooLarge) {
		payload["limits"] = limits
	}
	writeJSON(w, status, payload)
}
