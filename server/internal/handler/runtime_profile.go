// 本文件：自定义 runtime 配置（runtime_profile）的 HTTP 接口——创建/列出/查询/局部更新/
// 删除，外加一个给 daemon 的「列出已启用配置」端点。设计说明见下（MUL-3284）。
package handler

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/pkg/agent"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// ---------------------------------------------------------------------------
// 自定义 Runtime Profiles（MUL-3284）
//
// runtime_profile 是工作区级、团队共享的自定义 runtime 定义——例如内部自研的 Codex 包装器。
// daemon 拉取所在工作区启用的 profile，在 PATH 上解析 command_name，然后注册一条带着
// profile_id 的 agent_runtime 实例。profile 只改变 runtime 的启动/展示方式；底层
// protocol_family 必须是 Multica 官方支持的后端（对照 agent.SupportedTypes 校验）。
//
// 铁律：profile 不携带任何「按 Agent 定制的通用参数」。按 Agent 的启动参数留在
// agent.custom_args。唯一的 args 字段是 fixed_args——这台 runtime 上每个 Agent 都必须继承、
// 以进入兼容模式的参数。
// ---------------------------------------------------------------------------

// RuntimeProfileResponse 是 runtime profile 的对外 JSON 结构。
type RuntimeProfileResponse struct {
	ID             string   `json:"id"`
	WorkspaceID    string   `json:"workspace_id"`
	DisplayName    string   `json:"display_name"`
	ProtocolFamily string   `json:"protocol_family"`
	CommandName    string   `json:"command_name"`
	Description    *string  `json:"description"`
	FixedArgs      []string `json:"fixed_args"`
	Visibility     string   `json:"visibility"`
	CreatedBy      *string  `json:"created_by"`
	Enabled        bool     `json:"enabled"`
	CreatedAt      string   `json:"created_at"`
	UpdatedAt      string   `json:"updated_at"`
}

// runtimeProfileToResponse 把数据库行 db.RuntimeProfile 转成对外 JSON。FixedArgs 列存的是
// JSON 文本，这里尝试解析成字符串数组；为空时给空数组，避免前端拿到 null。
func runtimeProfileToResponse(p db.RuntimeProfile) RuntimeProfileResponse {
	args := []string{}
	if len(p.FixedArgs) > 0 {
		_ = json.Unmarshal(p.FixedArgs, &args)
		if args == nil {
			args = []string{}
		}
	}
	return RuntimeProfileResponse{
		ID:             uuidToString(p.ID),
		WorkspaceID:    uuidToString(p.WorkspaceID),
		DisplayName:    p.DisplayName,
		ProtocolFamily: p.ProtocolFamily,
		CommandName:    p.CommandName,
		Description:    textToPtr(p.Description),
		FixedArgs:      args,
		Visibility:     p.Visibility,
		CreatedBy:      uuidToPtr(p.CreatedBy),
		Enabled:        p.Enabled,
		CreatedAt:      timestampToString(p.CreatedAt),
		UpdatedAt:      timestampToString(p.UpdatedAt),
	}
}

// 注意：runtime_profile.visibility 在 v1 里刻意不允许用户设置。列存在、API 也返回它，但
// 创建时一律强制 'workspace'：daemon-pull、DaemonRegister 和 ListRuntimeProfiles 的读取路径
// 尚未落实 'private' 语义，此时若接受客户端的 'private'，会悄悄把「私有」profile 的名字/
// 命令泄露给其他成员，还允许别人机器上的 daemon 把它注册掉（横向数据泄露）。要重新开放
// visibility 控件，得等那些读取路径真的按创建者可见性过滤。跟进：MUL-3308。
const runtimeProfileDefaultVisibility = "workspace"

// marshalFixedArgs 校验并 JSON 编码 fixed_args 列表。每项必须是非空字符串；列为空数组。
func marshalFixedArgs(args []string) ([]byte, error) {
	if len(args) == 0 {
		return []byte("[]"), nil
	}
	clean := make([]string, 0, len(args))
	for _, a := range args {
		// fixed_args 是这台 runtime 上每个 Agent 都要继承的启动参数；空项永远是客户端写错。
		if strings.TrimSpace(a) == "" {
			return nil, errors.New("fixed_args entries must be non-empty")
		}
		if strings.ContainsRune(a, '\x00') {
			return nil, errors.New("fixed_args entries cannot contain NUL bytes")
		}
		clean = append(clean, a)
	}
	return json.Marshal(clean)
}

// validateRuntimeProfileCommandName 校验 command_name：必填、必须是单个可执行 token
// （不能带空格——参数要放 fixed_args）、不含 NUL 字节。
func validateRuntimeProfileCommandName(commandName string) error {
	if commandName == "" {
		return errors.New("command_name is required")
	}
	if strings.ContainsAny(commandName, " \t\r\n") {
		return errors.New("command_name must be a single executable token; put arguments in fixed_args")
	}
	if strings.ContainsRune(commandName, '\x00') {
		return errors.New("command_name cannot contain NUL bytes")
	}
	return nil
}

// createRuntimeProfileRequest 是 POST 创建 profile 的请求体。
type createRuntimeProfileRequest struct {
	DisplayName    string   `json:"display_name"`
	ProtocolFamily string   `json:"protocol_family"`
	CommandName    string   `json:"command_name"`
	Description    *string  `json:"description"`
	FixedArgs      []string `json:"fixed_args"`
	Enabled        *bool    `json:"enabled"`
}

// CreateRuntimeProfile 处理 POST /api/workspaces/:id/runtime-profiles——创建工作区级
// runtime profile。路由层已加 admin 门禁。protocol_family 对照 agent 后端白名单校验，
// display_name 工作区内唯一（冲突回 409）。成功后通知 daemon 刷新并广播 runtime_profile_id。
func (h *Handler) CreateRuntimeProfile(w http.ResponseWriter, r *http.Request) {
	wsID := strings.TrimSpace(chi.URLParam(r, "id"))
	member, ok := h.requireWorkspaceMember(w, r, wsID, "workspace not found")
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, wsID, "workspace id")
	if !ok {
		return
	}

	var req createRuntimeProfileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	req.DisplayName = strings.TrimSpace(req.DisplayName)
	req.ProtocolFamily = strings.TrimSpace(req.ProtocolFamily)
	req.CommandName = strings.TrimSpace(req.CommandName)

	if req.DisplayName == "" {
		writeError(w, http.StatusBadRequest, "display_name is required")
		return
	}
	if !agent.IsSupportedType(req.ProtocolFamily) {
		writeError(w, http.StatusBadRequest, "unsupported protocol_family: must be one of "+strings.Join(agent.SupportedTypes, ", "))
		return
	}
	if req.CommandName == "" {
		writeError(w, http.StatusBadRequest, "command_name is required")
		return
	}
	if err := validateRuntimeProfileCommandName(req.CommandName); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	fixedArgs, err := marshalFixedArgs(req.FixedArgs)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	profile, err := h.Queries.CreateRuntimeProfile(r.Context(), db.CreateRuntimeProfileParams{
		WorkspaceID:    wsUUID,
		DisplayName:    req.DisplayName,
		ProtocolFamily: req.ProtocolFamily,
		CommandName:    req.CommandName,
		Description:    ptrToText(req.Description),
		FixedArgs:      fixedArgs,
		Visibility:     runtimeProfileDefaultVisibility,
		CreatedBy:      member.UserID,
		Enabled:        enabled,
	})
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "a runtime profile with this display_name already exists")
			return
		}
		slog.Error("CreateRuntimeProfile failed", "error", err, "workspace_id", wsID)
		writeError(w, http.StatusInternalServerError, "failed to create runtime profile")
		return
	}

	profileID := uuidToString(profile.ID)
	h.requestDaemonRuntimeProfileRefresh(wsID, profileID)
	h.publish(protocol.EventDaemonRegister, wsID, "member", uuidToString(member.UserID), map[string]any{
		"runtime_profile_id": profileID,
	})

	writeJSON(w, http.StatusCreated, runtimeProfileToResponse(profile))
}

// ListRuntimeProfiles 处理 GET /api/workspaces/:id/runtime-profiles——返回工作区内全部
// profile。路由层已加成员门禁。
func (h *Handler) ListRuntimeProfiles(w http.ResponseWriter, r *http.Request) {
	wsID := strings.TrimSpace(chi.URLParam(r, "id"))
	if _, ok := h.requireWorkspaceMember(w, r, wsID, "workspace not found"); !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, wsID, "workspace id")
	if !ok {
		return
	}

	profiles, err := h.Queries.ListRuntimeProfiles(r.Context(), wsUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list runtime profiles")
		return
	}
	resp := make([]RuntimeProfileResponse, len(profiles))
	for i, p := range profiles {
		resp[i] = runtimeProfileToResponse(p)
	}
	writeJSON(w, http.StatusOK, map[string]any{"runtime_profiles": resp})
}

// GetRuntimeProfile 处理 GET /api/workspaces/:id/runtime-profiles/:profileId——返回单个
// profile。路由层已加成员门禁；找不到（含属于别的工作区）回 404。
func (h *Handler) GetRuntimeProfile(w http.ResponseWriter, r *http.Request) {
	wsID := strings.TrimSpace(chi.URLParam(r, "id"))
	if _, ok := h.requireWorkspaceMember(w, r, wsID, "workspace not found"); !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, wsID, "workspace id")
	if !ok {
		return
	}
	profileUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "profileId"), "profile id")
	if !ok {
		return
	}

	profile, err := h.Queries.GetRuntimeProfileForWorkspace(r.Context(), db.GetRuntimeProfileForWorkspaceParams{
		ID:          profileUUID,
		WorkspaceID: wsUUID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "runtime profile not found")
		return
	}
	writeJSON(w, http.StatusOK, runtimeProfileToResponse(profile))
}

// updateRuntimeProfileRequest 是 PATCH 局部更新 profile 的请求体（指针字段区分「没传」与
// 「显式传 null/值」）。
type updateRuntimeProfileRequest struct {
	DisplayName *string   `json:"display_name"`
	CommandName *string   `json:"command_name"`
	Description *string   `json:"description"`
	FixedArgs   *[]string `json:"fixed_args"`
	Enabled     *bool     `json:"enabled"`
}

// UpdateRuntimeProfile 处理 PATCH /api/workspaces/:id/runtime-profiles/:profileId——局部
// 更新 profile。protocol_family 不可变（改了等于把已绑定的 Agent 悄悄指到另一个后端）。
// 路由层已加 admin 门禁。成功后通知 daemon 刷新并广播。
func (h *Handler) UpdateRuntimeProfile(w http.ResponseWriter, r *http.Request) {
	wsID := strings.TrimSpace(chi.URLParam(r, "id"))
	member, ok := h.requireWorkspaceMember(w, r, wsID, "workspace not found")
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, wsID, "workspace id")
	if !ok {
		return
	}
	profileUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "profileId"), "profile id")
	if !ok {
		return
	}

	var req updateRuntimeProfileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	params := db.UpdateRuntimeProfileParams{ID: profileUUID, WorkspaceID: wsUUID}
	if req.DisplayName != nil {
		name := strings.TrimSpace(*req.DisplayName)
		if name == "" {
			writeError(w, http.StatusBadRequest, "display_name cannot be empty")
			return
		}
		params.DisplayName = strToText(name)
	}
	if req.CommandName != nil {
		cmd := strings.TrimSpace(*req.CommandName)
		if err := validateRuntimeProfileCommandName(cmd); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		params.CommandName = strToText(cmd)
	}
	if req.Description != nil {
		params.Description = ptrToText(req.Description)
	}
	if req.FixedArgs != nil {
		fixedArgs, err := marshalFixedArgs(*req.FixedArgs)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		params.FixedArgs = fixedArgs
	}
	if req.Enabled != nil {
		params.Enabled = pgtype.Bool{Bool: *req.Enabled, Valid: true}
	}

	profile, err := h.Queries.UpdateRuntimeProfile(r.Context(), params)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "runtime profile not found")
			return
		}
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "a runtime profile with this display_name already exists")
			return
		}
		slog.Error("UpdateRuntimeProfile failed", "error", err, "profile_id", uuidToString(profileUUID))
		writeError(w, http.StatusInternalServerError, "failed to update runtime profile")
		return
	}

	profileID := uuidToString(profile.ID)
	h.requestDaemonRuntimeProfileRefresh(wsID, profileID)
	h.publish(protocol.EventDaemonRegister, wsID, "member", uuidToString(member.UserID), map[string]any{
		"runtime_profile_id": profileID,
	})

	writeJSON(w, http.StatusOK, runtimeProfileToResponse(profile))
}

// DeleteRuntimeProfile 处理 DELETE /api/workspaces/:id/runtime-profiles/:profileId——删除
// profile，并在同一事务里删除按它注册的 agent_runtime 实例行。migration 120 去掉了数据库
// ON DELETE CASCADE，所以这份应用层清理是防止孤儿 runtime 行的关键。profile 的 runtime 上
// 还有活跃 Agent 绑着时拒绝（409）。路由层已加 admin 门禁。
func (h *Handler) DeleteRuntimeProfile(w http.ResponseWriter, r *http.Request) {
	wsID := strings.TrimSpace(chi.URLParam(r, "id"))
	member, ok := h.requireWorkspaceMember(w, r, wsID, "workspace not found")
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, wsID, "workspace id")
	if !ok {
		return
	}
	profileUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "profileId"), "profile id")
	if !ok {
		return
	}

	// profile 删除的级联必须对每个 runtime 走与 runtime 删除路径相同的拆解：agent.runtime_id
	// 是 ON DELETE RESTRICT，若还有 Agent 指向这些行，裸删会变成 500。活跃 Agent 一律拒绝
	// （409）；其余的解绑而非销毁，与 service.TeardownRuntime 完全一致。
	// 守卫：只要还有活跃（非 archived）Agent 绑在某个 profile 的 runtime 上就拒绝。保持 409
	// ——profile 是这些 runtime 的定义者，用户应当主动退休或迁移 Agent，而不是让它们被批量
	// 悄悄解绑。
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to begin transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)

	// 规划级联前先锁 profile。daemon 注册在自己的事务里拿互斥的 KEY SHARE 锁，所以它不可
	// 能在方案定下后再插入一条 runtime 并让那行逃过删除。若 profile 行已经不在了，仍然要清
	// 理可能残留的孤儿 profile_id 行。
	_, profileErr := qtx.LockRuntimeProfileForDelete(r.Context(), db.LockRuntimeProfileForDeleteParams{
		ID:          profileUUID,
		WorkspaceID: wsUUID,
	})
	profileMissing := errors.Is(profileErr, pgx.ErrNoRows)
	if profileErr != nil && !profileMissing {
		writeError(w, http.StatusInternalServerError, "failed to load runtime profile")
		return
	}

	// 按确定性的 id 顺序锁 runtime 行。它们的 agent/task 外键插入会拿 KEY SHARE 锁，防止在
	// 活跃 Agent 检查之后又冒出依赖。
	runtimeIDs, err := qtx.ListAgentRuntimeIDsByProfile(r.Context(), db.ListAgentRuntimeIDsByProfileParams{
		ProfileID:   profileUUID,
		WorkspaceID: wsUUID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to enumerate profile runtimes")
		return
	}
	if profileMissing && len(runtimeIDs) == 0 {
		writeError(w, http.StatusNotFound, "runtime profile not found")
		return
	}
	for _, runtimeID := range runtimeIDs {
		if _, err := qtx.ListUserAgentsByRuntimeForUpdate(r.Context(), runtimeID); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to lock profile dependencies")
			return
		}
	}

	agentCount, err := qtx.CountAgentsByProfile(r.Context(), db.CountAgentsByProfileParams{
		ProfileID:   profileUUID,
		WorkspaceID: wsUUID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to check profile usage")
		return
	}
	if agentCount > 0 {
		writeError(w, http.StatusConflict, "cannot delete runtime profile: active agents are still bound to its runtimes")
		return
	}

	// 应用层级联，逐台 runtime，镜像 DeleteAgentRuntime：解绑剩下（已归档）的 Agent 及其
	// 任务历史、取消仍在飞的、只硬删系统 Agent——这样删下面那些 runtime 行不会毁掉任何
	// Agent、对话或任务记录。
	var teardowns []service.RuntimeTeardownResult
	for _, rid := range runtimeIDs {
		teardown, err := service.TeardownRuntime(r.Context(), qtx, rid, service.RuntimeTeardownOptions{CancelNonTerminalTasks: true})
		if err != nil {
			if errors.Is(err, service.ErrRuntimeNotDrained) {
				slog.Error("runtime profile delete aborted: tasks not drained",
					"runtime_id", uuidToString(rid), "profile_id", uuidToString(profileUUID), "error", err)
				writeJSON(w, http.StatusConflict, map[string]any{
					"error": "a runtime of this profile still has tasks in flight; retry in a moment.",
					"code":  "runtime_delete_not_drained",
				})
				return
			}
			if errors.Is(err, service.ErrRuntimeWorkspaceMismatch) {
				slog.Error("runtime profile delete aborted: agent workspace mismatch",
					"runtime_id", uuidToString(rid), "profile_id", uuidToString(profileUUID), "error", err)
				writeJSON(w, http.StatusConflict, map[string]any{
					"error": "a runtime of this profile has an invalid cross-workspace agent binding.",
					"code":  "runtime_delete_workspace_mismatch",
				})
				return
			}
			slog.Error("runtime profile delete teardown failed",
				"runtime_id", uuidToString(rid), "profile_id", uuidToString(profileUUID), "error", err)
			writeError(w, http.StatusInternalServerError, "failed to unbind agents")
			return
		}
		teardowns = append(teardowns, teardown)
	}

	// 现在 runtime 行已经没有任何 Agent 引用：删掉它们，再删 profile 本身。
	if _, err := qtx.DeleteAgentRuntimesByProfile(r.Context(), db.DeleteAgentRuntimesByProfileParams{
		ProfileID:   profileUUID,
		WorkspaceID: wsUUID,
	}); err != nil {
		slog.Error("DeleteAgentRuntimesByProfile failed", "error", err, "profile_id", uuidToString(profileUUID))
		writeError(w, http.StatusInternalServerError, "failed to clean up runtime instances")
		return
	}
	if !profileMissing {
		if err := qtx.DeleteRuntimeProfile(r.Context(), db.DeleteRuntimeProfileParams{
			ID:          profileUUID,
			WorkspaceID: wsUUID,
		}); err != nil {
			slog.Error("DeleteRuntimeProfile failed", "error", err, "profile_id", uuidToString(profileUUID))
			writeError(w, http.StatusInternalServerError, "failed to delete runtime profile")
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit transaction")
		return
	}

	// 通知在线客户端重新拉取 runtime 列表（实例已消失），并按台广播拆解结果，让被解绑的
	// Agent 与被取消的任务以与 runtime 删除路径相同的方式触达订阅方。
	profileID := uuidToString(profileUUID)
	userID := uuidToString(member.UserID)
	for _, teardown := range teardowns {
		h.publishRuntimeTeardown(r.Context(), teardown, wsID, userID)
	}
	h.requestDaemonRuntimeProfileRefresh(wsID, profileID)
	h.publish(protocol.EventDaemonRegister, wsID, "member", userID, map[string]any{
		"deleted_runtime_profile_id": profileID,
	})

	w.WriteHeader(http.StatusNoContent)
}

// DaemonListRuntimeProfiles 给某工作区的 daemon 提供其启用的 runtime profiles。daemon 在
// PATH 上解析每个 profile 的 command_name，并为每个能跑的 profile 注册一条 agent_runtime
// 实例。路由层已加 daemon token 门禁。
func (h *Handler) DaemonListRuntimeProfiles(w http.ResponseWriter, r *http.Request) {
	workspaceID := strings.TrimSpace(chi.URLParam(r, "workspaceId"))
	if !h.requireDaemonWorkspaceAccess(w, r, workspaceID) {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}

	profiles, err := h.Queries.ListEnabledRuntimeProfilesForWorkspace(r.Context(), wsUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list runtime profiles")
		return
	}
	resp := make([]RuntimeProfileResponse, len(profiles))
	for i, p := range profiles {
		resp[i] = runtimeProfileToResponse(p)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"workspace_id":     workspaceID,
		"runtime_profiles": resp,
	})
}

// requestDaemonRuntimeProfileRefresh 通知（relay/hub 上的）daemon：工作区的 runtime profile
// 变了，尽快重拉。尽力而为，失败只损失刷新延迟。
func (h *Handler) requestDaemonRuntimeProfileRefresh(workspaceID, profileID string) {
	if h.DaemonProfileRefresh == nil {
		return
	}
	h.DaemonProfileRefresh.NotifyRuntimeProfilesChanged(workspaceID, profileID)
}
