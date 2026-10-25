// 本文件：Plugin Action API（publicapi v1 契约）——插件 surface 通过宿主桥访问的端点，以及
// hook 的插件自有服务器用 token 发起的调用。设计说明见下。
package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/eventoutbox"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/plugincontract"
	publicapiv1 "github.com/multica-ai/multica/server/pkg/publicapi/v1"
)

// Action API 是插件 surface 透过宿主桥够到的能力面。
//
// 每次调用依次过三道检查：
//
//  1. 安装存在于本工作区且已启用；
//  2. 安装已被授予本端点需要的 scope；
//  3. 「登录中的用户」是否有权碰这个资源——由普通 loader 执行，不含任何插件特判。
//
// 第三道正是整套模型安全的原因：插件永远无法让人做他自己本来就做不到的事。第一、二道把
// 插件压在这个天花板之下。
//
// SURFACE 本身不持有任何凭据。iframe 向宿主页 postMessage，宿主在用户自己的会话里重发这次
// 调用，从 iframe 自己设置不了的 header 里读安装（见 pluginInstallationHeader）。
//
// Hook 追加第二种调用方：插件自己的服务器——它没有会话、将来也不会有。它出示 bearer token，
// 这只改变「这次调用以谁的身份」，上面三道检查两种情形都一样。身份如何判定见 pluginActor。
const pluginInstallationHeader = "X-Multica-Plugin-Installation"

// pluginActor 是一次调用「以谁的身份」，由调用方怎么认证决定，而不是由调用方索要什么决定。
//
//	session，或来自 ui/manual hook 的回调 token
//	    -> 那个人。写入记在他名下，带 via_plugin_id 标记。
//	install token，或来自 event hook 的回调 token
//	    -> 那次安装。写入是插件自己的。
//
// 第二种情形正是 author_type 增加 'plugin' 值的原因：event hook 背后没有人，把它的写入
// 记到「最后碰过这个 issue 的成员」头上，是审计记录也无法挽回的谎言。
type pluginActor struct {
	Type string
	// Member 只在 Type 为 "member" 时存在。插件身份的调用没有成员行可查权限——这正是不需要
	// 成员的那些端点会拒绝它的原因。
	Member db.Member
}

func (a pluginActor) isMember() bool { return a.Type == "member" }

// requireMember 拒绝那些「没有人在背后就没意义」的端点。
func (a pluginActor) requireMember(w http.ResponseWriter, r *http.Request) bool {
	if a.isMember() {
		return true
	}
	publicapiv1.WriteProblem(w, r, http.StatusForbidden, "member_required", "this endpoint requires a user; the presented token acts as the Plugin itself")
	return false
}

func (h *Handler) requirePluginActionV1(w http.ResponseWriter, r *http.Request) bool {
	if h.pluginsV1Enabled(r.Context()) {
		return true
	}
	publicapiv1.WriteProblem(w, r, http.StatusForbidden, "plugin_api_disabled", "Plugin management is not enabled")
	return false
}

// writePluginActionError 把 service 层失败映射进唯一的、稳定的 Public API problem 契约。
// 管理端点刻意保留它们现有的 App API 错误；只有带版本号的 Action/bridge 面用这里。
func writePluginActionError(w http.ResponseWriter, r *http.Request, err error, fallback string) {
	var pluginErr *service.PluginError
	if !errors.As(err, &pluginErr) {
		publicapiv1.WriteProblem(w, r, http.StatusInternalServerError, "internal_error", fallback)
		return
	}
	switch pluginErr.Kind {
	case service.PluginErrorInvalid:
		publicapiv1.WriteProblem(w, r, http.StatusBadRequest, "invalid_request", pluginErr.Message)
	case service.PluginErrorNotFound:
		publicapiv1.WriteProblem(w, r, http.StatusNotFound, "not_found", pluginErr.Message)
	case service.PluginErrorConflict:
		publicapiv1.WriteProblem(w, r, http.StatusConflict, "conflict", pluginErr.Message)
	case service.PluginErrorForbidden:
		publicapiv1.WriteProblem(w, r, http.StatusForbidden, "forbidden", pluginErr.Message)
	case service.PluginErrorIncompatible:
		publicapiv1.WriteProblem(w, r, http.StatusUnprocessableEntity, "incompatible", pluginErr.Message)
	case service.PluginErrorQuota:
		publicapiv1.WriteProblem(w, r, http.StatusInsufficientStorage, "quota_exceeded", pluginErr.Message)
	default:
		publicapiv1.WriteProblem(w, r, http.StatusBadGateway, "upstream_unavailable", pluginErr.Message)
	}
}

// pluginCaller 执行检查 1 和 2，返回已授权的调用方。按认证方式分流：插件自己的 bearer
// token 走 pluginTokenCaller，否则当登录用户走 pluginSessionCaller。
func (h *Handler) pluginCaller(w http.ResponseWriter, r *http.Request, scope string) (service.PluginActionCaller, pluginActor, bool) {
	if !h.requirePluginActionV1(w, r) {
		return service.PluginActionCaller{}, pluginActor{}, false
	}
	if token := middleware.BearerToken(r); middleware.IsPluginBearerToken(token) {
		return h.pluginTokenCaller(w, r, token, scope)
	}
	return h.pluginSessionCaller(w, r, scope)
}

// pluginSessionCaller 是 surface 路径，不变：一个真实登录用户，他自己的权限把所有插件能
// 触到的东西都压住。
func (h *Handler) pluginSessionCaller(w http.ResponseWriter, r *http.Request, scope string) (service.PluginActionCaller, pluginActor, bool) {
	userID := strings.TrimSpace(r.Header.Get("X-User-ID"))
	if userID == "" {
		publicapiv1.WriteProblem(w, r, http.StatusUnauthorized, "unauthorized", "missing authenticated user")
		return service.PluginActionCaller{}, pluginActor{}, false
	}
	parsedUserID, err := util.ParseUUID(userID)
	if err != nil {
		publicapiv1.WriteProblem(w, r, http.StatusBadRequest, "invalid_request", "user_id must be a valid UUID")
		return service.PluginActionCaller{}, pluginActor{}, false
	}

	caller, err := h.PluginService.AuthorizePluginAction(r.Context(), r.Header.Get(pluginInstallationHeader), parsedUserID, scope)
	if err != nil {
		writePluginActionError(w, r, err, "failed to authorize the Plugin call")
		return service.PluginActionCaller{}, pluginActor{}, false
	}

	// 工作区来自安装记录，绝不来自客户端 header：否则调用方可以把一次安装指向它从未被
	// 安装过的工作区。成员身份再对着那个工作区校验。
	member, err := h.getWorkspaceMember(r.Context(), userID, uuidToString(caller.WorkspaceID))
	if err != nil {
		publicapiv1.WriteProblem(w, r, http.StatusNotFound, "not_found", "workspace not found")
		return service.PluginActionCaller{}, pluginActor{}, false
	}
	return caller, pluginActor{Type: "member", Member: member}, true
}

// pluginTokenCaller 是 hook 路径：插件自己的服务器调进来。
//
// 回调 token 限定到一次 INVOCATION、存活几分钟、并带着 hook 发出时定下的 actor——所以
// ui 触发的 hook 以「按下按钮的那个人」的身份回写，且无法改选以别的身份写。install token
// 是常驻访问，永远以插件身份行动。
func (h *Handler) pluginTokenCaller(w http.ResponseWriter, r *http.Request, token, scope string) (service.PluginActionCaller, pluginActor, bool) {
	var installationID pgtype.UUID
	actor := pluginActor{Type: "plugin"}
	var memberUserID pgtype.UUID
	var issueScope pgtype.UUID

	switch {
	case strings.HasPrefix(token, "mpc_"):
		if h.PluginService.Callbacks == nil {
			publicapiv1.WriteProblem(w, r, http.StatusForbidden, "callback_tokens_disabled", "callback tokens are not enabled")
			return service.PluginActionCaller{}, pluginActor{}, false
		}
		grant, err := h.PluginService.Callbacks.Resolve(token)
		if err != nil {
			writePluginActionError(w, r, err, "failed to authorize the Plugin call")
			return service.PluginActionCaller{}, pluginActor{}, false
		}
		installationID = grant.InstallationID
		issueScope = grant.IssueID
		if grant.Actor.Type == "member" {
			memberUserID = grant.Actor.ID
		}
	default:
		installation, err := h.PluginService.AuthenticateInstallToken(r.Context(), token)
		if err != nil {
			writePluginActionError(w, r, err, "failed to authorize the Plugin call")
			return service.PluginActionCaller{}, pluginActor{}, false
		}
		installationID = installation.ID
	}

	caller, err := h.PluginService.AuthorizePluginAction(r.Context(), uuidToString(installationID), memberUserID, scope)
	if err != nil {
		writePluginActionError(w, r, err, "failed to authorize the Plugin call")
		return service.PluginActionCaller{}, pluginActor{}, false
	}

	// 授权时授予方说过这是针对哪个 issue 的。把它带到这里，才把「评论」变成「检查」——
	// 见 pluginIssueForUser。
	caller.IssueScope = issueScope

	// 代表某个人的回调 token，只在他「今天」仍是成员时才算数。在这里复查，意味着吊销某人
	// 权限的效果会作用到一条已经在飞的 token 上。
	if memberUserID.Valid {
		member, err := h.getWorkspaceMember(r.Context(), uuidToString(memberUserID), uuidToString(caller.WorkspaceID))
		if err != nil {
			publicapiv1.WriteProblem(w, r, http.StatusForbidden, "actor_membership_revoked", "the user this callback acts for is no longer a member")
			return service.PluginActionCaller{}, pluginActor{}, false
		}
		actor = pluginActor{Type: "member", Member: member}
	}
	return caller, actor, true
}

// pluginIssueForUser 是第三道检查。工作区来自安装而非客户端 header，且 pluginCaller 已经
// 验过该工作区的成员身份——所以「issue 在这工作区里」+「调用方是成员」正好等于该用户本就
// 有的触达范围。工作区之外的 issue 回 404，与普通端点的理由相同：插件不能靠试探来确认一个
// 它本就读不到的 id 是否存在。
func (h *Handler) pluginIssueForUser(w http.ResponseWriter, r *http.Request, caller service.PluginActionCaller, issueID string) (db.Issue, bool) {
	if issueID == "" {
		publicapiv1.WriteProblem(w, r, http.StatusNotFound, "not_found", "issue not found")
		return db.Issue{}, false
	}
	// 先解析、再比较 id：调用方可能用标识符（PLUG-12）或 uuid 指同一个 issue，直接比原始
	// 字符串会让同一个 issue 用其中一种写法通过、另一种写法失败。
	issue, ok := h.resolvePluginIssue(r, caller, issueID)
	if !ok {
		publicapiv1.WriteProblem(w, r, http.StatusNotFound, "not_found", "issue not found")
		return db.Issue{}, false
	}
	// 针对某个 issue 签发的回调授权只能够到那个 issue。没有这层，这份授权在存活的五分钟里
	// 等于工作区里其 actor 能看到的每一个 issue。
	//
	// 回 404 而非 403：调用方也许能用别的途径看到这个 issue，「你被限定在别处」就等于确认
	// 了这个 id 存在。
	if caller.IssueScope.Valid && uuidToString(issue.ID) != uuidToString(caller.IssueScope) {
		publicapiv1.WriteProblem(w, r, http.StatusNotFound, "not_found", "issue not found")
		return db.Issue{}, false
	}
	return issue, true
}

// resolvePluginIssue 在调用方的工作区里按标识符或 uuid 找 issue。该工作区的成员身份已由
// pluginCaller 确立，所以这正是调用方已有的触达范围。
func (h *Handler) resolvePluginIssue(r *http.Request, caller service.PluginActionCaller, issueID string) (db.Issue, bool) {
	workspaceID := uuidToString(caller.WorkspaceID)
	if issue, ok := h.resolveIssueByIdentifier(r.Context(), issueID, workspaceID); ok {
		return issue, true
	}
	parsed, err := util.ParseUUID(issueID)
	if err != nil {
		return db.Issue{}, false
	}
	issue, err := h.Queries.GetIssueInWorkspace(r.Context(), db.GetIssueInWorkspaceParams{
		ID:          parsed,
		WorkspaceID: caller.WorkspaceID,
	})
	if err != nil {
		return db.Issue{}, false
	}
	return issue, true
}

// GetPluginContext 处理 GET /v1/context——返回插件页当前的上下文（工作区 + 可选用户/issue）。
func (h *Handler) GetPluginContext(w http.ResponseWriter, r *http.Request) {
	// 不需要 scope：这就是用户正在看的那一页。
	caller, actor, ok := h.pluginCaller(w, r, "")
	if !ok {
		return
	}
	workspace, err := h.Queries.GetWorkspace(r.Context(), caller.WorkspaceID)
	if err != nil {
		publicapiv1.WriteProblem(w, r, http.StatusInternalServerError, "internal_error", "failed to load the workspace")
		return
	}
	// 插件身份的调用没有可描述的用户，payload 就明说这一点，而不是交出一个可能被 handler
	// 当成真人的空 user。
	var user *db.User
	if actor.isMember() {
		loaded, err := h.Queries.GetUser(r.Context(), actor.Member.UserID)
		if err != nil {
			publicapiv1.WriteProblem(w, r, http.StatusInternalServerError, "internal_error", "failed to load the user")
			return
		}
		user = &loaded
	}

	var issue *db.Issue
	if issueID := r.URL.Query().Get("issue_id"); issueID != "" {
		loaded, ok := h.pluginIssueForUser(w, r, caller, issueID)
		if !ok {
			return
		}
		issue = &loaded
	}

	writeJSON(w, http.StatusOK, publicPluginContext(h.PluginService.BuildPluginContext(caller, workspace, user, issue)))
}

// publicPluginContext 把内部 PluginContext 映射成 publicapi v1 的 Context DTO。
func publicPluginContext(context service.PluginContext) publicapiv1.Context {
	payload := publicapiv1.Context{
		Workspace: publicapiv1.ContextWorkspace{
			ID:   context.Workspace.ID,
			Name: context.Workspace.Name,
			Slug: context.Workspace.Slug,
		},
		Config:            context.Config,
		GrantedNetDomains: context.GrantedURLs,
		Actor:             context.Actor,
	}
	if context.User != nil {
		payload.User = &publicapiv1.ContextUser{ID: context.User.ID, Name: context.User.Name}
	}
	if context.Issue != nil {
		payload.Issue = &publicapiv1.ContextIssue{
			ID:         context.Issue.ID,
			Identifier: context.Issue.Identifier,
			Title:      context.Issue.Title,
		}
	}
	return payload
}

// GetPluginIssue 处理 GET /v1/issues/{issue_ref}——按标识符/uuid 读一个 issue（需
// issues:read scope；响应带 ETag 与稳定公共 DTO）。
func (h *Handler) GetPluginIssue(w http.ResponseWriter, r *http.Request) {
	caller, _, ok := h.pluginCaller(w, r, plugincontract.ScopeIssuesRead)
	if !ok {
		return
	}
	issue, ok := h.pluginIssueForUser(w, r, caller, chi.URLParam(r, "issue_ref"))
	if !ok {
		return
	}
	setPublicIssueETag(w, issue.Revision)
	writeJSON(w, http.StatusOK, h.pluginIssuePayload(r, caller, issue))
}

// PatchPluginIssue 处理 PATCH /v1/issues/{issue_ref}（需 issues:write scope）。
//
// 只支持标题和描述。状态/优先级/负责人/父级/项目/阶段各自带着派发、目录或层级语义——改状态
// 可能启动一次 Agent 运行，自定义状态要经工作区级目录解析——在这里复刻这些规则，等于给
// 插件一份会各自漂移的第二套拷贝。将来放宽这个集合是纯增量；现在把副作用搞错可没法补救。
func (h *Handler) PatchPluginIssue(w http.ResponseWriter, r *http.Request) {
	caller, _, ok := h.pluginCaller(w, r, plugincontract.ScopeIssuesWrite)
	if !ok {
		return
	}
	issue, ok := h.pluginIssueForUser(w, r, caller, chi.URLParam(r, "issue_ref"))
	if !ok {
		return
	}

	var req publicapiv1.PatchIssueRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		publicapiv1.WriteProblem(w, r, http.StatusBadRequest, "invalid_request", "invalid request body")
		return
	}
	if req.Title == nil && req.Description == nil {
		publicapiv1.WriteProblem(w, r, http.StatusBadRequest, "invalid_request", "title or description is required")
		return
	}
	expectedRevision, ok := publicIssueExpectedRevision(w, r, req.ExpectedRevision)
	if !ok {
		return
	}
	if expectedRevision != nil {
		if issue.Revision != *expectedRevision {
			writePublicIssueRevisionConflict(w, r)
			return
		}
	}

	var title *string
	if req.Title != nil {
		value := sanitizeNullBytes(*req.Title)
		if value == "" {
			publicapiv1.WriteProblem(w, r, http.StatusBadRequest, "invalid_request", "title must not be empty")
			return
		}
		title = &value
	}
	var description *string
	if req.Description != nil {
		value := sanitizeNullBytes(*req.Description)
		description = &value
	}

	updated, err := h.IssueService.UpdateContent(r.Context(), issue, service.IssueContentPatch{
		Title:            title,
		Description:      description,
		ExpectedRevision: expectedRevision,
	})
	if err != nil {
		if errors.Is(err, service.ErrIssueRevisionConflict) {
			writePublicIssueRevisionConflict(w, r)
			return
		}
		publicapiv1.WriteProblem(w, r, http.StatusInternalServerError, "internal_error", "failed to update the issue")
		return
	}
	setPublicIssueETag(w, updated.Revision)
	writeJSON(w, http.StatusOK, h.pluginIssuePayload(r, caller, updated))
}

// setPublicIssueETag 把 issue 版本号写成弱 ETag，供乐观并发控制（If-Match）使用。
func setPublicIssueETag(w http.ResponseWriter, revision int64) {
	w.Header().Set("ETag", fmt.Sprintf(`W/"%d"`, revision))
}

// publicIssueExpectedRevision 解析期望版本：body 里的 expected_revision 或 If-Match 头二选一
// （都传则必须一致），返回期望值；两个来源都没有就返回 nil（不做乐观校验）。
func publicIssueExpectedRevision(w http.ResponseWriter, r *http.Request, bodyRevision *int64) (*int64, bool) {
	header := strings.TrimSpace(r.Header.Get("If-Match"))
	if header == "" {
		if bodyRevision != nil && *bodyRevision < 1 {
			publicapiv1.WriteProblem(w, r, http.StatusBadRequest, "invalid_request", "expected_revision must be a positive integer")
			return nil, false
		}
		return bodyRevision, true
	}
	if strings.HasPrefix(header, "W/") {
		header = strings.TrimSpace(strings.TrimPrefix(header, "W/"))
	}
	header = strings.Trim(header, `"`)
	parsed, err := strconv.ParseInt(header, 10, 64)
	if err != nil || parsed < 1 {
		publicapiv1.WriteProblem(w, r, http.StatusBadRequest, "invalid_if_match", "If-Match must contain a positive issue revision")
		return nil, false
	}
	if bodyRevision != nil && *bodyRevision != parsed {
		publicapiv1.WriteProblem(w, r, http.StatusBadRequest, "revision_mismatch", "If-Match and expected_revision must identify the same revision")
		return nil, false
	}
	return &parsed, true
}

func writePublicIssueRevisionConflict(w http.ResponseWriter, r *http.Request) {
	publicapiv1.WriteProblem(w, r, http.StatusConflict, "revision_conflict", "resource changed since it was loaded")
}

// pluginIssuePayload 把 App 响应显式映射成稳定的公共 DTO。新的仅 App 字段不会意外漏进公共
// 契约。
func (h *Handler) pluginIssuePayload(r *http.Request, caller service.PluginActionCaller, issue db.Issue) publicapiv1.Issue {
	prefix := ""
	if workspace, err := h.Queries.GetWorkspace(r.Context(), caller.WorkspaceID); err == nil {
		prefix = workspace.IssuePrefix
	}
	app := issueToResponse(issue, prefix)
	return publicapiv1.Issue{
		ID:             app.ID,
		WorkspaceID:    app.WorkspaceID,
		Number:         app.Number,
		Identifier:     app.Identifier,
		Title:          app.Title,
		Description:    app.Description,
		Status:         app.Status,
		StatusCategory: app.StatusCategory,
		Priority:       app.Priority,
		AssigneeType:   app.AssigneeType,
		AssigneeID:     app.AssigneeID,
		CreatorType:    app.CreatorType,
		CreatorID:      app.CreatorID,
		ParentIssueID:  app.ParentIssueID,
		ProjectID:      app.ProjectID,
		Position:       app.Position,
		Stage:          app.Stage,
		StartDate:      app.StartDate,
		DueDate:        app.DueDate,
		CreatedAt:      app.CreatedAt,
		UpdatedAt:      app.UpdatedAt,
		Revision:       app.Revision,
		LastActivityAt: app.LastActivityAt,
		Metadata:       app.Metadata,
		Properties:     app.Properties,
	}
}

// ListPluginComments 处理 GET /v1/issues/{issue_ref}/comments（需 comments:read scope）——
// 列出某 issue 最近的评论（数量上限见 maxPluginCommentsPerRead）。
func (h *Handler) ListPluginComments(w http.ResponseWriter, r *http.Request) {
	caller, _, ok := h.pluginCaller(w, r, plugincontract.ScopeCommentsRead)
	if !ok {
		return
	}
	issue, ok := h.pluginIssueForUser(w, r, caller, chi.URLParam(r, "issue_ref"))
	if !ok {
		return
	}
	comments, err := h.Queries.ListCommentsForIssue(r.Context(), db.ListCommentsForIssueParams{
		IssueID:     issue.ID,
		WorkspaceID: caller.WorkspaceID,
		Limit:       maxPluginCommentsPerRead,
	})
	if err != nil {
		publicapiv1.WriteProblem(w, r, http.StatusInternalServerError, "internal_error", "failed to list comments")
		return
	}
	payload := make([]publicapiv1.Comment, 0, len(comments))
	for _, comment := range comments {
		payload = append(payload, publicPluginComment(comment))
	}
	writeJSON(w, http.StatusOK, publicapiv1.CommentListResponse{Comments: payload})
}

// publicPluginComment 把内部 Comment 映射成公共 DTO。
func publicPluginComment(comment db.Comment) publicapiv1.Comment {
	return publicapiv1.Comment{
		ID:         uuidToString(comment.ID),
		AuthorType: comment.AuthorType,
		AuthorID:   uuidToString(comment.AuthorID),
		Content:    comment.Content,
		Type:       comment.Type,
		ParentID:   uuidToString(comment.ParentID),
		CreatedAt:  comment.CreatedAt.Time.UTC().Format(timeFormatRFC3339),
	}
}

// CreatePluginComment 处理 POST /v1/issues/{issue_ref}/comments（需 comments:write scope）。
//
// 评论以用户身份署名、并用 via_plugin_id 标出是哪个插件产的，时间线因此能渲染出
// "Jiayuan (via Hello Panel)"。
//
// 它刻意不跑普通评论端点会跑的 @mention 触发派发。能发 mention 的插件就等于能从用户只想
// 点一下按钮的 surface 上启动 Agent 运行——并花掉工作区预算。想启动工作的插件会有专门的
// hook，而不是把「发文本」的副作用继承过去。
func (h *Handler) CreatePluginComment(w http.ResponseWriter, r *http.Request) {
	caller, actor, ok := h.pluginCaller(w, r, plugincontract.ScopeCommentsWrite)
	if !ok {
		return
	}
	issue, ok := h.pluginIssueForUser(w, r, caller, chi.URLParam(r, "issue_ref"))
	if !ok {
		return
	}

	var req publicapiv1.CreateCommentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		publicapiv1.WriteProblem(w, r, http.StatusBadRequest, "invalid_request", "invalid request body")
		return
	}
	content := sanitizeNullBytes(req.Content)
	if content == "" {
		publicapiv1.WriteProblem(w, r, http.StatusBadRequest, "invalid_request", "content is required")
		return
	}
	if len(content) > maxPluginCommentBytes {
		publicapiv1.WriteProblem(w, r, http.StatusBadRequest, "invalid_request", "content is too long")
		return
	}

	var parentID pgtype.UUID
	var rootComment *db.Comment
	if req.ParentID != nil && *req.ParentID != "" {
		parsed, err := util.ParseUUID(*req.ParentID)
		if err != nil {
			publicapiv1.WriteProblem(w, r, http.StatusBadRequest, "invalid_request", "parent_id must be a valid UUID")
			return
		}
		parent, err := h.Queries.GetComment(r.Context(), parsed)
		if err != nil || uuidToString(parent.IssueID) != uuidToString(issue.ID) {
			publicapiv1.WriteProblem(w, r, http.StatusBadRequest, "invalid_parent_comment", "invalid parent comment")
			return
		}
		parentID = parsed
		rootComment = &parent
	}

	// 署名跟着 actor 走，而 actor 由「调用方怎么认证」决定，不由请求体里的任何东西决定。
	// 人在调用后面时用自己的名字署名（via_plugin_id 记录是插件产的）；event hook 以安装身份
	// 写，因为背后没有可归属的人。
	authorType := "member"
	authorID := actor.Member.UserID
	if !actor.isMember() {
		authorType = "plugin"
		authorID = caller.Installation.ID
	}

	if h.TxStarter == nil {
		publicapiv1.WriteProblem(w, r, http.StatusInternalServerError, "internal_error", "failed to start comment transaction")
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		publicapiv1.WriteProblem(w, r, http.StatusInternalServerError, "internal_error", "failed to create the comment")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	createdComment, err := qtx.CreateComment(r.Context(), db.CreateCommentParams{
		ID:          dbid.NewV7(),
		IssueID:     issue.ID,
		WorkspaceID: caller.WorkspaceID,
		AuthorType:  authorType,
		AuthorID:    authorID,
		Content:     content,
		Type:        "comment",
		ParentID:    parentID,
		ViaPluginID: caller.Installation.ID,
	})
	if err != nil {
		publicapiv1.WriteProblem(w, r, http.StatusInternalServerError, "internal_error", "failed to create the comment")
		return
	}
	comment := createdComment.Comment()

	// 其它评论路径都会做的两件善后。跳掉它们会让插件发的评论要到下次刷新才可见，还会让
	// 对已解决线程的回复不重新打开它——以用户身份落地的评论就该表现得像用户的评论，只少了
	// 故意被拿掉的（mention 派发）。
	commentResponse := commentToResponse(comment, nil, nil)
	commentResponse.IssueRevision = createdComment.IssueRevision
	createdEvent, err := eventoutbox.Enqueue(r.Context(), qtx, buildCommentCreatedEvent(issue, commentResponse, authorType, uuidToString(authorID)))
	if err != nil {
		publicapiv1.WriteProblem(w, r, http.StatusInternalServerError, "internal_error", "failed to create the comment")
		return
	}
	var unresolvedEvent events.Event
	var reopened bool
	if rootComment != nil {
		unresolvedEvent, reopened, err = service.UnresolveThreadOnReply(r.Context(), qtx, rootComment, uuidToString(caller.WorkspaceID), authorType, uuidToString(authorID))
		if err != nil {
			publicapiv1.WriteProblem(w, r, http.StatusInternalServerError, "internal_error", "failed to create the comment")
			return
		}
		if reopened {
			unresolvedEvent, err = eventoutbox.Enqueue(r.Context(), qtx, unresolvedEvent)
			if err != nil {
				publicapiv1.WriteProblem(w, r, http.StatusInternalServerError, "internal_error", "failed to create the comment")
				return
			}
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		publicapiv1.WriteProblem(w, r, http.StatusInternalServerError, "internal_error", "failed to create the comment")
		return
	}
	h.publishEvent(createdEvent)
	if reopened {
		h.publishEvent(unresolvedEvent)
	}

	writeJSON(w, http.StatusCreated, publicPluginComment(comment))
}

// maxPluginCommentBytes 防止 surface 把评论当批量存储用。
const maxPluginCommentBytes = 64 * 1024

// maxPluginCommentsPerRead 限制单次读取条数。查询返回「时间序里最新 N 条」，让长线程上的
// surface 看到的是靠后那端，而不是被截断的开头。
const maxPluginCommentsPerRead = 200

// --- 存储 ---

// pluginStorageScope 解析 /v1/storage/{scope} 的 scope 维度并做授权：检查安装被授予了对应
// scope（workspace 或 user），user 维度的写入必须是「某个人」，然后把维度解析成确定的作用域
// id（见 service.ResolveStorageScope）。
func (h *Handler) pluginStorageScope(w http.ResponseWriter, r *http.Request, caller service.PluginActionCaller, actor pluginActor) (string, pgtype.UUID, bool) {
	scopeType := chi.URLParam(r, "scope")
	required := plugincontract.ScopeStorageWorkspace
	if scopeType == service.PluginStorageUser {
		required = plugincontract.ScopeStorageUser
	}
	if !hasGrantedScope(caller.Scopes, required) {
		publicapiv1.WriteProblem(w, r, http.StatusForbidden, "missing_scope", "this Plugin was not granted the "+required+" scope")
		return "", pgtype.UUID{}, false
	}
	// storage:user 是逐成员的状态，没有成员身份的调用方根本没有这个 scope 可解析。放它漏
	// 过去会把每次插件身份的写入都键到零 UUID——一个伪装成某人私有桶的共享桶。
	if scopeType == service.PluginStorageUser && !actor.requireMember(w, r) {
		return "", pgtype.UUID{}, false
	}
	scopeID, err := service.ResolveStorageScope(scopeType, caller.WorkspaceID, actor.Member.UserID)
	if err != nil {
		writePluginActionError(w, r, err, "invalid storage scope")
		return "", pgtype.UUID{}, false
	}
	return scopeType, scopeID, true
}

// hasGrantedScope 判断授权 scope 列表里是否含 want。
func hasGrantedScope(scopes []string, want string) bool {
	for _, scope := range scopes {
		if scope == want {
			return true
		}
	}
	return false
}

// ListPluginStorage 处理 GET /v1/storage/{scope}——列出插件在该 scope 下存的 key 及元信息。
func (h *Handler) ListPluginStorage(w http.ResponseWriter, r *http.Request) {
	caller, actor, ok := h.pluginCaller(w, r, "")
	if !ok {
		return
	}
	scopeType, scopeID, ok := h.pluginStorageScope(w, r, caller, actor)
	if !ok {
		return
	}
	keys, err := h.PluginService.ListStorageKeys(r.Context(), caller.Installation.ID, scopeType, scopeID)
	if err != nil {
		writePluginActionError(w, r, err, "failed to list storage")
		return
	}
	payload := make([]publicapiv1.StorageKey, 0, len(keys))
	for _, key := range keys {
		payload = append(payload, publicapiv1.StorageKey{
			Key: key.Key, SizeBytes: key.SizeBytes, UpdatedAt: key.UpdatedAt,
		})
	}
	writeJSON(w, http.StatusOK, publicapiv1.StorageKeyListResponse{Keys: payload})
}

// GetPluginStorage 处理 GET /v1/storage/{scope}/{key}——读取一个键的值。
func (h *Handler) GetPluginStorage(w http.ResponseWriter, r *http.Request) {
	caller, actor, ok := h.pluginCaller(w, r, "")
	if !ok {
		return
	}
	scopeType, scopeID, ok := h.pluginStorageScope(w, r, caller, actor)
	if !ok {
		return
	}
	value, err := h.PluginService.GetStorageValue(r.Context(), caller.Installation.ID, scopeType, scopeID, chi.URLParam(r, "key"))
	if err != nil {
		writePluginActionError(w, r, err, "failed to read storage")
		return
	}
	writeJSON(w, http.StatusOK, publicapiv1.StorageValueResponse{Value: value})
}

// PutPluginStorage 处理 PUT /v1/storage/{scope}/{key}——写入一个键，成功返回 204。
func (h *Handler) PutPluginStorage(w http.ResponseWriter, r *http.Request) {
	caller, actor, ok := h.pluginCaller(w, r, "")
	if !ok {
		return
	}
	scopeType, scopeID, ok := h.pluginStorageScope(w, r, caller, actor)
	if !ok {
		return
	}
	var req publicapiv1.PutStorageValueRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		publicapiv1.WriteProblem(w, r, http.StatusBadRequest, "invalid_request", "invalid request body")
		return
	}
	if err := h.PluginService.SetStorageValue(r.Context(), caller.Installation.ID, scopeType, scopeID, chi.URLParam(r, "key"), req.Value); err != nil {
		writePluginActionError(w, r, err, "failed to write storage")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// DeletePluginStorage 处理 DELETE /v1/storage/{scope}/{key}——删除一个键，成功返回 204。
func (h *Handler) DeletePluginStorage(w http.ResponseWriter, r *http.Request) {
	caller, actor, ok := h.pluginCaller(w, r, "")
	if !ok {
		return
	}
	scopeType, scopeID, ok := h.pluginStorageScope(w, r, caller, actor)
	if !ok {
		return
	}
	if err := h.PluginService.DeleteStorageValue(r.Context(), caller.Installation.ID, scopeType, scopeID, chi.URLParam(r, "key")); err != nil {
		writePluginActionError(w, r, err, "failed to delete storage")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
