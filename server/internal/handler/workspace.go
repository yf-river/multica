// Package handler 是整个 HTTP API 的处理器集合。
//
// 本文件（workspace.go）承载 workspace 与成员（member）生命周期的主体接口：
// workspace 的创建/查询/改名/删除，成员的角色变更、踢人、自己退出（复用
// workspace_revoke.go 的撤销流程），以及删 workspace 时整套拆除事务的编排
// （分页清扫任务、加锁顺序、锁超时可重试）。此外还定义 workspace 的 issue
// 前缀派生规则与仓库（repos）绑定字段的校验。
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/logger"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// nonAlpha 匹配任意非英文字母字符（用于从名字里取前缀字母）。
var nonAlpha = regexp.MustCompile(`[^a-zA-Z]`)

// nonAlphanumeric 匹配任意非字母数字字符（用于从 slug 里取前缀字符）。
var nonAlphanumeric = regexp.MustCompile(`[^a-zA-Z0-9]`)

// workspaceSlugPattern 校验 workspace slug 的合法形态：小写字母/数字组成的段，
// 段间以单横线连接，不允许开头/结尾/连续横线。
var workspaceSlugPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// issuePrefixPattern 校验 issue 前缀：1~10 位大写字母或数字。
var issuePrefixPattern = regexp.MustCompile(`^[A-Z0-9]{1,10}$`)

// defaultIssuePrefixFromSlug 从新 workspace 的 slug 派生默认 issue 前缀：
// 只留字母数字、取前 4 位、转大写。示例："acme"→"ACME"、"front-end"→"FRON"、
// "team-2"→"TEAM"。
//
// 派生源刻意选 slug 而不是 name（MUL-6050）：name 是创建流程里唯一允许非
// ASCII 的字段，若从 name 派生纯 ASCII 前缀，所有中文/emoji 命名的 workspace
// 都会被挤到同一个 "WS" 兜底。slug 被限定为 ^[a-z0-9]+(-[a-z0-9]+)*$ 且必填，
// 因此总能得出非空、且每个 workspace 各不相同的前缀。
//
// 与客户端预览逐字节保持一致（packages/views/onboarding/steps/
// step-workspace.tsx）——创建页上用户看到什么，服务端就该派生什么。
func defaultIssuePrefixFromSlug(slug string) string {
	head := nonAlphanumeric.ReplaceAllString(slug, "")
	if head == "" {
		// 对校验过的 slug 实际到不了这里；保留兜底是为让函数保持全函数性质，
		// 未来的调用方永远不可能落一个空前缀进库。
		return "WS"
	}
	if len(head) > 4 {
		head = head[:4]
	}
	return strings.ToUpper(head)
}

// legacyIssuePrefixFromName 是 MUL-6050 之前的旧派生逻辑：取 workspace 名字里
// 前 3 个 ASCII 字母转大写，一个字母都没有就退回 "WS"。
//
// 冻结——不要"修正"它去对齐 defaultIssuePrefixFromSlug。它唯一的调用方是
// getIssuePrefix 里对"库里前缀为空（该列出现前建的行）"的 workspace 的回退。
// issue 标识符是读取时按当前前缀现算的，改动它的返回值等于悄悄改写这些
// workspace 里全部历史 issue 的标识。这是明确的产品决定：不做回填，存量
// workspace 保持原样；只有新建的才走 slug 派生规则。
func legacyIssuePrefixFromName(name string) string {
	letters := nonAlpha.ReplaceAllString(name, "")
	if len(letters) == 0 {
		return "WS"
	}
	letters = strings.ToUpper(letters)
	if len(letters) > 3 {
		letters = letters[:3]
	}
	return letters
}

// normalizeIssuePrefix 清洗客户端提交的 issue 前缀：去空格并转大写。
// 全空白值视为"未提供"，返回 ("", true) 让调用方回退到默认值。其余值必须是
// 1~10 位 A-Z0-9，与客户端侧（packages/views/settings/components/
// workspace-tab.tsx）的护栏保持一致。
func normalizeIssuePrefix(raw string) (string, bool) {
	prefix := strings.ToUpper(strings.TrimSpace(raw))
	if prefix == "" {
		return "", true
	}
	if !issuePrefixPattern.MatchString(prefix) {
		return "", false
	}
	return prefix, true
}

// issuePrefixFormatError 是非法 issue 前缀的统一报错文案，供各处直接透出。
const issuePrefixFormatError = "issue prefix must be 1-10 uppercase letters or digits"

// WorkspaceResponse 是单个 workspace 对外展示的 JSON 形态；Settings 与 Repos
// 都是各自 JSONB 字段解出来的结构（Settings 兜底为空对象、Repos 兜底为空数组）。
type WorkspaceResponse struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Slug        string  `json:"slug"`
	Description *string `json:"description"`
	Context     *string `json:"context"`
	Settings    any     `json:"settings"`
	Repos       any     `json:"repos"`
	IssuePrefix string  `json:"issue_prefix"`
	AvatarURL   *string `json:"avatar_url"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
}

// workspaceToResponse 把库行转成对外响应；Settings/Repos 存的是 JSONB，这里
// 尽力解成通用结构体，解不出就保持零值再兜底为空对象/空数组。
func (h *Handler) workspaceToResponse(ctx context.Context, w db.Workspace) (WorkspaceResponse, error) {
	var settings any
	if w.Settings != nil {
		json.Unmarshal(w.Settings, &settings)
	}
	if settings == nil {
		settings = map[string]any{}
	}
	repos, err := repositoryConfigs(ctx, h.Queries, w.ID, false)
	if err != nil {
		return WorkspaceResponse{}, err
	}
	return WorkspaceResponse{
		ID:          uuidToString(w.ID),
		Name:        w.Name,
		Slug:        w.Slug,
		Description: textToPtr(w.Description),
		Context:     textToPtr(w.Context),
		Settings:    settings,
		Repos:       repos,
		IssuePrefix: w.IssuePrefix,
		AvatarURL:   h.resolveAvatarURLPtr(textToPtr(w.AvatarUrl)),
		CreatedAt:   timestampToString(w.CreatedAt),
		UpdatedAt:   timestampToString(w.UpdatedAt),
	}, nil
}

// MemberResponse 是成员关系（谁以什么角色属于哪个 workspace）的对外形态。
type MemberResponse struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	UserID      string `json:"user_id"`
	Role        string `json:"role"`
	CreatedAt   string `json:"created_at"`
}

// memberToResponse 把成员库行转成对外响应。
func memberToResponse(m db.Member) MemberResponse {
	return MemberResponse{
		ID:          uuidToString(m.ID),
		WorkspaceID: uuidToString(m.WorkspaceID),
		UserID:      uuidToString(m.UserID),
		Role:        m.Role,
		CreatedAt:   timestampToString(m.CreatedAt),
	}
}

// ListWorkspaces 列出当前登录用户属于的全部 workspace。需要登录；客户端主动
// 断开时按取消处理而不报错。
func (h *Handler) ListWorkspaces(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	workspaces, err := h.Queries.ListWorkspaces(r.Context(), parseUUID(userID))
	if err != nil {
		if writeClientClosedIfCanceled(w, err) {
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to list workspaces")
		return
	}

	resp := make([]WorkspaceResponse, len(workspaces))
	for i, ws := range workspaces {
		resp[i], err = h.workspaceToResponse(r.Context(), ws)
		if err != nil {
			writeError(w, 500, "读取仓库失败")
			return
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

// GetWorkspace 返回单个 workspace 的详情。只按 id 取行，不校验请求者是否为
// 成员——路由层的中间件负责这道门槛。
func (h *Handler) GetWorkspace(w http.ResponseWriter, r *http.Request) {
	id := workspaceIDFromURL(r, "id")
	idUUID, ok := parseUUIDOrBadRequest(w, id, "workspace id")
	if !ok {
		return
	}

	ws, err := h.Queries.GetWorkspace(r.Context(), idUUID)
	if err != nil {
		writeError(w, http.StatusNotFound, "workspace not found")
		return
	}
	response, responseErr := h.workspaceToResponse(r.Context(), ws)
	if responseErr != nil {
		writeError(w, 500, "读取仓库失败")
		return
	}
	writeJSON(w, http.StatusOK, response)
}

// CreateWorkspaceRequest 是创建 workspace 的入参。name 允许任意文本
// （含中文/emoji），slug 必须符合 ^[a-z0-9]+(-[a-z0-9]+)*$ 且不在保留名单里；
// issue_prefix 可空，空则由 slug 派生默认前缀。
type CreateWorkspaceRequest struct {
	Name        string  `json:"name"`
	Slug        string  `json:"slug"`
	Description *string `json:"description"`
	Context     *string `json:"context"`
	IssuePrefix *string `json:"issue_prefix"`
}

// CreateWorkspace 创建一个新 workspace 并让当前用户以 owner 身份加入，同一
// 事务内再播种 7 个内置 issue 状态并记录事件。需要登录。命中保留 slug 或 slug
// 重复时分别回 400/409；实例配置禁止创建时（自托管开关）一律回 403。
func (h *Handler) CreateWorkspace(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	// 自托管闸门（#3433）：运营商设了 DISABLE_WORKSPACE_CREATION=true 时，任何
	// 调用方——包括已有 workspace 的 owner——都不许再建 workspace。前端通过
	// /api/config 隐藏所有"创建 workspace"入口，但这里的 403 才是唯一权威校验。
	if h.cfg.DisableWorkspaceCreation {
		writeError(w, http.StatusForbidden, "workspace creation is disabled for this instance")
		return
	}

	var req CreateWorkspaceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	req.Slug = strings.ToLower(strings.TrimSpace(req.Slug))
	if req.Name == "" || req.Slug == "" {
		writeError(w, http.StatusBadRequest, "name and slug are required")
		return
	}
	if !workspaceSlugPattern.MatchString(req.Slug) {
		writeError(w, http.StatusBadRequest, "slug must contain only lowercase letters, numbers, and hyphens")
		return
	}
	if isReservedSlug(req.Slug) {
		writeError(w, http.StatusBadRequest, "slug is reserved")
		return
	}

	// 开事务前先定前缀：非法的前缀是 400，不该为此占用一条数据库连接。
	issuePrefix := ""
	if req.IssuePrefix != nil {
		prefix, ok := normalizeIssuePrefix(*req.IssuePrefix)
		if !ok {
			writeError(w, http.StatusBadRequest, issuePrefixFormatError)
			return
		}
		issuePrefix = prefix
	}
	if issuePrefix == "" {
		issuePrefix = defaultIssuePrefixFromSlug(req.Slug)
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create workspace")
		return
	}
	defer tx.Rollback(r.Context())

	qtx := h.Queries.WithTx(tx)
	ws, err := qtx.CreateWorkspace(r.Context(), db.CreateWorkspaceParams{
		Name:        req.Name,
		Slug:        req.Slug,
		Description: ptrToText(req.Description),
		Context:     ptrToText(req.Context),
		IssuePrefix: issuePrefix,
	})
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "workspace slug already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to create workspace: "+err.Error())
		return
	}

	member, err := qtx.CreateMember(r.Context(), db.CreateMemberParams{
		WorkspaceID: ws.ID,
		UserID:      parseUUID(userID),
		Role:        "owner",
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to add owner: "+err.Error())
		return
	}

	// 在同一事务里播种 7 个内置 issue 状态，保证 workspace 从不以"没有状态
	// 目录"的形态出现——issue 得先能解析到状态才能被创建。（MUL-6243）
	if err := issuestatus.Ensure(r.Context(), qtx, ws.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to seed issue statuses: "+err.Error())
		return
	}
	workspaceEvent, err := service.RecordDurableEventTx(r.Context(), qtx, buildWorkspaceDomainEvent(
		protocol.EventWorkspaceUpdated, ws, "member", userID, map[string]any{"action": "created"},
	))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record workspace event")
		return
	}
	memberEvent, err := service.RecordDurableEventTx(r.Context(), qtx, buildMemberDomainEvent(
		protocol.EventMemberAdded, member, "member", userID, map[string]any{"action": "created"},
	))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record member event")
		return
	}

	// 注意：CreateWorkspace 刻意不把用户标记为已 onboarding。onboarded_at
	// 标志归 CompleteOnboarding（流程第 3 步）与 AcceptInvitation（受邀者加入
	// 既有 workspace）所有。这解耦了"用户有了 workspace"与"用户走完引导"两件
	// 事；web 布局里 workspace 层的路由闸门会把未引导完成的用户重定向回
	// /onboarding。

	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create workspace")
		return
	}

	wsID := uuidToString(ws.ID)
	h.publishEvent(workspaceEvent)
	h.publishEvent(memberEvent)

	// "是不是用户的第一个 workspace？"由 PostHog 端去看用户此前有没有
	// workspace_created 事件来推导，而不是在发出时打标记。若在此打标记，并发
	// 创建场景下不改表结构就会竞态；而事件流能精确回答这个问题。
	obsmetrics.RecordEvent(h.Analytics, h.Metrics, analytics.WorkspaceCreated(userID, wsID))
	h.notifyDaemonWorkspacesChanged(userID)

	slog.Info("workspace created", append(logger.RequestAttrs(r), "workspace_id", wsID, "name", ws.Name, "slug", ws.Slug)...)
	response, responseErr := h.workspaceToResponse(r.Context(), ws)
	if responseErr != nil {
		writeError(w, 500, "读取仓库失败")
		return
	}
	writeJSON(w, http.StatusCreated, response)
}

// UpdateWorkspaceRequest 是更新 workspace 的入参。各字段均为指针以区分
// "未传"与"传了空值"；Settings 与 Repos 是整块替换的 JSONB。
type UpdateWorkspaceRequest struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
	Context     *string `json:"context"`
	Settings    any     `json:"settings"`
	Repos       any     `json:"repos"`
	IssuePrefix *string `json:"issue_prefix"`
	AvatarURL   *string `json:"avatar_url"`
}

// UpdateWorkspace 更新 workspace 的可变字段（名称/描述/上下文/设置/仓库/
// issue 前缀/头像）。校验前置在事务外完成（avatar 除外，见内）。改名或改
// settings 会唤醒该 workspace 全体成员的 daemon。路由层负责 owner/admin 门槛。
func (h *Handler) UpdateWorkspace(w http.ResponseWriter, r *http.Request) {
	id := workspaceIDFromURL(r, "id")
	idUUID, ok := parseUUIDOrBadRequest(w, id, "workspace id")
	if !ok {
		return
	}

	var req UpdateWorkspaceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	params := db.UpdateWorkspaceParams{
		ID: idUUID,
	}
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" {
			writeError(w, http.StatusBadRequest, "name is required")
			return
		}
		params.Name = pgtype.Text{String: name, Valid: true}
	}
	if req.Description != nil {
		params.Description = pgtype.Text{String: *req.Description, Valid: true}
	}
	if req.Context != nil {
		params.Context = pgtype.Text{String: *req.Context, Valid: true}
	}
	if req.Settings != nil {
		s, _ := json.Marshal(req.Settings)
		params.Settings = s
	}
	if req.Repos != nil {
		writeError(w, 400, "请通过仓库管理接口修改仓库")
		return
	}
	if req.IssuePrefix != nil {
		prefix, ok := normalizeIssuePrefix(*req.IssuePrefix)
		if !ok {
			writeError(w, http.StatusBadRequest, issuePrefixFormatError)
			return
		}
		if prefix != "" {
			params.IssuePrefix = pgtype.Text{String: prefix, Valid: true}
		}
	}
	if req.AvatarURL != nil {
		// 先读出库里当前值，让"原样重发"能跳过重新校验——本 handler 是唯一
		// 写头像的路径里手上没有那行的。
		var current string
		if existing, err := h.Queries.GetWorkspace(r.Context(), idUUID); err == nil {
			current = existing.AvatarUrl.String
		}
		accepted, ok := h.acceptAvatarURL(w, r, *req.AvatarURL, current)
		if !ok {
			return
		}
		params.AvatarUrl = pgtype.Text{String: accepted, Valid: true}
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start workspace update")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)

	ws, err := qtx.UpdateWorkspace(r.Context(), params)
	if err != nil {
		slog.Warn("update workspace failed", append(logger.RequestAttrs(r), "error", err, "workspace_id", id)...)
		writeError(w, http.StatusInternalServerError, "failed to update workspace: "+err.Error())
		return
	}
	repos, err := repositoryConfigs(r.Context(), qtx, idUUID, false)
	if err != nil {
		writeError(w, 500, "读取仓库失败")
		return
	}
	ws.Repos, _ = json.Marshal(repos)
	workspaceEvent, err := service.RecordDurableEventTx(r.Context(), qtx, buildWorkspaceDomainEvent(
		protocol.EventWorkspaceUpdated, ws, "member", requestUserID(r), map[string]any{"action": "updated"},
	))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record workspace event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit workspace update")
		return
	}

	slog.Info("workspace updated", append(logger.RequestAttrs(r), "workspace_id", id)...)
	h.publishEvent(workspaceEvent)
	// 改名会改变 daemon 显示的名字；改 settings 会改变 daemon 的行为——GitHub
	// 总开关和 Co-authored-by 开关都从这个 JSONB 里读。daemon 缓存了 settings，
	// 没有别的途径得知它变了，所以这两类编辑都得唤醒每个成员的 daemon。
	if req.Name != nil || req.Settings != nil {
		if members, err := h.Queries.ListMembers(r.Context(), ws.ID); err == nil {
			userIDs := make([]string, 0, len(members))
			for _, member := range members {
				userIDs = append(userIDs, uuidToString(member.UserID))
			}
			h.notifyDaemonWorkspacesChanged(userIDs...)
		}
	}

	response, responseErr := h.workspaceToResponse(r.Context(), ws)
	if responseErr != nil {
		writeError(w, 500, "读取仓库失败")
		return
	}
	writeJSON(w, http.StatusOK, response)
}

// ListMembers 返回 workspace 的成员列表（仅成员关系，不含用户资料）。调用者
// 必须是 workspace 成员，否则 404。
func (h *Handler) ListMembers(w http.ResponseWriter, r *http.Request) {
	workspaceID := chi.URLParam(r, "id")
	member, ok := h.requireWorkspaceMember(w, r, workspaceID, "workspace not found")
	if !ok {
		return
	}

	members, err := h.Queries.ListMembers(r.Context(), member.WorkspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list members")
		return
	}

	resp := make([]MemberResponse, len(members))
	for i, m := range members {
		resp[i] = memberToResponse(m)
	}

	writeJSON(w, http.StatusOK, resp)
}

// MemberWithUserResponse 是"成员 + 用户资料"联查的对外形态，供成员管理界面
// 展示头像、姓名、邮箱。
type MemberWithUserResponse struct {
	ID          string  `json:"id"`
	WorkspaceID string  `json:"workspace_id"`
	UserID      string  `json:"user_id"`
	Role        string  `json:"role"`
	CreatedAt   string  `json:"created_at"`
	Name        string  `json:"name"`
	Email       string  `json:"email"`
	AvatarURL   *string `json:"avatar_url"`
}

// ListMembersWithUser 联查 workspace 成员及其用户资料。调用者需为成员；响应里
// 不校验角色——联查 SQL 已把 workspace_id 作为筛选条件。
func (h *Handler) ListMembersWithUser(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromURL(r, "id")
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}

	members, err := h.Queries.ListMembersWithUser(r.Context(), wsUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list members")
		return
	}

	resp := make([]MemberWithUserResponse, len(members))
	for i, m := range members {
		resp[i] = MemberWithUserResponse{
			ID:          uuidToString(m.ID),
			WorkspaceID: uuidToString(m.WorkspaceID),
			UserID:      uuidToString(m.UserID),
			Role:        m.Role,
			CreatedAt:   timestampToString(m.CreatedAt),
			Name:        m.UserName,
			Email:       m.UserEmail,
			AvatarURL:   h.resolveAvatarURLPtr(textToPtr(m.UserAvatarUrl)),
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

// CreateMemberRequest 是添加成员的入参：按邮箱找人，role 可空（默认 member）。
type CreateMemberRequest struct {
	Email string `json:"email"`
	Role  string `json:"role"`
}

// memberWithUserResponse 组装"成员 + 用户资料"的联查响应。
func (h *Handler) memberWithUserResponse(member db.Member, user db.User) MemberWithUserResponse {
	return MemberWithUserResponse{
		ID:          uuidToString(member.ID),
		WorkspaceID: uuidToString(member.WorkspaceID),
		UserID:      uuidToString(member.UserID),
		Role:        member.Role,
		CreatedAt:   timestampToString(member.CreatedAt),
		Name:        user.Name,
		Email:       user.Email,
		AvatarURL:   h.resolveAvatarURLPtr(textToPtr(user.AvatarUrl)),
	}
}

// normalizeMemberRole 归一化成员角色：空值视为 "member"；只接受
// owner/admin/member 三种取值，其余返回非法。
func normalizeMemberRole(role string) (string, bool) {
	if role == "" {
		return "member", true
	}

	role = strings.TrimSpace(role)
	switch role {
	case "owner", "admin", "member":
		return role, true
	default:
		return "", false
	}
}

// UpdateMemberRequest 是修改成员角色的入参。
type UpdateMemberRequest struct {
	Role string `json:"role"`
}

// UpdateMember 修改某成员的角色。权限与约束：只有 owner 能改动 owner 的角色；
// 把最后一个 owner 降级/移除会被拒绝（workspace 至少留一个 owner）。成功后在
// 同一事务里记录 member_updated 事件并作废该成员的成员缓存。
func (h *Handler) UpdateMember(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromURL(r, "id")
	requester, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return
	}

	memberID := chi.URLParam(r, "memberId")
	memberUUID, ok := parseUUIDOrBadRequest(w, memberID, "member id")
	if !ok {
		return
	}
	target, err := h.Queries.GetMember(r.Context(), memberUUID)
	if err != nil || uuidToString(target.WorkspaceID) != uuidToString(requester.WorkspaceID) {
		writeError(w, http.StatusNotFound, "member not found")
		return
	}

	var req UpdateMemberRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(req.Role) == "" {
		writeError(w, http.StatusBadRequest, "role is required")
		return
	}

	role, valid := normalizeMemberRole(req.Role)
	if !valid {
		writeError(w, http.StatusBadRequest, "invalid member role")
		return
	}

	if (target.Role == "owner" || role == "owner") && requester.Role != "owner" {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}

	if target.Role == "owner" && role != "owner" {
		members, err := h.Queries.ListMembers(r.Context(), target.WorkspaceID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to update member")
			return
		}
		if countOwners(members) <= 1 {
			writeError(w, http.StatusBadRequest, "workspace must have at least one owner")
			return
		}
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start member update")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	updatedMember, err := qtx.UpdateMemberRole(r.Context(), db.UpdateMemberRoleParams{
		ID:   target.ID,
		Role: role,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update member")
		return
	}

	h.MembershipCache.Invalidate(r.Context(), uuidToString(target.UserID), workspaceID)

	user, err := qtx.GetUser(r.Context(), updatedMember.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load member")
		return
	}

	userID := requestUserID(r)
	memberEvent := events.Event{
		Type:           protocol.EventMemberUpdated,
		IdempotencyKey: "member:" + protocol.EventMemberUpdated + ":" + uuidToString(updatedMember.ID) + ":" + role,
		StreamKey:      "workspace:" + uuidToString(updatedMember.WorkspaceID),
		WorkspaceID:    uuidToString(updatedMember.WorkspaceID),
		ActorType:      "member", ActorID: userID,
		Payload: map[string]any{"member": h.memberWithUserResponse(updatedMember, user), "action": "role_updated"},
	}
	memberEvent, err = service.RecordDurableEventTx(r.Context(), qtx, memberEvent)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record member event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit member update")
		return
	}
	h.publishEvent(memberEvent)

	writeJSON(w, http.StatusOK, h.memberWithUserResponse(updatedMember, user))
}

// DeleteMember 把某成员移出 workspace（管理员踢人）。前置权限校验：只有 owner
// 能移除 owner；最后一个 owner 不可移除。实质删除动作全部委托给
// revokeAndRemoveMember（单事务收敛 runtime/agent/任务/订阅），提交后发布事件
// 并通知该成员的 daemon 刷新。
func (h *Handler) DeleteMember(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromURL(r, "id")
	requester, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return
	}

	memberID := chi.URLParam(r, "memberId")
	memberUUID, ok := parseUUIDOrBadRequest(w, memberID, "member id")
	if !ok {
		return
	}
	target, err := h.Queries.GetMember(r.Context(), memberUUID)
	if err != nil || uuidToString(target.WorkspaceID) != uuidToString(requester.WorkspaceID) {
		writeError(w, http.StatusNotFound, "member not found")
		return
	}

	if target.Role == "owner" && requester.Role != "owner" {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}

	if target.Role == "owner" {
		members, err := h.Queries.ListMembers(r.Context(), target.WorkspaceID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to delete member")
			return
		}
		if countOwners(members) <= 1 {
			writeError(w, http.StatusBadRequest, "workspace must have at least one owner")
			return
		}
	}

	requesterUserID := requestUserID(r)
	result, err := h.revokeAndRemoveMember(r.Context(), target.WorkspaceID, target.UserID, target.ID, parseUUID(requesterUserID))
	if err != nil {
		slog.Warn("delete member failed", append(logger.RequestAttrs(r), "error", err, "member_id", memberID, "workspace_id", workspaceID)...)
		writeError(w, http.StatusInternalServerError, "failed to delete member")
		return
	}

	h.MembershipCache.Invalidate(r.Context(), uuidToString(target.UserID), workspaceID)

	wsIDStr := uuidToString(requester.WorkspaceID)
	logRevocation(result, wsIDStr, uuidToString(target.UserID))
	h.publishRevocation(r.Context(), result, wsIDStr, "member", requesterUserID)

	slog.Info("member removed", append(logger.RequestAttrs(r), "member_id", uuidToString(target.ID), "workspace_id", workspaceID, "user_id", uuidToString(target.UserID))...)
	h.notifyDaemonWorkspacesChanged(uuidToString(target.UserID))

	w.WriteHeader(http.StatusNoContent)
}

// LeaveWorkspace 当前用户主动退出 workspace。owner 退出前需保证 workspace 还
// 有别的 owner。实质删除走 revokeAndRemoveMember，之后发事件并通知 daemon。
func (h *Handler) LeaveWorkspace(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromURL(r, "id")
	member, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return
	}

	if member.Role == "owner" {
		members, err := h.Queries.ListMembers(r.Context(), member.WorkspaceID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to leave workspace")
			return
		}
		if countOwners(members) <= 1 {
			writeError(w, http.StatusBadRequest, "workspace must have at least one owner")
			return
		}
	}

	result, err := h.revokeAndRemoveMember(r.Context(), member.WorkspaceID, member.UserID, member.ID, member.UserID)
	if err != nil {
		slog.Warn("leave workspace failed", append(logger.RequestAttrs(r), "error", err, "workspace_id", workspaceID)...)
		writeError(w, http.StatusInternalServerError, "failed to leave workspace")
		return
	}

	h.MembershipCache.Invalidate(r.Context(), uuidToString(member.UserID), workspaceID)

	userID := requestUserID(r)
	logRevocation(result, workspaceID, uuidToString(member.UserID))
	h.publishRevocation(r.Context(), result, workspaceID, "member", userID)

	slog.Info("member removed", append(logger.RequestAttrs(r), "member_id", uuidToString(member.ID), "workspace_id", workspaceID, "user_id", uuidToString(member.UserID))...)
	h.notifyDaemonWorkspacesChanged(uuidToString(member.UserID))

	w.WriteHeader(http.StatusNoContent)
}

// workspaceDeleteLockTimeout 限定拆除 workspace 事务内每次等待锁的时间上限。
//
// 拆除过程要等三类锁：workspace 行锁（FOR UPDATE）、该 workspace 下每条
// chat_session 行锁（FOR UPDATE）、以及那把把它与 task_usage_hourly 汇总
// 串行化的全局 advisory 锁 4246。advisory 锁最危险——它全局且被批处理持有：
// 一次汇总 tick 可能持有它长达调度器 25 分钟的 RunTimeout；回填命令
// （cmd/backfill_task_usage_hourly、cmd/backfill_codex_usage_cache）会持有它
// 跑完整个任务；在 272 号迁移之前，被取消的 tick 的查询还会把它泄漏到连接池
// 连接剩余的生命周期里。
//
// 等这把锁没有上限时，用户看到的现象是"删除没反应"：请求永不返回，而它之上
// 没有任何一层会超时——浏览器 fetch（packages/core/api/client.ts）没有截止时间，
// 删除弹窗永远停在"Deleting…"（MUL-5983）。有界等待能把同样的锁竞争变成
// 一个可重试的错误。
//
// 10 秒远大于无竞争拆除时毫秒级的等待，又短到能让用户在仍盯着弹窗时得到失败。
const workspaceDeleteLockTimeout = 10 * time.Second

// workspaceDeleteLockTimeoutOverride 非零时，在测试期间临时顶替
// workspaceDeleteLockTimeout。除了 effectiveWorkspaceDeleteLockTimeout，任何
// 地方都不得直接读它——见 workspace_delete_lock_test.go。
var workspaceDeleteLockTimeoutOverride time.Duration

// effectiveWorkspaceDeleteLockTimeout 返回生效的锁超时：测试覆盖存在时用覆盖
// 值，否则用默认值。
func effectiveWorkspaceDeleteLockTimeout() time.Duration {
	if workspaceDeleteLockTimeoutOverride > 0 {
		return workspaceDeleteLockTimeoutOverride
	}
	return workspaceDeleteLockTimeout
}

// isRetryableLockFailure 判断 err 是否为拆除过程可能撞上的两种瞬时锁失败之一：
// lock_not_available（55P03），即 SET LOCAL lock_timeout 在等待超预算时抛出的
// 错误；以及 deadlock_detected（40P01）——owner 行锁可能在与"并发任务更新"以
// 相反顺序取同一批锁（先任务行、再其 owner）时互相死锁。这两种都说明不了
// workspace 本身有问题：事务已原样回滚，调用方可以直接重试。
func isRetryableLockFailure(err error) bool {
	if err == nil {
		return false
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "55P03" || pgErr.Code == "40P01"
	}
	return false
}

// failWorkspaceDelete 记录一次失败的拆除步骤并写出对应响应。锁超时或死锁属于
// 瞬时、可重试的失败，所以回 503 并给出删除弹窗能展示的提示；其余失败保持 500。
func failWorkspaceDelete(w http.ResponseWriter, r *http.Request, workspaceID, step string, err error) {
	attrs := append(logger.RequestAttrs(r), "error", err, "workspace_id", workspaceID, "step", step)
	if isRetryableLockFailure(err) {
		slog.Warn("workspace delete blocked by lock contention", attrs...)
		writeError(w, http.StatusServiceUnavailable, "workspace deletion is temporarily blocked by another operation, please try again")
		return
	}
	slog.Warn("workspace delete step failed", attrs...)
	writeError(w, http.StatusInternalServerError, "failed to delete workspace")
}

// workspaceDeleteTaskPageSize 限定单个任务页的大小：包括本进程内持有的 id 数、
// 回传给 Postgres 的数组参数长度、以及单条 DELETE 影响的行数。一个 workspace
// 的任务总量不受任何东西约束——单个活跃 agent 就能拥有数百万历史行——因此
// 绝不能一次性物化全量（MUL-5999 评审结论）。
const workspaceDeleteTaskPageSize = 1000

// workspaceDeleteOwnerPageSize 以同样方式约束 owner 枚举的页大小。agent 或
// issue 集合很大的 workspace，其完整 id 列表也不能整个拿进本进程。
const workspaceDeleteOwnerPageSize = 500

// workspaceDeleteVerifyPasses 限定单个 owner 最多被清扫几轮。
//
// 第 1 轮清掉已有的；第 2 轮确认该 owner 回来已是空。这是廉价的自检，不是
// 并发保证：在"最后一次空扫描之后、COMMIT 之前"插入的任务，再多轮也扫不到。
// 真正封住那个窗口的是任务写入的事务内围栏（迁移 284）加上拆除过程已经持有的
// workspace 行锁。设这么多轮是为了让任何意外——某条绕过了围栏的路径——要么
// 被清掉，要么让整个拆除失败回滚，而不是留下"租户已删但残留行"的结果。
const workspaceDeleteVerifyPasses = 3

// deleteWorkspaceTasks 以有界的 keyset 分页方式删除一个 workspace 名下的全部
// agent_task_queue 行，以及挂在这些任务下的关联行。
//
// 形态与原因：
//
//   - 每条查询只用一个 owner 键。agent_task_queue 没有 workspace_id，三种归属
//     路径都得保留（没有任何约束保证任务的 agent、runtime、issue 同属一个
//     workspace），但若把它们并进一条语句——像旧的"三种 OR"、把 join 做 UNION、
//     或对全 workspace 的 agents 用 = ANY(array)——会让规划器按"全局表的某个
//     比例"来估算，从而选 Seq Scan。
//
//   - 用 keyset 分页，因此被约束的是"扫描本身"而不只是结果。每页都是索引范围
//     扫描，从上一页停下的位置继续。只约束结果（ORDER BY id LIMIT n，而索引里
//     没有 id）会在每页前面给 owner 的整份任务集加一次 Sort，活跃 owner 的代价
//     约等于 O(N²/page)。
//
//   - 用行锁，不用读快照。每页都带 FOR UPDATE，所以在 READ COMMITTED 下，
//     Postgres 拿到每个锁后会重查归属：并发提交已把任务移到别处的，按过期主张
//     直接跳过而不误删；被我们锁住的任务在删除前不可能被移走。
//
//   - 应用层先删子行。DetachTaskBatchReferences 打断入站引用，
//     DeleteTaskBatch 删除 task_usage、task_message、task_token、两张出站卡片表
//     与 chat_draft_restore，然后才删任务行本身。旧的外键级联只作最后安全网。
//
// 迟到的新任务是围栏处理的，不是这里：迁移 284 让每次任务写入都对所属
// workspace 行取 FOR KEY SHARE，与 LockWorkspaceForDelete 在整个事务期间持有的
// FOR UPDATE 冲突。
// deleteWorkspaceTasks 的 owner 形态表：每种归属（agent/issue/runtime）都带
// 各自的"分页列 owner、按 owner 分页列任务"函数，供下面统一循环清扫。
func deleteWorkspaceTasks(ctx context.Context, qtx *db.Queries, workspaceID pgtype.UUID) error {
	ownerKinds := []struct {
		label          string
		ownerFirstPage func(context.Context, int32) ([]pgtype.UUID, error)
		ownerPage      func(context.Context, pgtype.UUID, int32) ([]pgtype.UUID, error)
		taskFirstPage  func(context.Context, pgtype.UUID, int32) ([]pgtype.UUID, error)
		taskPage       func(context.Context, pgtype.UUID, pgtype.UUID, int32) ([]pgtype.UUID, error)
		sweepAgent     bool
	}{
		{
			label: "agent",
			ownerFirstPage: func(ctx context.Context, limit int32) ([]pgtype.UUID, error) {
				return qtx.ListWorkspaceAgentIDFirstPage(ctx, db.ListWorkspaceAgentIDFirstPageParams{
					WorkspaceID: workspaceID, Limit: limit,
				})
			},
			ownerPage: func(ctx context.Context, cursor pgtype.UUID, limit int32) ([]pgtype.UUID, error) {
				return qtx.ListWorkspaceAgentIDPage(ctx, db.ListWorkspaceAgentIDPageParams{
					WorkspaceID: workspaceID, ID: cursor, Limit: limit,
				})
			},
			taskFirstPage: func(ctx context.Context, owner pgtype.UUID, limit int32) ([]pgtype.UUID, error) {
				return qtx.ListTaskIDsByAgentFirstPage(ctx, db.ListTaskIDsByAgentFirstPageParams{
					AgentID: owner, Limit: limit,
				})
			},
			taskPage: func(ctx context.Context, owner, cursor pgtype.UUID, limit int32) ([]pgtype.UUID, error) {
				return qtx.ListTaskIDsByAgentPage(ctx, db.ListTaskIDsByAgentPageParams{
					AgentID: owner, ID: cursor, Limit: limit,
				})
			},
			// 这里补第三条显式的 task_token 清理路径。workspace_id（叶子数据）
			// 与 task_id（DeleteTaskBatch）两条线盖不住一种形状：token 自己的
			// workspace_id 指向隔壁 workspace，而它的 agent 住在本 workspace。
			sweepAgent: true,
		},
		{
			label: "issue",
			ownerFirstPage: func(ctx context.Context, limit int32) ([]pgtype.UUID, error) {
				return qtx.ListWorkspaceIssueIDFirstPage(ctx, db.ListWorkspaceIssueIDFirstPageParams{
					WorkspaceID: workspaceID, Limit: limit,
				})
			},
			ownerPage: func(ctx context.Context, cursor pgtype.UUID, limit int32) ([]pgtype.UUID, error) {
				return qtx.ListWorkspaceIssueIDPage(ctx, db.ListWorkspaceIssueIDPageParams{
					WorkspaceID: workspaceID, ID: cursor, Limit: limit,
				})
			},
			taskFirstPage: func(ctx context.Context, owner pgtype.UUID, limit int32) ([]pgtype.UUID, error) {
				return qtx.ListTaskIDsByIssueFirstPage(ctx, db.ListTaskIDsByIssueFirstPageParams{
					IssueID: owner, Limit: limit,
				})
			},
			taskPage: func(ctx context.Context, owner, cursor pgtype.UUID, limit int32) ([]pgtype.UUID, error) {
				return qtx.ListTaskIDsByIssuePage(ctx, db.ListTaskIDsByIssuePageParams{
					IssueID: owner, ID: cursor, Limit: limit,
				})
			},
		},
		{
			label: "runtime",
			ownerFirstPage: func(ctx context.Context, limit int32) ([]pgtype.UUID, error) {
				return qtx.ListWorkspaceRuntimeIDFirstPage(ctx, db.ListWorkspaceRuntimeIDFirstPageParams{
					WorkspaceID: workspaceID, Limit: limit,
				})
			},
			ownerPage: func(ctx context.Context, cursor pgtype.UUID, limit int32) ([]pgtype.UUID, error) {
				return qtx.ListWorkspaceRuntimeIDPage(ctx, db.ListWorkspaceRuntimeIDPageParams{
					WorkspaceID: workspaceID, ID: cursor, Limit: limit,
				})
			},
			taskFirstPage: func(ctx context.Context, owner pgtype.UUID, limit int32) ([]pgtype.UUID, error) {
				return qtx.ListTaskIDsByRuntimeFirstPage(ctx, db.ListTaskIDsByRuntimeFirstPageParams{
					RuntimeID: owner, Limit: limit,
				})
			},
			taskPage: func(ctx context.Context, owner, cursor pgtype.UUID, limit int32) ([]pgtype.UUID, error) {
				return qtx.ListTaskIDsByRuntimePage(ctx, db.ListTaskIDsByRuntimePageParams{
					RuntimeID: owner, ID: cursor, Limit: limit,
				})
			},
		},
	}

	for _, kind := range ownerKinds {
		owners, err := kind.ownerFirstPage(ctx, workspaceDeleteOwnerPageSize)
		if err != nil {
			return fmt.Errorf("list workspace %ss: %w", kind.label, err)
		}
		// 用 keyset 游标翻遍全部 owner：取完一页，拿最后一个 id 当下一页起点。
		for len(owners) > 0 {
			for _, ownerID := range owners {
				if err := sweepTasksForOwner(ctx, kind.label, ownerID,
					kind.taskFirstPage, kind.taskPage,
					qtx.DetachTaskBatchReferences, qtx.DeleteTaskBatch); err != nil {
					return err
				}
				if kind.sweepAgent {
					if err := qtx.DeleteTaskTokensByAgent(ctx, ownerID); err != nil {
						return fmt.Errorf("delete task tokens by agent: %w", err)
					}
				}
			}
			if owners, err = kind.ownerPage(ctx, owners[len(owners)-1], workspaceDeleteOwnerPageSize); err != nil {
				return fmt.Errorf("list workspace %ss: %w", kind.label, err)
			}
		}
	}

	return nil
}

// sweepTasksForOwner 删除单个 owner 的任务，然后回头复核该 owner。
//
// 每轮按 keyset 走完该 owner 的任务并逐页删除。一整轮什么都没删到，说明该
// owner 已经干净。后续轮次仍发现任务，说明有东西没走围栏就摸到了
// agent_task_queue——记日志并重试；轮次耗尽就让整个拆除失败回滚，而不是提交
// 一个"workspace 删了但任务残留"的结果。
func sweepTasksForOwner(
	ctx context.Context,
	label string,
	ownerID pgtype.UUID,
	taskFirstPage func(context.Context, pgtype.UUID, int32) ([]pgtype.UUID, error),
	taskPage func(context.Context, pgtype.UUID, pgtype.UUID, int32) ([]pgtype.UUID, error),
	detach func(context.Context, []pgtype.UUID) error,
	deletePage func(context.Context, []pgtype.UUID) error,
) error {
	for pass := 1; pass <= workspaceDeleteVerifyPasses; pass++ {
		deleted := 0
		taskIDs, err := taskFirstPage(ctx, ownerID, workspaceDeleteTaskPageSize)
		if err != nil {
			return fmt.Errorf("list tasks by %s: %w", label, err)
		}
		for len(taskIDs) > 0 {
			if err := detach(ctx, taskIDs); err != nil {
				return fmt.Errorf("detach task page references: %w", err)
			}
			if err := deletePage(ctx, taskIDs); err != nil {
				return fmt.Errorf("delete task page: %w", err)
			}
			deleted += len(taskIDs)
			if taskIDs, err = taskPage(ctx, ownerID, taskIDs[len(taskIDs)-1], workspaceDeleteTaskPageSize); err != nil {
				return fmt.Errorf("list tasks by %s: %w", label, err)
			}
		}
		if deleted == 0 {
			return nil
		}
		if pass > 1 {
			slog.Warn("workspace delete swept tasks that appeared after the owner was fenced",
				"owner_kind", label, "owner_id", uuidToString(ownerID), "pass", pass, "deleted", deleted)
		}
	}
	return fmt.Errorf("tasks kept appearing for %s %s after %d sweep passes; refusing to commit a partial teardown",
		label, uuidToString(ownerID), workspaceDeleteVerifyPasses)
}

// lockWorkspaceTaskOwners 给所有"任务可能经由它属于本 workspace"的行加锁。
//
// 这是真实围栏之上的纵深防御。封住"清扫之后又插入"窗口的围栏是迁移 284 的
// lock_task_owner_rows——一个谓词，每条写归属的语句都在自己的 WHERE 子句里调用
// 它：先对任务 owner 背后的 workspace 行取 FOR KEY SHARE，再对 owner 行本身取，
// 这与 LockWorkspaceForDelete 在整个事务期间持有的 FOR UPDATE 冲突。它由写
// 语句调用，因此天然落在写者的事务里，且独立于旧外键。
//
// 这些 owner 行锁再添一道更窄的屏障——它们同样会挡住对"已存在任务"的换归属
// UPDATE——而且持有它们不花钱，因为拆除几行之后就要删掉这些行本身。它们是
// set-based 的、不返回 id，owner 集再大的 workspace 在这里也不占内存。
//
// 等待被 SET LOCAL lock_timeout 封顶；取这些锁也可能与"以相反顺序加锁（先任务
// 行、再 owner）的并发任务更新"死锁；两种结局都以可重试的 503 浮出。
func lockWorkspaceTaskOwners(ctx context.Context, qtx *db.Queries, workspaceID pgtype.UUID) error {
	if err := qtx.LockWorkspaceTaskOwnerAgents(ctx, workspaceID); err != nil {
		return fmt.Errorf("lock workspace agents: %w", err)
	}
	if err := qtx.LockWorkspaceTaskOwnerIssues(ctx, workspaceID); err != nil {
		return fmt.Errorf("lock workspace issues: %w", err)
	}
	if err := qtx.LockWorkspaceTaskOwnerRuntimes(ctx, workspaceID); err != nil {
		return fmt.Errorf("lock workspace runtimes: %w", err)
	}
	return nil
}

// DeleteWorkspace 删除整个 workspace：先作废全体成员的成员缓存，再在单个大
// 事务里依次加锁（workspace 行 → 全部 chat_session 行 → 任务 owner 行 → 全局
// 汇总锁）并按依赖顺序分批删光任务与各资源行。锁等待统一受
// workspaceDeleteLockTimeout 约束，瞬时锁失败回 503 可重试。只有 owner 能执行。
// 提交后清理对象存储附件、发事件并通知成员 daemon 刷新。
func (h *Handler) DeleteWorkspace(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromURL(r, "id")

	// 纵深防御：路由已用 RequireWorkspaceRoleFromURL("owner") 中间件挡过，这里
	// 再查一遍，让本 handler 无论怎么接线都安全（测试里直接调用、将来路由重构
	// 等）。
	requester, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return
	}
	if requester.Role != "owner" {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}

	// 删除前先作废全体成员的成员缓存。CASCADE 删掉成员行之后，缓存条目只是无
	// 害的孤儿（对已删 workspace 的下游查询会失败），但主动作废能消除最长到 TTL
	// 的"读到过期成员关系"窗口。
	var affectedUserIDs []string
	if members, err := h.Queries.ListMembers(r.Context(), requester.WorkspaceID); err == nil {
		affectedUserIDs = make([]string, 0, len(members))
		for _, m := range members {
			userID := uuidToString(m.UserID)
			h.MembershipCache.Invalidate(r.Context(), userID, workspaceID)
			affectedUserIDs = append(affectedUserIDs, userID)
		}
	}

	// 拆除在一个事务里进行，这样 DeleteWorkspace 清扫 chat_draft_restore 时，
	// 下面的 chat_session 行锁仍然有效。没有这些锁，FinalizeDeferredCancelledChat
	// 可能在清扫取完快照之后、为其中某个会话提交一条 restore：会话被级联删掉，
	// restore 没有可跟随的外键（MUL-3515）也没有收割器，用户的提示就被永久搁浅
	// （#5219）。终结器在插入前取同一把锁，所以它要么阻塞到会话消失后跳过插入，
	// 要么先提交而让清扫看到它的行。
	//
	// workspace 行最先加锁，因为会话锁只覆盖已存在的会话：删除窗口期内提交的
	// CreateChatSession 会偷偷塞进一个谁都没锁的会话，其 restore 会以同样方式
	// 活过级联。持有 workspace 行 FOR UPDATE，会在会话插入走 workspace 外键
	// （FOR KEY SHARE）时把它挡住。
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		slog.Warn("begin workspace delete tx failed", append(logger.RequestAttrs(r), "error", err, "workspace_id", workspaceID)...)
		writeError(w, http.StatusInternalServerError, "failed to delete workspace")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	var sourceContextAttachmentURLs []string
	var sourceContextIntentURLs []string

	// SET LOCAL 是事务级作用域，事务 COMMIT/ROLLBACK 后 pgxpool 会把这条连接
	// 以默认（无上限的）lock_timeout 还回池子。它约束的是"等锁"而不是拆除本身
	// 的工作量——删大 workspace 本来就允许想删多久删多久。
	lockTimeoutMs := int(effectiveWorkspaceDeleteLockTimeout() / time.Millisecond)
	if _, err := tx.Exec(r.Context(), fmt.Sprintf("SET LOCAL lock_timeout = %d", lockTimeoutMs)); err != nil {
		failWorkspaceDelete(w, r, workspaceID, "set lock timeout", err)
		return
	}

	if _, err := qtx.LockWorkspaceForDelete(r.Context(), requester.WorkspaceID); err != nil {
		failWorkspaceDelete(w, r, workspaceID, "lock workspace", err)
		return
	}

	if _, err := qtx.LockChatSessionsByWorkspace(r.Context(), requester.WorkspaceID); err != nil {
		failWorkspaceDelete(w, r, workspaceID, "lock chat sessions", err)
		return
	}
	if sourceContextAttachmentURLs, err = qtx.ListSourceContextAttachmentURLsByWorkspace(r.Context(), requester.WorkspaceID); err != nil {
		failWorkspaceDelete(w, r, workspaceID, "list source context attachment objects", err)
		return
	}
	if sourceContextIntentURLs, err = qtx.ListSourceContextObjectIntentURLsByWorkspace(r.Context(), requester.WorkspaceID); err != nil {
		failWorkspaceDelete(w, r, workspaceID, "list source context pending objects", err)
		return
	}

	// 关系图保持在应用层处理。每个步骤都是按 workspace_id 范围执行的集合式
	// DELETE；旧级联只作为展开阶段的兜底安全网，直到未来的 schema 合约替换它。
	ctx := r.Context()
	var workspaceDeletedEvent events.Event
	if err := deleteDeliveryData(ctx, tx, workspaceID, ""); err != nil {
		failWorkspaceDelete(w, r, workspaceID, "delete delivery data", err)
		return
	}
	if err := deleteSupervisionData(ctx, tx, workspaceID, ""); err != nil {
		failWorkspaceDelete(w, r, workspaceID, "delete supervision data", err)
		return
	}
	deleteSteps := []struct {
		name string
		run  func() error
	}{
		{
			name: "set teardown mode",
			run:  func() error { return qtx.SetWorkspaceTeardownMode(ctx) },
		},
		{
			// 封住本事务后续的任务入队/改派。必须在任何任务清扫之前执行——见
			// lockWorkspaceTaskOwners。
			name: "lock task owners",
			run:  func() error { return lockWorkspaceTaskOwners(ctx, qtx, requester.WorkspaceID) },
		},
		{
			name: "prepare relationship graph",
			run:  func() error { return qtx.PrepareWorkspaceDeletionLinks(ctx, requester.WorkspaceID) },
		},
		{
			// Life 维护自己派生的关系图与认知队列。在 source 任务、评论、
			// 项目与聊天行消失之前清掉它；上下文状态等图触发器跑完后再清一次。
			name: "delete life data",
			run: func() error {
				if err := qtx.DeleteWorkspaceLifeData(ctx, requester.WorkspaceID); err != nil {
					return err
				}
				return qtx.DeleteWorkspaceLifeContextState(ctx, requester.WorkspaceID)
			},
		},
		{
			// Outbox 行是 workspace 持久历史的一部分。在 source 行消失前移除
			// 它们，让没有信封 workspace_id 的旧事件仍能通过其 owner 解析。
			name: "delete durable events",
			run:  func() error { return qtx.DeleteWorkspaceDomainEvents(ctx, requester.WorkspaceID) },
		},
		{
			name: "delete chat pins",
			run:  func() error { return qtx.DeleteChatPinnedAgentsByWorkspace(ctx, requester.WorkspaceID) },
		},
		{
			// 这是第一个碰 usage 汇总的阶段。把全局汇总锁放到关系准备之后，让
			// 无关的 workspace 只经历尽可能短的汇总窗口。这次等待由
			// workspaceDeleteLockTimeout 封顶——4246 被批处理任务持有，这里若不
			// 封顶，删除就会挂死。
			name: "lock task usage rollup",
			run:  func() error { return qtx.LockTaskUsageRollupForWorkspaceDelete(ctx) },
		},
		{
			// 有界分批，放在汇总锁之后是因为它要删 task_usage。它替代了旧叶子
			// 数据语句里按任务键的两条臂，以及那条独立的"整个 workspace 删任务"。
			name: "delete tasks",
			run:  func() error { return deleteWorkspaceTasks(ctx, qtx, requester.WorkspaceID) },
		},
		{
			name: "delete leaf data",
			run:  func() error { return qtx.DeleteWorkspaceLeafData(ctx, requester.WorkspaceID) },
		},
		{
			name: "delete autopilot runs",
			run:  func() error { return qtx.DeleteWorkspaceAutopilotRuns(ctx, requester.WorkspaceID) },
		},
		{
			name: "delete chat messages",
			run:  func() error { return qtx.DeleteWorkspaceChatMessages(ctx, requester.WorkspaceID) },
		},
		{
			name: "delete communication roots",
			run:  func() error { return qtx.DeleteWorkspaceCommunicationRoots(ctx, requester.WorkspaceID) },
		},
		{
			name: "delete comments",
			run:  func() error { return qtx.DeleteWorkspaceComments(ctx, requester.WorkspaceID) },
		},
		// source-context 的对象 intent 要留到 workspace 行消失之后。它是一份持久
		// 的重试账本，登记"在加 workspace 锁之前就开始了上传、却在本事务的即时
		// 对象删除之后才完成"的上传。没有附件残留时，reconciler 会先删对象再删
		// intent；若在这里就删账本，崩溃窗口会让迟到的对象永久泄漏。
		{
			name: "delete source context attachments",
			run:  func() error { return qtx.DeleteSourceContextAttachmentsByWorkspace(ctx, requester.WorkspaceID) },
		},
		{
			name: "delete source contexts",
			run: func() error {
				_, err := qtx.DeleteIssueSourceContextsForWorkspace(ctx, requester.WorkspaceID)
				return err
			},
		},
		{
			name: "delete issue roots",
			run:  func() error { return qtx.DeleteWorkspaceIssueRoots(ctx, requester.WorkspaceID) },
		},
		{name: "delete private meal data", run: func() error {
			_, err := tx.Exec(ctx, `DELETE FROM meal_workspace WHERE workspace_id=$1`, requester.WorkspaceID)
			return err
		}},
		{name: "delete private body data", run: func() error { return qtx.DeleteWorkspaceHealth(ctx, requester.WorkspaceID) }},
		{
			// issue_status 按项目规则不带外键，所以显式清扫它的行。放在 issue
			// 删除之后，保证不会有 issue 行活得比它的状态键要解析的状态目录久。
			// （MUL-6243）
			name: "delete issue statuses",
			run: func() error {
				return qtx.DeleteIssueStatusEntriesForWorkspace(ctx, requester.WorkspaceID)
			},
		},
		{
			name: "delete autopilot children",
			run:  func() error { return qtx.DeleteWorkspaceAutopilotChildren(ctx, requester.WorkspaceID) },
		},
		{
			name: "delete autopilots",
			run:  func() error { return qtx.DeleteWorkspaceAutopilots(ctx, requester.WorkspaceID) },
		},
		{
			name: "delete pull requests",
			run:  func() error { return qtx.DeleteWorkspacePullRequests(ctx, requester.WorkspaceID) },
		},
		{
			name: "delete integrations",
			run:  func() error { return qtx.DeleteWorkspaceConnections(ctx, requester.WorkspaceID) },
		},
		{
			name: "delete squads and skills",
			run:  func() error { return qtx.DeleteWorkspaceSquadsAndSkills(ctx, requester.WorkspaceID) },
		},
		{
			name: "delete plugin data",
			run:  func() error { return qtx.DeleteWorkspacePluginData(ctx, requester.WorkspaceID) },
		},
		{
			name: "delete agents",
			run:  func() error { return qtx.DeleteWorkspaceAgents(ctx, requester.WorkspaceID) },
		},
		{
			name: "delete runtimes and projects",
			run:  func() error { return qtx.DeleteWorkspaceRuntimesAndProjects(ctx, requester.WorkspaceID) },
		},
		{
			name: "delete administration data",
			run:  func() error { return qtx.DeleteWorkspaceAdministration(ctx, requester.WorkspaceID) },
		},
		{
			// 走到这里说明 workspaceMember 已解析成功 → workspaceID 是合法 UUID，
			// 直接复用解析结果。这句收尾语句同时清扫展开阶段遗留的兼容数据。
			name: "delete workspace",
			run: func() error {
				if err := qtx.DeleteWorkspace(ctx, requester.WorkspaceID); err != nil {
					return err
				}
				var eventErr error
				workspaceDeletedEvent, eventErr = service.RecordDurableEventTx(ctx, qtx, buildWorkspaceDomainEvent(
					protocol.EventWorkspaceDeleted, db.Workspace{ID: requester.WorkspaceID}, "member", requestUserID(r), nil,
				))
				return eventErr
			},
		},
	}
	for _, step := range deleteSteps {
		if err := step.run(); err != nil {
			failWorkspaceDelete(w, r, workspaceID, step.name, err)
			return
		}
	}

	if err := tx.Commit(r.Context()); err != nil {
		slog.Warn("commit workspace delete failed", append(logger.RequestAttrs(r), "error", err, "workspace_id", workspaceID)...)
		writeError(w, http.StatusInternalServerError, "failed to delete workspace")
		return
	}
	h.deleteS3Objects(r.Context(), append(sourceContextAttachmentURLs, sourceContextIntentURLs...))
	if err := os.RemoveAll(filepath.Join(healthPrivateRoot(), uuidToString(requester.WorkspaceID))); err != nil {
		slog.Error("清理工作区体检文件失败", "workspace_id", workspaceID)
	}

	slog.Info("workspace deleted", append(logger.RequestAttrs(r), "workspace_id", workspaceID)...)
	h.publishEvent(workspaceDeletedEvent)
	h.notifyDaemonWorkspacesChanged(affectedUserIDs...)

	w.WriteHeader(http.StatusNoContent)
}
