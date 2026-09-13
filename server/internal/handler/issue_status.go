// 职责定位：issue 状态目录（status catalog）API（MUL-6243）：内置 + 自定义状态的列表、创建、
// 更新（改名/改类别/排序）、归档与重排。状态目录决定「自定义状态继承哪个平台行为类别」，
// 是 issue 写路径解析与表格分组/过滤的基础。
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/eventoutbox"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/logger"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// issue 状态目录 API（MUL-6243）。
//
// 读目录对任何工作区成员开放——每个客户端渲染状态都需要它。改目录仅限 owner/admin：状态是
// 全工作区共享的流程配置，创建一条就改变了「能命令 agent 做什么」。

// IssueStatusResponse 是单条状态目录项的响应体。
type IssueStatusResponse struct {
	ID          string  `json:"id"`
	WorkspaceID string  `json:"workspace_id"`
	Key         string  `json:"key"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Category    string  `json:"category"`
	Color       string  `json:"color"`
	IsSystem    bool    `json:"is_system"`
	Position    float64 `json:"position"`
	ArchivedAt  *string `json:"archived_at"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
}

// terminalIssueStatusKeys 解析出「类别带终态行为」的具体状态 key。调用方把结果喂给索引化状态
// 谓词，而不是逐行解析类别。
func (h *Handler) terminalIssueStatusKeys(ctx context.Context, workspaceID pgtype.UUID) ([]string, error) {
	return issuestatus.ExpandCategories(ctx, h.Queries, workspaceID, []string{
		issuestatus.Done,
		issuestatus.Cancelled,
	})
}

// issueStatusToResponse 把目录行转成响应体。
func issueStatusToResponse(s db.IssueStatus) IssueStatusResponse {
	return IssueStatusResponse{
		ID:          uuidToString(s.ID),
		WorkspaceID: uuidToString(s.WorkspaceID),
		Key:         s.Key,
		Name:        s.Name,
		Description: s.Description,
		Category:    s.Category,
		Color:       s.Color,
		IsSystem:    s.IsSystem,
		Position:    s.Position,
		ArchivedAt:  timestampToPtr(s.ArchivedAt),
		CreatedAt:   timestampToString(s.CreatedAt),
		UpdatedAt:   timestampToString(s.UpdatedAt),
	}
}

// CreateIssueStatusRequest 是创建自定义状态的请求体。
type CreateIssueStatusRequest struct {
	// Key 可选；省略时由 Name 派生。创建后不可变，因为它是 issue.status 里存的值。
	Key         string `json:"key"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Category    string `json:"category"`
	Color       string `json:"color"`
}

// UpdateIssueStatusRequest 刻意没有 Key 与 Category 字段——两者不可变：改类别会悄悄改写所有
// 已在该状态上 issue 的机器语义，改 key 则会让它们悬空。
type UpdateIssueStatusRequest struct {
	Name        *string  `json:"name"`
	Description *string  `json:"description"`
	Color       *string  `json:"color"`
	Position    *float64 `json:"position"`
}

// ListIssueStatuses 返回工作区状态目录（展示序）。任何成员可读。
func (h *Handler) ListIssueStatuses(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	if _, ok := h.requireWorkspaceMember(w, r, workspaceID, "workspace not found"); !ok {
		return
	}

	// 自愈：由早于本特性的 pod 创建的工作区没有目录行。读时播种让滚动部署期间的端点保持正确，
	// 不必再来一遍回填。幂等，行已存在时是空操作。
	if err := issuestatus.Ensure(r.Context(), h.Queries, wsUUID); err != nil {
		slog.Warn("failed to ensure issue status catalog", append(logger.RequestAttrs(r), "error", err)...)
	}

	includeArchived := strings.EqualFold(r.URL.Query().Get("include_archived"), "true")
	entries, err := h.Queries.ListIssueStatusEntries(r.Context(), db.ListIssueStatusEntriesParams{
		WorkspaceID:     wsUUID,
		IncludeArchived: includeArchived,
	})
	if err != nil {
		slog.Warn("ListIssueStatuses failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to list issue statuses")
		return
	}

	resp := make([]IssueStatusResponse, len(entries))
	for i, e := range entries {
		resp[i] = issueStatusToResponse(e)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"statuses":   resp,
		"categories": issuestatus.Canonical(),
		"total":      len(resp),
	})
}

// CreateIssueStatus 处理 POST .../issue-statuses：向工作区目录加一条自定义状态。
// 权限：owner/admin（目录是全工作区共享的流程配置）。成功返回 201 + 新条目。
func (h *Handler) CreateIssueStatus(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	member, ok := h.requireWorkspaceRole(w, r, workspaceID, "workspace not found", "owner", "admin")
	if !ok {
		return
	}

	var req CreateIssueStatusRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" || len([]rune(name)) > 64 {
		writeError(w, http.StatusBadRequest, "name must be 1-64 characters")
		return
	}
	if len([]rune(req.Description)) > 256 {
		writeError(w, http.StatusBadRequest, "description must be at most 256 characters")
		return
	}
	if !issuestatus.IsCategory(req.Category) {
		writeError(w, http.StatusBadRequest, "category must be one of: "+strings.Join(issuestatus.Canonical(), ", "))
		return
	}
	color, err := normalizeColor(req.Color)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// 显式传了 key 就在此优先并立即校验：对着保留的内置状态名与存储格式检查。没传 key 时
	// key 会从显示名「派生」出来——派生要读目录定名，依赖锁，所以放在 createIssueStatusEntry
	// 的事务里做。
	var explicitKey string
	if strings.TrimSpace(req.Key) != "" {
		explicitKey, err = issuestatus.ValidateKey(req.Key)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	entry, badRequest, persistedEvent, err := h.createIssueStatusEntry(r.Context(), wsUUID, member, db.CreateIssueStatusEntryParams{
		WorkspaceID: wsUUID,
		Key:         explicitKey,
		Name:        name,
		Description: req.Description,
		Category:    req.Category,
		Color:       strings.ToLower(color),
	})
	if badRequest != "" {
		writeError(w, http.StatusBadRequest, badRequest)
		return
	}
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "a status with this key or name already exists")
			return
		}
		slog.Warn("CreateIssueStatus failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to create issue status")
		return
	}
	h.publishEvent(persistedEvent)
	writeJSON(w, http.StatusCreated, issueStatusToResponse(entry))
}

// createIssueStatusEntry 在事务里创建一条状态目录行；传入的 arg.Key 为空时从显示名派生一个。
//
// 为什么包成「读 + 写」的单一原子步骤：派生 key 要「读一遍目录」才能挑出没人占用的名字，
// 因此读取与 INSERT 之间不能有缝——否则两个管理员同一瞬间创建中文名的 in_review 状态会算出
// 同一个 `in_review_2`，后提交者收到「一个自己从未输入过的 key 已被占用」的报错。这里用的
// EXCLUSIVE 目录锁（归档也用它）负责把它们串行化。
//
// 每次创建都拿这把锁，包括自带 key 的创建：把自带 key 的排除掉等于只堵一半竞态——一个显式
// 携带 `in_review_2` 的插入仍可能落在某次派生的读与写之间，让那次派生（UI 请求，连可归咎的
// key 字段都没有）返回 409。锁只有目录写入才争用，属罕见的管理员操作，串行化它们几乎不花
// 代价，没必要为此留一个洞。
//
// 第二个返回值非空表示调用方错误，handler 会当作 400 上报；区别于 nil error 的成功与基础设施
// 故障两种情形。
func (h *Handler) createIssueStatusEntry(ctx context.Context, workspaceID pgtype.UUID, actor db.Member, arg db.CreateIssueStatusEntryParams) (db.IssueStatus, string, events.Event, error) {
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return db.IssueStatus{}, "", events.Event{}, err
	}
	defer tx.Rollback(ctx)
	qtx := h.Queries.WithTx(tx)

	if err := qtx.LockIssueStatusCatalog(ctx, workspaceID); err != nil {
		return db.IssueStatus{}, "", events.Event{}, err
	}

	if arg.Key == "" {
		// 读目录必须带上已归档行：idx_issue_status_workspace_key 不是部分索引，退役状态
		// 仍然占着它的 key。若不带上，派生会认为该 key 空闲、选出它后 INSERT 撞唯一约束而
		// 失败，而不是继续生成下一个可用编号。
		entries, err := qtx.ListIssueStatusEntries(ctx, db.ListIssueStatusEntriesParams{
			WorkspaceID:     workspaceID,
			IncludeArchived: true,
		})
		if err != nil {
			return db.IssueStatus{}, "", events.Event{}, err
		}
		taken := make(map[string]bool, len(entries))
		for _, e := range entries {
			taken[e.Key] = true
		}
		key, err := issuestatus.DeriveKey(arg.Name, arg.Category, taken)
		if err != nil {
			return db.IssueStatus{}, err.Error(), events.Event{}, nil
		}
		arg.Key = key
	}

	entry, err := qtx.CreateIssueStatusEntry(ctx, arg)
	if err != nil {
		return db.IssueStatus{}, "", events.Event{}, err
	}
	event := issueStatusChangedEvent(uuidToString(workspaceID), actor, "created", uuidToString(entry.ID))
	event, err = eventoutbox.Enqueue(ctx, qtx, event)
	if err != nil {
		return db.IssueStatus{}, "", events.Event{}, fmt.Errorf("record issue status event: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return db.IssueStatus{}, "", events.Event{}, err
	}
	return entry, "", event, nil
}

// UpdateIssueStatus 处理 PATCH/PUT .../issue-statuses/:key：修改自定义状态的外观（名称/描述/
// 颜色/位置）。内置状态在 v1 完全不可改——名字和颜色也一样——这样从未打开过这个设置页的
// 工作区，默认状态目录长得与行为都完全一致。权限 owner/admin；expected_revision 乐观并发
// 不匹配时返回 409。
func (h *Handler) UpdateIssueStatus(w http.ResponseWriter, r *http.Request) {
	entry, wsUUID, member, ok := h.loadIssueStatusForAdmin(w, r)
	if !ok {
		return
	}

	var req UpdateIssueStatusRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if entry.IsSystem {
		writeError(w, http.StatusForbidden, "built-in statuses cannot be modified")
		return
	}
	if entry.ArchivedAt.Valid {
		writeError(w, http.StatusConflict, "archived statuses cannot be modified")
		return
	}

	var name pgtype.Text
	if req.Name != nil {
		trimmed := strings.TrimSpace(*req.Name)
		if trimmed == "" || len([]rune(trimmed)) > 64 {
			writeError(w, http.StatusBadRequest, "name must be 1-64 characters")
			return
		}
		name = pgtype.Text{String: trimmed, Valid: true}
	}
	var description pgtype.Text
	if req.Description != nil {
		if len([]rune(*req.Description)) > 256 {
			writeError(w, http.StatusBadRequest, "description must be at most 256 characters")
			return
		}
		description = pgtype.Text{String: *req.Description, Valid: true}
	}
	var color pgtype.Text
	if req.Color != nil {
		normalized, err := normalizeColor(*req.Color)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		color = pgtype.Text{String: strings.ToLower(normalized), Valid: true}
	}
	var position pgtype.Float8
	if req.Position != nil {
		position = pgtype.Float8{Float64: *req.Position, Valid: true}
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start status update")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	updated, err := qtx.UpdateIssueStatusEntry(r.Context(), db.UpdateIssueStatusEntryParams{
		ID: entry.ID, WorkspaceID: wsUUID, Name: name, Description: description,
		Color: color, Position: position,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// 行在我们眼前被移走了：要么被并发归档，要么被语句里的 is_system 守卫拒绝。
			writeError(w, http.StatusConflict, "status is no longer editable")
			return
		}
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "a status with this name already exists")
			return
		}
		slog.Warn("UpdateIssueStatus failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to update issue status")
		return
	}
	persistedEvent, err := eventoutbox.Enqueue(r.Context(), qtx,
		issueStatusChangedEvent(uuidToString(wsUUID), member, "updated", uuidToString(updated.ID)))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record status update")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit status update")
		return
	}
	h.publishEvent(persistedEvent)
	writeJSON(w, http.StatusOK, issueStatusToResponse(updated))
}

// ArchiveIssueStatus 处理 POST .../issue-statuses/:key/archive：把一条自定义状态从「未来可分配」
// 中退役。已在它上面的 issue 被刻意保留不动——详见函数体内的锁说明，那也正是「已归档状态
// 不接受新分配」从近似走向精确的原因。权限 owner/admin。
func (h *Handler) ArchiveIssueStatus(w http.ResponseWriter, r *http.Request) {
	entry, wsUUID, member, ok := h.loadIssueStatusForAdmin(w, r)
	if !ok {
		return
	}

	if entry.IsSystem {
		writeError(w, http.StatusForbidden, "built-in statuses cannot be archived")
		return
	}
	if entry.ArchivedAt.Valid {
		writeJSON(w, http.StatusOK, issueStatusToResponse(entry))
		return
	}

	// 归档把状态从「未来可用」里退役，同时刻意不动已在它上面的 issue：它们保留原状态、照常
	// 渲染、照常按所属类别解析行为（issuestatus.Effective 有意忽略 archived_at）。若强制先迁移
	// 它们，等于为了退役一个标签而改写历史。
	//
	// 但仍要拿目录的 EXCLUSIVE 锁——正是它让「已归档状态不再接受新分配」从近似变成精确：针对
	// 自定义状态的 issue 写路径会在同一把锁的 SHARED 侧重新解析该状态（assertIssueStatusStillActive），
	// 所以一次写入永远不可能插进「本次归档」与「它自己的状态校验」之间。（MUL-6243）
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		slog.Warn("ArchiveIssueStatus begin failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to archive issue status")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)

	if err := qtx.LockIssueStatusCatalog(r.Context(), wsUUID); err != nil {
		slog.Warn("ArchiveIssueStatus lock failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to archive issue status")
		return
	}

	archived, err := qtx.ArchiveIssueStatusEntry(r.Context(), db.ArchiveIssueStatusEntryParams{
		ID:          entry.ID,
		WorkspaceID: wsUUID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusConflict, "status is no longer archivable")
			return
		}
		slog.Warn("ArchiveIssueStatus failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to archive issue status")
		return
	}
	persistedEvent, eventErr := eventoutbox.Enqueue(r.Context(), qtx,
		issueStatusChangedEvent(uuidToString(wsUUID), member, "archived", uuidToString(archived.ID)))
	if eventErr != nil {
		writeError(w, http.StatusInternalServerError, "failed to record status archive")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		slog.Warn("ArchiveIssueStatus commit failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to archive issue status")
		return
	}
	// 事件必须在提交之后发、绝不能在提交之前发：若事件宣告的变更随后被事务回滚，其它标签页
	// 会收到通知重读目录，并把归档前的旧行当作新真相缓存下来。
	h.publishEvent(persistedEvent)
	writeJSON(w, http.StatusOK, issueStatusToResponse(archived))
}

// loadIssueStatusForAdmin 在调用者工作区内解析 {id} 路径参数并执行 owner/admin 门槛校验。
// 解析出的 member 一并返回，好让写操作在实时事件上署上操作者身份。权限不足写 403。
func (h *Handler) loadIssueStatusForAdmin(w http.ResponseWriter, r *http.Request) (db.IssueStatus, pgtype.UUID, db.Member, bool) {
	workspaceID := h.resolveWorkspaceID(r)
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return db.IssueStatus{}, pgtype.UUID{}, db.Member{}, false
	}
	member, ok := h.requireWorkspaceRole(w, r, workspaceID, "workspace not found", "owner", "admin")
	if !ok {
		return db.IssueStatus{}, pgtype.UUID{}, db.Member{}, false
	}
	idUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "issue status id")
	if !ok {
		return db.IssueStatus{}, pgtype.UUID{}, db.Member{}, false
	}
	entry, err := h.Queries.GetIssueStatusEntryByID(r.Context(), db.GetIssueStatusEntryByIDParams{
		ID:          idUUID,
		WorkspaceID: wsUUID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "issue status not found")
			return db.IssueStatus{}, pgtype.UUID{}, db.Member{}, false
		}
		slog.Warn("load issue status failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to load issue status")
		return db.IssueStatus{}, pgtype.UUID{}, db.Member{}, false
	}
	return entry, wsUUID, member, true
}

// issueStatusChangedEvent 构造一条「状态目录发生变更」的工作区事件，通知各端重读目录。
//
// 每次写操作发一条事件（见 protocol.EventIssueStatusChanged），客户端收到后重读目录、不合并
// payload。被改动的行内容刻意不随事件走：若客户端从事件里合并条目，还得与一条它看不见的
// 并发写做对账；而目录只有几行，重读一次成本远低于此。
func issueStatusChangedEvent(workspaceID string, actor db.Member, action, resourceID string) events.Event {
	return events.Event{
		Type: protocol.EventIssueStatusChanged, WorkspaceID: workspaceID,
		ActorType: "member", ActorID: uuidToString(actor.UserID),
		StreamKey:      "issue-status-catalog:" + workspaceID,
		IdempotencyKey: "issue-status:" + action + ":" + resourceID + ":" + uuid.NewString(),
		Payload:        map[string]any{"action": action, "status_id": resourceID},
	}
}

// ReorderIssueStatusesRequest 携带某类别下自定义状态的新顺序。重排按类别限定，因为 position
// 是类别内的：一个状态只能在它自己的列里相对移动。
//
// `ids` 必须点名该类别「每个」活跃自定义状态。部分排序会被拒绝而不是部分应用——position 由
// 数组下标派生，对子集重排会写进与被漏行相撞的位置。
type ReorderIssueStatusesRequest struct {
	Category string   `json:"category"`
	IDs      []string `json:"ids"`
}

// ReorderIssueStatuses 原子地重写某类别下自定义状态的类别内顺序。
//
// 一切发生在一个持有目录 SHARED 锁的事务里——它是归档路径（持 EXCLUSIVE 锁）的对应方。
// 这不是装饰：
//
//   - 在事务外校验会留一个窗口。「先校验 A 和 B → 别的请求归档 B → 再跑重排」会写入 A 的新
//     位置、悄悄跳过 B（UPDATE 排除已归档行），然后对半应用过的顺序报 200。
//   - 受影响行数要与 payload 对得上：UPDATE 拒绝触碰的任何行都会让整个请求失败，而不是提交
//     一个前缀。
func (h *Handler) ReorderIssueStatuses(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	member, ok := h.requireWorkspaceRole(w, r, workspaceID, "workspace not found", "owner", "admin")
	if !ok {
		return
	}

	var req ReorderIssueStatusesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if !issuestatus.IsCategory(req.Category) {
		writeError(w, http.StatusBadRequest, "category must be one of: "+strings.Join(issuestatus.Canonical(), ", "))
		return
	}
	if len(req.IDs) == 0 {
		writeError(w, http.StatusBadRequest, "ids must not be empty")
		return
	}
	ids := make([]pgtype.UUID, 0, len(req.IDs))
	seen := make(map[string]struct{}, len(req.IDs))
	for _, raw := range req.IDs {
		if _, duplicate := seen[raw]; duplicate {
			writeError(w, http.StatusBadRequest, "duplicate ids")
			return
		}
		seen[raw] = struct{}{}
		idUUID, err := util.ParseUUID(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid issue status id")
			return
		}
		ids = append(ids, idUUID)
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		slog.Warn("ReorderIssueStatuses begin failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to reorder issue statuses")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)

	// 取目录锁的 SHARED 侧：它不挡其它重排与 issue 写入，只挡 EXCLUSIVE 的归档路径——
	// 正是这一点消除了「先校验后写入」的竞态窗口。
	if err := qtx.LockIssueStatusCatalogShared(r.Context(), wsUUID); err != nil {
		slog.Warn("ReorderIssueStatuses lock failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to reorder issue statuses")
		return
	}

	// 权威集合：在锁内读出。拿 payload 与它逐一比对，一次覆盖所有拒绝情形——内置状态、已归档
	// 状态、别的类别/别的工作区的行，以及逐条查 id 时最容易漏掉的那种：payload 干脆没点名的
	// 活跃状态。
	active, err := qtx.ListActiveCustomIssueStatusEntries(r.Context(), db.ListActiveCustomIssueStatusEntriesParams{
		WorkspaceID: wsUUID,
		Category:    req.Category,
	})
	if err != nil {
		slog.Warn("ReorderIssueStatuses list failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to reorder issue statuses")
		return
	}
	activeIDs := make(map[string]struct{}, len(active))
	for _, entry := range active {
		activeIDs[util.UUIDToString(entry.ID)] = struct{}{}
	}
	for i, raw := range req.IDs {
		if _, isActive := activeIDs[raw]; isActive {
			continue
		}
		// 仍在锁内，诊断不会过期。按原因逐类报告而不是合并成一个笼统的冲突——因为「你拖了
		// 内置状态」与「拖动期间别人归档了它」对调用方是两种不同的问题。
		entry, err := qtx.GetIssueStatusEntryByID(r.Context(), db.GetIssueStatusEntryByIDParams{
			ID:          ids[i],
			WorkspaceID: wsUUID,
		})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			writeError(w, http.StatusNotFound, "issue status not found")
		case err != nil:
			slog.Warn("load issue status for reorder failed", append(logger.RequestAttrs(r), "error", err)...)
			writeError(w, http.StatusInternalServerError, "failed to reorder issue statuses")
		case entry.IsSystem:
			writeError(w, http.StatusForbidden, "built-in statuses cannot be reordered")
		case entry.ArchivedAt.Valid:
			writeError(w, http.StatusConflict, "archived statuses cannot be reordered")
		case entry.Category != req.Category:
			writeError(w, http.StatusBadRequest, "ids must all belong to the requested category")
		default:
			writeError(w, http.StatusConflict, "issue status catalog changed during reorder")
		}
		return
	}
	// 每个 id 都是有效且活跃的——那么数量对不上只可能意味着 payload「漏点了一个」。照单应用
	// 会按数组下标派位置，与被漏掉的那行撞位，所以整个请求必须被拒绝。
	if len(active) != len(ids) {
		writeError(w, http.StatusConflict, "ids must name every active custom status in the category")
		return
	}

	affected, err := qtx.ReorderIssueStatusEntries(r.Context(), db.ReorderIssueStatusEntriesParams{
		Ids:         ids,
		WorkspaceID: wsUUID,
	})
	if err != nil {
		slog.Warn("ReorderIssueStatuses failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to reorder issue statuses")
		return
	}
	if affected != int64(len(ids)) {
		// 锁内的双保险：万一 UPDATE 仍拒绝了一行，就把整个顺序回滚，而不是提交一个残缺前缀。
		slog.Warn("ReorderIssueStatuses touched an unexpected row count",
			append(logger.RequestAttrs(r), "affected", affected, "expected", len(ids))...)
		writeError(w, http.StatusConflict, "issue status catalog changed during reorder")
		return
	}

	entries, err := qtx.ListIssueStatusEntries(r.Context(), db.ListIssueStatusEntriesParams{
		WorkspaceID:     wsUUID,
		IncludeArchived: true,
	})
	if err != nil {
		slog.Warn("list issue statuses after reorder failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to list issue statuses")
		return
	}
	persistedEvent, err := eventoutbox.Enqueue(r.Context(), qtx,
		issueStatusChangedEvent(workspaceID, member, "reordered", strings.Join(req.IDs, ",")))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record status reorder")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		slog.Warn("ReorderIssueStatuses commit failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to reorder issue statuses")
		return
	}
	h.publishEvent(persistedEvent)

	resp := make([]IssueStatusResponse, len(entries))
	for i, e := range entries {
		resp[i] = issueStatusToResponse(e)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"statuses":   resp,
		"categories": issuestatus.Canonical(),
		"total":      len(resp),
	})
}
