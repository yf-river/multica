// 职责定位：标签（Label）体系的 HTTP 处理器。标签可挂在三类资源上——issue、agent、skill，
// 这里提供标签本身的增删改查，以及「资源 ↔ 标签」的挂接/解绑接口；三类资源的挂接逻辑高度
// 同构，只是绑定的资源不同、事件 StreamKey 不同。
//
// 核心约定：名称/颜色/描述在入库前先做规范化（颜色只收 #rrggbb 六位十六进制，见
// normalizeColor，避免前端把它直接当 CSS 用而变成注入面）；创建/改名都依赖数据库唯一
// 约束防重名；删除标签时必须先把三类资源的挂接关系清干净，用事务保证原子（库里不加外键
// 级联，按项目约定在应用层做）。
package handler

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/logger"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

// LabelResponse 是标签返回给前端的数据结构。UsageCount（被多少资源引用）只有列表查询
// 才有，详情/单条创建返回的映射函数（labelToResponse）不给它赋值，保持零值即可。
type LabelResponse struct {
	ID           string `json:"id"`
	WorkspaceID  string `json:"workspace_id"`
	ResourceType string `json:"resource_type"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	Color        string `json:"color"`
	UsageCount   int64  `json:"usage_count"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
}

// labelToResponse 把数据库里的单条标签记录（不含引用计数）转成接口响应结构。
func labelToResponse(l db.IssueLabel) LabelResponse {
	return LabelResponse{
		ID:           uuidToString(l.ID),
		WorkspaceID:  uuidToString(l.WorkspaceID),
		ResourceType: l.ResourceType,
		Name:         l.Name,
		Description:  l.Description,
		Color:        l.Color,
		CreatedAt:    timestampToString(l.CreatedAt),
		UpdatedAt:    timestampToString(l.UpdatedAt),
	}
}

// labelListRowToResponse 把列表查询返回的行（比 IssueLabel 多一个 UsageCount 引用数）
// 转成接口响应结构。
func labelListRowToResponse(l db.ListLabelsRow) LabelResponse {
	return LabelResponse{
		ID:           uuidToString(l.ID),
		WorkspaceID:  uuidToString(l.WorkspaceID),
		ResourceType: l.ResourceType,
		Name:         l.Name,
		Description:  l.Description,
		Color:        l.Color,
		UsageCount:   l.UsageCount,
		CreatedAt:    timestampToString(l.CreatedAt),
		UpdatedAt:    timestampToString(l.UpdatedAt),
	}
}

// labelsToResponse 批量转换，保持顺序与入参一致。
func labelsToResponse(list []db.IssueLabel) []LabelResponse {
	out := make([]LabelResponse, len(list))
	for i, l := range list {
		out[i] = labelToResponse(l)
	}
	return out
}

// CreateLabelRequest 是创建标签的请求体。ResourceType 缺省为 "issue"。
type CreateLabelRequest struct {
	ResourceType string `json:"resource_type"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	Color        string `json:"color"`
}

// UpdateLabelRequest 是更新标签的请求体。三个字段都是指针：nil 表示本次不改该字段，
// 传了非 nil 才会覆盖旧值。
type UpdateLabelRequest struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
	Color       *string `json:"color"`
}

// defaultLabelResourceType 是创建标签未指定资源类型时的兜底值。
const defaultLabelResourceType = "issue"

// parseLabelResourceType 解析并校验 resource_type 参数：空串当作缺省值 issue；
// 只接受 issue / agent / skill 三种，其它一律报错（错误信息直接给前端当 400 文案）。
func parseLabelResourceType(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return defaultLabelResourceType, nil
	}
	switch value {
	case "issue", "agent", "skill":
		return value, nil
	default:
		return "", errors.New("resource_type must be issue, agent, or skill")
	}
}

// 六位十六进制颜色，允许带或不带开头的 '#'。
var hexColorRE = regexp.MustCompile(`^#?[0-9a-fA-F]{6}$`)

// normalizeColor 把颜色统一成 "#rrggbb" 的小写形式；不合法就返回适合做 400 文案的错误。
//
// 必须守住的安全不变量：前端 LabelChip 直接把 color 当 style={{ backgroundColor: color }}
// 内联渲染。一旦这里放开校验、允许任意 CSS（命名颜色、url(...) 等），那段内联样式就变成
// 注入面。正则必须保持严格。
func normalizeColor(c string) (string, error) {
	c = strings.TrimSpace(c)
	if !hexColorRE.MatchString(c) {
		return "", errors.New("color must be a 6-digit hex value like #3b82f6")
	}
	if !strings.HasPrefix(c, "#") {
		c = "#" + c
	}
	return strings.ToLower(c), nil
}

const maxLabelNameLen = 32

// validateLabelName 校验并清理标签名，返回裁剪掉首尾空格后的名字。名字不能为空、
// 不能超过 32 个字符（按「字符数」而非字节数），且不能含控制字符（防 \n 之类混进
// JSON/展示层制造脏数据）。
func validateLabelName(raw string) (string, error) {
	for _, r := range raw {
		if unicode.IsControl(r) {
			return "", errors.New("name cannot contain tabs, newlines, or control characters")
		}
	}
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", errors.New("name is required")
	}
	if utf8.RuneCountInString(name) > maxLabelNameLen {
		return "", errors.New("name must be 32 characters or fewer")
	}
	return name, nil
}

// ---------------------------------------------------------------------------
// Handlers — label CRUD
// ---------------------------------------------------------------------------

// ListLabels 列出当前 workspace 下某类资源的全部标签。resource_type 可缺省（默认 issue），
// 返回 {"labels": [...], "total": N}。
func (h *Handler) ListLabels(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	resourceType, err := parseLabelResourceType(r.URL.Query().Get("resource_type"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	labels, err := h.Queries.ListLabels(r.Context(), db.ListLabelsParams{
		WorkspaceID: parseUUID(workspaceID), ResourceType: resourceType,
	})
	if err != nil {
		slog.Warn("ListLabels failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to list labels")
		return
	}
	resp := make([]LabelResponse, len(labels))
	for i, label := range labels {
		resp[i] = labelListRowToResponse(label)
	}
	writeJSON(w, http.StatusOK, map[string]any{"labels": resp, "total": len(resp)})
}

// GetLabel 按 id 查单条标签。id 必须属于当前 workspace（查询条件带 WorkspaceID），
// 不存在或不属于本空间统一返回 404。
func (h *Handler) GetLabel(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	workspaceID := h.resolveWorkspaceID(r)
	idUUID, ok := parseUUIDOrBadRequest(w, id, "label id")
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	label, err := h.Queries.GetLabel(r.Context(), db.GetLabelParams{
		ID: idUUID, WorkspaceID: wsUUID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "label not found")
			return
		}
		slog.Warn("GetLabel failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to get label")
		return
	}
	writeJSON(w, http.StatusOK, labelToResponse(label))
}

// CreateLabel 创建一条标签：校验名称/颜色/资源类型后开事务插入，撞到名称唯一约束返回
// 409；成功后把 EventLabelCreated 事件落进 outbox（与插入同事务），提交后再对外发布。
func (h *Handler) CreateLabel(w http.ResponseWriter, r *http.Request) {
	var req CreateLabelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	name, err := validateLabelName(req.Name)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	color, err := normalizeColor(req.Color)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	resourceType, err := parseLabelResourceType(req.ResourceType)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	workspaceID := h.resolveWorkspaceID(r)
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start label transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	label, err := qtx.CreateLabel(r.Context(), db.CreateLabelParams{
		WorkspaceID:  parseUUID(workspaceID),
		ResourceType: resourceType,
		Name:         name,
		Description:  sanitizeNullBytes(strings.TrimSpace(req.Description)),
		Color:        color,
	})
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "a label with that name already exists")
			return
		}
		slog.Warn("CreateLabel failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to create label")
		return
	}
	resp := labelToResponse(label)
	event, err := service.RecordDurableEventTx(r.Context(), qtx, buildLabelDomainEvent(protocol.EventLabelCreated, label, "member", userID, nil))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record label event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit label")
		return
	}
	h.publishEvent(event)
	writeJSON(w, http.StatusCreated, resp)
}

// UpdateLabel 部分更新一条标签（只更新请求体里非 nil 的字段）。字段逐一校验后拼进
// UPDATE 参数，成功后记录 EventLabelUpdated 事件并发布。
func (h *Handler) UpdateLabel(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	workspaceID := h.resolveWorkspaceID(r)

	var req UpdateLabelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	idUUID, ok := parseUUIDOrBadRequest(w, id, "label id")
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	params := db.UpdateLabelParams{
		ID:          idUUID,
		WorkspaceID: wsUUID,
	}
	if req.Name != nil {
		name, err := validateLabelName(*req.Name)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		params.Name = pgtype.Text{String: name, Valid: true}
	}
	if req.Description != nil {
		params.Description = pgtype.Text{String: sanitizeNullBytes(strings.TrimSpace(*req.Description)), Valid: true}
	}
	if req.Color != nil {
		color, err := normalizeColor(*req.Color)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		params.Color = pgtype.Text{String: color, Valid: true}
	}

	// 直接靠 UPDATE 返回的 pgx.ErrNoRows 分流，不再预先 GetLabel 查一次——WHERE 条件里
	// 已经同时锁定了 (id, workspace_id)，查不到只可能是「标签不存在或不在本空间」。
	// 去掉预检既消除了 check-then-act 的 TOCTOU 窗口，也省了一次数据库往返。
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start label transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	label, err := qtx.UpdateLabel(r.Context(), params)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "label not found")
			return
		}
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "a label with that name already exists")
			return
		}
		slog.Warn("UpdateLabel failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to update label")
		return
	}
	resp := labelToResponse(label)
	event, err := service.RecordDurableEventTx(r.Context(), qtx, buildLabelDomainEvent(protocol.EventLabelUpdated, label, "member", userID, nil))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record label event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit label")
		return
	}
	h.publishEvent(event)
	writeJSON(w, http.StatusOK, resp)
}

// DeleteLabel 删除一条标签。删除必须把三类资源上的挂接记录一起清掉并保持原子（项目约定
// 不用数据库外键级联，改为在应用层显式清理）；真正的删除用 :one RETURNING id，查不到
// 行就说明标签不在本空间，返回 404。成功后记 EventLabelDeleted 事件并发布。
func (h *Handler) DeleteLabel(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	workspaceID := h.resolveWorkspaceID(r)
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	idUUID, ok := parseUUIDOrBadRequest(w, id, "label id")
	if !ok {
		return
	}
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

	// 把三类资源各自的标签挂接清理和标签目录删除放进同一个事务。项目硬性约定：
	// 资源-标签关联表不加外键级联，必须由应用层显式、原子地清理。
	for _, cleanup := range []func() error{
		func() error { return qtx.DeleteIssueLabelAssignmentsByLabel(r.Context(), idUUID) },
		func() error { return qtx.DeleteAgentLabelAssignmentsByLabel(r.Context(), idUUID) },
		func() error { return qtx.DeleteSkillLabelAssignmentsByLabel(r.Context(), idUUID) },
	} {
		if err := cleanup(); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to remove label assignments")
			return
		}
	}

	// DeleteLabel 是 :one RETURNING id 查询——返回 ErrNoRows 表示标签不在当前
	// workspace（按 404 处理）；其余错误才是真正的 500。
	if _, err := qtx.DeleteLabel(r.Context(), db.DeleteLabelParams{
		ID: idUUID, WorkspaceID: wsUUID,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "label not found")
			return
		}
		slog.Warn("DeleteLabel failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to delete label")
		return
	}
	event, err := service.RecordDurableEventTx(r.Context(), qtx, buildLabelDomainEvent(protocol.EventLabelDeleted, db.IssueLabel{
		ID: idUUID, WorkspaceID: wsUUID,
	}, "member", userID, nil))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record label event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit label deletion")
		return
	}
	h.publishEvent(event)
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Handlers — issue↔label attach/detach
// ---------------------------------------------------------------------------

// AttachLabelRequest 是给资源挂标签的请求体，只带一个 label_id。
type AttachLabelRequest struct {
	LabelID string `json:"label_id"`
}

// listLabelsForIssueSafe 在挂/解成功之后读取 issue 的最新标签列表；读失败只打日志并返回
// (nil, false)。这里故意用「容忍读失败」的姿势：变更已经提交，读失败时返回空列表并广播
// 出去，会把每个订阅者的乐观状态错刷成空——所以宁可让客户端靠 query 失效自己去重拉。
func (h *Handler) listLabelsForIssueSafe(r *http.Request, issueID, workspaceID pgtype.UUID) ([]db.IssueLabel, bool) {
	labels, err := h.Queries.ListLabelsByIssue(r.Context(), db.ListLabelsByIssueParams{
		IssueID:     issueID,
		WorkspaceID: workspaceID,
	})
	if err != nil {
		slog.Warn("ListLabelsByIssue failed after mutation", append(logger.RequestAttrs(r), "error", err, "issue_id", uuidToString(issueID))...)
		return nil, false
	}
	return labels, true
}

// ListLabelsForIssue 返回某个 issue 当前挂着的标签，并顺带返回 issue_revision
// （供前端做并发一致性判断）。
func (h *Handler) ListLabelsForIssue(w http.ResponseWriter, r *http.Request) {
	issueID := chi.URLParam(r, "id")
	// 用 issue 做鉴权：issue 不在当前 workspace，调用方就不该看到它的标签。
	issue, ok := h.loadIssueForUser(w, r, issueID)
	if !ok {
		return
	}
	labels, err := h.Queries.ListLabelsByIssue(r.Context(), db.ListLabelsByIssueParams{
		IssueID:     issue.ID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		slog.Warn("ListLabelsForIssue failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to list labels")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"labels":         labelsToResponse(labels),
		"issue_revision": issue.Revision,
	})
}

// AttachLabel 给 issue 挂一个标签。issue 和 label 必须同属一个 workspace，且 label 的
// resource_type 必须是 issue。数据库侧 AttachLabelToIssue 会去重（同一条标签重复挂时
// Changed=false、不再发事件），所以幂等；挂在变化时记 EventLabelUpdated 事件并发布。
func (h *Handler) AttachLabel(w http.ResponseWriter, r *http.Request) {
	issueID := chi.URLParam(r, "id")
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	var req AttachLabelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.LabelID == "" {
		writeError(w, http.StatusBadRequest, "label_id is required")
		return
	}

	// issue 和 label 都必须属于当前 workspace，缺一不可。
	issue, ok := h.loadIssueForUser(w, r, issueID)
	if !ok {
		return
	}
	labelID, ok := parseUUIDOrBadRequest(w, req.LabelID, "label_id")
	if !ok {
		return
	}
	label, err := h.Queries.GetLabel(r.Context(), db.GetLabelParams{
		ID: labelID, WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "label not found")
			return
		}
		slog.Warn("GetLabel in AttachLabel failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to attach label")
		return
	}
	if label.ResourceType != "issue" {
		writeError(w, http.StatusNotFound, "issue label not found")
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start label transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	attached, err := qtx.AttachLabelToIssue(r.Context(), db.AttachLabelToIssueParams{
		IssueID:     issue.ID,
		LabelID:     labelID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		slog.Warn("AttachLabelToIssue failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to attach label")
		return
	}

	// 挂好后再读一遍最新标签列表；此时若读取失败，挂接其实已经成功了——就返回成功但不
	// 带 labels 正文（客户端会靠 query 失效重拉），也不广播，避免拿一张错误的空列表覆盖
	// 每个订阅者的乐观状态。
	labels, err := qtx.ListLabelsByIssue(r.Context(), db.ListLabelsByIssueParams{IssueID: issue.ID, WorkspaceID: issue.WorkspaceID})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load labels after attach")
		return
	}
	resp := labelsToResponse(labels)
	var event events.Event
	if attached.Changed {
		event, err = service.RecordDurableEventTx(r.Context(), qtx, buildIssueLabelsChangedEvent(issue, "member", userID, resp, attached.IssueRevision))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to record label event")
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit label change")
		return
	}
	if attached.Changed {
		h.publishEvent(event)
	}
	payload := map[string]any{"labels": resp}
	if attached.IssueRevision > 0 {
		payload["issue_revision"] = attached.IssueRevision
	}
	writeJSON(w, http.StatusOK, payload)
}

// DetachLabel 把标签从 issue 上解下来，逻辑与 AttachLabel 镜像。同 workspace 校验在此
// 格外重要：若省略，一个捏造了别人空间 label_id 的请求会静默「无事发生」地返回 200——
// 这种假成功比显式 404 更糟。
func (h *Handler) DetachLabel(w http.ResponseWriter, r *http.Request) {
	issueID := chi.URLParam(r, "id")
	labelID := chi.URLParam(r, "labelId")
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	// 解绑前先确认 issue 和 label 都在本 workspace（与 AttachLabel 的校验互为镜像）。
	// 少了这一步，一个带了外部 labelID 的伪造请求会「静默无事发生」地返回 200——
	// 假成功比显式 404 更糟。
	issue, ok := h.loadIssueForUser(w, r, issueID)
	if !ok {
		return
	}
	labelUUID, ok := parseUUIDOrBadRequest(w, labelID, "label id")
	if !ok {
		return
	}
	label, err := h.Queries.GetLabel(r.Context(), db.GetLabelParams{
		ID: labelUUID, WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "label not found")
			return
		}
		slog.Warn("GetLabel in DetachLabel failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to detach label")
		return
	}
	if label.ResourceType != "issue" {
		writeError(w, http.StatusNotFound, "issue label not found")
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start label transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	detached, err := qtx.DetachLabelFromIssue(r.Context(), db.DetachLabelFromIssueParams{
		IssueID:     issue.ID,
		LabelID:     labelUUID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		slog.Warn("DetachLabelFromIssue failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to detach label")
		return
	}

	labels, err := qtx.ListLabelsByIssue(r.Context(), db.ListLabelsByIssueParams{IssueID: issue.ID, WorkspaceID: issue.WorkspaceID})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load labels after detach")
		return
	}
	resp := labelsToResponse(labels)
	var event events.Event
	if detached.Changed {
		event, err = service.RecordDurableEventTx(r.Context(), qtx, buildIssueLabelsChangedEvent(issue, "member", userID, resp, detached.IssueRevision))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to record label event")
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit label change")
		return
	}
	if detached.Changed {
		h.publishEvent(event)
	}
	payload := map[string]any{"labels": resp}
	if detached.IssueRevision > 0 {
		payload["issue_revision"] = detached.IssueRevision
	}
	writeJSON(w, http.StatusOK, payload)
}

// ---------------------------------------------------------------------------
// Handlers — agent/skill↔label attach/detach
// ---------------------------------------------------------------------------

// ListLabelsForAgent 返回某个 agent 当前挂着的全部标签（列表查询自带 workspace 过滤）。
func (h *Handler) ListLabelsForAgent(w http.ResponseWriter, r *http.Request) {
	agent, ok := h.loadAgentForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	labels, err := h.Queries.ListLabelsByAgent(r.Context(), db.ListLabelsByAgentParams{
		AgentID: agent.ID, WorkspaceID: agent.WorkspaceID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list agent labels")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"labels": labelsToResponse(labels)})
}

// AttachLabelToAgent 给 agent 挂标签。与 issue 版本不同，agent 需要管理权限
// （canManageAgent）；挂接成功发出 EventLabelUpdated 事件后，直接内部转调
// ListLabelsForAgent 把最新列表作为响应返回。
func (h *Handler) AttachLabelToAgent(w http.ResponseWriter, r *http.Request) {
	agent, ok := h.loadAgentForUser(w, r, chi.URLParam(r, "id"))
	if !ok || !h.canManageAgent(w, r, agent) {
		return
	}
	var req AttachLabelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.LabelID == "" {
		writeError(w, http.StatusBadRequest, "label_id is required")
		return
	}
	labelID, ok := parseUUIDOrBadRequest(w, req.LabelID, "label_id")
	if !ok {
		return
	}
	label, err := h.Queries.GetLabel(r.Context(), db.GetLabelParams{ID: labelID, WorkspaceID: agent.WorkspaceID})
	if err != nil || label.ResourceType != "agent" {
		writeError(w, http.StatusNotFound, "agent label not found")
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start label transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	if err := qtx.AttachLabelToAgent(r.Context(), db.AttachLabelToAgentParams{
		AgentID: agent.ID, LabelID: labelID, WorkspaceID: agent.WorkspaceID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to attach agent label")
		return
	}
	event, err := service.RecordDurableEventTx(r.Context(), qtx, events.Event{
		Type:           protocol.EventLabelUpdated,
		IdempotencyKey: "label:agent_attach:" + uuidToString(agent.ID) + ":" + uuidToString(labelID),
		StreamKey:      "agent:" + uuidToString(agent.ID), WorkspaceID: uuidToString(agent.WorkspaceID),
		ActorType: "member", ActorID: requestUserID(r),
		Payload: map[string]any{"label": labelToResponse(label), "resource_type": "agent", "agent_id": uuidToString(agent.ID)},
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record label event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit label change")
		return
	}
	h.publishEvent(event)
	h.ListLabelsForAgent(w, r)
}

// DetachLabelFromAgent 把标签从 agent 上解下来，同样要求管理权限，成功后发布事件并
// 转调 ListLabelsForAgent 返回最新列表。
func (h *Handler) DetachLabelFromAgent(w http.ResponseWriter, r *http.Request) {
	agent, ok := h.loadAgentForUser(w, r, chi.URLParam(r, "id"))
	if !ok || !h.canManageAgent(w, r, agent) {
		return
	}
	labelID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "labelId"), "label id")
	if !ok {
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start label transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	if err := qtx.DetachLabelFromAgent(r.Context(), db.DetachLabelFromAgentParams{
		AgentID: agent.ID, LabelID: labelID, WorkspaceID: agent.WorkspaceID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to detach agent label")
		return
	}
	event, err := service.RecordDurableEventTx(r.Context(), qtx, events.Event{
		Type: protocol.EventLabelUpdated, IdempotencyKey: "label:agent_detach:" + uuidToString(agent.ID) + ":" + uuidToString(labelID),
		StreamKey: "agent:" + uuidToString(agent.ID), WorkspaceID: uuidToString(agent.WorkspaceID), ActorType: "member", ActorID: requestUserID(r),
		Payload: map[string]any{"label_id": uuidToString(labelID), "resource_type": "agent", "agent_id": uuidToString(agent.ID)},
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record label event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit label change")
		return
	}
	h.publishEvent(event)
	h.ListLabelsForAgent(w, r)
}

// ListLabelsForSkill 返回某个 skill 当前挂着的全部标签。
func (h *Handler) ListLabelsForSkill(w http.ResponseWriter, r *http.Request) {
	skill, ok := h.loadSkillForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	labels, err := h.Queries.ListLabelsBySkill(r.Context(), db.ListLabelsBySkillParams{
		SkillID: skill.ID, WorkspaceID: skill.WorkspaceID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list skill labels")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"labels": labelsToResponse(labels)})
}

// AttachLabelToSkill 给 skill 挂标签（需管理权限），逻辑同 AttachLabelToAgent。
func (h *Handler) AttachLabelToSkill(w http.ResponseWriter, r *http.Request) {
	skill, ok := h.loadSkillForUser(w, r, chi.URLParam(r, "id"))
	if !ok || !h.canManageSkill(w, r, skill) {
		return
	}
	var req AttachLabelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.LabelID == "" {
		writeError(w, http.StatusBadRequest, "label_id is required")
		return
	}
	labelID, ok := parseUUIDOrBadRequest(w, req.LabelID, "label_id")
	if !ok {
		return
	}
	label, err := h.Queries.GetLabel(r.Context(), db.GetLabelParams{ID: labelID, WorkspaceID: skill.WorkspaceID})
	if err != nil || label.ResourceType != "skill" {
		writeError(w, http.StatusNotFound, "skill label not found")
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start label transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	if err := qtx.AttachLabelToSkill(r.Context(), db.AttachLabelToSkillParams{
		SkillID: skill.ID, LabelID: labelID, WorkspaceID: skill.WorkspaceID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to attach skill label")
		return
	}
	event, err := service.RecordDurableEventTx(r.Context(), qtx, events.Event{
		Type: protocol.EventLabelUpdated, IdempotencyKey: "label:skill_attach:" + uuidToString(skill.ID) + ":" + uuidToString(labelID),
		StreamKey: "skill:" + uuidToString(skill.ID), WorkspaceID: uuidToString(skill.WorkspaceID), ActorType: "member", ActorID: requestUserID(r),
		Payload: map[string]any{"label": labelToResponse(label), "resource_type": "skill", "skill_id": uuidToString(skill.ID)},
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record label event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit label change")
		return
	}
	h.publishEvent(event)
	h.ListLabelsForSkill(w, r)
}

// DetachLabelFromSkill 把标签从 skill 上解下来（需管理权限），逻辑同 DetachLabelFromAgent。
func (h *Handler) DetachLabelFromSkill(w http.ResponseWriter, r *http.Request) {
	skill, ok := h.loadSkillForUser(w, r, chi.URLParam(r, "id"))
	if !ok || !h.canManageSkill(w, r, skill) {
		return
	}
	labelID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "labelId"), "label id")
	if !ok {
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start label transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	if err := qtx.DetachLabelFromSkill(r.Context(), db.DetachLabelFromSkillParams{
		SkillID: skill.ID, LabelID: labelID, WorkspaceID: skill.WorkspaceID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to detach skill label")
		return
	}
	event, err := service.RecordDurableEventTx(r.Context(), qtx, events.Event{
		Type: protocol.EventLabelUpdated, IdempotencyKey: "label:skill_detach:" + uuidToString(skill.ID) + ":" + uuidToString(labelID),
		StreamKey: "skill:" + uuidToString(skill.ID), WorkspaceID: uuidToString(skill.WorkspaceID), ActorType: "member", ActorID: requestUserID(r),
		Payload: map[string]any{"label_id": uuidToString(labelID), "resource_type": "skill", "skill_id": uuidToString(skill.ID)},
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record label event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit label change")
		return
	}
	h.publishEvent(event)
	h.ListLabelsForSkill(w, r)
}
