// 职责定位：已保存的 issue 视图（MUL-4796）：服务端存的过滤器定义 + 展示默认值 CRUD。
package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// 已保存的 issue 视图（MUL-4796）：带展示默认值的服务端过滤器定义。`query` 是视图的共享
// 身份；`display` 只播种成员的首次打开。两者对服务端都是不透明 JSON——解释权在客户端的
// definition_version 契约里。

// issueViewNameMaxLen 是视图名称的字符上限（1-80）。
const issueViewNameMaxLen = 80

// issueViewBodyMaxBytes 限制所有保存视图的写入。query/display blob 是过滤器文档——真实值就
// 几 KB；这个上限是滥用兜底，舒适地高于任何合法 payload。
const issueViewBodyMaxBytes = 128 * 1024

// issueViewsPerOwnerMax 限制每个成员在每个工作区的表增长。
const issueViewsPerOwnerMax = 100

// 允许的 scope 取值组合：scope_type 与 scope_variant 配对才有意义——my 的变体是「我」的关系
// 收窄（assigned/created/involved/any），workspace 的变体是受理人类型收窄（members/agents），
// visibility 决定视图是仅自己可见还是整工作区可见。
var (
	validIssueViewScopeTypes        = []string{"workspace", "my", "project"}
	validIssueViewMyVariants        = []string{"assigned", "created", "involved", "any"}
	validIssueViewWorkspaceVariants = []string{"members", "agents"}
	validIssueViewVisibilities      = []string{"private", "workspace"}
)

// validateIssueViewVariant 在给定 scope_type 下校验 scope_variant 输入并返回 pgtype 值；
// 配对非法时 ok=false。workspace/project 变体是可选的受理人类型收窄（NULL = 不限的 All 标签页）；
// my 变体必填。
func validateIssueViewVariant(scopeType string, variant *string) (pgtype.Text, bool) {
	switch scopeType {
	case "my":
		if variant == nil || !contains(validIssueViewMyVariants, *variant) {
			return pgtype.Text{}, false
		}
		return pgtype.Text{String: *variant, Valid: true}, true
	default: // workspace 与 project 两种 scope_type 共用此默认分支
		if variant == nil || *variant == "" || *variant == "all" {
			return pgtype.Text{}, true
		}
		if !contains(validIssueViewWorkspaceVariants, *variant) {
			return pgtype.Text{}, false
		}
		return pgtype.Text{String: *variant, Valid: true}, true
	}
}

// IssueViewResponse 是已保存视图的响应体。
type IssueViewResponse struct {
	ID                string          `json:"id"`
	WorkspaceID       string          `json:"workspace_id"`
	OwnerID           string          `json:"owner_id"`
	Name              string          `json:"name"`
	ScopeType         string          `json:"scope_type"`
	ScopeID           *string         `json:"scope_id"`
	ScopeVariant      *string         `json:"scope_variant"`
	Visibility        string          `json:"visibility"`
	DefinitionVersion int32           `json:"definition_version"`
	Query             json.RawMessage `json:"query"`
	Display           json.RawMessage `json:"display"`
	Revision          int32           `json:"revision"`
	CreatedAt         string          `json:"created_at"`
	UpdatedAt         string          `json:"updated_at"`
}

// issueViewToResponse 把数据库行转成视图响应体（uuid / 时间戳字段统一格式化）。
func issueViewToResponse(v db.IssueView) IssueViewResponse {
	return IssueViewResponse{
		ID:                uuidToString(v.ID),
		WorkspaceID:       uuidToString(v.WorkspaceID),
		OwnerID:           uuidToString(v.OwnerID),
		Name:              v.Name,
		ScopeType:         v.ScopeType,
		ScopeID:           uuidToPtr(v.ScopeID),
		ScopeVariant:      textToPtr(v.ScopeVariant),
		Visibility:        v.Visibility,
		DefinitionVersion: v.DefinitionVersion,
		Query:             json.RawMessage(v.Query),
		Display:           json.RawMessage(v.Display),
		Revision:          v.Revision,
		CreatedAt:         timestampToString(v.CreatedAt),
		UpdatedAt:         timestampToString(v.UpdatedAt),
	}
}

// canReadIssueView 判断调用者能否读取该视图：本人创建的总是可读；visibility=workspace 的共享
// 视图对任意工作区成员开放。（my 作用域的视图被 DB CHECK 限定为只能 private，所以它们永远只会
// 命中「本人」这一分支。）
func canReadIssueView(v db.IssueView, userID pgtype.UUID) bool {
	return v.OwnerID == userID || v.Visibility == "workspace"
}

// isJSONObject 校验 payload 是「非 null」的 JSON 对象。null 也能解成 map 且不报错，这里显式
// 拒绝它，而不是放行后让 DB CHECK 把它变成一次 500。
func isJSONObject(raw json.RawMessage) bool {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return false
	}
	return m != nil
}

// CreateIssueViewRequest 是创建已保存视图的请求体（query 为过滤器文档、display 为展示默认值，
// 均为不透明 JSON）。
type CreateIssueViewRequest struct {
	Name              string          `json:"name"`
	ScopeType         string          `json:"scope_type"`
	ScopeID           *string         `json:"scope_id"`
	ScopeVariant      *string         `json:"scope_variant"`
	Visibility        string          `json:"visibility"`
	DefinitionVersion int32           `json:"definition_version"`
	Query             json.RawMessage `json:"query"`
	Display           json.RawMessage `json:"display"`
}

// CreateIssueView 处理 POST .../issue-views：创建一条已保存视图。校验：名称 1-80 字符、每
// owner 上限、scope 组合、visibility（缺省 private）、definition_version（缺省 1）、query 必须
// 是 JSON 对象（display 缺省 {}）。成功返回 201 + 视图。
func (h *Handler) CreateIssueView(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, h.resolveWorkspaceID(r), "workspace id")
	if !ok {
		return
	}

	var req CreateIssueViewRequest
	r.Body = http.MaxBytesReader(w, r.Body, issueViewBodyMaxBytes)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if l := len([]rune(req.Name)); l < 1 || l > issueViewNameMaxLen {
		writeError(w, http.StatusBadRequest, "name must be between 1 and 80 characters")
		return
	}
	ownedCount, err := h.Queries.CountIssueViewsByOwner(r.Context(), db.CountIssueViewsByOwnerParams{
		WorkspaceID: wsUUID,
		OwnerID:     parseUUID(userID),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to check view quota")
		return
	}
	if ownedCount >= issueViewsPerOwnerMax {
		writeError(w, http.StatusBadRequest, "view limit reached for this workspace")
		return
	}
	if !contains(validIssueViewScopeTypes, req.ScopeType) {
		writeError(w, http.StatusBadRequest, "invalid scope_type")
		return
	}
	if req.Visibility == "" {
		req.Visibility = "private"
	}
	if !contains(validIssueViewVisibilities, req.Visibility) {
		writeError(w, http.StatusBadRequest, "invalid visibility")
		return
	}
	if req.DefinitionVersion <= 0 {
		req.DefinitionVersion = 1
	}
	if len(req.Query) == 0 || !isJSONObject(req.Query) {
		writeError(w, http.StatusBadRequest, "query must be a JSON object")
		return
	}
	if len(req.Display) == 0 {
		req.Display = json.RawMessage("{}")
	}
	if !isJSONObject(req.Display) {
		writeError(w, http.StatusBadRequest, "display must be a JSON object")
		return
	}

	scopeVariant, variantOK := validateIssueViewVariant(req.ScopeType, req.ScopeVariant)
	if !variantOK {
		writeError(w, http.StatusBadRequest, "invalid scope_variant for this scope_type")
		return
	}
	var scopeID pgtype.UUID
	switch req.ScopeType {
	case "project":
		if req.ScopeID == nil {
			writeError(w, http.StatusBadRequest, "scope_id is required for project views")
			return
		}
		projUUID, ok := parseUUIDOrBadRequest(w, *req.ScopeID, "scope_id")
		if !ok {
			return
		}
		// 项目必须存在于本工作区——挂在外部或已删除项目上的视图既不可达，也可能跨租户泄漏。
		if _, err := h.Queries.GetProjectInWorkspace(r.Context(), db.GetProjectInWorkspaceParams{
			ID: projUUID, WorkspaceID: wsUUID,
		}); err != nil {
			writeError(w, http.StatusNotFound, "project not found")
			return
		}
		scopeID = projUUID
	case "my":
		// My Issues 是个人视角，把它共享出去没有意义。
		req.Visibility = "private"
	}

	view, err := h.Queries.CreateIssueView(r.Context(), db.CreateIssueViewParams{
		WorkspaceID:       wsUUID,
		OwnerID:           parseUUID(userID),
		Name:              req.Name,
		ScopeType:         req.ScopeType,
		ScopeID:           scopeID,
		ScopeVariant:      scopeVariant,
		Visibility:        req.Visibility,
		DefinitionVersion: req.DefinitionVersion,
		Query:             req.Query,
		Display:           req.Display,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create view")
		return
	}
	writeJSON(w, http.StatusCreated, issueViewToResponse(view))
}

// ListIssueViews 处理 GET .../issue-views：列出当前用户可读的视图（自己创建的 + 工作区可见
// 的共享视图）。
func (h *Handler) ListIssueViews(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, h.resolveWorkspaceID(r), "workspace id")
	if !ok {
		return
	}

	scopeType := r.URL.Query().Get("scope_type")
	if !contains(validIssueViewScopeTypes, scopeType) {
		writeError(w, http.StatusBadRequest, "invalid scope_type")
		return
	}
	var scopeID pgtype.UUID
	if raw := r.URL.Query().Get("scope_id"); raw != "" {
		id, ok := parseUUIDOrBadRequest(w, raw, "scope_id")
		if !ok {
			return
		}
		scopeID = id
	}

	views, err := h.Queries.ListIssueViewsForUser(r.Context(), db.ListIssueViewsForUserParams{
		WorkspaceID: wsUUID,
		ScopeType:   scopeType,
		OwnerID:     parseUUID(userID),
		ScopeID:     scopeID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list views")
		return
	}
	resp := make([]IssueViewResponse, len(views))
	for i, v := range views {
		resp[i] = issueViewToResponse(v)
	}
	writeJSON(w, http.StatusOK, resp)
}

// loadIssueViewForUser 解析视图 id、强制限定在工作区内，并执行读授权。未授权与不存在统一返回
// 404，避免暴露某条 private 视图是否存在。
func (h *Handler) loadIssueViewForUser(w http.ResponseWriter, r *http.Request, userID string) (db.IssueView, pgtype.UUID, bool) {
	wsUUID, ok := parseUUIDOrBadRequest(w, h.resolveWorkspaceID(r), "workspace id")
	if !ok {
		return db.IssueView{}, pgtype.UUID{}, false
	}
	idUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "view id")
	if !ok {
		return db.IssueView{}, pgtype.UUID{}, false
	}
	view, err := h.Queries.GetIssueView(r.Context(), db.GetIssueViewParams{
		ID: idUUID, WorkspaceID: wsUUID,
	})
	if err != nil || !canReadIssueView(view, parseUUID(userID)) {
		writeError(w, http.StatusNotFound, "view not found")
		return db.IssueView{}, pgtype.UUID{}, false
	}
	return view, wsUUID, true
}

// GetIssueViewByID 处理 GET .../issue-views/:id：返回单条视图详情。
func (h *Handler) GetIssueViewByID(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	view, _, ok := h.loadIssueViewForUser(w, r, userID)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, issueViewToResponse(view))
}

// canManageIssueView 判断调用者能否管理该视图：本人创建的可以；workspace 可见的共享视图则要求
// 调用者是工作区 owner/admin。别人的 private 视图在 loader 阶段就已不可见（404），管理员权限
// 永远够不到它们。
func (h *Handler) canManageIssueView(r *http.Request, view db.IssueView, userID string) bool {
	if view.OwnerID == parseUUID(userID) {
		return true
	}
	if view.Visibility != "workspace" {
		return false
	}
	member, err := h.Queries.GetMemberByUserAndWorkspace(r.Context(), db.GetMemberByUserAndWorkspaceParams{
		UserID:      parseUUID(userID),
		WorkspaceID: view.WorkspaceID,
	})
	if err != nil {
		return false
	}
	return roleAllowed(member.Role, "owner", "admin")
}

// UpdateIssueViewRequest 是更新视图的请求体（可改名/可见性/scope_variant/query/display，
// ExpectedRevision 做乐观并发）。
type UpdateIssueViewRequest struct {
	Name             *string         `json:"name"`
	Visibility       *string         `json:"visibility"`
	ScopeVariant     *string         `json:"scope_variant"`
	Query            json.RawMessage `json:"query"`
	Display          json.RawMessage `json:"display"`
	ExpectedRevision int32           `json:"expected_revision"`
}

// UpdateIssueView 处理 PATCH .../issue-views/:id：更新视图内容。权限同 canManageIssueView。
func (h *Handler) UpdateIssueView(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	view, wsUUID, ok := h.loadIssueViewForUser(w, r, userID)
	if !ok {
		return
	}
	if !h.canManageIssueView(r, view, userID) {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}

	var req UpdateIssueViewRequest
	r.Body = http.MaxBytesReader(w, r.Body, issueViewBodyMaxBytes)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.ExpectedRevision <= 0 {
		writeError(w, http.StatusBadRequest, "expected_revision is required")
		return
	}

	name := view.Name
	if req.Name != nil {
		if l := len([]rune(*req.Name)); l < 1 || l > issueViewNameMaxLen {
			writeError(w, http.StatusBadRequest, "name must be between 1 and 80 characters")
			return
		}
		name = *req.Name
	}
	visibility := view.Visibility
	if req.Visibility != nil {
		if !contains(validIssueViewVisibilities, *req.Visibility) {
			writeError(w, http.StatusBadRequest, "invalid visibility")
			return
		}
		if view.ScopeType == "my" && *req.Visibility != "private" {
			writeError(w, http.StatusBadRequest, "my views are always private")
			return
		}
		visibility = *req.Visibility
	}
	query := json.RawMessage(view.Query)
	if len(req.Query) > 0 {
		if !isJSONObject(req.Query) {
			writeError(w, http.StatusBadRequest, "query must be a JSON object")
			return
		}
		query = req.Query
	}
	display := json.RawMessage(view.Display)
	if len(req.Display) > 0 {
		if !isJSONObject(req.Display) {
			writeError(w, http.StatusBadRequest, "display must be a JSON object")
			return
		}
		display = req.Display
	}
	// scope_type 本身不可改；它内部的变体可以切换。
	scopeVariant := view.ScopeVariant
	if req.ScopeVariant != nil {
		next, variantOK := validateIssueViewVariant(view.ScopeType, req.ScopeVariant)
		if !variantOK {
			writeError(w, http.StatusBadRequest, "invalid scope_variant for this scope_type")
			return
		}
		scopeVariant = next
	}

	updated, err := h.Queries.UpdateIssueView(r.Context(), db.UpdateIssueViewParams{
		ID:           view.ID,
		WorkspaceID:  wsUUID,
		Name:         name,
		Visibility:   visibility,
		ScopeVariant: scopeVariant,
		Query:        query,
		Display:      display,
		Revision:     req.ExpectedRevision,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// 行必然存在（我们刚加载过它）——返回 0 行只可能是 revision 在调用方眼皮底下被别的
			// 请求推进了。
			writeError(w, http.StatusConflict, "view was modified by someone else")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to update view")
		return
	}
	writeJSON(w, http.StatusOK, issueViewToResponse(updated))
}

// DeleteIssueView 处理 DELETE .../issue-views/:id：删除一条视图。权限同 canManageIssueView。
func (h *Handler) DeleteIssueView(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	view, wsUUID, ok := h.loadIssueViewForUser(w, r, userID)
	if !ok {
		return
	}
	if !h.canManageIssueView(r, view, userID) {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}
	if _, err := h.Queries.DeleteIssueView(r.Context(), db.DeleteIssueViewParams{
		ID: view.ID, WorkspaceID: wsUUID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete view")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
